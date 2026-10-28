package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"discord-music-bot/internal/audio"
	"discord-music-bot/internal/config"
	"discord-music-bot/internal/player"
	"discord-music-bot/internal/spotify"
	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/thomas-vilte/dave-go/session"
)

type Bot struct {
	cfg      config.Config
	logger   *slog.Logger
	client   *bot.Client
	resolver *audio.Resolver
	streamer *audio.Streamer
	spotify  *spotify.Client

	mu              sync.Mutex
	players         map[snowflake.ID]*player.Player
	playlistLoads   map[snowflake.ID]map[uint64]context.CancelFunc
	nextPlaylistID  uint64
	connections     map[snowflake.ID]voice.Conn
	daveSessions    map[snowflake.ID]*session.Session
	channels        map[snowflake.ID]snowflake.ID
	commandChannels map[snowflake.ID]snowflake.ID
	joinLocks       map[snowflake.ID]*sync.Mutex
	leaveTimers     map[snowflake.ID]*time.Timer
	panels          map[snowflake.ID]controlPanel
	panelRefreshes  map[snowflake.ID]*panelRefreshState
	ready           bool
}

type panelRefreshState struct {
	pending bool
	running bool
}

type controlPanel struct {
	channelID snowflake.ID
	messageID snowflake.ID
}

func New(cfg config.Config, logger *slog.Logger) (*Bot, error) {
	application := &Bot{
		cfg:             cfg,
		logger:          logger,
		resolver:        audio.NewResolver(),
		streamer:        audio.NewStreamer(logger),
		players:         make(map[snowflake.ID]*player.Player),
		playlistLoads:   make(map[snowflake.ID]map[uint64]context.CancelFunc),
		connections:     make(map[snowflake.ID]voice.Conn),
		daveSessions:    make(map[snowflake.ID]*session.Session),
		channels:        make(map[snowflake.ID]snowflake.ID),
		commandChannels: make(map[snowflake.ID]snowflake.ID),
		joinLocks:       make(map[snowflake.ID]*sync.Mutex),
		leaveTimers:     make(map[snowflake.ID]*time.Timer),
		panels:          make(map[snowflake.ID]controlPanel),
		panelRefreshes:  make(map[snowflake.ID]*panelRefreshState),
	}
	application.resolver.CookiesFile = cfg.YTDLPCookiesFile
	application.streamer.CookiesFile = cfg.YTDLPCookiesFile
	if cfg.SpotifyClientID != "" {
		client, err := spotify.NewWithUserOAuth(
			cfg.SpotifyClientID,
			cfg.SpotifyClientSecret,
			cfg.SpotifyRedirectURI,
			cfg.SpotifyTokenFile,
		)
		if err != nil {
			return nil, fmt.Errorf("configure Spotify authorization: %w", err)
		}
		application.spotify = client
	}

	client, err := disgo.New(
		cfg.DiscordToken,
		bot.WithLogger(logger),
		bot.WithGatewayConfigOpts(gateway.WithIntents(
			gateway.IntentGuilds,
			gateway.IntentGuildVoiceStates,
			gateway.IntentGuildMessages,
			gateway.IntentMessageContent,
		)),
		bot.WithCacheConfigOpts(cache.WithCaches(cache.FlagGuilds, cache.FlagVoiceStates)),
		bot.WithVoiceManagerConfigOpts(
			voice.WithConnCreateFunc(func(
				guildID, userID snowflake.ID,
				stateUpdate voice.StateUpdateFunc,
				removeConn func(),
				opts ...voice.ConnConfigOpt,
			) voice.Conn {
				daveSessionCreate := session.CreateFunc(session.WithSessionHook(func(daveSession *session.Session) {
					application.mu.Lock()
					previous := application.daveSessions[guildID]
					application.daveSessions[guildID] = daveSession
					application.mu.Unlock()
					if previous != nil && previous != daveSession {
						previous.Close()
					}
				}))
				opts = append(opts, voice.WithConnDaveSessionCreateFunc(daveSessionCreate))
				return voice.NewConn(guildID, userID, stateUpdate, removeConn, opts...)
			}),
			voice.WithDaveSessionLogger(logger),
		),
		bot.WithEventListenerFunc(application.onMessage),
		bot.WithEventListenerFunc(application.onComponent),
		bot.WithEventListenerFunc(application.onVoiceStateUpdate),
	)
	if err != nil {
		return nil, fmt.Errorf("create DisGo client with DAVE voice support: %w", err)
	}
	application.client = client
	return application, nil
}

func (b *Bot) Run(ctx context.Context) error {
	server := &http.Server{
		Addr:              b.cfg.HTTPAddr,
		Handler:           b.healthHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- fmt.Errorf("health server: %w", err)
		}
	}()

	if err := b.client.OpenGateway(ctx); err != nil {
		_ = server.Close()
		return fmt.Errorf("open Discord Gateway: %w", err)
	}
	if err := b.client.SetPresence(ctx, gateway.WithListeningActivity("!play")); err != nil {
		b.logger.Error("set listening activity", "error", err)
	}
	if _, err := b.client.Rest.SetGlobalCommands(b.client.ApplicationID, []discord.ApplicationCommandCreate{}); err != nil {
		b.shutdown(server)
		return fmt.Errorf("remove obsolete Discord slash commands: %w", err)
	}
	b.mu.Lock()
	b.ready = true
	b.mu.Unlock()
	b.logger.Info("bot is ready with DAVE-enabled voice", "http_addr", b.cfg.HTTPAddr)

	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		b.shutdown(server)
		return err
	}
	b.shutdown(server)
	return ctx.Err()
}

func (b *Bot) shutdown(server *http.Server) {
	b.mu.Lock()
	b.ready = false
	guildIDs := make(map[snowflake.ID]struct{}, len(b.players)+len(b.connections)+len(b.panels))
	for guildID := range b.players {
		guildIDs[guildID] = struct{}{}
	}
	for guildID := range b.connections {
		guildIDs[guildID] = struct{}{}
	}
	for guildID := range b.panels {
		guildIDs[guildID] = struct{}{}
	}
	for guildID := range b.playlistLoads {
		guildIDs[guildID] = struct{}{}
	}
	b.mu.Unlock()

	for guildID := range guildIDs {
		b.stopGuild(guildID)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b.client.Close(closeCtx)
	if err := server.Shutdown(closeCtx); err != nil {
		b.logger.Error("shut down health server", "error", err)
	}
}

func (b *Bot) healthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		b.mu.Lock()
		ready := b.ready
		b.mu.Unlock()
		if !ready {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	if b.spotify != nil {
		mux.HandleFunc("/spotify/login", b.spotifyLoginHandler)
		mux.HandleFunc("/spotify/callback", b.spotifyCallbackHandler)
	}
	return mux
}

func (b *Bot) spotifyLoginHandler(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	authorizationURL, err := b.spotify.BeginUserAuthorization()
	if err != nil {
		b.logger.Error("begin Spotify user authorization", "error", err)
		http.Error(w, "could not start Spotify authorization", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, request, authorizationURL, http.StatusFound)
}

func (b *Bot) spotifyCallbackHandler(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.URL.Query().Get("error") != "" {
		http.Error(w, "Spotify authorization was declined; you can close this page.", http.StatusBadRequest)
		return
	}
	code := request.URL.Query().Get("code")
	state := request.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Error(w, "Spotify authorization callback is missing code or state.", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	if err := b.spotify.CompleteUserAuthorization(ctx, code, state); err != nil {
		b.logger.Error("complete Spotify user authorization", "error", err)
		http.Error(w, "Spotify authorization failed. Check bot logs and try again.", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Spotify authorization complete. You can close this page and try the playlist again.\n"))
}

func (b *Bot) onMessage(event *events.GuildMessageCreate) {
	if event.Message.Author.Bot {
		return
	}
	command, args, ok := parseCommand(event.Message.Content)
	if !ok {
		return
	}
	if isMusicCommand(command) && !b.isCommandChannel(event) {
		return
	}
	switch command {
	case "join":
		b.joinCommand(event)
	case "play":
		if args == "" {
			b.sendMessage(event, "Uso: `!play <URL o búsqueda>`")
			return
		}
		if isDuplicatePlayCommand(args) {
			b.sendMessage(event, "No repitas el comando. Usa `!play <URL o búsqueda>`.")
			return
		}
		b.playCommand(event, args)
	case "skip":
		b.skipCommand(event)
	case "previous":
		b.previousCommand(event)
	case "pause":
		b.pauseCommand(event)
	case "shuffle":
		b.shuffleCommand(event)
	case "stop", "leave":
		b.stopCommand(event)
	case "queue":
		b.queueCommand(event)
	}
}

func isMusicCommand(command string) bool {
	switch command {
	case "join", "play", "skip", "previous", "pause", "shuffle", "stop", "leave", "queue":
		return true
	default:
		return false
	}
}

func isDuplicatePlayCommand(args string) bool {
	command, _, ok := parseCommand(args)
	return ok && command == "play"
}

func commandChannelMatches(assignedChannelID, incomingChannelID snowflake.ID) bool {
	return assignedChannelID == 0 || assignedChannelID == incomingChannelID
}

func (b *Bot) isCommandChannel(event *events.GuildMessageCreate) bool {
	b.mu.Lock()
	commandChannelID := b.commandChannels[event.GuildID]
	b.mu.Unlock()
	if commandChannelMatches(commandChannelID, event.ChannelID) {
		return true
	}

	b.sendMessage(event, fmt.Sprintf("Este servidor ya tiene un canal de música asignado. Usa los comandos en <#%s>.", commandChannelID.String()))
	return false
}

func (b *Bot) joinCommand(event *events.GuildMessageCreate) {
	guildID := event.GuildID
	user := event.Message.Author
	requestChannelID := event.ChannelID
	voiceState, ok := event.Client().Caches.VoiceState(guildID, user.ID)
	if !ok || voiceState.ChannelID == nil {
		b.sendMessage(event, "Debes estar en un canal de voz para usar `!join`.")
		return
	}
	channelID := *voiceState.ChannelID

	go func() {
		b.logger.Info("received join command", "guild_id", guildID, "channel_id", channelID)
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := b.joinVoice(ctx, guildID, channelID); err != nil {
			b.sendChannelMessage(requestChannelID, "No pude moverme a tu canal de voz. Confirma los permisos de acceso y conexión. Detalle: "+err.Error())
			return
		}
		b.cancelLeaveTimer(guildID)
		b.sendChannelMessage(requestChannelID, "Me moví a tu canal de voz.")
	}()
}

func parseCommand(content string) (name, args string, ok bool) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "!") {
		return "", "", false
	}
	content = strings.TrimSpace(content[1:])
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return "", "", false
	}
	return strings.ToLower(fields[0]), strings.TrimSpace(strings.TrimPrefix(content, fields[0])), true
}

func (b *Bot) playCommand(event *events.GuildMessageCreate, query string) {
	guildID := event.GuildID
	user := event.Message.Author
	b.logger.Info("received play command", "guild_id", guildID)
	voiceState, ok := event.Client().Caches.VoiceState(guildID, user.ID)
	if !ok || voiceState.ChannelID == nil {
		b.sendMessage(event, "Debes estar en un canal de voz para usar `!play`.")
		return
	}
	b.mu.Lock()
	commandChannelID := b.commandChannels[guildID]
	if commandChannelID == 0 {
		b.commandChannels[guildID] = event.ChannelID
		commandChannelID = event.ChannelID
	}
	b.mu.Unlock()
	if commandChannelID != event.ChannelID {
		b.sendMessage(event, fmt.Sprintf("Este servidor ya tiene un canal de música asignado. Usa los comandos en <#%s>.", commandChannelID.String()))
		return
	}
	voiceChannelID := *voiceState.ChannelID

	go func() {
		if err := b.resolveAndEnqueue(guildID, voiceChannelID, event.ChannelID, user.Username, user.ID.String(), query); err != nil {
			b.sendChannelMessage(event.ChannelID, "No pude añadir la canción: "+err.Error())
		}
	}()
}

func (b *Bot) resolveAndEnqueue(guildID, voiceChannelID, requestChannelID snowflake.ID, username, requesterID, query string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	type resolveResult struct {
		tracks        []audio.Track
		pendingTracks []spotify.Track
		totalTracks   int
		err           error
	}
	startedAt := time.Now()
	resolved := make(chan resolveResult, 1)
	joined := make(chan error, 1)
	go func() {
		stageStartedAt := time.Now()
		tracks, pendingTracks, totalTracks, err := b.resolveQuery(ctx, query, username, requesterID)
		b.logger.Info("play request stage completed", "stage", "resolve_media", "elapsed", time.Since(stageStartedAt), "error", err)
		resolved <- resolveResult{tracks: tracks, pendingTracks: pendingTracks, totalTracks: totalTracks, err: err}
	}()
	go func() {
		stageStartedAt := time.Now()
		err := b.joinVoice(ctx, guildID, voiceChannelID)
		b.logger.Info("play request stage completed", "stage", "join_voice", "elapsed", time.Since(stageStartedAt), "error", err)
		joined <- err
	}()
	var tracks []audio.Track
	var pendingTracks []spotify.Track
	totalTracks := 0
	mediaResolved, voiceJoined := false, false
	for !mediaResolved || !voiceJoined {
		select {
		case result := <-resolved:
			if result.err != nil {
				cancel()
				return fmt.Errorf("no pude resolver la canción: %w", result.err)
			}
			tracks = result.tracks
			pendingTracks = result.pendingTracks
			totalTracks = result.totalTracks
			mediaResolved = true
		case err := <-joined:
			if err != nil {
				cancel()
				return fmt.Errorf("no pude conectarme al canal de voz. Confirma permisos de acceso, conexión y habla: %w", err)
			}
			voiceJoined = true
		case <-ctx.Done():
			return fmt.Errorf("play request timed out: %w", ctx.Err())
		}
	}
	b.logger.Info("play request resolution completed", "guild_id", guildID, "elapsed", time.Since(startedAt))
	if len(tracks) == 0 {
		return fmt.Errorf("no pude resolver canciones para añadir a la cola")
	}
	var playlistID uint64
	var playlistContext context.Context
	var cancelPlaylist context.CancelFunc
	if len(pendingTracks) > 0 {
		playlistContext, cancelPlaylist, playlistID = b.registerPlaylistLoad(guildID)
	}
	p := b.getPlayer(guildID)
	position, err := p.EnqueueWithPosition(tracks...)
	if err != nil {
		if cancelPlaylist != nil {
			b.finishPlaylistLoad(guildID, playlistID)
			cancelPlaylist()
		}
		return fmt.Errorf("no pude añadir la canción a la cola: %w", err)
	}
	b.logger.Info("track added to playback queue", "guild_id", guildID, "track", tracks[0].Title, "position", position)
	b.cancelLeaveTimer(guildID)
	if cancelPlaylist != nil {
		go b.loadRemainingSpotifyTracks(playlistContext, cancelPlaylist, playlistID, guildID, requestChannelID, pendingTracks, username, requesterID)
	}

	var confirmation *discord.Message
	if len(pendingTracks) > 0 {
		confirmation, err = b.sendPlaylistQueueConfirmation(requestChannelID, tracks[0], position, totalTracks)
	} else {
		confirmation, err = b.sendQueueConfirmation(requestChannelID, tracks[0], position, len(tracks))
	}
	if err != nil {
		b.logger.Error("send queue confirmation", "guild_id", guildID, "error", err)
	} else if err := b.showControlPanel(guildID, confirmation); err != nil {
		b.logger.Error("show playback control panel", "guild_id", guildID, "error", err)
	}
	return nil
}

func (b *Bot) sendQueueConfirmation(channelID snowflake.ID, track audio.Track, position, addedCount int) (*discord.Message, error) {
	message, err := b.client.Rest.CreateMessage(channelID, queueConfirmationMessage(track, position, addedCount))
	if err != nil {
		return nil, fmt.Errorf("create queue confirmation: %w", err)
	}
	return message, nil
}

func (b *Bot) sendPlaylistQueueConfirmation(channelID snowflake.ID, track audio.Track, position, totalCount int) (*discord.Message, error) {
	message, err := b.client.Rest.CreateMessage(channelID, playlistQueueConfirmationMessage(track, position, totalCount))
	if err != nil {
		return nil, fmt.Errorf("create playlist queue confirmation: %w", err)
	}
	return message, nil
}

func queueConfirmationMessage(track audio.Track, position, addedCount int) discord.MessageCreate {
	title := fmt.Sprintf("🎵 Song Added to Queue #%d", position)
	trackTitle := track.Title
	if trackTitle == "" {
		trackTitle = "Untitled track"
	}
	description := escapeMarkdownLink(trackTitle)
	if track.URL != "" {
		description = fmt.Sprintf("[%s](%s)", escapeMarkdownLink(trackTitle), track.URL)
	}
	if addedCount > 1 {
		title = fmt.Sprintf("🎵 %d Songs Added to Queue #%d", addedCount, position)
		description += fmt.Sprintf("\n+ %d more tracks", addedCount-1)
	}
	return discord.NewMessageCreate().WithEmbeds(
		discord.NewEmbed().
			WithTitle(title).
			WithDescription(truncateText(description, 4096)).
			WithFooter("Loading music panel...", ""),
	)
}

func playlistQueueConfirmationMessage(track audio.Track, position, totalCount int) discord.MessageCreate {
	trackTitle := track.Title
	if trackTitle == "" {
		trackTitle = "Untitled track"
	}
	description := escapeMarkdownLink(trackTitle)
	if track.URL != "" {
		description = fmt.Sprintf("[%s](%s)", escapeMarkdownLink(trackTitle), track.URL)
	}
	description += fmt.Sprintf("\nCargando las otras %d canciones en segundo plano.", totalCount-1)
	return discord.NewMessageCreate().WithEmbeds(
		discord.NewEmbed().
			WithTitle(fmt.Sprintf("🎵 Playlist iniciada — posición #%d en cola", position)).
			WithDescription(truncateText(description, 4096)).
			WithFooter("Loading music panel...", ""),
	)
}

func (b *Bot) sendChannelMessage(channelID snowflake.ID, content string) {
	if _, err := b.client.Rest.CreateMessage(channelID, discord.NewMessageCreate().WithContent(content)); err != nil {
		b.logger.Error("send playback request error", "channel_id", channelID, "error", err)
	}
}

func (b *Bot) resolveQuery(ctx context.Context, query, requester, requesterID string) ([]audio.Track, []spotify.Track, int, error) {
	if b.spotify == nil && spotify.IsSpotifyLink(query) {
		return nil, nil, 0, fmt.Errorf("Spotify requiere configurar SPOTIFY_CLIENT_ID y SPOTIFY_CLIENT_SECRET")
	}
	if b.spotify != nil {
		spotifyStartedAt := time.Now()
		spotifyTracks, handled, err := b.spotify.Resolve(ctx, query)
		if handled {
			b.logger.Info("play request stage completed", "stage", "spotify_metadata", "elapsed", time.Since(spotifyStartedAt), "track_count", len(spotifyTracks), "error", err)
		}
		if err != nil {
			return nil, nil, 0, err
		}
		if handled {
			if len(spotifyTracks) == 0 {
				return nil, nil, 0, fmt.Errorf("Spotify no devolvió canciones para esta lista")
			}
			youtubeStartedAt := time.Now()
			resolved, err := resolveSpotifyTracks(ctx, spotifyTracks[:1], func(ctx context.Context, spotifyTrack spotify.Track) (audio.Track, error) {
				searches := spotifySearchQueries(spotifyTrack)
				track, err := b.resolver.ResolveAlternatives(ctx, searches...)
				if err != nil {
					return audio.Track{}, fmt.Errorf("buscar %q en YouTube: %w", strings.Join(searches, " / "), err)
				}
				track.Requester = requester
				track.RequesterID = requesterID
				if track.Artist == "" {
					track.Artist = spotifyTrack.Artist
				}
				return track, nil
			})
			if err != nil {
				b.logger.Info("play request stage completed", "stage", "youtube_search", "elapsed", time.Since(youtubeStartedAt), "track_count", len(resolved), "error", err)
				return nil, nil, 0, err
			}
			b.logger.Info("play request stage completed", "stage", "youtube_search", "elapsed", time.Since(youtubeStartedAt), "track_count", len(resolved))
			return resolved, spotifyTracks[1:], len(spotifyTracks), nil
		}
	}
	youtubeStartedAt := time.Now()
	track, err := b.resolver.Resolve(ctx, query)
	b.logger.Info("play request stage completed", "stage", "youtube_resolve", "elapsed", time.Since(youtubeStartedAt), "error", err)
	if err != nil {
		return nil, nil, 0, err
	}
	track.Requester = requester
	track.RequesterID = requesterID
	return []audio.Track{track}, nil, 1, nil
}

func (b *Bot) registerPlaylistLoad(guildID snowflake.ID) (context.Context, context.CancelFunc, uint64) {
	ctx, cancel := context.WithCancel(context.Background())
	b.mu.Lock()
	b.nextPlaylistID++
	id := b.nextPlaylistID
	if b.playlistLoads[guildID] == nil {
		b.playlistLoads[guildID] = make(map[uint64]context.CancelFunc)
	}
	b.playlistLoads[guildID][id] = cancel
	b.mu.Unlock()
	return ctx, cancel, id
}

func (b *Bot) finishPlaylistLoad(guildID snowflake.ID, id uint64) {
	b.mu.Lock()
	if loads := b.playlistLoads[guildID]; loads != nil {
		delete(loads, id)
		if len(loads) == 0 {
			delete(b.playlistLoads, guildID)
		}
	}
	b.mu.Unlock()
}

func (b *Bot) loadRemainingSpotifyTracks(
	ctx context.Context,
	cancel context.CancelFunc,
	loadID uint64,
	guildID, channelID snowflake.ID,
	spotifyTracks []spotify.Track,
	requester, requesterID string,
) {
	defer cancel()
	defer b.finishPlaylistLoad(guildID, loadID)

	startedAt := time.Now()
	failedCount, err := resolveSpotifyTracksProgressively(ctx, spotifyTracks, func(ctx context.Context, spotifyTrack spotify.Track) (audio.Track, error) {
		searches := spotifySearchQueries(spotifyTrack)
		track, err := b.resolver.ResolveAlternatives(ctx, searches...)
		if err != nil {
			return audio.Track{}, fmt.Errorf("buscar %q en YouTube: %w", strings.Join(searches, " / "), err)
		}
		track.Requester = requester
		track.RequesterID = requesterID
		if track.Artist == "" {
			track.Artist = spotifyTrack.Artist
		}
		return track, nil
	}, func(resolved []audio.Track) error {
		p := b.player(guildID)
		if p == nil {
			return fmt.Errorf("player is unavailable")
		}
		if _, err := p.EnqueueWithPosition(resolved...); err != nil {
			return err
		}
		b.cancelLeaveTimer(guildID)
		b.logger.Info("Spotify playlist tracks added to playback queue", "guild_id", guildID, "added_count", len(resolved))
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		b.logger.Warn("Spotify playlist resolution finished with errors", "guild_id", guildID, "failed_count", failedCount, "error", err)
	}
	b.logger.Info("Spotify playlist resolution completed",
		"guild_id", guildID,
		"track_count", len(spotifyTracks)+1,
		"failed_count", failedCount,
		"elapsed", time.Since(startedAt),
	)
	if ctx.Err() == nil && failedCount > 0 {
		b.sendChannelMessage(channelID, fmt.Sprintf("No pude resolver %d canciones de Spotify en esta lista; agregué a la cola las demás.", failedCount))
	}
}

func spotifySearchQueries(track spotify.Track) []string {
	name := strings.TrimSpace(track.Name)
	artist := strings.TrimSpace(track.Artist)
	if name == "" {
		return nil
	}
	if artist == "" {
		return []string{name}
	}
	return []string{
		artist + " " + name,
		name + " " + artist,
		name,
	}
}

func (b *Bot) skipCommand(event *events.GuildMessageCreate) {
	p := b.player(event.GuildID)
	if p == nil {
		b.sendMessage(event, "No hay ninguna canción en reproducción.")
		return
	}
	if err := p.Skip(); err != nil {
		b.sendMessage(event, err.Error())
		return
	}
	b.sendMessage(event, "Canción saltada.")
}

func (b *Bot) previousCommand(event *events.GuildMessageCreate) {
	p := b.player(event.GuildID)
	if p == nil {
		b.sendMessage(event, "No hay ninguna canción en reproducción.")
		return
	}
	if err := p.Previous(); err != nil {
		b.sendMessage(event, err.Error())
		return
	}
	b.sendMessage(event, "Reproduciendo la canción anterior.")
}

func (b *Bot) pauseCommand(event *events.GuildMessageCreate) {
	p := b.player(event.GuildID)
	if p == nil {
		b.sendMessage(event, "No hay ninguna canción en reproducción.")
		return
	}
	paused, err := p.TogglePause()
	if err != nil {
		b.sendMessage(event, err.Error())
		return
	}
	if paused {
		b.sendMessage(event, "Reproducción pausada.")
		return
	}
	b.sendMessage(event, "Reproducción reanudada.")
}

func (b *Bot) shuffleCommand(event *events.GuildMessageCreate) {
	p := b.player(event.GuildID)
	if p == nil {
		b.sendMessage(event, "No hay canciones en espera para mezclar.")
		return
	}
	if count := p.ShuffleQueue(); count == 0 {
		b.sendMessage(event, "Agrega al menos dos canciones a la cola para mezclarlas.")
		return
	}
	b.sendMessage(event, "Mezclé las canciones que estaban en espera.")
}

func (b *Bot) stopCommand(event *events.GuildMessageCreate) {
	if b.stopGuildWithActor(event.GuildID, event.Message.Author.ID) {
		return
	}
	b.sendStoppedPlaybackMessage(event.ChannelID, event.Message.Author.ID, nil)
}

func (b *Bot) queueCommand(event *events.GuildMessageCreate) {
	p := b.player(event.GuildID)
	if p == nil {
		b.sendMessage(event, "La cola está vacía.")
		return
	}
	snapshot := p.Snapshot()
	lines := make([]string, 0, 11)
	if snapshot.Current != nil {
		lines = append(lines, "**Reproduciendo:** "+snapshot.Current.Title)
	}
	for index, track := range snapshot.Queue {
		if index == 10 {
			lines = append(lines, fmt.Sprintf("… y %d canciones más.", len(snapshot.Queue)-index))
			break
		}
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, track.Title))
	}
	if len(lines) == 0 {
		b.sendMessage(event, "La cola está vacía.")
		return
	}
	b.sendMessage(event, strings.Join(lines, "\n"))
}

func (b *Bot) showControlPanel(guildID snowflake.ID, confirmation *discord.Message) error {
	if confirmation == nil {
		return fmt.Errorf("queue confirmation message is nil")
	}
	timer := time.NewTimer(1200 * time.Millisecond)
	defer timer.Stop()
	<-timer.C

	b.mu.Lock()
	oldPanel, hadOldPanel := b.panels[guildID]

	snapshot := player.Snapshot{Volume: 1}
	if p := b.players[guildID]; p != nil {
		snapshot = p.Snapshot()
	}
	panel := controlPanel{channelID: confirmation.ChannelID, messageID: confirmation.ID}
	b.panels[guildID] = panel
	b.mu.Unlock()

	if err := b.updateMessageToControlPanel(panel, snapshot); err != nil {
		b.mu.Lock()
		if b.panels[guildID] == panel {
			if hadOldPanel {
				b.panels[guildID] = oldPanel
			} else {
				delete(b.panels, guildID)
			}
		}
		b.mu.Unlock()
		return err
	}
	if hadOldPanel {
		if oldPanel != panel {
			if err := b.client.Rest.DeleteMessage(oldPanel.channelID, oldPanel.messageID); err != nil {
				b.logger.Error("delete previous playback control panel", "guild_id", guildID, "error", err)
			}
		}
	}
	return nil
}

func (b *Bot) onComponent(event *events.ComponentInteractionCreate) {
	if event.Data.Type() != discord.ComponentTypeButton {
		return
	}
	action := event.ButtonInteractionData().CustomID()
	if !strings.HasPrefix(action, "music:") {
		return
	}
	guildID := event.GuildID()
	if guildID == nil {
		b.componentError(event, "Estos controles solo funcionan dentro de un servidor.")
		return
	}
	b.mu.Lock()
	panel, exists := b.panels[*guildID]
	voiceChannelID := b.channels[*guildID]
	b.mu.Unlock()
	if !exists || panel.messageID != event.Message.ID {
		b.componentError(event, "Este panel ya no está activo. Usa `!play` para crear uno nuevo.")
		return
	}

	voiceState, inVoice := event.Client().Caches.VoiceState(*guildID, event.User().ID)
	if !inVoice || voiceState.ChannelID == nil || *voiceState.ChannelID != voiceChannelID {
		b.componentError(event, "Debes estar en el mismo canal de voz que el bot para usar estos controles.")
		return
	}
	if err := event.DeferUpdateMessage(); err != nil {
		b.logger.Error("acknowledge playback control", "error", err)
		return
	}

	switch strings.TrimPrefix(action, "music:") {
	case "pause":
		p := b.player(*guildID)
		if p == nil {
			b.componentFollowupError(event, "No hay ninguna canción en reproducción.")
			return
		}
		if _, err := p.TogglePause(); err != nil {
			b.componentFollowupError(event, err.Error())
			return
		}
		b.refreshControlPanel(*guildID)
	case "stop":
		b.stopGuildWithActor(*guildID, event.User().ID)
	case "next":
		p := b.player(*guildID)
		if p == nil {
			b.componentFollowupError(event, "No hay ninguna canción para saltar.")
			return
		}
		if err := p.Skip(); err != nil {
			b.componentFollowupError(event, err.Error())
		}
	case "previous":
		p := b.player(*guildID)
		if p == nil {
			b.componentFollowupError(event, "No hay canciones anteriores.")
			return
		}
		if err := p.Previous(); err != nil {
			b.componentFollowupError(event, err.Error())
		}
	case "shuffle":
		p := b.player(*guildID)
		if p == nil || p.ShuffleQueue() == 0 {
			b.componentFollowupError(event, "Agrega al menos dos canciones en espera para mezclarlas.")
		}
	case "loop":
		p := b.player(*guildID)
		if p == nil {
			b.componentFollowupError(event, "No hay un reproductor activo.")
			return
		}
		p.ToggleLoopTrack()
	case "autoplay":
		p := b.player(*guildID)
		if p == nil {
			b.componentFollowupError(event, "No hay un reproductor activo.")
			return
		}
		p.ToggleAutoPlay()
	case "playlist":
		p := b.player(*guildID)
		if p == nil {
			b.componentFollowup(event, "La cola está vacía.")
			return
		}
		b.componentFollowup(event, renderPlaylist(p.Snapshot()))
	default:
		b.componentFollowupError(event, "Acción de control desconocida.")
	}
	b.refreshControlPanel(*guildID)
}

func (b *Bot) componentFollowup(event *events.ComponentInteractionCreate, content string) {
	if _, err := event.Client().Rest.CreateFollowupMessage(
		event.ApplicationID(),
		event.Token(),
		discord.NewMessageCreate().WithEphemeral(true).WithContent(content),
	); err != nil {
		b.logger.Error("send playback control follow-up", "error", err)
	}
}

func (b *Bot) componentError(event *events.ComponentInteractionCreate, content string) {
	if err := event.CreateMessage(discord.NewMessageCreate().WithEphemeral(true).WithContent(content)); err != nil {
		b.logger.Error("respond to playback control", "error", err)
	}
}

func (b *Bot) componentFollowupError(event *events.ComponentInteractionCreate, content string) {
	b.componentFollowup(event, content)
}

func (b *Bot) refreshControlPanel(guildID snowflake.ID) {
	b.mu.Lock()
	state := b.panelRefreshes[guildID]
	if state == nil {
		state = &panelRefreshState{}
		b.panelRefreshes[guildID] = state
	}
	state.pending = true
	if state.running {
		b.mu.Unlock()
		return
	}
	state.running = true
	b.mu.Unlock()

	go b.runControlPanelRefresh(guildID)
}

func (b *Bot) runControlPanelRefresh(guildID snowflake.ID) {
	for {
		time.Sleep(100 * time.Millisecond)

		b.mu.Lock()
		state := b.panelRefreshes[guildID]
		if state == nil {
			b.mu.Unlock()
			return
		}
		state.pending = false
		panel, exists := b.panels[guildID]
		b.mu.Unlock()

		if exists {
			b.updateControlPanel(guildID, panel, "")
		}

		b.mu.Lock()
		state = b.panelRefreshes[guildID]
		if state == nil {
			b.mu.Unlock()
			return
		}
		if state.pending {
			b.mu.Unlock()
			continue
		}
		delete(b.panelRefreshes, guildID)
		b.mu.Unlock()
		return
	}
}

func (b *Bot) updateControlPanel(guildID snowflake.ID, panel controlPanel, note string) {
	b.mu.Lock()
	currentPanel, exists := b.panels[guildID]
	p := b.players[guildID]
	b.mu.Unlock()
	if !exists || currentPanel != panel {
		return
	}
	snapshot := player.Snapshot{Volume: 1}
	if p != nil {
		snapshot = p.Snapshot()
	}
	buttonRows := controlButtons(snapshot)
	update := discord.NewMessageUpdate().
		ClearContent().
		WithEmbeds(renderControlEmbed(note, snapshot)).
		WithComponents(
			discord.NewActionRow(buttonRows[0]...),
			discord.NewActionRow(buttonRows[1]...),
		)
	if _, err := b.client.Rest.UpdateMessage(panel.channelID, panel.messageID, update); err != nil {
		b.logger.Error("update playback control panel", "error", err)
	}
}

func (b *Bot) updateMessageToControlPanel(panel controlPanel, snapshot player.Snapshot) error {
	buttonRows := controlButtons(snapshot)
	update := discord.NewMessageUpdate().
		ClearContent().
		WithEmbeds(renderControlEmbed("", snapshot)).
		WithComponents(
			discord.NewActionRow(buttonRows[0]...),
			discord.NewActionRow(buttonRows[1]...),
		)
	if _, err := b.client.Rest.UpdateMessage(panel.channelID, panel.messageID, update); err != nil {
		return fmt.Errorf("update queue confirmation to music panel: %w", err)
	}
	return nil
}

func truncateDiscordContent(content string) string {
	const maxRunes = 1900
	runes := []rune(content)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes-1]) + "…"
	}
	return content
}

func renderControlPanel(note string, snapshot player.Snapshot) string {
	var content strings.Builder
	if note != "" {
		content.WriteString(note)
		content.WriteString("\n")
	}
	switch {
	case snapshot.Current != nil:
		fmt.Fprintf(&content, "🎵 **Reproduciendo:** %s\n", snapshot.Current.Title)
	case len(snapshot.Queue) > 0:
		content.WriteString("⏳ Preparando la siguiente canción...\n")
	default:
		content.WriteString("La reproducción está detenida. Usa `!play` para agregar canciones.\n")
	}
	if snapshot.Paused {
		content.WriteString("⏸ Pausado\n")
	}
	if len(snapshot.Queue) > 0 {
		fmt.Fprintf(&content, "\n**En espera (%d):**\n", len(snapshot.Queue))
		for index, track := range snapshot.Queue {
			if index == 5 {
				fmt.Fprintf(&content, "… y %d más\n", len(snapshot.Queue)-index)
				break
			}
			fmt.Fprintf(&content, "%d. %s\n", index+1, track.Title)
		}
	}
	loopStatus, autoplayStatus := "Desactivado", "Desactivado"
	if snapshot.LoopTrack {
		loopStatus = "Activado"
	}
	if snapshot.AutoPlay {
		autoplayStatus = "Activado"
	}
	fmt.Fprintf(&content, "\n🔊 **Volumen:** %.0f%% · 🔁 Loop: %s · 🔄 AutoPlay: %s\n", snapshot.Volume*100, loopStatus, autoplayStatus)
	return strings.TrimSpace(content.String())
}

func stoppedPlaybackEmbed(stoppedBy snowflake.ID, track *audio.Track) discord.Embed {
	var description strings.Builder
	if stoppedBy != 0 {
		fmt.Fprintf(&description, "✅ <@%s> Stopped", stoppedBy.String())
	} else {
		description.WriteString("⏹ Playback stopped")
	}
	if track != nil && track.Title != "" {
		description.WriteByte(' ')
		if track.URL != "" {
			fmt.Fprintf(&description, "[%s](%s)", escapeMarkdownLink(track.Title), track.URL)
		} else {
			description.WriteString(escapeMarkdownLink(track.Title))
		}
	}
	return discord.NewEmbed().
		WithDescription(truncateDiscordContent(description.String())).
		WithColor(0x2B2D31)
}

func renderControlEmbed(note string, snapshot player.Snapshot) discord.Embed {
	description := strings.Builder{}
	if note != "" {
		description.WriteString(note)
		description.WriteString("\n")
	}
	if snapshot.Current != nil {
		fmt.Fprintf(&description, "🎵 %s", formatTrackTitle(*snapshot.Current))
	} else if len(snapshot.Queue) > 0 {
		fmt.Fprintf(&description, "⏳ Preparing: %s", formatTrackTitle(snapshot.Queue[0]))
	} else if note == "" {
		description.WriteString("Use `!play` to add a song.")
	}
	if snapshot.Paused {
		description.WriteString("\n⏸ Paused")
	}
	queueStart := 0
	if snapshot.Current == nil && len(snapshot.Queue) > 0 {
		queueStart = 1
	}
	if len(snapshot.Queue) > queueStart {
		description.WriteString("\n\n**Up Next:**")
		for index, track := range snapshot.Queue[queueStart:] {
			if index == 5 {
				fmt.Fprintf(&description, "\n… and %d more", len(snapshot.Queue)-queueStart-index)
				break
			}
			fmt.Fprintf(&description, "\n%d. %s", index+1, track.Title)
		}
	}
	embed := discord.NewEmbed().
		WithAuthor("MUSIC PANEL", "", "").
		WithDescription(truncateDiscordContent(strings.TrimSpace(description.String()))).
		WithColor(0x2B2D31)
	duration, artist := "—", "—"
	if track := displayTrack(snapshot); track != nil {
		duration = formatTrackDuration(track.Duration)
		if track.Artist != "" {
			artist = truncateText(track.Artist, 1024)
		}
	}
	embed = embed.
		AddField("🙋 Requested By", formatRequesterLink(displayTrack(snapshot)), true).
		AddField("🎼 Music Duration", duration, true).
		AddField("🎙️ Music Author", artist, true)
	loopStatus, autoplayStatus := "Off", "Off"
	if snapshot.LoopTrack {
		loopStatus = "On"
	}
	if snapshot.AutoPlay {
		autoplayStatus = "On"
	}
	return embed.WithFooter(
		fmt.Sprintf("🔊 Volume %.0f%%   ·   🔁 Loop %s   ·   🔄 AutoPlay %s", snapshot.Volume*100, loopStatus, autoplayStatus),
		"",
	)
}

func displayTrack(snapshot player.Snapshot) *audio.Track {
	if snapshot.Current != nil {
		return snapshot.Current
	}
	if len(snapshot.Queue) > 0 {
		return &snapshot.Queue[0]
	}
	return nil
}

func formatRequester(track audio.Track) string {
	if track.Requester != "" {
		if track.Requester == "AutoPlay" {
			return track.Requester
		}
		return "@" + track.Requester
	}
	if track.RequesterID != "" {
		return "<@" + track.RequesterID + ">"
	}
	return "—"
}

func formatRequesterLink(track *audio.Track) string {
	if track == nil {
		return "—"
	}
	if track.RequesterID == "" || track.Requester == "AutoPlay" {
		return formatRequester(*track)
	}
	requester := track.Requester
	if requester == "" {
		requester = "user"
	}
	return fmt.Sprintf("[%s](https://discord.com/users/%s)", escapeMarkdownLink("@"+requester), track.RequesterID)
}

func formatTrackTitle(track audio.Track) string {
	if track.URL == "" {
		return fmt.Sprintf("`%s`", escapeInlineCode(track.Title))
	}
	return fmt.Sprintf("[%s](%s)", escapeMarkdownLink(track.Title), track.URL)
}

func formatTrackDuration(duration time.Duration) string {
	if duration <= 0 {
		return "Unknown"
	}
	totalMinutes := int(duration.Round(time.Second).Minutes())
	hours := totalMinutes / 60
	minutes := totalMinutes % 60
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	seconds := int(duration.Round(time.Second).Seconds()) % 60
	if minutes > 0 {
		if seconds == 0 {
			return fmt.Sprintf("%dm", minutes)
		}
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

func escapeInlineCode(value string) string {
	return strings.ReplaceAll(value, "`", "ˋ")
}

func escapeMarkdownLink(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]")
	return replacer.Replace(value)
}

func truncateText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

func renderPlaylist(snapshot player.Snapshot) string {
	lines := make([]string, 0, len(snapshot.Queue)+1)
	if snapshot.Current != nil {
		lines = append(lines, "🎵 **Reproduciendo:** "+snapshot.Current.Title)
	}
	for index, track := range snapshot.Queue {
		if index == 20 {
			lines = append(lines, fmt.Sprintf("… y %d canciones más.", len(snapshot.Queue)-index))
			break
		}
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, track.Title))
	}
	if len(lines) == 0 {
		return "La cola está vacía."
	}
	return truncateDiscordContent(strings.Join(lines, "\n"))
}

func controlButtons(snapshot player.Snapshot) [][]discord.InteractiveComponent {
	pauseEmoji := "⏸️"
	if snapshot.Paused {
		pauseEmoji = "▶️"
	}
	loopButton := discord.NewSecondaryButton("", "music:loop")
	if snapshot.LoopTrack {
		loopButton = discord.NewSuccessButton("", "music:loop")
	}
	autoPlayButton := discord.NewSecondaryButton("", "music:autoplay")
	if snapshot.AutoPlay {
		autoPlayButton = discord.NewSuccessButton("", "music:autoplay")
	}
	return [][]discord.InteractiveComponent{
		{
			discord.NewSecondaryButton("", "music:previous").WithEmoji(discord.NewComponentEmoji("⏮️")),
			discord.NewSecondaryButton("", "music:pause").WithEmoji(discord.NewComponentEmoji(pauseEmoji)),
			discord.NewSecondaryButton("", "music:next").WithEmoji(discord.NewComponentEmoji("⏭️")),
			discord.NewSecondaryButton("", "music:playlist").WithEmoji(discord.NewComponentEmoji("📃")),
		},
		{
			discord.NewSecondaryButton("", "music:shuffle").WithEmoji(discord.NewComponentEmoji("🔀")),
			loopButton.WithEmoji(discord.NewComponentEmoji("🔁")),
			discord.NewSecondaryButton("", "music:stop").WithEmoji(discord.NewComponentEmoji("⏹️")),
			autoPlayButton.WithEmoji(discord.NewComponentEmoji("🔄")),
		},
	}
}

func (b *Bot) sendMessage(event *events.GuildMessageCreate, content string) (*discord.Message, error) {
	message, err := event.Client().Rest.CreateMessage(event.ChannelID, discord.NewMessageCreate().WithContent(content))
	if err != nil {
		b.logger.Error("send command response", "error", err)
		return nil, err
	}
	return message, nil
}

func (b *Bot) getPlayer(guildID snowflake.ID) *player.Player {
	b.mu.Lock()
	if existing := b.players[guildID]; existing != nil {
		b.mu.Unlock()
		return existing
	}
	p := player.New(func(ctx context.Context, track audio.Track, controls audio.PlaybackControls) error {
		connection := b.voiceConnection(guildID)
		if connection == nil {
			return fmt.Errorf("voice connection is unavailable")
		}
		b.logger.Info("starting track playback", "guild_id", guildID, "track", track.Title)
		var wasHolding atomic.Bool
		var daveStateLogged atomic.Bool
		controls.HoldFrames = func() bool {
			daveSession := b.daveSession(guildID)
			if !daveStateLogged.Swap(true) {
				if daveSession == nil {
					b.logger.Error("DAVE session unavailable while starting audio", "guild_id", guildID)
				} else {
					state := daveSession.State()
					b.logger.Info("DAVE audio gate state",
						"guild_id", guildID,
						"ready", state.Ready,
						"protocol_version", state.ProtocolVersion,
						"epoch_id", state.EpochID,
						"degraded_since", state.DegradedSince,
					)
				}
			}
			holding := daveSession == nil || daveSession.ShouldHoldFrames()
			if holding && !wasHolding.Swap(true) {
				b.logger.Info("holding audio frames until DAVE is ready", "guild_id", guildID, "track", track.Title)
			} else if !holding && wasHolding.Swap(false) {
				b.logger.Info("DAVE ready; resuming audio frames", "guild_id", guildID, "track", track.Title)
			}
			return holding
		}
		return b.streamer.PlayWithFallbacks(ctx, connection, track, controls)
	}, func(err error) {
		b.logger.Error("track playback failed", "guild_id", guildID, "error", err)
	})
	p.SetAutoPlayFunc(func(ctx context.Context, previous audio.Track) (audio.Track, error) {
		track, err := b.resolver.ResolveRelated(ctx, previous)
		if err != nil {
			return audio.Track{}, err
		}
		track.Requester = "AutoPlay"
		return track, nil
	})
	b.players[guildID] = p
	b.mu.Unlock()
	p.SetOnChange(func(_ player.Snapshot) {
		b.refreshControlPanel(guildID)
	})
	return p
}

func (b *Bot) player(guildID snowflake.ID) *player.Player {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.players[guildID]
}

func (b *Bot) voiceConnection(guildID snowflake.ID) voice.Conn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connections[guildID]
}

func (b *Bot) daveSession(guildID snowflake.ID) *session.Session {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.daveSessions[guildID]
}

func (b *Bot) joinVoice(ctx context.Context, guildID, channelID snowflake.ID) error {
	b.mu.Lock()
	lock := b.joinLocks[guildID]
	if lock == nil {
		lock = &sync.Mutex{}
		b.joinLocks[guildID] = lock
	}
	b.mu.Unlock()

	lock.Lock()
	defer lock.Unlock()

	b.mu.Lock()
	current := b.connections[guildID]
	currentChannel := b.channels[guildID]
	b.mu.Unlock()
	if current != nil && currentChannel == channelID {
		return nil
	}
	p := b.player(guildID)
	resumePlayback := func() {}
	if p != nil {
		resumePlayback = p.BeginConnectionMove()
		defer resumePlayback()
	}
	if current != nil {
		current.Close(ctx)
		b.mu.Lock()
		delete(b.connections, guildID)
		delete(b.channels, guildID)
		daveSession := b.daveSessions[guildID]
		delete(b.daveSessions, guildID)
		b.mu.Unlock()
		if daveSession != nil {
			daveSession.Close()
		}
	}

	connection := b.client.VoiceManager.CreateConn(guildID)
	joinCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := connection.Open(joinCtx, channelID, false, true); err != nil {
		connection.Close(context.Background())
		b.mu.Lock()
		daveSession := b.daveSessions[guildID]
		delete(b.daveSessions, guildID)
		b.mu.Unlock()
		if daveSession != nil {
			daveSession.Close()
		}
		return fmt.Errorf("open DAVE voice connection: %w", err)
	}

	b.mu.Lock()
	b.connections[guildID] = connection
	b.channels[guildID] = channelID
	b.mu.Unlock()
	b.logger.Info("voice connection opened", "guild_id", guildID, "channel_id", channelID)
	return nil
}

func (b *Bot) stopGuild(guildID snowflake.ID) {
	b.stopGuildWithActor(guildID, 0)
}

func (b *Bot) stopGuildWithActor(guildID, stoppedBy snowflake.ID) bool {
	b.mu.Lock()
	for _, cancel := range b.playlistLoads[guildID] {
		cancel()
	}
	delete(b.playlistLoads, guildID)
	p := b.players[guildID]
	panel := b.panels[guildID]
	delete(b.players, guildID)
	connection := b.connections[guildID]
	delete(b.connections, guildID)
	daveSession := b.daveSessions[guildID]
	delete(b.daveSessions, guildID)
	delete(b.channels, guildID)
	if timer := b.leaveTimers[guildID]; timer != nil {
		timer.Stop()
		delete(b.leaveTimers, guildID)
	}
	delete(b.panels, guildID)
	delete(b.commandChannels, guildID)
	b.mu.Unlock()

	snapshot := player.Snapshot{}
	if p != nil {
		snapshot = p.Snapshot()
	}
	track := displayTrack(snapshot)
	if daveSession != nil {
		daveSession.Close()
	}
	if p != nil {
		p.Close()
	}
	if connection != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		connection.Close(closeCtx)
		cancel()
	}
	if panel.messageID != 0 {
		if _, err := b.client.Rest.UpdateMessage(panel.channelID, panel.messageID,
			discord.NewMessageUpdate().
				ClearContent().
				WithEmbeds(stoppedPlaybackEmbed(stoppedBy, track)).
				ClearComponents(),
		); err != nil {
			b.logger.Error("disable stopped playback controls", "error", err)
		}
		return true
	}
	return false
}

func (b *Bot) sendStoppedPlaybackMessage(channelID, stoppedBy snowflake.ID, track *audio.Track) {
	if _, err := b.client.Rest.CreateMessage(channelID, discord.NewMessageCreate().WithEmbeds(stoppedPlaybackEmbed(stoppedBy, track))); err != nil {
		b.logger.Error("send stopped playback message", "channel_id", channelID, "error", err)
	}
}

func (b *Bot) onVoiceStateUpdate(event *events.GuildVoiceStateUpdate) {
	state := event.VoiceState
	if state.UserID == event.Client().ID() {
		b.logger.Info("bot voice state updated",
			"guild_id", state.GuildID,
			"connected", state.ChannelID != nil,
			"channel_id", state.ChannelID,
		)
	}
	b.mu.Lock()
	channelID := b.channels[state.GuildID]
	b.mu.Unlock()
	if channelID == 0 {
		return
	}
	if b.humanCount(state.GuildID, channelID) == 0 {
		b.scheduleLeave(state.GuildID)
	} else {
		b.cancelLeaveTimer(state.GuildID)
	}
}

func (b *Bot) humanCount(guildID, channelID snowflake.ID) int {
	humans := 0
	for state := range b.client.Caches.VoiceStates(guildID) {
		if state.ChannelID != nil && *state.ChannelID == channelID && state.UserID != b.client.ID() {
			humans++
		}
	}
	return humans
}

func (b *Bot) scheduleLeave(guildID snowflake.ID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.leaveTimers[guildID] != nil {
		return
	}
	b.leaveTimers[guildID] = time.AfterFunc(time.Duration(b.cfg.AutoLeaveSeconds)*time.Second, func() {
		b.mu.Lock()
		delete(b.leaveTimers, guildID)
		channelID := b.channels[guildID]
		b.mu.Unlock()
		if channelID != 0 && b.humanCount(guildID, channelID) == 0 {
			b.logger.Info("leaving empty voice channel", "guild_id", guildID)
			b.stopGuild(guildID)
		}
	})
}

func (b *Bot) cancelLeaveTimer(guildID snowflake.ID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if timer := b.leaveTimers[guildID]; timer != nil {
		timer.Stop()
		delete(b.leaveTimers, guildID)
	}
}

package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"discord-music-bot/internal/audio"
	"discord-music-bot/internal/config"
	"discord-music-bot/internal/player"
	"discord-music-bot/internal/spotify"
	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/thomas-vilte/dave-go/session"
)

const maxConcurrentTrackPrefetches = 2

type Bot struct {
	cfg      config.Config
	logger   *slog.Logger
	client   *bot.Client
	resolver *audio.Resolver
	streamer *audio.Streamer
	spotify  *spotify.Client

	prefetchSlots chan struct{}

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
		prefetchSlots:   make(chan struct{}, maxConcurrentTrackPrefetches),
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
	application.streamer.Resolver = application.resolver
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
		bot.WithEventListenerFunc(application.onApplicationCommand),
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
	if err := b.client.SetPresence(ctx, gateway.WithListeningActivity("/play")); err != nil {
		b.logger.Error("set listening activity", "error", err)
	}
	if _, err := b.client.Rest.SetGlobalCommands(b.client.ApplicationID, slashCommands()); err != nil {
		b.shutdown(server)
		return fmt.Errorf("register Discord slash commands: %w", err)
	}
	b.logger.Info("registered global Discord slash commands", "count", len(slashCommands()))
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

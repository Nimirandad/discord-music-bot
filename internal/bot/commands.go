package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"discord-music-bot/internal/audio"
	"discord-music-bot/internal/spotify"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

func (b *Bot) onMessage(event *events.GuildMessageCreate) {
	if event.Message.Author.Bot {
		return
	}
	command, args, ok := parseCommand(event.Message.Content)
	if !ok {
		return
	}
	if command == "help" || command == "ayuda" {
		b.sendMessage(event, helpMessage())
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
		b.queueCommand(event, args)
	case "remove", "move", "clearqueue", "skipto":
		b.manageQueueCommand(event, command, args)
	}
}

func isMusicCommand(command string) bool {
	switch command {
	case "join", "play", "skip", "previous", "pause", "shuffle", "stop", "leave", "queue", "remove", "move", "clearqueue", "skipto":
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
			b.sendChannelMessage(requestChannelID, "No pude conectarme al canal de voz. Comprueba mis permisos de acceso y conexión. "+userFacingError(err))
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
			b.logger.Error("play request failed", "guild_id", guildID)
		}
	}()
}

func (b *Bot) resolveAndEnqueue(guildID, voiceChannelID, requestChannelID snowflake.ID, username, requesterID, query string) (resultErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	stopTyping := b.startLoadingTyping(ctx, requestChannelID)
	defer stopTyping()
	var loadingMessage *discord.Message
	defer func() {
		if resultErr == nil {
			return
		}
		content := "No pude añadir la canción: " + userFacingError(resultErr)
		if loadingMessage != nil {
			if _, err := b.client.Rest.UpdateMessage(requestChannelID, loadingMessage.ID, discord.NewMessageUpdate().WithContent(content).WithEmbeds()); err == nil {
				return
			}
		}
		b.sendChannelMessage(requestChannelID, content)
	}()
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
	loadingMessage, _ = b.client.Rest.CreateMessage(requestChannelID, loadingMusicMessage())
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
	if loadingMessage != nil {
		payload := queueConfirmationMessage(tracks[0], position, len(tracks))
		if len(pendingTracks) > 0 {
			payload = playlistQueueConfirmationMessage(tracks[0], position, totalTracks)
		}
		confirmation, err = b.client.Rest.UpdateMessage(requestChannelID, loadingMessage.ID, discord.NewMessageUpdate().WithContent("").WithEmbeds(payload.Embeds...))
	} else if len(pendingTracks) > 0 {
		confirmation, err = b.sendPlaylistQueueConfirmation(requestChannelID, tracks[0], position, totalTracks)
	} else {
		confirmation, err = b.sendQueueConfirmation(requestChannelID, tracks[0], position, len(tracks))
	}
	if err != nil {
		b.logger.Error("send queue confirmation", "guild_id", guildID, "error", err)
	} else if err := b.showControlPanel(guildID, confirmation); err != nil {
		b.logger.Error("show playback control panel", "guild_id", guildID, "error", err)
	}
	if position == 1 {
		waitForPlaybackStart(ctx, p.Snapshot, tracks[0].URL)
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
	trackTitle := displayMusicTitle(track)
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
	trackTitle := displayMusicTitle(track)
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
				track.SongTitle = spotifyTrack.Name
				if spotifyTrack.Artist != "" {
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
		track.SongTitle = spotifyTrack.Name
		if spotifyTrack.Artist != "" {
			track.Artist = spotifyTrack.Artist
		}
		return track, nil
	}, func(resolved []audio.Track) error {
		p := b.player(guildID)
		if p == nil {
			return fmt.Errorf("player is unavailable")
		}
		if _, err := p.EnqueueWithPositionContext(ctx, resolved...); err != nil {
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
		b.sendMessage(event, userFacingError(err))
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
		b.sendMessage(event, userFacingError(err))
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
		b.sendMessage(event, userFacingError(err))
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

func (b *Bot) queueCommand(event *events.GuildMessageCreate, args string) {
	page, err := parseQueuePage(args)
	if err != nil {
		b.sendMessage(event, err.Error())
		return
	}
	p := b.player(event.GuildID)
	if p == nil {
		b.sendMessage(event, "La cola está vacía.")
		return
	}
	content, err := renderQueue(p.Snapshot(), page)
	if err != nil {
		b.sendMessage(event, err.Error())
		return
	}
	b.sendMessage(event, content)
}

func (b *Bot) sendMessage(event *events.GuildMessageCreate, content string) (*discord.Message, error) {
	message, err := event.Client().Rest.CreateMessage(event.ChannelID, discord.NewMessageCreate().WithContent(content))
	if err != nil {
		b.logger.Error("send command response", "error", err)
		return nil, err
	}
	return message, nil
}

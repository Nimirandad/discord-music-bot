package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"discord-music-bot/internal/audio"
	"discord-music-bot/internal/spotify"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

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

package bot

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"discord-music-bot/internal/audio"
	"discord-music-bot/internal/player"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/thomas-vilte/dave-go/session"
)

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
	p.SetPrefetchFunc(func(ctx context.Context, next audio.Track) error {
		select {
		case b.prefetchSlots <- struct{}{}:
			defer func() { <-b.prefetchSlots }()
		case <-ctx.Done():
			return ctx.Err()
		}
		startedAt := time.Now()
		err := b.resolver.Prepare(ctx, next.URL)
		if ctx.Err() == nil {
			if err != nil {
				b.logger.Debug("next track metadata prefetch failed", "elapsed", time.Since(startedAt))
			} else {
				b.logger.Info("next track metadata prefetched", "elapsed", time.Since(startedAt))
			}
		}
		return err
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

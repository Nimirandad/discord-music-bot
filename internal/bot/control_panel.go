package bot

import (
	"fmt"
	"strings"
	"time"

	"discord-music-bot/internal/audio"
	"discord-music-bot/internal/player"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

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
		b.componentError(event, "Este panel ya no está activo. Usa `/play` para crear uno nuevo.")
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
			fmt.Fprintf(&description, "[%s](%s)", escapeMarkdownLink(displayMusicTitle(*track)), track.URL)
		} else {
			description.WriteString(escapeMarkdownLink(displayMusicTitle(*track)))
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
		if snapshot.Loading {
			fmt.Fprintf(&description, "⏳ Cargando audio: %s", formatTrackTitle(*snapshot.Current))
		} else {
			fmt.Fprintf(&description, "🎵 %s", formatTrackTitle(*snapshot.Current))
		}
	} else if len(snapshot.Queue) > 0 {
		fmt.Fprintf(&description, "⏳ Preparing: %s", formatTrackTitle(snapshot.Queue[0]))
	} else if note == "" {
		description.WriteString("Use `/play` to add a song.")
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
			fmt.Fprintf(&description, "\n%d. %s", index+1, displayMusicTitle(track))
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
			artist = truncateText(cleanMusicArtist(track.Artist), 1024)
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
		return fmt.Sprintf("`%s`", escapeInlineCode(displayMusicTitle(track)))
	}
	return fmt.Sprintf("[%s](%s)", escapeMarkdownLink(displayMusicTitle(track)), track.URL)
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
		lines = append(lines, "🎵 **Reproduciendo:** "+displayMusicTitle(*snapshot.Current))
	}
	for index, track := range snapshot.Queue {
		if index == 20 {
			lines = append(lines, fmt.Sprintf("… y %d canciones más.", len(snapshot.Queue)-index))
			break
		}
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, displayMusicTitle(track)))
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

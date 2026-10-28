package bot

import (
	"strings"
	"testing"
	"time"

	"discord-music-bot/internal/audio"
	"discord-music-bot/internal/player"
	"github.com/disgoorg/disgo/discord"
)

func TestControlButtonsReflectPlayerState(t *testing.T) {
	snapshot := player.Snapshot{
		Current:     &audio.Track{Title: "Current track"},
		Queue:       []audio.Track{{Title: "Next track"}, {Title: "Another track"}},
		Paused:      true,
		CanPrevious: true,
		Volume:      1.2,
	}
	rows := controlButtons(snapshot)
	if len(rows) != 2 {
		t.Fatalf("controlButtons() returned %d rows, want 2", len(rows))
	}
	if len(rows[0]) != 4 || len(rows[1]) != 4 {
		t.Fatalf("controlButtons() returned row sizes %v, want two rows of four", []int{len(rows[0]), len(rows[1])})
	}

	wantIDs := []string{
		"music:previous",
		"music:pause",
		"music:next",
		"music:playlist",
		"music:shuffle",
		"music:loop",
		"music:stop",
		"music:autoplay",
	}
	for index, wantID := range wantIDs {
		row, column := index/4, index%4
		button := rows[row][column].(discord.ButtonComponent)
		if button.CustomID != wantID {
			t.Errorf("button %d custom ID = %q, want %q", index, button.CustomID, wantID)
		}
		if button.Disabled {
			t.Errorf("button %q should remain enabled", wantID)
		}
		if (wantID == "music:pause" || wantID == "music:stop") && button.Style != discord.ButtonStyleSecondary {
			t.Errorf("button %q style = %v, want neutral secondary style", wantID, button.Style)
		}
	}
	if got := rows[0][1].(discord.ButtonComponent).Emoji.Name; got != "▶️" {
		t.Errorf("pause button emoji = %q, want ▶️ while paused", got)
	}
	wantEmojis := []string{"⏮️", "▶️", "⏭️", "📃", "🔀", "🔁", "⏹️", "🔄"}
	for index, wantEmoji := range wantEmojis {
		button := rows[index/4][index%4].(discord.ButtonComponent)
		if button.Label != "" {
			t.Errorf("button %q label = %q, want emoji-only control", button.CustomID, button.Label)
		}
		if button.Emoji == nil || button.Emoji.Name != wantEmoji {
			t.Errorf("button %q emoji = %+v, want %q", button.CustomID, button.Emoji, wantEmoji)
		}
	}
}

func TestControlEmbedMatchesPlaybackCard(t *testing.T) {
	embed := renderControlEmbed("", player.Snapshot{
		Current: &audio.Track{
			Title:       "Current track",
			URL:         "https://www.youtube.com/watch?v=abc123",
			Artist:      "Music author",
			Duration:    2*time.Hour + 7*time.Minute,
			Requester:   "Shichimen",
			RequesterID: "1234",
		},
		Volume: 1.2,
	})
	if embed.Author == nil || embed.Author.Name != "MUSIC PANEL" {
		t.Fatalf("embed author = %+v, want MUSIC PANEL", embed.Author)
	}
	if !strings.Contains(embed.Description, "[Current track](https://www.youtube.com/watch?v=abc123)") {
		t.Errorf("embed description = %q, want current title linked to its media URL", embed.Description)
	}
	wantFields := []struct {
		name  string
		value string
	}{
		{"🙋 Requested By", "[@Shichimen](https://discord.com/users/1234)"},
		{"🎼 Music Duration", "2h 7m"},
		{"🎙️ Music Author", "Music author"},
	}
	if len(embed.Fields) != len(wantFields) {
		t.Fatalf("embed has %d fields, want %d", len(embed.Fields), len(wantFields))
	}
	for index, want := range wantFields {
		if embed.Fields[index].Name != want.name || embed.Fields[index].Value != want.value {
			t.Errorf("embed field %d = %+v, want name %q and value %q", index, embed.Fields[index], want.name, want.value)
		}
	}
	if !strings.Contains(embed.Footer.Text, "Volume 120%") {
		t.Errorf("embed footer = %q, want volume status", embed.Footer.Text)
	}
}

func TestControlEmbedShowsQueuedTrackMetadataBeforePlaybackStarts(t *testing.T) {
	embed := renderControlEmbed("", player.Snapshot{
		Queue: []audio.Track{{
			Title:       "Queued song",
			URL:         "https://www.youtube.com/watch?v=queued",
			Artist:      "Queued author",
			Duration:    12 * time.Minute,
			Requester:   "Requester",
			RequesterID: "5678",
		}},
		Volume: 1,
	})
	if !strings.Contains(embed.Description, "Queued song") {
		t.Errorf("embed description = %q, want queued song title", embed.Description)
	}
	if !strings.Contains(embed.Description, "[Queued song](https://www.youtube.com/watch?v=queued)") {
		t.Errorf("embed description = %q, want queued title linked to its media URL", embed.Description)
	}
	if got := embed.Fields[0].Value; got != "[@Requester](https://discord.com/users/5678)" {
		t.Errorf("requester field = %q, want requester linked to their Discord profile", got)
	}
	if got := embed.Fields[1].Value; got != "12m" {
		t.Errorf("duration field = %q, want 12m", got)
	}
	if got := embed.Fields[2].Value; got != "Queued author" {
		t.Errorf("author field = %q, want Queued author", got)
	}
}

func TestStoppedPlaybackEmbedShowsActorAndTrackLink(t *testing.T) {
	embed := stoppedPlaybackEmbed(1234, &audio.Track{
		Title: "Acepta tu peo",
		URL:   "https://www.youtube.com/watch?v=abc123",
	})
	if embed.Description != "✅ <@1234> Stopped [Acepta tu peo](https://www.youtube.com/watch?v=abc123)" {
		t.Errorf("stopped embed description = %q", embed.Description)
	}
	if embed.Color != 0x2B2D31 {
		t.Errorf("stopped embed color = %#x, want neutral panel color", embed.Color)
	}
}

func TestFormatTrackDuration(t *testing.T) {
	for _, test := range []struct {
		duration time.Duration
		want     string
	}{
		{0, "Unknown"},
		{2*time.Hour + 7*time.Minute, "2h 7m"},
		{3*time.Minute + 42*time.Second, "3m 42s"},
		{42 * time.Second, "42s"},
	} {
		if got := formatTrackDuration(test.duration); got != test.want {
			t.Errorf("formatTrackDuration(%v) = %q, want %q", test.duration, got, test.want)
		}
	}
}

func TestControlButtonsRemainEnabledWithoutTracks(t *testing.T) {
	rows := controlButtons(player.Snapshot{Volume: 1})
	for _, row := range rows {
		for _, component := range row {
			button := component.(discord.ButtonComponent)
			if button.Disabled {
				t.Errorf("button %q should remain enabled without tracks", button.CustomID)
			}
		}
	}
}

func TestQueueConfirmationIncludesResolvedTrackMetadata(t *testing.T) {
	track := audio.Track{
		Title:     "Song title",
		URL:       "https://www.youtube.com/watch?v=abc123",
		Artist:    "Music author",
		Duration:  3*time.Minute + 24*time.Second,
		Requester: "Shichimen",
	}
	if got := formatRequester(track); got != "@Shichimen" {
		t.Errorf("formatRequester() = %q, want @Shichimen", got)
	}
	if got := formatTrackDuration(track.Duration); got != "3m 24s" {
		t.Errorf("formatTrackDuration() = %q, want 3m 24s", got)
	}
	if got := escapeMarkdownLink(track.Title); got != "Song title" {
		t.Errorf("escapeMarkdownLink() = %q, want Song title", got)
	}
	message := queueConfirmationMessage(track, 2, 1)
	if len(message.Embeds) != 1 {
		t.Fatalf("queue confirmation has %d embeds, want 1", len(message.Embeds))
	}
	embed := message.Embeds[0]
	if embed.Title != "🎵 Song Added to Queue #2" || !strings.Contains(embed.Description, "[Song title](https://www.youtube.com/watch?v=abc123)") {
		t.Errorf("queue confirmation embed = %+v, want track title linked to its URL", embed)
	}
	if len(embed.Fields) != 0 {
		t.Errorf("queue confirmation should not include music panel metadata; fields = %+v", embed.Fields)
	}
	if embed.Footer == nil || embed.Footer.Text != "Loading music panel..." {
		t.Errorf("queue confirmation footer = %+v, want loading state", embed.Footer)
	}
}

func TestPlaylistQueueConfirmationShowsBackgroundLoading(t *testing.T) {
	message := playlistQueueConfirmationMessage(audio.Track{Title: "First song"}, 3, 200)
	if len(message.Embeds) != 1 {
		t.Fatalf("playlist queue confirmation has %d embeds, want 1", len(message.Embeds))
	}
	embed := message.Embeds[0]
	if embed.Title != "🎵 Playlist iniciada — posición #3 en cola" {
		t.Errorf("playlist confirmation title = %q", embed.Title)
	}
	if !strings.Contains(embed.Description, "Cargando las otras 199 canciones en segundo plano.") {
		t.Errorf("playlist confirmation description = %q", embed.Description)
	}
}

func TestTruncateDiscordContentRespectsRuneLimit(t *testing.T) {
	content := truncateDiscordContent(strings.Repeat("ñ", 2000))
	if got := len([]rune(content)); got != 1900 {
		t.Fatalf("truncated content has %d runes, want 1900", got)
	}
}

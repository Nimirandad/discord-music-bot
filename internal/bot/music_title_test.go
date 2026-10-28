package bot

import (
	"context"
	"discord-music-bot/internal/audio"
	"discord-music-bot/internal/player"
	"strings"
	"testing"
	"time"
)

func TestDisplayMusicTitlePreservesSongVersions(t *testing.T) {
	for _, tc := range []struct{ title, artist, want string }{
		{"Artist - Song (Official Lyric Video)", "ArtistVEVO", "Song"},
		{"Song [Lyrics / Letra] (HD)", "Artist", "Song"},
		{"Song - Official Music Video", "Artist", "Song"},
		{"Song (Official Lyrics)", "Artist", "Song"},
		{"Song - Official Visualizer", "Artist", "Song"},
		{"Song (Live at Wembley)", "Artist", "Song (Live at Wembley)"},
		{"Song (feat. Someone)", "Artist", "Song (feat. Someone)"},
		{"Video Games", "Artist", "Video Games"},
	} {
		if got := displayMusicTitle(audio.Track{Title: tc.title, Artist: tc.artist}); got != tc.want {
			t.Errorf("%q => %q want %q", tc.title, got, tc.want)
		}
	}
	if got := displayMusicTitle(audio.Track{Title: "upload lyrics", SongTitle: "Canonical Song"}); got != "Canonical Song" {
		t.Fatal(got)
	}
}

func TestPanelShowsLoadingUntilFirstAudio(t *testing.T) {
	state := player.Snapshot{Current: &audio.Track{Title: "Song (Official Video)", Artist: "Artist - Topic"}, Loading: true}
	loading := renderControlEmbed("", state)
	if !strings.Contains(loading.Description, "Cargando audio") || strings.Contains(loading.Description, "Official Video") {
		t.Fatal(loading.Description)
	}
	state.Loading = false
	if strings.Contains(renderControlEmbed("", state).Description, "Cargando audio") {
		t.Fatal("ready panel still loading")
	}
}

func TestLoadingTypingStopsWithRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	sent := make(chan struct{}, 1)
	go func() {
		runLoadingTyping(ctx, func() {
			select {
			case sent <- struct{}{}:
			default:
			}
		}, time.Hour)
		close(done)
	}()
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("typing not sent")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("typing did not stop")
	}
}

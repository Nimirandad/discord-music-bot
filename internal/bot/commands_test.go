package bot

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"discord-music-bot/internal/audio"
	"github.com/disgoorg/snowflake/v2"

	"discord-music-bot/internal/spotify"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantCommand string
		wantArgs    string
		wantOK      bool
	}{
		{name: "play search", content: "!play  Artist - Track ", wantCommand: "play", wantArgs: "Artist - Track", wantOK: true},
		{name: "case insensitive command", content: "!QUEUE", wantCommand: "queue", wantOK: true},
		{name: "join", content: "!join", wantCommand: "join", wantOK: true},
		{name: "requires prefix", content: "/play song"},
		{name: "ignores empty prefix", content: "!   "},
		{name: "does not accept prefix inside text", content: "hello !skip"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, args, ok := parseCommand(test.content)
			if command != test.wantCommand || args != test.wantArgs || ok != test.wantOK {
				t.Fatalf("parseCommand(%q) = (%q, %q, %t), want (%q, %q, %t)",
					test.content, command, args, ok, test.wantCommand, test.wantArgs, test.wantOK)
			}
		})
	}
}

func TestIsDuplicatePlayCommand(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{input: "!play https://youtube.com/watch?v=example", want: true},
		{input: "  !PLAY song", want: true},
		{input: "play song", want: false},
		{input: "!playful song", want: false},
		{input: "Artist - Track", want: false},
	}
	for _, test := range tests {
		if got := isDuplicatePlayCommand(test.input); got != test.want {
			t.Errorf("isDuplicatePlayCommand(%q) = %t, want %t", test.input, got, test.want)
		}
	}
}

func TestResolveSpotifyTracksKeepsPlaylistOrderAndBoundsConcurrency(t *testing.T) {
	tracks := make([]spotify.Track, 12)
	for index := range tracks {
		tracks[index] = spotify.Track{Name: strconv.Itoa(index)}
	}
	var active atomic.Int32
	var maxActive atomic.Int32
	resolved, err := resolveSpotifyTracks(context.Background(), tracks, func(_ context.Context, track spotify.Track) (audio.Track, error) {
		current := active.Add(1)
		for previous := maxActive.Load(); current > previous && !maxActive.CompareAndSwap(previous, current); previous = maxActive.Load() {
		}
		time.Sleep(10 * time.Millisecond)
		active.Add(-1)
		return audio.Track{Title: track.Name}, nil
	})
	if err != nil {
		t.Fatalf("resolveSpotifyTracks() error = %v", err)
	}
	if got, want := maxActive.Load(), int32(spotifyResolutionWorkers); got != want {
		t.Errorf("maximum concurrent searches = %d, want %d", got, want)
	}
	for index, track := range resolved {
		if track.Title != tracks[index].Name {
			t.Errorf("resolved track at index %d = %q, want %q", index, track.Title, tracks[index].Name)
		}
	}
}

func TestResolveSpotifyTracksReportsTrackOnFailure(t *testing.T) {
	_, err := resolveSpotifyTracks(context.Background(), []spotify.Track{{Name: "Song", Artist: "Artist"}}, func(context.Context, spotify.Track) (audio.Track, error) {
		return audio.Track{}, context.DeadlineExceeded
	})
	if err == nil || !strings.Contains(err.Error(), `resolve Spotify track "Song" by "Artist"`) {
		t.Fatalf("resolveSpotifyTracks() error = %v, want track context", err)
	}
}

func TestResolveSpotifyTracksKeepsSuccessfulTracksWhenOneFails(t *testing.T) {
	tracks := []spotify.Track{
		{Name: "First"},
		{Name: "Unavailable"},
		{Name: "Third"},
	}
	resolved, err := resolveSpotifyTracks(context.Background(), tracks, func(_ context.Context, track spotify.Track) (audio.Track, error) {
		if track.Name == "Unavailable" {
			return audio.Track{}, context.DeadlineExceeded
		}
		return audio.Track{Title: track.Name}, nil
	})
	if err == nil || !strings.Contains(err.Error(), `"Unavailable"`) {
		t.Fatalf("resolveSpotifyTracks() error = %v, want failure context for unavailable track", err)
	}
	if len(resolved) != 2 || resolved[0].Title != "First" || resolved[1].Title != "Third" {
		t.Fatalf("resolveSpotifyTracks() = %+v, want successful tracks in playlist order", resolved)
	}
}

func TestResolveSpotifyTracksProgressivelyKeepsWorkersBusyAndPlaylistOrder(t *testing.T) {
	tracks := make([]spotify.Track, 8)
	for index := range tracks {
		tracks[index] = spotify.Track{Name: strconv.Itoa(index)}
	}

	firstTrackRelease := make(chan struct{})
	fifthTrackStarted := make(chan struct{})
	resolvedTracks := make([]string, 0, len(tracks))
	done := make(chan error, 1)
	go func() {
		_, err := resolveSpotifyTracksProgressively(
			context.Background(),
			tracks,
			func(_ context.Context, track spotify.Track) (audio.Track, error) {
				if track.Name == "0" {
					<-firstTrackRelease
				}
				if track.Name == "4" {
					close(fifthTrackStarted)
				}
				return audio.Track{Title: track.Name}, nil
			},
			func(batch []audio.Track) error {
				for _, track := range batch {
					resolvedTracks = append(resolvedTracks, track.Title)
				}
				return nil
			},
		)
		done <- err
	}()

	select {
	case <-fifthTrackStarted:
	case <-time.After(time.Second):
		close(firstTrackRelease)
		t.Fatal("fifth track did not start while the first track was still resolving")
	}
	close(firstTrackRelease)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("resolveSpotifyTracksProgressively() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("resolveSpotifyTracksProgressively() did not finish")
	}

	if len(resolvedTracks) != len(tracks) {
		t.Fatalf("resolved %d tracks, want %d", len(resolvedTracks), len(tracks))
	}
	for index, name := range resolvedTracks {
		if want := strconv.Itoa(index); name != want {
			t.Errorf("track at queue position %d = %q, want %q", index, name, want)
		}
	}
}

func TestSpotifySearchQueriesPrioritizesArtistBeforeTrackName(t *testing.T) {
	got := spotifySearchQueries(spotify.Track{
		Name:   "花になって",
		Artist: "緑黄色社会",
	})
	want := []string{
		"緑黄色社会 花になって",
		"花になって 緑黄色社会",
		"花になって",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spotifySearchQueries() = %v, want %v", got, want)
	}
}

func TestCommandChannelMatches(t *testing.T) {
	tests := []struct {
		name        string
		assigned    snowflake.ID
		incoming    snowflake.ID
		wantMatches bool
	}{
		{name: "unassigned channel", incoming: 22, wantMatches: true},
		{name: "assigned channel", assigned: 22, incoming: 22, wantMatches: true},
		{name: "different channel", assigned: 22, incoming: 33, wantMatches: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := commandChannelMatches(test.assigned, test.incoming); got != test.wantMatches {
				t.Fatalf("commandChannelMatches(%d, %d) = %t, want %t",
					test.assigned, test.incoming, got, test.wantMatches)
			}
		})
	}
}

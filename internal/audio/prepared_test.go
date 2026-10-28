package audio

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPreparedMediaConsumptionExpiryAndBound(t *testing.T) {
	r := NewResolver()
	now := time.Now()
	data := []byte(`{"formats":[{"url":"https://media.example/audio"}]}`)
	for i := 0; i < maxPreparedTracks+1; i++ {
		r.storePrepared(fmt.Sprint(i), data, now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(r.prepared) != maxPreparedTracks {
		t.Fatalf("prepared entries=%d", len(r.prepared))
	}
	if r.takePrepared("0", now) != nil {
		t.Fatal("oldest entry was not evicted")
	}
	if r.takePrepared("1", now) == nil || r.takePrepared("1", now) != nil {
		t.Fatal("prepared media must be consumed once")
	}
	if r.takePrepared("2", now.Add(preparedTTL+time.Second)) != nil {
		t.Fatal("expired media was reused")
	}
	if r.storePrepared("invalid", []byte(`{"title":"Unavailable"}`), now) {
		t.Fatal("metadata without media accepted")
	}
}

func TestPreparedMediaRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name                string
		delivered, canceled bool
		attempts            int
	}{
		{"expired source", false, false, 2}, {"partial playback", true, false, 1}, {"stopped", false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			err := playPreparedOrSource(ctx, []byte("prepared"), func(data []byte) (bool, error) {
				calls++
				if calls == 1 {
					if tc.canceled {
						cancel()
					}
					return tc.delivered, errors.New("source failed")
				}
				if len(data) > 0 {
					t.Fatal("retry did not discard prepared source")
				}
				return true, nil
			})
			if calls != tc.attempts {
				t.Fatalf("calls=%d want=%d", calls, tc.attempts)
			}
			if tc.attempts == 2 && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMusicSearchPrefersLyricsAndRejectsOfficialVideo(t *testing.T) {
	tracks := preferAudioTracks([]Track{{Title: "Artist Song Official Music Video"}, {Title: "Artist Song (Official Audio)"}, {Title: "Artist Song (Official Lyric Video)"}, {Title: "Artist Song (Lyrics)"}})
	if len(tracks) != 3 {
		t.Fatalf("tracks=%v", tracks)
	}
	if tracks[0].Title != "Artist Song (Official Lyric Video)" || tracks[2].Title != "Artist Song (Official Audio)" {
		t.Fatalf("ranking=%v", tracks)
	}
	if lyricSearchQuery("artist song") != "artist song lyrics" {
		t.Fatal("query does not prioritize lyrics")
	}
	if lyricSearchQuery("artist song lyrics") != "artist song lyrics" {
		t.Fatal("duplicated lyrics suffix")
	}
	if lyricSearchQuery("Audioslave Like a Stone") != "Audioslave Like a Stone lyrics" {
		t.Fatal("artist name was mistaken for an audio preference")
	}
}

func TestPlaybackJoinsInFlightPreparation(t *testing.T) {
	r := NewResolver()
	flight := &mediaPreparation{done: make(chan struct{})}
	r.preparing = map[string]*mediaPreparation{"next": flight}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.awaitPrepared(ctx, "next"); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting did not honor cancellation: %v", err)
	}
	if r.preparing["next"] != flight {
		t.Fatal("waiting canceled the shared extraction")
	}
	r.storePrepared("next", []byte(`{"formats":[{"url":"https://example.test/audio"}]}`), time.Now())
	close(flight.done)
	data, err := r.awaitPrepared(context.Background(), "next")
	if err != nil || len(data) == 0 {
		t.Fatalf("prepared source not reused: %v", err)
	}
}

package player

import (
	"context"
	"discord-music-bot/internal/audio"
	"testing"
	"time"
)

func TestPrefetchStartsAfterAudioAndControlsCancel(t *testing.T) {
	for _, action := range []string{"stop", "previous", "close"} {
		t.Run(action, func(t *testing.T) { testPrefetchCancellation(t, action) })
	}
}

func testPrefetchCancellation(t *testing.T, action string) {
	ready := make(chan audio.PlaybackControls, 1)
	prefetchStarted := make(chan string, 1)
	prefetchStopped := make(chan struct{})
	p := New(func(ctx context.Context, _ audio.Track, c audio.PlaybackControls) error {
		ready <- c
		<-ctx.Done()
		return ctx.Err()
	}, nil)
	defer p.Close()
	p.SetPrefetchFunc(func(ctx context.Context, track audio.Track) error {
		prefetchStarted <- track.URL
		<-ctx.Done()
		close(prefetchStopped)
		return ctx.Err()
	})
	if err := p.Enqueue(audio.Track{URL: "first"}, audio.Track{URL: "next"}); err != nil {
		t.Fatal(err)
	}
	var controls audio.PlaybackControls
	select {
	case controls = <-ready:
	case <-time.After(time.Second):
		t.Fatal("playback did not start")
	}
	select {
	case <-prefetchStarted:
		t.Fatal("prefetch competed with initial playback")
	default:
	}
	controls.OnFirstFrame()
	select {
	case url := <-prefetchStarted:
		if url != "next" {
			t.Fatalf("prepared %s", url)
		}
	case <-time.After(time.Second):
		t.Fatal("prefetch did not start")
	}
	switch action {
	case "stop":
		p.Stop()
	case "skip":
		if err := p.Skip(); err != nil {
			t.Fatal(err)
		}
	case "previous":
		if err := p.Previous(); err != nil {
			t.Fatal(err)
		}
	case "close":
		p.Close()
	}
	select {
	case <-prefetchStopped:
	case <-time.After(time.Second):
		t.Fatal("control did not cancel prefetch")
	}
}

func TestSkipKeepsPreparationForTrackBecomingCurrent(t *testing.T) {
	ready := make(chan audio.PlaybackControls, 2)
	preparing := make(chan context.Context, 1)
	p := New(func(ctx context.Context, _ audio.Track, c audio.PlaybackControls) error {
		ready <- c
		<-ctx.Done()
		return ctx.Err()
	}, nil)
	defer p.Close()
	p.SetPrefetchFunc(func(ctx context.Context, _ audio.Track) error { preparing <- ctx; <-ctx.Done(); return ctx.Err() })
	if err := p.Enqueue(audio.Track{URL: "first"}, audio.Track{URL: "next"}); err != nil {
		t.Fatal(err)
	}
	var controls audio.PlaybackControls
	select {
	case controls = <-ready:
	case <-time.After(time.Second):
		t.Fatal("no playback")
	}
	controls.OnFirstFrame()
	var preparation context.Context
	select {
	case preparation = <-preparing:
	case <-time.After(time.Second):
		t.Fatal("no prefetch")
	}
	if err := p.Skip(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("next did not start")
	}
	if preparation.Err() != nil {
		t.Fatal("skip discarded extraction needed by next track")
	}
	if !p.Snapshot().Loading {
		t.Fatal("panel marked playing before audio")
	}
	p.Stop()
	select {
	case <-preparation.Done():
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel pending extraction")
	}
}

package player

import (
	"context"
	"discord-music-bot/internal/audio"
	"errors"
	"reflect"
	"testing"
)

func queuedPlayer() *Player {
	return &Player{queue: []audio.Track{{Title: "B", URL: "b"}, {Title: "C", URL: "c"}, {Title: "D", URL: "d"}}, wake: make(chan struct{}, 1), volume: 1}
}

func queueTitles(p *Player) []string {
	var titles []string
	for _, track := range p.Snapshot().Queue {
		titles = append(titles, track.Title)
	}
	return titles
}

func TestMoveQueuePositions(t *testing.T) {
	for _, tc := range []struct {
		from, to int
		want     []string
	}{
		{1, 3, []string{"C", "D", "B"}}, {3, 1, []string{"D", "B", "C"}}, {2, 2, []string{"B", "C", "D"}},
	} {
		p := queuedPlayer()
		if err := p.Move(tc.from, tc.to); err != nil {
			t.Fatal(err)
		}
		if got := queueTitles(p); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("move %d->%d: %v", tc.from, tc.to, got)
		}
	}
	p := queuedPlayer()
	if p.Move(1, 4) == nil || p.Move(0, 1) == nil {
		t.Fatal("invalid position accepted")
	}
	if !reflect.DeepEqual(queueTitles(p), []string{"B", "C", "D"}) {
		t.Fatal("invalid move changed queue")
	}
}

func TestRemoveOnlyAffectsWaitingTracks(t *testing.T) {
	p := queuedPlayer()
	p.current = &audio.Track{Title: "A", URL: "a"}
	track, err := p.Remove(2)
	if err != nil || track.Title != "C" {
		t.Fatalf("remove=%v %v", track, err)
	}
	if p.Snapshot().Current.Title != "A" || !reflect.DeepEqual(queueTitles(p), []string{"B", "D"}) {
		t.Fatal("wrong track removed")
	}
	if _, err := p.Remove(3); err == nil {
		t.Fatal("out-of-range removal accepted")
	}
}

func TestClearQueuePreservesCurrentExtractionAndRejectsCanceledProducer(t *testing.T) {
	p := queuedPlayer()
	p.current = &audio.Track{Title: "A", URL: "a"}
	currentCtx, cancelCurrent := context.WithCancel(context.Background())
	defer cancelCurrent()
	p.cancel = cancelCurrent
	preparedCtx, cancelPrepared := context.WithCancel(context.Background())
	defer cancelPrepared()
	p.prefetchKey = "a"
	p.prefetchCancel = cancelPrepared
	producer, cancelProducer := context.WithCancel(context.Background())
	cancelProducer()
	n, err := p.ClearQueue()
	if err != nil || n != 3 || len(p.Snapshot().Queue) != 0 {
		t.Fatalf("clear=%d %v", n, err)
	}
	if currentCtx.Err() != nil || preparedCtx.Err() != nil || p.Snapshot().Current.Title != "A" {
		t.Fatal("clear interrupted current track")
	}
	if _, err := p.EnqueueWithPositionContext(producer, audio.Track{Title: "late"}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled playlist refilled queue")
	}
}

func TestSkipToKeepsSelectedTrackAndTail(t *testing.T) {
	p := queuedPlayer()
	p.current = &audio.Track{Title: "A", URL: "a"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.cancel = cancel
	preparedCtx, cancelPrepared := context.WithCancel(context.Background())
	defer cancelPrepared()
	p.prefetchKey = "c"
	p.prefetchCancel = cancelPrepared
	track, err := p.SkipTo(2)
	if err != nil || track.Title != "C" || ctx.Err() == nil {
		t.Fatalf("skipto=%v %v", track, err)
	}
	if !reflect.DeepEqual(queueTitles(p), []string{"C", "D"}) || preparedCtx.Err() != nil {
		t.Fatal("lost target, following track or prepared source")
	}
	q := queuedPlayer()
	if _, err := q.SkipTo(4); err == nil || !reflect.DeepEqual(queueTitles(q), []string{"B", "C", "D"}) {
		t.Fatal("invalid skipto modified queue")
	}
	if _, err := q.SkipTo(1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-q.wake:
	default:
		t.Fatal("idle player not woken")
	}
}

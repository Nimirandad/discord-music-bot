package player

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"discord-music-bot/internal/audio"
)

func TestPlayerPlaysQueuedTracksInOrder(t *testing.T) {
	played := make(chan string, 2)
	p := New(func(_ context.Context, track audio.Track, _ audio.PlaybackControls) error {
		played <- track.Title
		return nil
	}, nil)
	defer p.Close()

	if err := p.Enqueue(audio.Track{Title: "first"}, audio.Track{Title: "second"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	for _, want := range []string{"first", "second"} {
		awaitTrack(t, played, want)
	}
}

func TestEnqueueWithPositionIncludesCurrentTrack(t *testing.T) {
	started := make(chan string, 1)
	p := New(func(ctx context.Context, track audio.Track, _ audio.PlaybackControls) error {
		started <- track.Title
		<-ctx.Done()
		return ctx.Err()
	}, nil)
	defer p.Close()

	if err := p.Enqueue(audio.Track{Title: "current"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	awaitTrack(t, started, "current")
	position, err := p.EnqueueWithPosition(
		audio.Track{Title: "queued one"},
		audio.Track{Title: "queued two"},
	)
	if err != nil {
		t.Fatalf("EnqueueWithPosition() error = %v", err)
	}
	if position != 2 {
		t.Fatalf("EnqueueWithPosition() position = %d, want 2", position)
	}
	if got := p.Snapshot().Queue; len(got) != 2 {
		t.Fatalf("queue after EnqueueWithPosition() has %d tracks, want 2", len(got))
	}
	p.Stop()
}

func TestSkipCancelsCurrentTrack(t *testing.T) {
	started := make(chan string, 2)
	p := New(func(ctx context.Context, track audio.Track, _ audio.PlaybackControls) error {
		started <- track.Title
		if track.Title == "first" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}, nil)
	defer p.Close()

	if err := p.Enqueue(audio.Track{Title: "first"}, audio.Track{Title: "second"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	awaitTrack(t, started, "first")
	if err := p.Skip(); err != nil {
		t.Fatalf("Skip() error = %v", err)
	}
	awaitTrack(t, started, "second")
}

func TestBeginConnectionMoveRestartsCurrentTrackAfterMove(t *testing.T) {
	started := make(chan string, 3)
	finishRestart := make(chan struct{})
	var calls int
	p := New(func(ctx context.Context, track audio.Track, _ audio.PlaybackControls) error {
		calls++
		call := calls
		started <- track.Title
		if call == 1 {
			<-ctx.Done()
			return ctx.Err()
		}
		<-finishRestart
		return nil
	}, nil)
	defer p.Close()

	if err := p.Enqueue(audio.Track{Title: "current"}, audio.Track{Title: "next"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	awaitTrack(t, started, "current")

	resumePlayback := p.BeginConnectionMove()
	defer resumePlayback()

	deadline := time.After(time.Second)
	for {
		snapshot := p.Snapshot()
		if snapshot.Current == nil && len(snapshot.Queue) == 2 &&
			snapshot.Queue[0].Title == "current" && snapshot.Queue[1].Title == "next" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("player did not pause for connection move; snapshot = %+v", snapshot)
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case track := <-started:
		t.Fatalf("track %q started while connection was moving", track)
	case <-time.After(25 * time.Millisecond):
	}

	resumePlayback()
	awaitTrack(t, started, "current")
	close(finishRestart)
}

func TestPreviousRestartsPreviousTrackAndPreservesCurrentInQueue(t *testing.T) {
	started := make(chan string, 4)
	finish := make(chan struct{}, 2)
	p := New(func(ctx context.Context, track audio.Track, _ audio.PlaybackControls) error {
		started <- track.Title
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-finish:
			return nil
		}
	}, nil)
	defer p.Close()

	if err := p.Enqueue(audio.Track{Title: "first"}, audio.Track{Title: "second"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	awaitTrack(t, started, "first")
	finish <- struct{}{}
	awaitTrack(t, started, "second")

	if err := p.Previous(); err != nil {
		t.Fatalf("Previous() error = %v", err)
	}
	awaitTrack(t, started, "first")

	snapshot := p.Snapshot()
	if len(snapshot.Queue) != 1 || snapshot.Queue[0].Title != "second" {
		t.Fatalf("queue after previous = %+v, want second track queued", snapshot.Queue)
	}
}

func TestTogglePauseChangesPlaybackState(t *testing.T) {
	pausedSignal := make(chan bool, 2)
	started := make(chan struct{}, 1)
	p := New(func(ctx context.Context, _ audio.Track, controls audio.PlaybackControls) error {
		started <- struct{}{}
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		var last bool
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				paused := controls.IsPaused()
				if paused != last {
					pausedSignal <- paused
					last = paused
				}
			}
		}
	}, nil)
	defer p.Close()

	if err := p.Enqueue(audio.Track{Title: "current"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for current track")
	}
	if paused, err := p.TogglePause(); err != nil || !paused {
		t.Fatalf("TogglePause() = (%t, %v), want (true, nil)", paused, err)
	}
	awaitPauseState(t, pausedSignal, true)
	if paused, err := p.TogglePause(); err != nil || paused {
		t.Fatalf("TogglePause() = (%t, %v), want (false, nil)", paused, err)
	}
	awaitPauseState(t, pausedSignal, false)
}

func TestShuffleQueuePreservesAllTracks(t *testing.T) {
	started := make(chan struct{}, 1)
	p := New(func(ctx context.Context, _ audio.Track, _ audio.PlaybackControls) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}, nil)
	defer p.Close()

	tracks := []audio.Track{
		{Title: "one"},
		{Title: "two"},
		{Title: "three"},
		{Title: "four"},
	}
	if err := p.Enqueue(tracks...); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a track to start")
	}
	if got := p.ShuffleQueue(); got != len(tracks)-1 {
		t.Fatalf("ShuffleQueue() = %d, want %d", got, len(tracks)-1)
	}
	snapshot := p.Snapshot()
	got := make([]string, len(snapshot.Queue))
	want := make([]string, len(tracks)-1)
	for index := range snapshot.Queue {
		got[index] = snapshot.Queue[index].Title
	}
	for index := range want {
		want[index] = tracks[index+1].Title
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shuffled tracks = %v, want same tracks as %v", got, want)
	}
}

func TestAdjustVolumeClampsAndUsesSinkOnlyWhilePlaying(t *testing.T) {
	type volumeRequest struct {
		value float64
		reply chan error
	}
	requests := make(chan volumeRequest, 4)
	started := make(chan struct{}, 1)
	p := New(func(ctx context.Context, _ audio.Track, controls audio.PlaybackControls) error {
		if err := controls.RegisterVolumeSink(func(value float64) error {
			reply := make(chan error, 1)
			requests <- volumeRequest{value: value, reply: reply}
			return <-reply
		}); err != nil {
			return err
		}
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}, nil)
	defer p.Close()

	if err := p.Enqueue(audio.Track{Title: "current"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for current track")
	}

	type adjustment struct {
		volume  float64
		changed bool
		err     error
	}
	result := make(chan adjustment, 1)
	go func() {
		volume, changed, err := p.AdjustVolume(0.1)
		result <- adjustment{volume: volume, changed: changed, err: err}
	}()
	request := <-requests
	if request.value != 1.1 {
		t.Fatalf("volume sink received %v, want 1.1", request.value)
	}
	request.reply <- nil
	if got := <-result; got.err != nil || !got.changed || got.volume != 1.1 {
		t.Fatalf("AdjustVolume(+0.1) = (%v, %t, %v), want (1.1, true, nil)", got.volume, got.changed, got.err)
	}

	result = make(chan adjustment, 1)
	go func() {
		volume, changed, err := p.AdjustVolume(2)
		result <- adjustment{volume: volume, changed: changed, err: err}
	}()
	request = <-requests
	request.reply <- nil
	if got := <-result; got.err != nil || !got.changed || got.volume != 2 {
		t.Fatalf("AdjustVolume(+2) = (%v, %t, %v), want (2, true, nil)", got.volume, got.changed, got.err)
	}

	if paused, err := p.TogglePause(); err != nil || !paused {
		t.Fatalf("TogglePause() = (%t, %v), want (true, nil)", paused, err)
	}
	if volume, changed, err := p.AdjustVolume(-2); err != nil || !changed || volume != 0 {
		t.Fatalf("AdjustVolume(-2) while paused = (%v, %t, %v), want (0, true, nil)", volume, changed, err)
	}
	select {
	case request := <-requests:
		t.Fatalf("volume sink called while paused with %v", request.value)
	default:
	}
}

func TestLoopAndAutoPlayDefaultOffAndToggle(t *testing.T) {
	p := New(func(ctx context.Context, _ audio.Track, _ audio.PlaybackControls) error {
		<-ctx.Done()
		return ctx.Err()
	}, nil)
	defer p.Close()

	snapshot := p.Snapshot()
	if snapshot.LoopTrack || snapshot.AutoPlay || snapshot.Volume != 1 {
		t.Fatalf("initial controls = %+v, want loop/autoplay off and volume 1", snapshot)
	}
	if !p.ToggleLoopTrack() || !p.ToggleAutoPlay() {
		t.Fatal("toggle methods should enable loop and autoplay")
	}
	if snapshot = p.Snapshot(); !snapshot.LoopTrack || !snapshot.AutoPlay {
		t.Fatalf("controls after enabling = %+v, want loop/autoplay on", snapshot)
	}
}

func TestLoopTrackRequeuesCompletedTrack(t *testing.T) {
	started := make(chan string, 2)
	finish := make(chan struct{}, 2)
	p := New(func(ctx context.Context, track audio.Track, _ audio.PlaybackControls) error {
		started <- track.Title
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-finish:
			return nil
		}
	}, nil)
	defer p.Close()

	if err := p.Enqueue(audio.Track{Title: "repeat"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	awaitTrack(t, started, "repeat")
	if !p.ToggleLoopTrack() {
		t.Fatal("ToggleLoopTrack() should enable the repeat mode")
	}
	finish <- struct{}{}
	awaitTrack(t, started, "repeat")
	if snapshot := p.Snapshot(); snapshot.Current == nil || snapshot.Current.Title != "repeat" {
		t.Fatalf("current track after loop = %+v, want repeat", snapshot.Current)
	}
	p.Stop()
}

func TestAutoPlayDoesNotEnqueueAfterBeingDisabledDuringSearch(t *testing.T) {
	started := make(chan string, 2)
	searchStarted := make(chan struct{})
	finishSearch := make(chan struct{})
	p := New(func(ctx context.Context, track audio.Track, _ audio.PlaybackControls) error {
		started <- track.Title
		if track.Title == "related" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}, nil)
	defer p.Close()
	p.SetAutoPlayFunc(func(ctx context.Context, _ audio.Track) (audio.Track, error) {
		close(searchStarted)
		select {
		case <-ctx.Done():
			return audio.Track{}, ctx.Err()
		case <-finishSearch:
			return audio.Track{Title: "related", URL: "https://example.test/related"}, nil
		}
	})
	if !p.ToggleAutoPlay() {
		t.Fatal("ToggleAutoPlay() should enable autoplay")
	}
	if err := p.Enqueue(audio.Track{Title: "finished", URL: "https://example.test/finished"}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	awaitTrack(t, started, "finished")
	select {
	case <-searchStarted:
	case <-time.After(time.Second):
		t.Fatal("autoplay search did not start")
	}
	if p.ToggleAutoPlay() {
		t.Fatal("ToggleAutoPlay() should disable autoplay while search is in progress")
	}
	close(finishSearch)
	select {
	case track := <-started:
		t.Fatalf("autoplay started %q after being disabled", track)
	case <-time.After(100 * time.Millisecond):
	}
	if got := p.Snapshot().Queue; len(got) != 0 {
		t.Fatalf("queue after autoplay was disabled = %+v, want empty", got)
	}
}

func awaitTrack(t *testing.T, started <-chan string, want string) {
	t.Helper()
	select {
	case got := <-started:
		if got != want {
			t.Fatalf("started track = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for track %q", want)
	}
}

func awaitPauseState(t *testing.T, states <-chan bool, want bool) {
	t.Helper()
	select {
	case got := <-states:
		if got != want {
			t.Fatalf("pause state = %t, want %t", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for pause state %t", want)
	}
}

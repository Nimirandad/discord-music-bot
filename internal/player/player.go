package player

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"discord-music-bot/internal/audio"
)

type PlayFunc func(context.Context, audio.Track, audio.PlaybackControls) error
type ErrorFunc func(error)
type AutoPlayFunc func(context.Context, audio.Track) (audio.Track, error)
type PrefetchFunc func(context.Context, audio.Track) error

type Snapshot struct {
	Current     *audio.Track
	Queue       []audio.Track
	Paused      bool
	Loading     bool
	CanPrevious bool
	LoopTrack   bool
	AutoPlay    bool
	Volume      float64
}

type Player struct {
	mu                    sync.Mutex
	queue                 []audio.Track
	history               []audio.Track
	current               *audio.Track
	cancel                context.CancelFunc
	volumeSink            func(float64) error
	volume                float64
	loopTrack             bool
	autoPlay              bool
	connectionMoving      bool
	autoPlayFunc          AutoPlayFunc
	prefetchFunc          PrefetchFunc
	prefetchCancel        context.CancelFunc
	prefetchKey           string
	prefetchGeneration    uint64
	prefetchAttemptedKey  string
	prefetchedKey         string
	prefetchReady         bool
	playbackGeneration    uint64
	closed                bool
	skipHistoryForCurrent bool
	wake                  chan struct{}
	done                  chan struct{}
	paused                atomic.Bool
	play                  PlayFunc
	onError               ErrorFunc
	onChange              func(Snapshot)
}

func New(play PlayFunc, onError ErrorFunc) *Player {
	p := &Player{
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		volume:  1,
		play:    play,
		onError: onError,
	}
	go p.run()
	return p
}

func (p *Player) SetAutoPlayFunc(autoPlayFunc AutoPlayFunc) {
	p.mu.Lock()
	p.autoPlayFunc = autoPlayFunc
	p.mu.Unlock()
}

func (p *Player) SetPrefetchFunc(prefetchFunc PrefetchFunc) {
	p.mu.Lock()
	p.prefetchFunc = prefetchFunc
	p.mu.Unlock()
}

func (p *Player) SetOnChange(onChange func(Snapshot)) {
	p.mu.Lock()
	p.onChange = onChange
	p.mu.Unlock()
}

func (p *Player) Enqueue(tracks ...audio.Track) error {
	_, err := p.EnqueueWithPosition(tracks...)
	return err
}

func (p *Player) EnqueueWithPosition(tracks ...audio.Track) (int, error) {
	return p.EnqueueWithPositionContext(context.Background(), tracks...)
}

func (p *Player) EnqueueWithPositionContext(ctx context.Context, tracks ...audio.Track) (int, error) {
	if len(tracks) == 0 {
		return 0, fmt.Errorf("no tracks to enqueue")
	}
	p.mu.Lock()
	if err := ctx.Err(); err != nil {
		p.mu.Unlock()
		return 0, err
	}
	if p.closed {
		p.mu.Unlock()
		return 0, fmt.Errorf("player is closed")
	}
	position := len(p.queue) + 1
	if p.current != nil {
		position++
	}
	p.queue = append(p.queue, tracks...)
	p.signal()
	p.mu.Unlock()
	p.notifyChange()
	p.refreshPrefetch()
	return position, nil
}

func (p *Player) Skip() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return fmt.Errorf("player is closed")
	}
	if p.cancel != nil {
		p.cancel()
		if len(p.queue) == 0 || p.prefetchKey != p.queue[0].URL {
			p.cancelPrefetchLocked()
		}
		p.prefetchReady = false
		p.playbackGeneration++
		p.mu.Unlock()
		p.notifyChange()
		return nil
	}
	if len(p.queue) > 0 {
		p.queue = p.queue[1:]
		p.mu.Unlock()
		p.notifyChange()
		return nil
	}
	p.mu.Unlock()
	return fmt.Errorf("nothing is currently playing")
}

func (p *Player) BeginConnectionMove() func() {
	p.mu.Lock()
	p.connectionMoving = true
	if p.current != nil && p.cancel != nil {
		p.queue = append([]audio.Track{*p.current}, p.queue...)
		p.skipHistoryForCurrent = true
		p.cancel()
		p.cancelPrefetchLocked()
		p.prefetchReady = false
		p.playbackGeneration++
		p.paused.Store(false)
	}
	p.mu.Unlock()
	p.notifyChange()

	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			p.connectionMoving = false
			p.signal()
			p.mu.Unlock()
		})
	}
}

func (p *Player) Previous() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return fmt.Errorf("player is closed")
	}

	var previous audio.Track
	hasHistory := false
	if historyLength := len(p.history); historyLength > 0 {
		previous = p.history[historyLength-1]
		p.history = p.history[:historyLength-1]
		hasHistory = true
	} else if p.current != nil {
		previous = *p.current
	} else {
		p.mu.Unlock()
		return fmt.Errorf("no previous track is available")
	}

	if p.current != nil {
		if hasHistory {
			current := *p.current
			p.queue = append([]audio.Track{previous, current}, p.queue...)
		} else {
			p.queue = append([]audio.Track{previous}, p.queue...)
		}
		p.skipHistoryForCurrent = true
		p.cancel()
		p.playbackGeneration++
	} else {
		p.queue = append([]audio.Track{previous}, p.queue...)
		p.signal()
	}
	p.paused.Store(false)
	p.cancelPrefetchLocked()
	p.prefetchReady = false
	p.mu.Unlock()
	p.notifyChange()
	return nil
}

func (p *Player) TogglePause() (bool, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return false, fmt.Errorf("player is closed")
	}
	if p.current == nil {
		p.mu.Unlock()
		return false, fmt.Errorf("nothing is currently playing")
	}
	paused := !p.paused.Load()
	p.paused.Store(paused)
	p.mu.Unlock()
	p.notifyChange()
	return paused, nil
}

func (p *Player) AdjustVolume(delta float64) (float64, bool, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, false, fmt.Errorf("player is closed")
	}
	volume := p.volume + delta
	if volume < 0 {
		volume = 0
	}
	if volume > 2 {
		volume = 2
	}
	if volume == p.volume {
		p.mu.Unlock()
		return volume, false, nil
	}
	if p.volumeSink != nil && !p.paused.Load() {
		if err := p.volumeSink(volume); err != nil {
			currentVolume := p.volume
			p.mu.Unlock()
			return currentVolume, false, fmt.Errorf("apply playback volume: %w", err)
		}
	}
	p.volume = volume
	p.mu.Unlock()
	p.notifyChange()
	return volume, true, nil
}

func (p *Player) ToggleLoopTrack() bool {
	p.mu.Lock()
	p.loopTrack = !p.loopTrack
	enabled := p.loopTrack
	p.mu.Unlock()
	p.notifyChange()
	return enabled
}

func (p *Player) ToggleAutoPlay() bool {
	p.mu.Lock()
	p.autoPlay = !p.autoPlay
	enabled := p.autoPlay
	p.mu.Unlock()
	p.notifyChange()
	return enabled
}

func (p *Player) ShuffleQueue() int {
	p.mu.Lock()
	if p.closed || len(p.queue) < 2 {
		p.mu.Unlock()
		return 0
	}
	rand.Shuffle(len(p.queue), func(i, j int) {
		p.queue[i], p.queue[j] = p.queue[j], p.queue[i]
	})
	shuffled := len(p.queue)
	p.mu.Unlock()
	p.notifyChange()
	p.refreshPrefetch()
	return shuffled
}

func (p *Player) Stop() {
	p.mu.Lock()
	p.queue = nil
	p.history = nil
	p.paused.Store(false)
	p.skipHistoryForCurrent = false
	p.loopTrack = false
	p.autoPlay = false
	p.volumeSink = nil
	p.cancelPrefetchLocked()
	p.prefetchReady = false
	if p.cancel != nil {
		p.cancel()
		p.playbackGeneration++
	}
	p.mu.Unlock()
	p.notifyChange()
}

func (p *Player) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshotLocked()
}

func (p *Player) Close() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		p.queue = nil
		if p.cancel != nil {
			p.cancel()
			p.playbackGeneration++
		}
		p.cancelPrefetchLocked()
		p.prefetchReady = false
		p.paused.Store(false)
		close(p.done)
		p.signal()
	}
	p.mu.Unlock()
}

func (p *Player) run() {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}
		if p.connectionMoving {
			p.mu.Unlock()
			select {
			case <-p.wake:
				continue
			case <-p.done:
				return
			}
		}
		if len(p.queue) == 0 {
			p.mu.Unlock()
			select {
			case <-p.wake:
				continue
			case <-p.done:
				return
			}
		}

		track := p.queue[0]
		p.queue = p.queue[1:]
		if p.prefetchKey != track.URL {
			p.cancelPrefetchLocked()
		}
		p.prefetchReady = false
		p.playbackGeneration++
		generation := p.playbackGeneration
		ctx, cancel := context.WithCancel(context.Background())
		p.current = &track
		p.cancel = cancel
		p.paused.Store(false)
		p.volumeSink = nil
		p.mu.Unlock()

		p.notifyChange()
		p.refreshPrefetch()
		err := p.play(ctx, track, audio.PlaybackControls{
			IsPaused: p.paused.Load,
			Volume:   p.currentVolume,
			OnFirstFrame: func() {
				p.markPrefetchReady(generation)
			},
			RegisterVolumeSink: func(sink func(float64) error) error {
				return p.registerVolumeSink(ctx, sink)
			},
		})

		p.mu.Lock()
		completed := err == nil && ctx.Err() == nil
		if !p.skipHistoryForCurrent && (completed || errors.Is(err, context.Canceled)) {
			p.history = append(p.history, track)
			if len(p.history) > 100 {
				p.history = p.history[len(p.history)-100:]
			}
		}
		loopTrack := p.loopTrack && completed
		if loopTrack && !p.closed {
			p.queue = append([]audio.Track{track}, p.queue...)
		}
		autoPlay := p.autoPlay && completed && !loopTrack && len(p.queue) == 0 && !p.closed
		autoPlayFunc := p.autoPlayFunc
		p.skipHistoryForCurrent = false
		p.current = nil
		p.cancel = nil
		p.volumeSink = nil
		p.paused.Store(false)
		closed := p.closed
		p.mu.Unlock()
		cancel()
		p.notifyChange()

		if err != nil && ctx.Err() == nil && p.onError != nil {
			p.onError(err)
		}
		if closed {
			return
		}
		if autoPlay && autoPlayFunc != nil {
			p.enqueueAutoPlay(track, autoPlayFunc)
		}
	}
}

func (p *Player) currentVolume() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.volume
}

func (p *Player) registerVolumeSink(ctx context.Context, sink func(float64) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || ctx.Err() != nil {
		return context.Canceled
	}
	p.volumeSink = sink
	return nil
}

func (p *Player) refreshPrefetch() {
	p.mu.Lock()
	if !p.closed && p.current != nil && !p.prefetchReady && p.prefetchKey == p.current.URL {
		p.mu.Unlock()
		return
	}
	if p.closed || p.current == nil || len(p.queue) == 0 || p.prefetchFunc == nil || p.queue[0].URL == "" {
		p.cancelPrefetchLocked()
		p.mu.Unlock()
		return
	}
	if !p.prefetchReady {
		p.mu.Unlock()
		return
	}

	next := p.queue[0]
	if (p.prefetchKey == next.URL && p.prefetchCancel != nil) || p.prefetchAttemptedKey == next.URL || p.prefetchedKey == next.URL {
		p.mu.Unlock()
		return
	}
	p.cancelPrefetchLocked()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	p.prefetchCancel = cancel
	p.prefetchKey = next.URL
	generation := p.prefetchGeneration
	p.prefetchAttemptedKey = next.URL
	p.prefetchedKey = ""
	prefetch := p.prefetchFunc
	p.mu.Unlock()

	go func() {
		err := prefetch(ctx, next)
		completed := err == nil && ctx.Err() == nil
		cancel()

		p.mu.Lock()
		if p.prefetchGeneration == generation && p.prefetchKey == next.URL {
			p.prefetchCancel = nil
			p.prefetchKey = ""
			if completed {
				p.prefetchedKey = next.URL
			}
		}
		p.mu.Unlock()
	}()
}

func (p *Player) markPrefetchReady(generation uint64) {
	p.mu.Lock()
	valid := !p.closed && p.current != nil && p.playbackGeneration == generation
	if valid {
		p.prefetchReady = true
	}
	p.mu.Unlock()
	if valid {
		p.notifyChange()
		p.refreshPrefetch()
	}
}

func (p *Player) cancelPrefetchLocked() {
	p.prefetchGeneration++
	if p.prefetchCancel != nil {
		p.prefetchCancel()
	}
	p.prefetchCancel = nil
	p.prefetchKey = ""
	p.prefetchAttemptedKey = ""
	p.prefetchedKey = ""
}

func (p *Player) enqueueAutoPlay(previous audio.Track, autoPlay AutoPlayFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	track, err := autoPlay(ctx, previous)
	if err != nil {
		if p.onError != nil {
			p.onError(fmt.Errorf("autoplay: %w", err))
		}
		return
	}
	if track.URL == "" || track.URL == previous.URL {
		if p.onError != nil {
			p.onError(fmt.Errorf("autoplay returned the same or an invalid track"))
		}
		return
	}

	p.mu.Lock()
	if p.closed || !p.autoPlay || len(p.queue) != 0 {
		p.mu.Unlock()
		return
	}
	p.queue = append(p.queue, track)
	p.signal()
	p.mu.Unlock()
	p.notifyChange()
}

func (p *Player) snapshotLocked() Snapshot {
	snapshot := Snapshot{
		Queue:       append([]audio.Track(nil), p.queue...),
		Paused:      p.paused.Load(),
		Loading:     p.current != nil && !p.prefetchReady,
		CanPrevious: p.current != nil || len(p.history) > 0,
		LoopTrack:   p.loopTrack,
		AutoPlay:    p.autoPlay,
		Volume:      p.volume,
	}
	if p.current != nil {
		current := *p.current
		snapshot.Current = &current
	}
	return snapshot
}

func (p *Player) notifyChange() {
	p.mu.Lock()
	onChange := p.onChange
	snapshot := p.snapshotLocked()
	p.mu.Unlock()
	if onChange != nil {
		onChange(snapshot)
	}
}

func (p *Player) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

package player

import (
	"discord-music-bot/internal/audio"
	"fmt"
)

// Positions are one-based and refer only to waiting tracks, as shown by !queue.
func (p *Player) queuePositionLocked(position int) error {
	if p.closed {
		return fmt.Errorf("el reproductor está cerrado")
	}
	if len(p.queue) == 0 {
		return fmt.Errorf("no hay canciones en espera")
	}
	if position < 1 || position > len(p.queue) {
		return fmt.Errorf("la posición debe estar entre 1 y %d", len(p.queue))
	}
	return nil
}

func (p *Player) Remove(position int) (audio.Track, error) {
	p.mu.Lock()
	if err := p.queuePositionLocked(position); err != nil {
		p.mu.Unlock()
		return audio.Track{}, err
	}
	removed := p.queue[position-1]
	copy(p.queue[position-1:], p.queue[position:])
	p.queue[len(p.queue)-1] = audio.Track{}
	p.queue = p.queue[:len(p.queue)-1]
	p.mu.Unlock()
	p.notifyChange()
	p.refreshPrefetch()
	return removed, nil
}

func (p *Player) Move(from, to int) error {
	p.mu.Lock()
	if err := p.queuePositionLocked(from); err != nil {
		p.mu.Unlock()
		return err
	}
	if err := p.queuePositionLocked(to); err != nil {
		p.mu.Unlock()
		return err
	}
	track := p.queue[from-1]
	if from < to {
		copy(p.queue[from-1:to-1], p.queue[from:to])
	} else if from > to {
		copy(p.queue[to:from], p.queue[to-1:from-1])
	}
	p.queue[to-1] = track
	p.mu.Unlock()
	p.notifyChange()
	p.refreshPrefetch()
	return nil
}

func (p *Player) ClearQueue() (int, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, fmt.Errorf("el reproductor está cerrado")
	}
	count := len(p.queue)
	p.queue = nil
	// Extraction for the track already becoming current must survive clearing the queue.
	if p.current == nil || p.prefetchKey != p.current.URL {
		p.cancelPrefetchLocked()
	}
	p.mu.Unlock()
	p.notifyChange()
	return count, nil
}

func (p *Player) SkipTo(position int) (audio.Track, error) {
	p.mu.Lock()
	if err := p.queuePositionLocked(position); err != nil {
		p.mu.Unlock()
		return audio.Track{}, err
	}
	track := p.queue[position-1]
	for i := 0; i < position-1; i++ {
		p.queue[i] = audio.Track{}
	}
	p.queue = p.queue[position-1:]
	if p.prefetchKey != track.URL {
		p.cancelPrefetchLocked()
	}
	p.prefetchReady = false
	p.playbackGeneration++
	p.paused.Store(false)
	if p.cancel != nil {
		p.cancel()
	} else {
		p.signal()
	}
	p.mu.Unlock()
	p.notifyChange()
	return track, nil
}

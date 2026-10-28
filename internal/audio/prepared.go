package audio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const (
	maxMetadataBytes  = 4 << 20
	maxPreparedTracks = 8
	preparedTTL       = 5 * time.Minute
)

// Prepared data contains signed media URLs. Keep it bounded in memory and never log it.
type preparedMedia struct {
	data    []byte
	created time.Time
}

type mediaPreparation struct {
	done chan struct{}
	err  error
}

type metadataBuffer struct{ bytes.Buffer }

func (b *metadataBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > maxMetadataBytes {
		return 0, fmt.Errorf("media metadata exceeds size limit")
	}
	return b.Buffer.Write(data)
}

func (r *Resolver) Prepare(ctx context.Context, resource string) error {
	r.preparedMu.Lock()
	if value, ok := r.prepared[resource]; ok && time.Since(value.created) < preparedTTL {
		r.preparedMu.Unlock()
		return nil
	}
	if existing := r.preparing[resource]; existing != nil {
		r.preparedMu.Unlock()
		select {
		case <-existing.done:
			return existing.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	flight := &mediaPreparation{done: make(chan struct{})}
	if r.preparing == nil {
		r.preparing = make(map[string]*mediaPreparation)
	}
	r.preparing[resource] = flight
	r.preparedMu.Unlock()
	err := r.prepareSource(ctx, resource)
	r.preparedMu.Lock()
	flight.err = err
	delete(r.preparing, resource)
	close(flight.done)
	r.preparedMu.Unlock()
	return err
}

func (r *Resolver) prepareSource(ctx context.Context, resource string) error {
	output, err := r.extractMetadata(ctx, resource)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.storePrepared(resource, output, time.Now()) {
		return fmt.Errorf("media metadata has no playable formats")
	}
	return nil
}

// Playback joins extraction already running for its source instead of starting it again.
func (r *Resolver) awaitPrepared(ctx context.Context, resource string) ([]byte, error) {
	r.preparedMu.Lock()
	flight := r.preparing[resource]
	r.preparedMu.Unlock()
	if flight != nil {
		select {
		case <-flight.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return r.takePrepared(resource, time.Now()), nil
}

func (r *Resolver) storePrepared(resource string, data []byte, now time.Time) bool {
	if len(data) > maxMetadataBytes {
		return false
	}
	var info struct {
		Formats []json.RawMessage `json:"formats"`
		URL     string            `json:"url"`
	}
	if json.Unmarshal(data, &info) != nil || (len(info.Formats) == 0 && info.URL == "") {
		return false
	}
	r.preparedMu.Lock()
	defer r.preparedMu.Unlock()
	if r.prepared == nil {
		r.prepared = make(map[string]preparedMedia)
	}
	for key, value := range r.prepared {
		if now.Sub(value.created) >= preparedTTL {
			delete(r.prepared, key)
		}
	}
	if _, exists := r.prepared[resource]; !exists && len(r.prepared) >= maxPreparedTracks {
		oldestKey := ""
		var oldest time.Time
		for key, value := range r.prepared {
			if oldestKey == "" || value.created.Before(oldest) {
				oldestKey = key
				oldest = value.created
			}
		}
		delete(r.prepared, oldestKey)
	}
	r.prepared[resource] = preparedMedia{data: append([]byte(nil), data...), created: now}
	return true
}

func (r *Resolver) takePrepared(resource string, now time.Time) []byte {
	r.preparedMu.Lock()
	defer r.preparedMu.Unlock()
	value, exists := r.prepared[resource]
	delete(r.prepared, resource)
	if !exists || now.Sub(value.created) >= preparedTTL {
		return nil
	}
	return value.data
}

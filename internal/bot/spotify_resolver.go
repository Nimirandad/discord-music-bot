package bot

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"discord-music-bot/internal/audio"
	"discord-music-bot/internal/spotify"
)

const spotifyResolutionWorkers = 4

func resolveSpotifyTracks(
	ctx context.Context,
	tracks []spotify.Track,
	resolve func(context.Context, spotify.Track) (audio.Track, error),
) ([]audio.Track, error) {
	resolved := make([]audio.Track, 0, len(tracks))
	_, err := resolveSpotifyTracksProgressively(ctx, tracks, resolve, func(batch []audio.Track) error {
		resolved = append(resolved, batch...)
		return nil
	})
	return resolved, err
}

func resolveSpotifyTracksProgressively(
	ctx context.Context,
	tracks []spotify.Track,
	resolve func(context.Context, spotify.Track) (audio.Track, error),
	enqueue func([]audio.Track) error,
) (int, error) {
	if len(tracks) == 0 {
		return 0, nil
	}

	workerContext, cancel := context.WithCancel(ctx)
	jobs := make(chan int, len(tracks))
	results := make(chan spotifyTrackResult, len(tracks))
	for index := range tracks {
		jobs <- index
	}
	close(jobs)

	var workers sync.WaitGroup
	workerCount := min(spotifyResolutionWorkers, len(tracks))
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-workerContext.Done():
					return
				case index, ok := <-jobs:
					if !ok {
						return
					}
					track, err := resolve(workerContext, tracks[index])
					result := spotifyTrackResult{index: index, track: track, err: err}
					select {
					case results <- result:
					case <-workerContext.Done():
						return
					}
				}
			}
		}()
	}
	defer func() {
		cancel()
		workers.Wait()
	}()

	ready := make([]spotifyTrackResult, len(tracks))
	completed := make([]bool, len(tracks))
	nextToEnqueue := 0
	var errs []error
	failedCount := 0
	for received := 0; received < len(tracks); received++ {
		var result spotifyTrackResult
		select {
		case result = <-results:
		case <-workerContext.Done():
			return failedCount, errors.Join(append(errs, workerContext.Err())...)
		}
		ready[result.index] = result
		completed[result.index] = true

		batch := make([]audio.Track, 0, spotifyResolutionWorkers)
		for nextToEnqueue < len(tracks) && completed[nextToEnqueue] {
			result := ready[nextToEnqueue]
			if result.err != nil {
				failedCount++
				errs = append(errs, fmt.Errorf(
					"resolve Spotify track %q by %q: %w",
					tracks[nextToEnqueue].Name,
					tracks[nextToEnqueue].Artist,
					result.err,
				))
			} else {
				batch = append(batch, result.track)
			}
			nextToEnqueue++
		}
		if len(batch) > 0 {
			if err := enqueue(batch); err != nil {
				return failedCount, fmt.Errorf("enqueue resolved Spotify tracks: %w", err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		errs = append(errs, err)
	}
	return failedCount, errors.Join(errs...)
}

type spotifyTrackResult struct {
	index int
	track audio.Track
	err   error
}

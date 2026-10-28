package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/go-zeromq/zmq4"
)

func TestPlayCandidatesTriesFallbackAfterFailure(t *testing.T) {
	var attempts []string
	err := playCandidates(context.Background(), []Track{
		{Title: "unplayable"},
		{Title: "playable"},
	}, nil, func(_ context.Context, track Track) error {
		attempts = append(attempts, track.Title)
		if track.Title == "unplayable" {
			return fmt.Errorf("invalid media")
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("playCandidates() error = %v", err)
	}
	if !reflect.DeepEqual(attempts, []string{"unplayable", "playable"}) {
		t.Fatalf("attempts = %v, want [unplayable playable]", attempts)
	}
}

func TestPlayCandidatesStopsAfterAllCandidatesFail(t *testing.T) {
	attempts := 0
	err := playCandidates(context.Background(), []Track{{Title: "first"}, {Title: "second"}}, nil,
		func(context.Context, Track) error {
			attempts++
			return fmt.Errorf("unavailable")
		}, nil)
	if err == nil {
		t.Fatal("playCandidates() error = nil, want failure")
	}
	if attempts != 2 {
		t.Fatalf("attempt count = %d, want 2", attempts)
	}
	if !strings.Contains(err.Error(), "all 2 audio candidates failed") {
		t.Errorf("error = %q, want candidate count", err)
	}
}

func TestPlayCandidatesDoesNotFallbackAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := playCandidates(ctx, []Track{{Title: "first"}, {Title: "second"}}, nil,
		func(context.Context, Track) error {
			attempts++
			cancel()
			return fmt.Errorf("interrupted")
		}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("playCandidates() error = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempt count = %d, want 1", attempts)
	}
}

func TestPlayCandidatesResolvesDeferredFallbackOnlyAfterFailure(t *testing.T) {
	track := Track{Title: "direct video", FallbackQueries: []string{"artist song"}}
	resolutionCount := 0
	var attempts []string
	err := playCandidates(context.Background(), []Track{track}, func(_ context.Context, failed Track) ([]Track, error) {
		resolutionCount++
		if failed.Title != "direct video" {
			t.Fatalf("failed track = %q, want direct video", failed.Title)
		}
		return []Track{{Title: "search fallback", URL: "https://youtube.com/watch?v=fallback"}}, nil
	}, func(_ context.Context, candidate Track) error {
		attempts = append(attempts, candidate.Title)
		if candidate.Title == "direct video" {
			return fmt.Errorf("unavailable")
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("playCandidates() error = %v", err)
	}
	if resolutionCount != 1 {
		t.Fatalf("fallback resolution count = %d, want 1", resolutionCount)
	}
	if !reflect.DeepEqual(attempts, []string{"direct video", "search fallback"}) {
		t.Fatalf("attempts = %v, want [direct video search fallback]", attempts)
	}
}

func TestPlayCandidatesDoesNotResolveFallbackWhenTrackPlays(t *testing.T) {
	resolutionCount := 0
	err := playCandidates(context.Background(), []Track{{Title: "direct video", FallbackQueries: []string{"artist song"}}},
		func(context.Context, Track) ([]Track, error) {
			resolutionCount++
			return nil, nil
		}, func(context.Context, Track) error {
			return nil
		}, nil)
	if err != nil {
		t.Fatalf("playCandidates() error = %v", err)
	}
	if resolutionCount != 0 {
		t.Fatalf("fallback resolution count = %d, want 0", resolutionCount)
	}
}

func TestFFmpegVolumeControlSendsNamedFilterCommand(t *testing.T) {
	port, err := reserveVolumePort()
	if err != nil {
		t.Fatalf("reserve volume port: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := zmq4.NewRep(ctx, zmq4.WithTimeout(2*time.Second))
	defer server.Close()
	if err := server.Listen(fmt.Sprintf("tcp://127.0.0.1:%d", port)); err != nil {
		t.Fatalf("listen for volume-control request: %v", err)
	}

	type requestResult struct {
		command string
		err     error
	}
	requests := make(chan requestResult, 1)
	go func() {
		request, err := server.Recv()
		if err != nil {
			requests <- requestResult{err: err}
			return
		}
		requests <- requestResult{command: string(request.Bytes())}
		if err := server.Send(zmq4.NewMsgString("0 Success")); err != nil {
			requests <- requestResult{err: err}
		}
	}()

	control := newFFmpegVolumeControl(port)
	if err := control.SetVolume(0.7); err != nil {
		t.Fatalf("SetVolume() error = %v", err)
	}
	result := <-requests
	if result.err != nil {
		t.Fatalf("receive volume-control request: %v", result.err)
	}
	if result.command != "volume@music volume 0.70" {
		t.Fatalf("volume command = %q, want %q", result.command, "volume@music volume 0.70")
	}
	if got := control.AppliedVolume(); got != 0.7 {
		t.Fatalf("applied volume = %v, want 0.7", got)
	}
}

func TestFFmpegVolumeFilterEscapesZMQAddress(t *testing.T) {
	const want = `volume@music=1.0,azmq=bind_address=tcp\\://127.0.0.1\\:56199`
	if got := ffmpegVolumeFilter(56199); got != want {
		t.Fatalf("ffmpegVolumeFilter() = %q, want %q", got, want)
	}
}

func TestOggOpusProviderPausesWithoutConsumingAudio(t *testing.T) {
	stream := oggPage([][]byte{
		[]byte("OpusHead"),
		[]byte("OpusTags"),
		{0xAA, 0xBB},
	})
	paused := true
	volume := 1.0
	var pauseChanges []bool
	var appliedVolume = 1.0
	provider := newOggOpusProvider(bytes.NewReader(stream), PlaybackControls{
		IsPaused: func() bool { return paused },
		Volume:   func() float64 { return volume },
	}, func(state bool) error {
		pauseChanges = append(pauseChanges, state)
		return nil
	}, func(value float64) error {
		appliedVolume = value
		return nil
	}, func() float64 { return appliedVolume })

	frame, err := provider.ProvideOpusFrame()
	if err != nil {
		t.Fatalf("ProvideOpusFrame() while paused error = %v", err)
	}
	if !bytes.Equal(frame, voice.SilenceAudioFrame) {
		t.Fatalf("paused frame = %v, want silence %v", frame, voice.SilenceAudioFrame)
	}

	paused = false
	frame, err = provider.ProvideOpusFrame()
	if err != nil {
		t.Fatalf("ProvideOpusFrame() after resume error = %v", err)
	}
	if want := []byte{0xAA, 0xBB}; !bytes.Equal(frame, want) {
		t.Fatalf("resumed frame = %v, want %v", frame, want)
	}
	if want := []bool{true, false}; !reflect.DeepEqual(pauseChanges, want) {
		t.Fatalf("pause transitions = %v, want %v", pauseChanges, want)
	}
}

func TestOggOpusProviderHoldsFramesUntilDAVEReady(t *testing.T) {
	stream := oggPage([][]byte{
		[]byte("OpusHead"),
		[]byte("OpusTags"),
		{0xAA, 0xBB},
	})
	holdFrames := true
	provider := newOggOpusProvider(bytes.NewReader(stream), PlaybackControls{
		HoldFrames: func() bool { return holdFrames },
	}, nil, nil, nil)

	frame, err := provider.ProvideOpusFrame()
	if err != nil {
		t.Fatalf("ProvideOpusFrame() while DAVE is negotiating error = %v", err)
	}
	if !bytes.Equal(frame, voice.SilenceAudioFrame) {
		t.Fatalf("frame while DAVE is negotiating = %v, want protocol silence %v", frame, voice.SilenceAudioFrame)
	}

	holdFrames = false
	frame, err = provider.ProvideOpusFrame()
	if err != nil {
		t.Fatalf("ProvideOpusFrame() after DAVE is ready error = %v", err)
	}
	if want := []byte{0xAA, 0xBB}; !bytes.Equal(frame, want) {
		t.Fatalf("frame after DAVE is ready = %v, want %v", frame, want)
	}
	if got := provider.heldFrameCount.Load(); got != 1 {
		t.Errorf("held frame count = %d, want 1", got)
	}
}

func TestOggOpusProviderAppliesVolumeAfterResuming(t *testing.T) {
	stream := oggPage([][]byte{
		[]byte("OpusHead"),
		[]byte("OpusTags"),
		{0xAA, 0xBB},
	})
	paused := true
	volume := 1.0
	appliedVolume := 1.0
	provider := newOggOpusProvider(bytes.NewReader(stream), PlaybackControls{
		IsPaused: func() bool { return paused },
		Volume:   func() float64 { return volume },
	}, func(bool) error {
		return nil
	}, func(value float64) error {
		appliedVolume = value
		return nil
	}, func() float64 { return appliedVolume })

	if _, err := provider.ProvideOpusFrame(); err != nil {
		t.Fatalf("ProvideOpusFrame() while paused error = %v", err)
	}
	volume = 0.7
	paused = false
	if _, err := provider.ProvideOpusFrame(); err != nil {
		t.Fatalf("ProvideOpusFrame() after resume error = %v", err)
	}
	if appliedVolume != 0.7 {
		t.Fatalf("applied volume = %v, want 0.7", appliedVolume)
	}
}

func TestOggOpusReaderSkipsHeadersAndReadsMultiplePacketsPerPage(t *testing.T) {
	stream := oggPage([][]byte{
		[]byte("OpusHead"),
		[]byte("OpusTags"),
		{0xAA, 0xBB},
		{0xCC, 0xDD},
	})
	reader := newOggOpusReader(bytes.NewReader(stream))

	for index, want := range [][]byte{{0xAA, 0xBB}, {0xCC, 0xDD}} {
		packet, err := reader.NextAudioPacket()
		if err != nil {
			t.Fatalf("NextAudioPacket() #%d error = %v", index+1, err)
		}
		if !bytes.Equal(packet, want) {
			t.Fatalf("NextAudioPacket() #%d = %v, want %v", index+1, packet, want)
		}
	}
	if _, err := reader.NextAudioPacket(); err != io.EOF {
		t.Fatalf("NextAudioPacket() after audio = %v, want EOF", err)
	}
}

func TestOggOpusReaderReadsPacketsAcrossPages(t *testing.T) {
	audioPrefix := bytes.Repeat([]byte{0x11}, 255)
	firstPage := oggPageWithSegments([]byte("OpusHead"), []byte("OpusTags"), audioPrefix)
	secondPage := oggPageWithSegments([]byte{0xAA, 0xBB})
	reader := newOggOpusReader(bytes.NewReader(append(firstPage, secondPage...)))

	packet, err := reader.NextAudioPacket()
	if err != nil {
		t.Fatalf("NextAudioPacket() error = %v", err)
	}
	want := append(audioPrefix, 0xAA, 0xBB)
	if !bytes.Equal(packet, want) {
		t.Fatalf("NextAudioPacket() length = %d, want %d", len(packet), len(want))
	}
}

func TestOggOpusReaderRejectsInvalidPage(t *testing.T) {
	reader := newOggOpusReader(bytes.NewReader([]byte("not an ogg stream")))
	if _, err := reader.NextAudioPacket(); err == nil {
		t.Fatal("NextAudioPacket() error = nil, want invalid page error")
	}
}

func TestOggOpusReaderRejectsTruncatedPage(t *testing.T) {
	page := oggPage([][]byte{[]byte("OpusHead")})
	reader := newOggOpusReader(bytes.NewReader(page[:len(page)-1]))
	if _, err := reader.NextAudioPacket(); err == nil {
		t.Fatal("NextAudioPacket() error = nil, want truncated page error")
	}
}

func oggPage(packets [][]byte) []byte {
	segments := make([][]byte, len(packets))
	copy(segments, packets)
	return oggPageWithSegments(segments...)
}

func oggPageWithSegments(segments ...[]byte) []byte {
	page := make([]byte, 27+len(segments))
	copy(page[:4], "OggS")
	page[26] = byte(len(segments))
	for index, segment := range segments {
		page[27+index] = byte(len(segment))
		page = append(page, segment...)
	}
	return page
}

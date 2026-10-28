package audio

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/go-zeromq/zmq4"
)

const sampleRate = 48000

type Streamer struct {
	YTDLPPath   string
	FFmpegPath  string
	CookiesFile string
	Logger      *slog.Logger
	Resolver    *Resolver
}

func NewStreamer(logger *slog.Logger) *Streamer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Streamer{YTDLPPath: "yt-dlp", FFmpegPath: "ffmpeg", Logger: logger, Resolver: NewResolver()}
}

type PlaybackControls struct {
	IsPaused           func() bool
	HoldFrames         func() bool
	Volume             func() float64
	RegisterVolumeSink func(func(float64) error) error
}

func (s *Streamer) Play(ctx context.Context, connection voice.Conn, track Track, controls PlaybackControls) error {
	startedAt := time.Now()
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	args := []string{"--no-warnings", "--quiet", "--no-cache-dir", "--no-playlist", "-f", "bestaudio/best", "-o", "-"}
	args = appendYTDLPCookies(args, s.CookiesFile)
	args = appendYTDLPPOProvider(args)
	args = append(args, track.URL)
	downloader := exec.CommandContext(streamCtx, s.YTDLPPath, args...)
	downloaderOutput, err := downloader.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open yt-dlp output: %w", err)
	}
	var downloaderStderr boundedBuffer
	downloader.Stderr = &downloaderStderr

	volumePort, err := reserveVolumePort()
	if err != nil {
		return fmt.Errorf("reserve FFmpeg volume-control port: %w", err)
	}
	filter := ffmpegVolumeFilter(volumePort)
	transcoder := exec.CommandContext(streamCtx, s.FFmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-i", "pipe:0", "-vn",
		"-af", filter,
		"-c:a", "libopus", "-application", "audio", "-frame_duration", "20",
		"-ar", fmt.Sprint(sampleRate), "-ac", "2", "-b:a", "128k", "-f", "ogg", "pipe:1",
	)
	transcoder.Stdin = downloaderOutput
	transcoderOutput, err := transcoder.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open ffmpeg output: %w", err)
	}
	transcoderStderr := boundedBuffer{
		onWrite: func(data []byte) {
			s.Logger.Error("ffmpeg reported an error", "output", strings.TrimSpace(string(data)))
		},
	}
	transcoder.Stderr = &transcoderStderr

	if err := downloader.Start(); err != nil {
		return fmt.Errorf("start yt-dlp: %w", err)
	}
	if err := transcoder.Start(); err != nil {
		_ = downloader.Process.Kill()
		_ = downloader.Wait()
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	pauser := newSubprocessPauser(downloader.Process, transcoder.Process)
	volumeControl := newFFmpegVolumeControl(volumePort)
	if controls.RegisterVolumeSink != nil {
		if err := controls.RegisterVolumeSink(volumeControl.SetVolume); err != nil {
			cancel()
			_ = transcoder.Wait()
			_ = downloader.Wait()
			return fmt.Errorf("register FFmpeg volume control: %w", err)
		}
	}
	provider := newOggOpusProvider(transcoderOutput, controls, pauser.SetPaused, volumeControl.SetVolume, volumeControl.AppliedVolume)
	provider.logger = s.Logger
	s.Logger.Info("audio stream started", "track", track.Title)
	connection.SetOpusFrameProvider(provider)
	select {
	case <-provider.Done():
	case <-ctx.Done():
		cancel()
		provider.Close()
		<-provider.Done()
	}

	providerErr := provider.Err()
	if providerErr != nil && !errors.Is(providerErr, io.EOF) {
		cancel()
	}
	transcoderErr := transcoder.Wait()
	downloaderErr := downloader.Wait()
	s.Logger.Info("audio stream ended",
		"track", track.Title,
		"opus_frames", provider.frameCount.Load(),
		"opus_bytes", provider.byteCount.Load(),
		"held_frames", provider.heldFrameCount.Load(),
		"elapsed", time.Since(startedAt),
		"playback_context_error", ctx.Err(),
		"provider_error", providerErr,
		"ffmpeg_error", transcoderErr,
		"yt_dlp_error", downloaderErr,
	)

	if err := ctx.Err(); err != nil {
		return err
	}
	if providerErr != nil && !errors.Is(providerErr, io.EOF) {
		return providerErr
	}
	if transcoderErr != nil {
		return processError("ffmpeg", transcoderErr, transcoderStderr.String())
	}
	if downloaderErr != nil {
		return processError("yt-dlp", downloaderErr, downloaderStderr.String())
	}
	if provider.frameCount.Load() == 0 {
		return fmt.Errorf("audio stream produced no Opus frames")
	}
	return nil
}

func (s *Streamer) PlayWithFallbacks(ctx context.Context, connection voice.Conn, track Track, controls PlaybackControls) error {
	candidates := make([]Track, 0, 1+len(track.Fallbacks))
	candidates = append(candidates, track)
	candidates = append(candidates, track.Fallbacks...)
	return playCandidates(ctx, candidates, func(ctx context.Context, failed Track) ([]Track, error) {
		if len(failed.FallbackQueries) == 0 {
			return nil, nil
		}
		if s.Resolver == nil {
			return nil, fmt.Errorf("resolve fallback audio: resolver is unavailable")
		}
		var fallbacks []Track
		var resolveErrors []error
		for _, query := range failed.FallbackQueries {
			resolved, err := s.Resolver.ResolveFallbackCandidates(ctx, query)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				resolveErrors = append(resolveErrors, fmt.Errorf("query %q: %w", query, err))
				continue
			}
			fallbacks = appendUniqueTracks(fallbacks, append([]Track{resolved}, resolved.Fallbacks...), failed.URL)
		}
		if len(fallbacks) == 0 && len(resolveErrors) > 0 {
			return nil, fmt.Errorf("resolve fallback audio for %q: %w", failed.Title, errors.Join(resolveErrors...))
		}
		return fallbacks, nil
	}, func(ctx context.Context, candidate Track) error {
		return s.Play(ctx, connection, candidate, controls)
	}, func(candidate, fallback Track, err error) {
		s.Logger.Warn("audio candidate failed; trying fallback",
			"track", candidate.Title,
			"fallback_track", fallback.Title,
			"error", err,
		)
	})
}

func playCandidates(
	ctx context.Context,
	candidates []Track,
	resolveFallback func(context.Context, Track) ([]Track, error),
	play func(context.Context, Track) error,
	onFallback func(Track, Track, error),
) error {
	if len(candidates) == 0 {
		return fmt.Errorf("no audio candidates to play")
	}
	for index := 0; index < len(candidates); index++ {
		candidate := candidates[index]
		if err := ctx.Err(); err != nil {
			return err
		}
		err := play(ctx, candidate)
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if index == len(candidates)-1 && len(candidate.FallbackQueries) > 0 && resolveFallback != nil {
			fallbacks, resolveErr := resolveFallback(ctx, candidate)
			if resolveErr != nil {
				return fmt.Errorf("audio candidate %q failed: %v; %w", candidate.Title, err, resolveErr)
			}
			candidates = appendUniqueTracks(candidates, fallbacks, candidate.URL)
		}
		if index == len(candidates)-1 {
			return fmt.Errorf("all %d audio candidates failed: %w", len(candidates), err)
		}
		if onFallback != nil {
			onFallback(candidate, candidates[index+1], err)
		}
	}
	return nil
}

func reserveVolumePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return 0, fmt.Errorf("unexpected listener address type %T", listener.Addr())
	}
	port := address.Port
	if err := listener.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

func ffmpegVolumeFilter(port int) string {
	return fmt.Sprintf(`volume@music=1.0,azmq=bind_address=tcp\\://127.0.0.1\\:%d`, port)
}

type ffmpegVolumeControl struct {
	endpoint string
	mu       sync.Mutex
	applied  atomic.Uint64
}

func newFFmpegVolumeControl(port int) *ffmpegVolumeControl {
	control := &ffmpegVolumeControl{endpoint: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	control.applied.Store(atomicFloat64(1))
	return control
}

func (c *ffmpegVolumeControl) SetVolume(volume float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	socket := zmq4.NewReq(ctx, zmq4.WithTimeout(2*time.Second))
	defer socket.Close()
	if err := socket.Dial("tcp://" + c.endpoint); err != nil {
		return fmt.Errorf("connect to FFmpeg volume control: %w", err)
	}
	command := fmt.Sprintf("volume@music volume %.2f", volume)
	if err := socket.Send(zmq4.NewMsgString(command)); err != nil {
		return fmt.Errorf("send FFmpeg volume command: %w", err)
	}
	response, err := socket.Recv()
	if err != nil {
		return fmt.Errorf("receive FFmpeg volume response: %w", err)
	}
	reply := strings.TrimSpace(string(response.Bytes()))
	fields := strings.Fields(reply)
	if len(fields) == 0 || fields[0] != "0" {
		return fmt.Errorf("FFmpeg rejected volume command %q: %s", command, reply)
	}
	c.applied.Store(atomicFloat64(volume))
	return nil
}

func (c *ffmpegVolumeControl) AppliedVolume() float64 {
	return float64FromAtomic(c.applied.Load())
}

func atomicFloat64(value float64) uint64 {
	return math.Float64bits(value)
}

func float64FromAtomic(value uint64) float64 {
	return math.Float64frombits(value)
}

func processError(name string, err error, output string) error {
	output = strings.TrimSpace(output)
	if output == "" {
		return fmt.Errorf("%s exited unsuccessfully: %w", name, err)
	}
	return fmt.Errorf("%s exited unsuccessfully: %w: %s", name, err, output)
}

type boundedBuffer struct {
	data    []byte
	onWrite func([]byte)
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	const maximum = 4096
	written := len(data)
	if remaining := maximum - len(b.data); remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		b.data = append(b.data, data...)
		if b.onWrite != nil && len(data) > 0 {
			b.onWrite(data)
		}
	}
	return written, nil
}

func (b *boundedBuffer) String() string {
	return string(b.data)
}

type oggOpusProvider struct {
	reader         *oggOpusReader
	controls       PlaybackControls
	setPaused      func(bool) error
	setVolume      func(float64) error
	appliedVolume  func() float64
	startedAt      time.Time
	pauseApplied   bool
	done           chan struct{}
	once           sync.Once
	frameCount     atomic.Uint64
	byteCount      atomic.Uint64
	heldFrameCount atomic.Uint64
	callCount      atomic.Uint64
	logger         *slog.Logger
	mu             sync.Mutex
	err            error
}

func newOggOpusProvider(source io.Reader, controls PlaybackControls, setPaused func(bool) error, setVolume func(float64) error, appliedVolume func() float64) *oggOpusProvider {
	return &oggOpusProvider{
		reader:        newOggOpusReader(source),
		controls:      controls,
		setPaused:     setPaused,
		setVolume:     setVolume,
		appliedVolume: appliedVolume,
		startedAt:     time.Now(),
		done:          make(chan struct{}),
	}
}

func (p *oggOpusProvider) ProvideOpusFrame() ([]byte, error) {
	select {
	case <-p.done:
		return nil, io.EOF
	default:
	}
	if p.callCount.Add(1) == 1 && p.logger != nil {
		p.logger.Info("voice audio sender requested its first Opus frame")
	}
	if p.controls.HoldFrames != nil && p.controls.HoldFrames() {
		p.heldFrameCount.Add(1)
		return append([]byte(nil), voice.SilenceAudioFrame...), nil
	}
	paused := p.controls.IsPaused != nil && p.controls.IsPaused()
	if paused != p.pauseApplied && p.setPaused != nil {
		if err := p.setPaused(paused); err != nil {
			p.setErr(fmt.Errorf("change audio process pause state: %w", err))
			p.once.Do(func() { close(p.done) })
			return nil, err
		}
		p.pauseApplied = paused
	}
	if paused {
		return append([]byte(nil), voice.SilenceAudioFrame...), nil
	}
	packet, err := p.reader.NextAudioPacket()
	if err != nil {
		p.setErr(err)
		p.once.Do(func() { close(p.done) })
		return nil, err
	}
	if p.frameCount.Add(1) == 1 && p.logger != nil {
		p.logger.Info("voice audio sender received its first music Opus frame", "elapsed", time.Since(p.startedAt))
	}
	p.byteCount.Add(uint64(len(packet)))
	if p.controls.Volume != nil && p.setVolume != nil && p.appliedVolume != nil {
		volume := p.controls.Volume()
		if volume != p.appliedVolume() {
			if err := p.setVolume(volume); err != nil {
				p.setErr(fmt.Errorf("change playback volume: %w", err))
				p.once.Do(func() { close(p.done) })
				return nil, err
			}
		}
	}
	return packet, nil
}

func (p *oggOpusProvider) setErr(err error) {
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
}

func (p *oggOpusProvider) Close() {
	p.once.Do(func() {
		p.mu.Lock()
		p.err = io.EOF
		p.mu.Unlock()
		close(p.done)
	})
}

func (p *oggOpusProvider) Done() <-chan struct{} {
	return p.done
}

func (p *oggOpusProvider) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

type oggOpusReader struct {
	reader        *bufio.Reader
	headersToSkip int
	packet        []byte
	pending       [][]byte
}

func newOggOpusReader(source io.Reader) *oggOpusReader {
	return &oggOpusReader{
		reader:        bufio.NewReader(source),
		headersToSkip: 2,
	}
}

func (r *oggOpusReader) NextAudioPacket() ([]byte, error) {
	for {
		packet, err := r.nextPacket()
		if err != nil {
			return nil, err
		}
		if r.headersToSkip > 0 {
			r.headersToSkip--
			continue
		}
		if len(packet) == 0 {
			continue
		}
		return packet, nil
	}
}

func (r *oggOpusReader) nextPacket() ([]byte, error) {
	for {
		if len(r.pending) > 0 {
			packet := r.pending[0]
			r.pending = r.pending[1:]
			return packet, nil
		}

		header := make([]byte, 27)
		if n, err := io.ReadFull(r.reader, header); err != nil {
			if errors.Is(err, io.EOF) && n == 0 {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("read Ogg page header: %w", err)
		}
		if string(header[:4]) != "OggS" || header[4] != 0 {
			return nil, fmt.Errorf("invalid Ogg page header")
		}

		lacing := make([]byte, int(header[26]))
		if _, err := io.ReadFull(r.reader, lacing); err != nil {
			return nil, fmt.Errorf("read Ogg page segment table: %w", err)
		}
		bodyLength := 0
		for _, segmentLength := range lacing {
			bodyLength += int(segmentLength)
		}
		body := make([]byte, bodyLength)
		if _, err := io.ReadFull(r.reader, body); err != nil {
			return nil, fmt.Errorf("read Ogg page body: %w", err)
		}

		offset := 0
		for _, segmentLength := range lacing {
			end := offset + int(segmentLength)
			r.packet = append(r.packet, body[offset:end]...)
			offset = end
			if segmentLength < 255 {
				r.pending = append(r.pending, r.packet)
				r.packet = nil
			}
		}
	}
}

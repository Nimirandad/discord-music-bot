package audio

import (
	"context"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-zeromq/zmq4"
)

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

package e2e

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/engine/ffmpeg"
	"github.com/stupside/castor/internal/cast/execute"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/probe"
)

// tools are resolved once per run and passed by value so cell dependencies are visible in signatures.
var findTools = sync.OnceValues(func() (tools, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return tools{}, err
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		return tools{}, err
	}
	return tools{ffmpeg: ffmpeg, ffprobe: ffprobe}, nil
})

type tools struct {
	ffmpeg  string
	ffprobe string
}

func newTools(t *testing.T) tools {
	t.Helper()
	if testing.Short() {
		t.Skip("live streams run in real time; skipped in -short mode")
	}

	found, err := findTools()
	if err != nil {
		t.Skipf("%v; this suite drives real ffmpeg", err)
	}
	return found
}

// rwTimeout is the shipped mid-read deadline, not a convenient test value.
const rwTimeout = 30 * time.Second

// castConfig is what every cast runs on: renderer port is the case's stand-in.
func castConfig(tl tools) execute.Config {
	return execute.Config{
		FFmpegPath: tl.ffmpeg,
		Encoders:   ffmpeg.Encoders(tl.ffmpeg),
		Probes:     probe.FFprobe(tl.ffprobe),
		MaxHeight:  1080,
	}
}

// noStage is the optional work casts run beside reads: none here (transcription would add cgo).
func noStage(context.Context, string) execute.Burn { return nil }

const localAddress = "127.0.0.1"

// newExecutor uses a stand-in renderer whose port is the case's parameter, not production's registry lookup.
func newExecutor(cfg execute.Config, dev device.Device, stage execute.Subtitles) *execute.Executor {
	cfg.Renderer = standIn{dev: dev}
	cfg.Addresses = fixedAddress(localAddress)
	cfg.Subtitles = stage
	return execute.NewExecutor(cfg)
}

// standIn is a one-device renderer port whose profile is derived (not stated) to match production behavior.
type standIn struct{ dev device.Device }

func (s standIn) Profile() media.Capabilities {
	return media.Capabilities{SelfFetch: s.dev.Capabilities().SelfFetch}
}

func (s standIn) Connect(context.Context) (device.Device, error) { return s.dev, nil }

type fixedAddress string

func (a fixedAddress) LocalIPv4(context.Context) (string, error) { return string(a), nil }

type servedRenderer struct {
	// decodes is the whole capability advertisement.
	decodes media.Capabilities

	// played sends each URL so tests can proceed instantly without polling.
	played chan string

	// drain fetches what is served on a goroutine so Play returns immediately (like a real renderer).
	drain bool

	mu    sync.Mutex
	plays []string
}

var _ device.Device = (*servedRenderer)(nil)

func (d *servedRenderer) Play(ctx context.Context, streamURL *url.URL, _ string) error {
	d.mu.Lock()
	d.plays = append(d.plays, streamURL.String())
	d.mu.Unlock()
	if d.played != nil {
		select {
		case d.played <- streamURL.String():
		default: // a case that has stopped listening must not wedge the cast
		}
	}
	if d.drain {
		go d.fetch(ctx, streamURL.String())
	}
	return nil
}

// fetch takes the served stream and discards it, bounded by the cast's context.
func (d *servedRenderer) fetch(ctx context.Context, streamURL string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}

func (d *servedRenderer) Capabilities() media.Capabilities { return d.decodes }

// AwaitEnd returns when the cast is torn down (no playback lifecycle here).
func (d *servedRenderer) AwaitEnd(ctx context.Context) error {
	<-ctx.Done()
	return context.Cause(ctx)
}

func (d *servedRenderer) StreamHeaders(string) map[string]string { return nil }
func (d *servedRenderer) Close() error                           { return nil }

func (d *servedRenderer) snapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.plays...)
}

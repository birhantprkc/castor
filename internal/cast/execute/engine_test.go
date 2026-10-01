package execute

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/fetch"
	"github.com/stupside/castor/internal/cast/transcode"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/probe"
	"github.com/stupside/castor/internal/source"
)

// fakeDevice plays what it is handed; if drain is set, it fetches the served stream and keeps it in served.
type fakeDevice struct {
	caps   media.Capabilities
	drain  bool
	refuse error
	closed atomic.Bool
	served []byte
}

func newExecutor(cfg Config, acquire acquireFunc, stage Subtitles) *Executor {
	cfg.Renderer = acquiring{Renderer: cfg.Renderer, acquire: acquire}
	cfg.Listeners = loopback{}
	cfg.Subtitles = stage
	return NewExecutor(cfg)
}

type acquireFunc func(context.Context) (device.Device, error)

type acquiring struct {
	Renderer
	acquire acquireFunc
}

func (a acquiring) Connect(ctx context.Context) (device.Device, error) { return a.acquire(ctx) }

type profile media.Capabilities

func (p profile) Profile() media.Capabilities { return media.Capabilities(p) }

func (p profile) Connect(context.Context) (device.Device, error) {
	return nil, errors.New("the renderer of a cast driven here is the fixture's to hand over")
}

// rendererOf is a renderer port with a static profile that connects to dev.
func rendererOf(static media.Capabilities, dev device.Device) Renderer {
	return acquiring{Renderer: profile(static), acquire: connectTo(dev)}
}

// loopback serves deliveries where these tests' renderers reach them.
type loopback struct{}

func (loopback) Listen(context.Context) (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

var _ device.Device = (*fakeDevice)(nil)

func (d *fakeDevice) Play(ctx context.Context, streamURL *url.URL, _ string) error {
	if d.refuse != nil {
		return d.refuse
	}
	if !d.drain {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL.String(), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	d.served, err = io.ReadAll(resp.Body)
	return err
}

func (d *fakeDevice) AwaitEnd(ctx context.Context) error {
	<-ctx.Done()
	return context.Cause(ctx)
}

func (d *fakeDevice) Capabilities() media.Capabilities       { return d.caps }
func (d *fakeDevice) StreamHeaders(string) map[string]string { return nil }
func (d *fakeDevice) Close() error                           { d.closed.Store(true); return nil }

func connectTo(dev device.Device) acquireFunc {
	return func(context.Context) (device.Device, error) { return dev, nil }
}

func selfFetching() media.Capabilities { return media.Capabilities{SelfFetch: true} }
func pushOnly() media.Capabilities     { return media.Capabilities{} }

func chromecastLike(accepts ...string) media.Capabilities {
	return media.Capabilities{
		SelfFetch:       true,
		Containers:      accepts,
		ServedContainer: media.MP4,
		Video:           []media.VideoSupport{{Codec: media.CodecH264}},
		Audio:           []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
	}
}

func dlnaLike() media.Capabilities {
	return media.Capabilities{
		Video:           []media.VideoSupport{{Codec: media.CodecH264}},
		Audio:           []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 2}},
		ServedContainer: media.MPEGTS,
	}
}

// TestEncoderFailureFailsTheCast is the regression test for "castor exited 0 having cast nothing".
func TestEncoderFailureFailsTheCast(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	srv := httptest.NewServer(http.HandlerFunc(http.NotFound))
	t.Cleanup(srv.Close)
	sourceURL, err := url.Parse(srv.URL + "/gone.mp4")
	if err != nil {
		t.Fatal(err)
	}

	// Rejects the source container, so it is remuxed by an encoder that cannot open its input.
	dev := &fakeDevice{
		caps:  media.Capabilities{SelfFetch: true, Containers: []string{media.MKV}, ServedContainer: media.MP4},
		drain: true,
	}
	ctx, cancel := context.WithTimeout(t.Context(), castTimeout)
	defer cancel()
	err = castOnce(ctx, t, castConfig(selfFetching(), ffmpegPath, ffprobePath), connectTo(dev), &source.Stream{URL: sourceURL, ContentType: media.MP4})
	if err == nil {
		t.Fatal("the cast reported success though its encoder died before producing anything")
	}
}

func TestADeadReadIsReportedAsTheReadsOwnFailure(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	srv := httptest.NewServer(http.HandlerFunc(http.NotFound))
	t.Cleanup(srv.Close)
	sourceURL, err := url.Parse(srv.URL + "/gone.mp4")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), castTimeout)
	defer cancel()
	program := programFromStream(t, &source.Stream{URL: sourceURL, ContentType: media.MP4})
	out := newExecutor(castConfig(pushOnly(), ffmpegPath, ffprobePath), connectTo(&fakeDevice{caps: dlnaLike()}), noStage).
		Run(ctx, attempt.Attempt{Try: 1, Program: program, Fetch: sourceFetchPlan(t, program, testReadDeadline)})

	if out.Evidence.ReadErr == nil || !errors.Is(out.Err, out.Evidence.ReadErr) {
		t.Fatalf("the cast reports %v with read error %v, want the read's own failure", out.Err, out.Evidence.ReadErr)
	}
	if out.Evidence.Reached != attempt.PhaseReading {
		t.Errorf("reached %s, want %s", out.Evidence.Reached, attempt.PhaseReading)
	}
	if out.Evidence.ReadExit <= 0 {
		t.Errorf("exit status %d for a reader that exited on its own account", out.Evidence.ReadExit)
	}
	if got := out.Evidence.Copied; !got.Video || !got.Audio {
		t.Errorf("the outcome says the reader was copying %s, want both halves", got)
	}
}

// testReadDeadline is the mid-read stall bound every cast in this suite reads with.
const testReadDeadline = 30 * time.Second

func castOnce(ctx context.Context, t *testing.T, cfg Config, connect acquireFunc, candidate *source.Stream) error {
	t.Helper()
	program := programFromStream(t, candidate)
	return newExecutor(cfg, connect, noStage).Run(ctx, attempt.Attempt{
		Try:     1,
		Program: program,
		Fetch:   sourceFetchPlan(t, program, testReadDeadline),
	}).Err
}

func sourceFetchPlan(t *testing.T, program media.Program, rwTimeout time.Duration) fetch.Plan {
	t.Helper()
	return uniformFetchPlan(program, fetch.For(media.Fetch{}, rwTimeout))
}

func uniformFetchPlan(program media.Program, policy fetch.Policy) fetch.Plan {
	plan := make(fetch.Plan, len(program.Inputs))
	for _, input := range program.Inputs {
		plan[input.ID] = policy
	}
	return plan
}

const castTimeout = 90 * time.Second

func castConfig(static media.Capabilities, ffmpegPath, ffprobePath string) Config {
	return Config{
		Renderer:   profile(static),
		FFmpegPath: ffmpegPath,
		Binary:     ffmpeg.Inspect(ffmpegPath),
		Encoders:   transcode.Encoders(ffmpegPath),
		Probes:     probe.FFprobe(ffprobePath),
		MaxHeight:  1080,
		Timelines:  direct{},
	}
}

// direct follows no timeline: every input is read as the origin publishes it.
type direct struct{}

func (direct) Republish(_ context.Context, program media.Program) (media.Program, func() error, error) {
	return program, func() error { return nil }, nil
}

type fixtureOrigin struct {
	server      *httptest.Server
	path        string
	contentType string
}

func (o fixtureOrigin) stream() *source.Stream {
	u, _ := url.Parse(o.server.URL + o.path)
	return &source.Stream{URL: u, ContentType: o.contentType}
}

// serveFixture serves a one-second H.264/AAC mp4.
func serveFixture(t *testing.T, ffmpegPath string) fixtureOrigin {
	t.Helper()
	return serveGenerated(t, ffmpegPath, "fixture.mp4", "/movie.mp4", media.MP4,
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-profile:v", "baseline",
		"-c:a", "aac", "-ac", "2", "-shortest", "-movflags", "+faststart",
	)
}

func serveSilentFixture(t *testing.T, ffmpegPath string) fixtureOrigin {
	t.Helper()
	return serveGenerated(t, ffmpegPath, "silent.mp4", "/silent.mp4", media.MP4,
		"-map", "0:v", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-profile:v", "baseline", "-movflags", "+faststart",
	)
}

func serveGenerated(t *testing.T, ffmpegPath, filename, urlPath, contentType string, outputArgs ...string) fixtureOrigin {
	t.Helper()
	path := generateFixture(t, ffmpegPath, filename, 1, outputArgs)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, path)
	}))
	t.Cleanup(server.Close)
	return fixtureOrigin{server: server, path: urlPath, contentType: contentType}
}

// generateFixture renders one synthetic program to disk and returns its path.
func generateFixture(t *testing.T, ffmpegPath, filename string, seconds int, outputArgs []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), filename)
	duration := strconv.Itoa(seconds)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15:duration=" + duration,
		"-f", "lavfi", "-i", "sine=frequency=440:duration=" + duration,
	}
	args = append(args, outputArgs...)
	args = append(args, path)

	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput(); err != nil {
		t.Fatalf("generating %s: %v\n%s", filename, err, out)
	}
	return path
}

func requireFFmpegTools(t *testing.T) (ffmpeg, ffprobe string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	ffprobe, err = exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not on PATH")
	}
	return ffmpeg, ffprobe
}

func programFromStream(t *testing.T, stream *source.Stream) media.Program {
	t.Helper()
	program, err := source.ProgramFor(stream)
	if err != nil {
		t.Fatal(err)
	}
	return program
}

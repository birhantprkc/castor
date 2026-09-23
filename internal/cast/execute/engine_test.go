package execute

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/engine/ffmpeg"
	"github.com/stupside/castor/internal/cast/policy/compose"
	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/probe"
	"github.com/stupside/castor/internal/source"
)

// fakeDevice records Play calls; if drain is set, fetches and drains the served stream.
type fakeDevice struct {
	caps   media.Capabilities
	drain  bool
	refuse error
	// tee, when set, receives the served bytes as they are drained.
	tee    io.Writer
	closed atomic.Bool

	mu    sync.Mutex
	plays []playCall
}

func newExecutorAt(cfg Config, acquire acquireFunc, stage Subtitles, address string) *Executor {
	cfg.Renderer = acquiring{Renderer: cfg.Renderer, acquire: acquire}
	cfg.Addresses = fixedAddress(address)
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

// renderer is a renderer port with a static profile that connects to dev.
func renderer(static media.Capabilities, dev device.Device) Renderer {
	return acquiring{Renderer: profile(static), acquire: connectTo(dev)}
}

type fixedAddress string

func (a fixedAddress) LocalIPv4(context.Context) (string, error) { return string(a), nil }

type playCall struct {
	url         string
	contentType string
}

var _ device.Device = (*fakeDevice)(nil)

func (d *fakeDevice) Play(ctx context.Context, streamURL *url.URL, contentType string) error {
	d.mu.Lock()
	d.plays = append(d.plays, playCall{url: streamURL.String(), contentType: contentType})
	d.mu.Unlock()

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
	var sink io.Writer = io.Discard
	if d.tee != nil {
		sink = d.tee
	}
	_, err = io.Copy(sink, resp.Body)
	return err
}

func (d *fakeDevice) AwaitEnd(ctx context.Context) error {
	<-ctx.Done()
	return context.Cause(ctx)
}

func (d *fakeDevice) Capabilities() media.Capabilities       { return d.caps }
func (d *fakeDevice) StreamHeaders(string) map[string]string { return nil }
func (d *fakeDevice) Close() error                           { d.closed.Store(true); return nil }

func (d *fakeDevice) snapshot() []playCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.plays)
}

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

type castCase struct {
	name     string
	source   func(*testing.T, string) fixtureOrigin
	declared int

	profile  media.Capabilities
	caps     media.Capabilities
	delivery compose.DeliveryPreference

	// served is the content type the renderer must be handed; empty means the source URL itself.
	served string
	video  media.Codec
	audio  media.Codec
	height int
}

// TestCastMatrix runs each composition end to end and reads what the renderer received.
func TestCastMatrix(t *testing.T) {
	cases := []castCase{{
		name:    "a source the renderer can fetch is handed over untouched",
		source:  serveFixture,
		profile: selfFetching(),
		caps:    chromecastLike(media.MP4),
	}, {
		name:     "the operator's serve preference relays a source nothing else would",
		source:   serveFixture,
		profile:  selfFetching(),
		caps:     chromecastLike(media.MP4),
		delivery: compose.DeliveryServe,
		served:   media.MP4,
	}, {
		name:    "a demuxed program keeps its audio through the spool",
		source:  serveDemuxedHLSFixture,
		profile: pushOnly(),
		caps:    dlnaLike(),
		served:  media.MPEGTS,
		video:   media.CodecH264,
		audio:   media.CodecAAC,
	}, {
		name:     "a source declared above the cast's ceiling is served scaled rather than handed over",
		source:   serveTallFixture,
		declared: 1440,
		profile:  selfFetching(),
		caps:     chromecastLike(media.MKV),
		served:   media.MP4,
		height:   1080,
	}}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ffmpegPath, ffprobePath := requireFFmpegTools(t)

			origin := tt.source(t, ffmpegPath)
			candidate := origin.stream()
			candidate.Probe = &media.ProbeInfo{
				VideoCodec: media.CodecH264, VideoBitDepth: 8,
				AudioCodec: media.CodecAAC, AudioChannels: 2,
			}

			received := filepath.Join(t.TempDir(), "received")
			sink, err := os.Create(received)
			if err != nil {
				t.Fatal(err)
			}
			defer sink.Close()

			dev := &fakeDevice{caps: tt.caps, drain: tt.served != "", tee: sink}
			ctx, cancel := context.WithTimeout(t.Context(), castTimeout)
			defer cancel()
			if err := castProgramRung(ctx, t, castConfig(tt.profile, ffmpegPath, ffprobePath), connectTo(dev),
				origin.program(t, candidate), source.Rendition{Height: tt.declared}, tt.delivery); err != nil {
				t.Fatalf("cast: %v", err)
			}

			plays := dev.snapshot()
			if len(plays) != 1 {
				t.Fatalf("expected exactly one Play call, got %d: %+v", len(plays), plays)
			}
			if tt.served == "" {
				if plays[0].url != candidate.URL.String() || plays[0].contentType != candidate.ContentType {
					t.Errorf("Play = %+v, want the source URL %q as %s", plays[0], candidate.URL, candidate.ContentType)
				}
				return
			}
			if plays[0].contentType != tt.served {
				t.Errorf("served content type = %q, want %q", plays[0].contentType, tt.served)
			}
			if plays[0].url == candidate.URL.String() {
				t.Errorf("a relayed cast pointed the device at the source URL %q", candidate.URL)
			}
			if err := sink.Close(); err != nil {
				t.Fatal(err)
			}
			assertReceived(t, ffprobePath, received, tt)
		})
	}
}

func assertReceived(t *testing.T, ffprobePath, path string, tt castCase) {
	t.Helper()
	if tt.video == "" && tt.audio == "" && tt.height == 0 {
		return
	}
	info, _, err := probe.FFprobe(ffprobePath).File(path).Probe(t.Context())
	if err != nil {
		t.Fatalf("probing what the renderer received: %v", err)
	}
	if tt.video != "" && info.VideoCodec != tt.video {
		t.Errorf("received video codec = %q, want %q", info.VideoCodec, tt.video)
	}
	if tt.audio != "" {
		if info.AudioCodec != tt.audio {
			t.Errorf("received audio codec = %q, want %q", info.AudioCodec, tt.audio)
		}
		if countPackets(t, ffprobePath, "a:0", path) == 0 {
			t.Error("the received stream declares audio but carries no packets")
		}
	}
	if tt.height > 0 && info.VideoHeight != tt.height {
		t.Errorf("received a %dp picture, want %dp", info.VideoHeight, tt.height)
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
	err = castOnce(ctx, t, castConfig(selfFetching(), ffmpegPath, ffprobePath), connectTo(dev), &source.Candidate{URL: sourceURL, ContentType: media.MP4})
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
	program := programFromStream(t, &source.Candidate{URL: sourceURL, ContentType: media.MP4})
	out := newExecutorAt(castConfig(pushOnly(), ffmpegPath, ffprobePath), connectTo(&fakeDevice{caps: dlnaLike()}), noStage, "127.0.0.1").
		Run(ctx, attempt.Attempt{Try: 1, Program: program, Read: sourceReadPlan(t, program, testReadDeadline)})

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

func castOnce(ctx context.Context, t *testing.T, cfg Config, connect acquireFunc, candidate *source.Candidate) error {
	t.Helper()
	return castProgramRung(ctx, t, cfg, connect, programFromStream(t, candidate), source.Rendition{}, compose.DeliveryAuto)
}

func castProgramRung(ctx context.Context, t *testing.T, cfg Config, connect acquireFunc, program media.Program, rung source.Rendition, delivery compose.DeliveryPreference) error {
	t.Helper()
	return newExecutorAt(cfg, connect, noStage, "127.0.0.1").Run(ctx, attempt.Attempt{
		Try:       1,
		Program:   program,
		Rendition: rung,
		Read:      sourceReadPlan(t, program, testReadDeadline),
		Delivery:  delivery,
	}).Err
}

func sourceReadPlan(t *testing.T, program media.Program, rwTimeout time.Duration) read.Plan {
	t.Helper()
	return uniformReadPlan(program, read.For(media.Fetch{}, rwTimeout))
}

func uniformReadPlan(program media.Program, policy read.Policy) read.Plan {
	plan := make(read.Plan, len(program.Inputs))
	for _, input := range program.Inputs {
		copy := policy
		copy.RetryStatuses = slices.Clone(policy.RetryStatuses)
		plan[input.ID] = copy
	}
	return plan
}

const castTimeout = 90 * time.Second

func castConfig(static media.Capabilities, ffmpegPath, ffprobePath string) Config {
	return Config{
		Renderer:   profile(static),
		FFmpegPath: ffmpegPath,
		Encoders:   ffmpeg.Encoders(ffmpegPath),
		Probes:     probe.FFprobe(ffprobePath),
		MaxHeight:  1080,
	}
}

type fixtureOrigin struct {
	server      *httptest.Server
	path        string
	audioPath   string // set when the origin publishes audio as its own rendition
	contentType string
}

func (o fixtureOrigin) stream() *source.Candidate {
	u, _ := url.Parse(o.server.URL + o.path)
	return &source.Candidate{URL: u, ContentType: o.contentType}
}

func (o fixtureOrigin) program(t *testing.T, stream *source.Candidate) media.Program {
	t.Helper()
	inputs := []media.Input{{
		ID: media.PrimaryInputID, URL: stream.URL, ContentType: stream.ContentType,
	}}
	tracks := []media.TrackRef{
		{Input: media.PrimaryInputID, Kind: media.TrackVideo, Optional: true},
		{Input: media.PrimaryInputID, Kind: media.TrackAudio, Optional: true},
	}
	end := media.EndAtLongest
	if o.audioPath != "" {
		audio, err := url.Parse(o.server.URL + o.audioPath)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, media.Input{
			ID: media.AudioInputID, URL: audio, ContentType: stream.ContentType,
		})
		tracks[1].Input = media.AudioInputID
		end = media.EndAtShortest
	}
	program, err := media.NewProgram(media.Program{
		Inputs:     inputs,
		Tracks:     tracks,
		ClockInput: media.PrimaryInputID,
		EndPolicy:  end,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stream.Probe != nil {
		program.SetMeasurement(*stream.Probe)
	}
	return program
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

func serveTallFixture(t *testing.T, ffmpegPath string) fixtureOrigin {
	t.Helper()
	return serveGenerated(t, ffmpegPath, "tall.mkv", "/tall.mkv", media.MKV,
		"-vf", "scale=1920:1440",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-profile:v", "baseline",
		"-c:a", "aac", "-ac", "2", "-shortest",
	)
}

func serveDemuxedHLSFixture(t *testing.T, ffmpegPath string) fixtureOrigin {
	t.Helper()
	dir := t.TempDir()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-profile:v", "baseline",
		"-c:a", "aac", "-ac", "2", "-shortest",
		"-f", "hls",
		"-hls_time", "1",
		"-hls_list_size", "0",
		"-var_stream_map", "v:0,agroup:aud a:0,agroup:aud",
		filepath.Join(dir, "rendition_%v.m3u8"),
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput(); err != nil {
		t.Fatalf("generating demuxed HLS fixture: %v\n%s", err, out)
	}

	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(server.Close)
	return fixtureOrigin{server: server, path: "/rendition_0.m3u8", audioPath: "/rendition_1.m3u8", contentType: media.HLS}
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

// countPackets counts the packets ffprobe can actually read off one stream.
func countPackets(t *testing.T, ffprobePath, stream, path string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffprobePath,
		"-v", "error", "-select_streams", stream, "-count_packets",
		"-show_entries", "stream=nb_read_packets", "-of", "csv=p=0", path,
	).Output()
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSuffix(fields[0], ","))
	return n
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

func programFromStream(t *testing.T, stream *source.Candidate) media.Program {
	t.Helper()
	program, err := source.ProgramFor(stream)
	if err != nil {
		t.Fatal(err)
	}
	return program
}

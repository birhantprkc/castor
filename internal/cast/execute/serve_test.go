package execute

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/engine/deliver"
	"github.com/stupside/castor/internal/cast/engine/ffmpeg"
	"github.com/stupside/castor/internal/cast/policy/compose"
	"github.com/stupside/castor/internal/cast/policy/plan"
	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/cast/policy/watch"
	"github.com/stupside/castor/internal/container"
	"github.com/stupside/castor/internal/media"
)

// took returns a playing buffered cast whose delivery ends as wait says.
func took(t *testing.T, wait func(ctx context.Context) error) *cast {
	t.Helper()
	c := gateFixture(t, 0)
	c.dev = observedRenderer{wait: blocking}
	c.sink = &fakeMechanism{wait: wait}
	return c
}

// blocking is a delivery that never finishes.
func blocking(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// over is a delivery that has already run its course.
func over(context.Context) error { return nil }

// fakeMechanism is a delivery whose answers a test states outright.
type fakeMechanism struct {
	wait     func(ctx context.Context) error
	audience watch.Audience
	settled  error
	asked    atomic.Int64
}

func (m *fakeMechanism) URL() *url.URL                  { return &url.URL{} }
func (m *fakeMechanism) Wait(ctx context.Context) error { return m.wait(ctx) }
func (m *fakeMechanism) Drained() <-chan struct{}       { return nil }
func (m *fakeMechanism) Audience() watch.Audience       { return m.audience }
func (m *fakeMechanism) Settled() error                 { m.asked.Add(1); return m.settled }
func (m *fakeMechanism) Close() error                   { return nil }

func (m *fakeMechanism) Artifact() deliver.Artifact {
	return deliver.Artifact{Subject: "a stated delivery", Landed: func() int64 { return 8 << 20 }}
}

// observedRenderer is a renderer whose playback lifecycle a case states.
type observedRenderer struct {
	wait func(context.Context) error
}

func (observedRenderer) Play(context.Context, *url.URL, string) error { return nil }
func (observedRenderer) StreamHeaders(string) map[string]string       { return nil }
func (observedRenderer) Capabilities() media.Capabilities             { return media.Capabilities{} }
func (observedRenderer) Close() error                                 { return nil }
func (r observedRenderer) AwaitEnd(ctx context.Context) error         { return r.wait(ctx) }

func TestAPlayingRemuxIsWatchedOverItsEncoder(t *testing.T) {
	c := took(t, blocking)
	c.row.Kind = compose.Remux
	c.sink.(*fakeMechanism).audience = stoppedRenderer{last: time.Now().Add(-watch.StallWindow - time.Second), buffered: 20 * time.Minute}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	fault, ok := errors.AsType[*watch.Fault](c.supervising(ctx))
	if !ok {
		t.Fatal("a remux whose renderer took nothing was never judged in flight")
	}
	if fault.Kind != watch.Unfetched || fault.Health.Headroom != 0 {
		t.Errorf("verdict %s at %gx headroom, want %s judged on castor's own encoder", fault.Kind, fault.Health.Headroom, watch.Unfetched)
	}
}

func TestPlaybackLifecycleStopsBothSides(t *testing.T) {
	t.Run("a remote stop cancels local delivery", func(t *testing.T) {
		cancelled := make(chan struct{})
		c := took(t, func(ctx context.Context) error {
			<-ctx.Done()
			close(cancelled)
			return context.Cause(ctx)
		})
		c.dev = observedRenderer{wait: func(context.Context) error { return nil }}

		if err := c.supervising(t.Context()); err != nil {
			t.Fatalf("supervising = %v, want a clean remote stop", err)
		}
		select {
		case <-cancelled:
		default:
			t.Fatal("remote playback ended without cancelling the local delivery")
		}
	})

	t.Run("a renderer that went away is reported as itself, not as teardown", func(t *testing.T) {
		gone := &media.Gone{Renderer: "Living Room TV", Err: errors.New("no route to host")}
		c := took(t, blocking)
		c.dev = observedRenderer{wait: func(context.Context) error { return gone }}

		err := c.supervising(t.Context())
		if away, ok := errors.AsType[*media.Gone](err); !ok || away != gone {
			t.Fatalf("supervising = %v, want the renderer's own account of having gone away", err)
		}
		if errors.Is(err, context.Canceled) {
			t.Errorf("supervising = %v, teardown cancellation reported as another failure", err)
		}
	})
}

func TestOnlyADeliveryThatRanItsCourseIsAskedWhetherTheRendererTookIt(t *testing.T) {
	short := errors.New("the renderer was handed a fraction of what this cast produced")
	inFlight := func(ctx context.Context, c *cast) error {
		return errors.Join(c.supervising(ctx), c.settled(ctx))
	}

	t.Run("a delivery that ran its course is asked", func(t *testing.T) {
		c := took(t, over)
		c.sink.(*fakeMechanism).settled = short
		if err := inFlight(t.Context(), c); !errors.Is(err, short) {
			t.Fatalf("the cast = %v, want the delivery's own account of what the renderer took", err)
		}
	})

	t.Run("a cast the user stopped is not asked", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		c := took(t, blocking)
		c.sink.(*fakeMechanism).settled = short
		if err := inFlight(ctx, c); !errors.Is(err, context.Canceled) || errors.Is(err, short) {
			t.Fatalf("the cast = %v, want the cancellation alone", err)
		}
		if asked := c.sink.(*fakeMechanism).asked.Load(); asked != 0 {
			t.Errorf("a cancelled cast was asked %d time(s) what its renderer took", asked)
		}
	})

	t.Run("a delivery the renderer took is clean", func(t *testing.T) {
		if err := inFlight(t.Context(), took(t, over)); err != nil {
			t.Fatalf("the cast = %v, want nil", err)
		}
	})
}

func TestAServedCastReachesEachPhaseAndHandsItsSupervisorAnAudience(t *testing.T) {
	c := openFixture(t, media.MP4, t.TempDir())
	if err := c.opened(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.evidence.Reached != attempt.PhaseOpening {
		t.Errorf("reached %s after the artifact gate, want %s", c.evidence.Reached, attempt.PhaseOpening)
	}
	if err := c.hand(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.evidence.Reached != attempt.PhasePlaying {
		t.Errorf("reached %s once the renderer accepted the URL, want %s", c.evidence.Reached, attempt.PhasePlaying)
	}

	aud := c.sink.Audience()
	if aud == nil {
		t.Fatal("the stream delivery handed its supervisor no audience")
	}
	if err := until(t.Context(), func() bool { return aud.Buffered() > 0 }); err != nil {
		t.Errorf("the delivery reported no fetchable media for an encode that ran: %v", err)
	}
}

// until polls cond until it holds or ctx ends.
func until(ctx context.Context, cond func() bool) error {
	for !cond() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}

func TestADeliveryPublishesItsOwnArtifactsAndNothingElseOfTheCasts(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "spool.ts"), []byte("the cast's own buffer"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := openFixture(t, media.HLS, workDir)
	if err := c.opened(t.Context()); err != nil {
		t.Fatalf("the playlist never appeared: %v", err)
	}
	leak := c.sink.URL()
	leak.Path = "/spool.ts"
	if got := statusOf(t, leak); got == http.StatusOK {
		t.Errorf("GET %s = 200: the delivery publishes the cast's private files", leak.Path)
	}
	if got := statusOf(t, c.sink.URL()); got != http.StatusOK {
		t.Errorf("GET playlist = %d, so this is a broken server rather than isolation", got)
	}
}

func statusOf(t *testing.T, u *url.URL) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestTeardownStopsAnEncoderParkedOnAnInputThatWentQuiet: castor's own kill is not an encoder failure.
func TestTeardownStopsAnEncoderParkedOnAnInputThatWentQuiet(t *testing.T) {
	ffmpegPath, _ := requireFFmpegTools(t)
	format, ok := container.FormatForContentType(media.MPEGTS)
	if !ok {
		t.Fatal("the format registry cannot produce mpegts")
	}
	head := programHead(t, ffmpegPath)
	copying := func(input ffmpeg.EncodeInput) ffmpeg.EncodeOptions {
		return ffmpeg.EncodeOptions{
			Format: format,
			Input:  input,
			Probe:  media.ProbeInfo{VideoCodec: media.CodecH264, AudioCodec: media.CodecAAC},
			Video:  plan.CopyVideo(),
			Audio:  plan.CopyAudio(),
		}
	}

	for _, tt := range []struct {
		name  string
		setup func(t *testing.T, c *cast)
	}{{
		name: "reading a buffer nothing will grow",
		setup: func(t *testing.T, c *cast) {
			sp, err := deliver.NewSpool(filepath.Join(c.workDir, "spool.ts"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sp.CloseWrite(nil) })
			if _, err := sp.Write(head); err != nil {
				t.Fatal(err)
			}
			c.row, c.spool = compose.Row{Kind: compose.ReadOnce}, sp
			c.opts = copying(ffmpeg.FromPipe(ffmpeg.SpoolFormat, read.Pace{}))
		},
	}, {
		name: "reading an origin that went quiet",
		setup: func(t *testing.T, c *cast) {
			c.opts = copying(ffmpeg.FromSource(programSourceWithin(t, quietOrigin(t, head), media.MPEGTS, time.Hour)))
		},
	}} {
		t.Run(tt.name, func(t *testing.T) {
			c := servingCast(t, ffmpegPath, t.TempDir())
			tt.setup(t, c)
			for _, step := range []step{(*cast).produce, (*cast).opened, (*cast).hand} {
				if err := step(c, t.Context()); err != nil {
					t.Fatal(err)
				}
			}

			const teardown = 30 * time.Second
			done := make(chan error, 1)
			go func() { done <- c.stop() }()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("the teardown reported %v for an encoder castor killed itself", err)
				}
			case <-time.After(teardown):
				t.Fatalf("the teardown did not return within %s", teardown)
			}
		})
	}
}

// quietOrigin hands over the head of a program and then goes quiet without ending the response.
func quietOrigin(t *testing.T, head []byte) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", media.MPEGTS)
		if _, err := w.Write(head); err != nil {
			return
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL + "/program.ts")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// programHead is the first two thirds of a program, so an encoder reading it never reaches its end.
func programHead(t *testing.T, ffmpegPath string) []byte {
	t.Helper()
	program, err := os.ReadFile(generateFixture(t, ffmpegPath, "program.ts", 12,
		[]string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ac", "2", "-shortest", "-f", "mpegts"}))
	if err != nil {
		t.Fatal(err)
	}
	return program[:len(program)*2/3]
}

// servingCast is the cast a delivery is opened by (reduced to this step's reads).
func servingCast(t *testing.T, ffmpegPath, workDir string) *cast {
	t.Helper()
	c := &cast{
		cfg:     Config{FFmpegPath: ffmpegPath, Encoders: ffmpeg.Encoders(ffmpegPath)},
		row:     compose.Row{Kind: compose.Remux},
		ctx:     t.Context(),
		localIP: "127.0.0.1",
		workDir: workDir,
		dev:     probingRenderer{},
	}
	c.stop = sync.OnceValue(c.teardown)
	t.Cleanup(func() { _ = c.stop() })
	return c
}

// openFixture opens one real delivery of a real encode over a real origin.
func openFixture(t *testing.T, contentType, workDir string) *cast {
	t.Helper()
	ffmpegPath, _ := requireFFmpegTools(t)
	origin := serveFixture(t, ffmpegPath)
	c := servingCast(t, ffmpegPath, workDir)
	format, ok := container.FormatForContentType(contentType)
	if !ok {
		t.Fatalf("the format registry cannot produce %s", contentType)
	}
	c.opts = ffmpeg.EncodeOptions{
		Format: format,
		Input:  ffmpeg.FromSource(programSourceWithin(t, origin.stream().URL, media.MP4, 30*time.Second)),
		Probe:  media.ProbeInfo{VideoCodec: media.CodecH264},
		Video:  plan.CopyVideo(),
		Audio:  plan.EncodeAudio(plan.AudioEncode{Codec: media.CodecAAC}),
	}
	if err := c.produce(t.Context()); err != nil {
		t.Fatal(err)
	}
	return c
}

// probingRenderer accepts the URL, probes it like a real firmware, and never takes a byte.
type probingRenderer struct{}

func (probingRenderer) Play(ctx context.Context, streamURL *url.URL, _ string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, streamURL.String(), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (probingRenderer) AwaitEnd(ctx context.Context) error {
	<-ctx.Done()
	return context.Cause(ctx)
}

func (probingRenderer) StreamHeaders(string) map[string]string { return nil }
func (probingRenderer) Capabilities() media.Capabilities       { return media.Capabilities{} }
func (probingRenderer) Close() error                           { return nil }

func programSourceWithin(t *testing.T, sourceURL *url.URL, contentType string, rwTimeout time.Duration) ffmpeg.ProgramSource {
	t.Helper()
	program, err := media.NewProgram(media.Program{
		Inputs: []media.Input{{ID: media.PrimaryInputID, URL: sourceURL, ContentType: contentType}},
		Tracks: []media.TrackRef{
			{Input: media.PrimaryInputID, Kind: media.TrackVideo, Optional: true},
			{Input: media.PrimaryInputID, Kind: media.TrackAudio, Optional: true},
		},
		ClockInput: media.PrimaryInputID,
		EndPolicy:  media.EndAtLongest,
	})
	if err != nil {
		t.Fatal(err)
	}
	source, err := ffmpeg.NewProgramSource(program, map[media.InputID]read.Policy{
		media.PrimaryInputID: read.For(media.Fetch{}, rwTimeout),
	})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

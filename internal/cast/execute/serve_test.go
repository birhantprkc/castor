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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/container"
	"github.com/stupside/castor/internal/cast/deliver"
	"github.com/stupside/castor/internal/cast/plan"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/cast/transcode"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
)

// playing is a buffered cast handed to a renderer, whose delivery ends as its mechanism says.
type playing struct {
	buf  *buffered
	dev  device.Device
	mech *fakeMechanism
}

func (p playing) delivery() delivery { return delivery{sink: p.mech, output: p.buf.reader} }

// took returns a playing buffered cast whose delivery ends as wait says.
func took(t *testing.T, wait func(ctx context.Context) error) playing {
	t.Helper()
	return playing{buf: gateFixture(t, 0), dev: observedRenderer{wait: blocking}, mech: &fakeMechanism{wait: wait}}
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
	p := took(t, blocking)
	p.mech.audience = stoppedRenderer{last: time.Now().Add(-watch.StallWindow - time.Second), buffered: 20 * time.Minute}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := supervising(ctx, p.dev, p.delivery(), feed{})
	fault, ok := errors.AsType[*watch.Fault](err)
	if !ok {
		t.Fatal("a remux whose renderer took nothing was never judged in flight")
	}
	if fault.Kind != watch.Unfetched || fault.Health.Headroom != 0 {
		t.Errorf("verdict %s at %gx headroom, want %s judged on castor's own encoder", fault.Kind, fault.Health.Headroom, watch.Unfetched)
	}
}

func TestARendererThatWentAwayIsReportedAsItselfNotAsTeardown(t *testing.T) {
	gone := &media.Gone{Renderer: "Living Room TV", Err: errors.New("no route to host")}
	p := took(t, blocking)
	p.dev = observedRenderer{wait: func(context.Context) error { return gone }}

	_, err := supervising(t.Context(), p.dev, p.delivery(), feed{buffered: p.buf})
	if away, ok := errors.AsType[*media.Gone](err); !ok || away != gone {
		t.Fatalf("supervising = %v, want the renderer's own account of having gone away", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("supervising = %v, teardown cancellation reported as another failure", err)
	}
}

func TestOnlyADeliveryThatRanItsCourseIsAskedWhetherTheRendererTookIt(t *testing.T) {
	short := errors.New("the renderer was handed a fraction of what this cast produced")
	inFlight := func(ctx context.Context, p playing) error {
		ran, err := supervising(ctx, p.dev, p.delivery(), feed{buffered: p.buf})
		if err != nil || !ran {
			return err
		}
		return p.mech.Settled()
	}

	t.Run("a delivery that ran its course is asked", func(t *testing.T) {
		p := took(t, over)
		p.mech.settled = short
		if err := inFlight(t.Context(), p); !errors.Is(err, short) {
			t.Fatalf("the cast = %v, want the delivery's own account of what the renderer took", err)
		}
	})

	t.Run("a cast the user stopped is not asked", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		p := took(t, blocking)
		p.mech.settled = short
		if err := inFlight(ctx, p); !errors.Is(err, context.Canceled) || errors.Is(err, short) {
			t.Fatalf("the cast = %v, want the cancellation alone", err)
		}
		if asked := p.mech.asked.Load(); asked != 0 {
			t.Errorf("a cancelled cast was asked %d time(s) what its renderer took", asked)
		}
	})
}

func TestADeliveryPublishesItsOwnArtifactsAndNothingElseOfTheCasts(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "spool.ts"), []byte("the cast's own buffer"), 0o600); err != nil {
		t.Fatal(err)
	}

	d := openFixture(t, media.HLS, workDir)
	if err := awaitArtifact(t.Context(), d); err != nil {
		t.Fatalf("the playlist never appeared: %v", err)
	}
	leak := d.sink.URL()
	leak.Path = "/spool.ts"
	if got := statusOf(t, leak); got == http.StatusOK {
		t.Errorf("GET %s = 200: the delivery publishes the cast's private files", leak.Path)
	}
	if got := statusOf(t, d.sink.URL()); got != http.StatusOK {
		t.Errorf("GET playlist = %d, so this is a broken server rather than isolation", got)
	}
}

// TestARelayedCastIsOpenedOverItsRead: with no encoder, the read is the producer whose end the opening judges.
func TestARelayedCastIsOpenedOverItsRead(t *testing.T) {
	s, buf := servingSession(t, ""), gateFixture(t, 0)
	buf.reader.spool.CloseWrite(nil)
	close(buf.reader.done)
	d, err := s.produce(observedRenderer{wait: blocking}, loopback(t), feed{buffered: buf}, verbatim(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Well inside the first-bytes grace, so only the read's own end can decide the opening.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = awaitArtifact(ctx, d)
	if fault, ok := errors.AsType[*watch.Fault](err); !ok || fault.Kind != watch.Dead {
		t.Fatalf("a read that ended having buffered nothing opened as %v, want a %s verdict", err, watch.Dead)
	}
}

// TestARelayedCastFailsWithItsRead: the read writes the served bytes, so its failure is the cast's as an encoder's was.
func TestARelayedCastFailsWithItsRead(t *testing.T) {
	failed := errors.New("upstream pull: the origin refused a segment on every retry")
	s, buf := servingSession(t, ""), gateFixture(t, 0)
	buf.reader.err = failed
	buf.reader.spool.CloseWrite(failed)
	close(buf.reader.done)
	if _, err := s.produce(observedRenderer{wait: blocking}, loopback(t), feed{buffered: buf}, verbatim(), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.releases.release(); !errors.Is(err, failed) {
		t.Errorf("releasing a cast whose read failed reported %v, want the read's own failure", err)
	}
}

// verbatim is an encode that would rewrite the buffer unchanged, so the buffer is served instead.
func verbatim() transcode.EncodeOptions {
	return transcode.EncodeOptions{
		Input:  transcode.FromPipe(transcode.SpoolFormat, read.Pace{}),
		Format: transcode.SpoolFormat,
		Video:  plan.CopyVideo(),
		Audio:  plan.CopyAudio(),
	}
}

// loopback is a workspace served on the loopback address.
func loopback(t *testing.T) workspace {
	t.Helper()
	return workspace{localIP: "127.0.0.1", dir: t.TempDir()}
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
	copying := func(input transcode.EncodeInput) transcode.EncodeOptions {
		return transcode.EncodeOptions{
			Format: format,
			Input:  input,
			Probe:  media.ProbeInfo{VideoCodec: media.CodecH264, AudioCodec: media.CodecAAC},
			Video:  plan.CopyVideo(),
			Audio:  plan.CopyAudio(),
		}
	}

	for _, tt := range []struct {
		name  string
		setup func(t *testing.T, s *session, ws workspace) (feed, transcode.EncodeOptions)
	}{{
		name: "reading a buffer nothing will grow",
		setup: func(t *testing.T, s *session, ws workspace) (feed, transcode.EncodeOptions) {
			sp, err := deliver.NewSpool(filepath.Join(ws.dir, "spool.ts"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sp.CloseWrite(nil) })
			if _, err := sp.Write(head); err != nil {
				t.Fatal(err)
			}
			opts := copying(transcode.FromPipe(transcode.SpoolFormat, read.Pace{}))
			// A buffer copied whole into its own container is served with no encoder at all.
			opts.Audio = plan.EncodeAudio(plan.AudioEncode{Codec: media.CodecAAC})
			return feed{buffered: &buffered{reader: &pull{spool: sp, done: make(chan struct{})}}}, opts
		},
	}, {
		name: "reading an origin that went quiet",
		setup: func(t *testing.T, s *session, _ workspace) (feed, transcode.EncodeOptions) {
			return feed{}, copying(transcode.FromSource(programSourceWithin(t, quietOrigin(t, head), media.MPEGTS, time.Hour)))
		},
	}} {
		t.Run(tt.name, func(t *testing.T) {
			s, ws, dev := servingSession(t, ffmpegPath), loopback(t), probingRenderer{}
			f, opts := tt.setup(t, s, ws)
			d, err := s.produce(dev, ws, f, opts, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := awaitArtifact(t.Context(), d); err != nil {
				t.Fatal(err)
			}
			if err := hand(t.Context(), dev, d.sink.URL(), opts.Format.ContentType, false); err != nil {
				t.Fatal(err)
			}

			const teardown = 30 * time.Second
			done := make(chan error, 1)
			go func() { done <- s.releases.release() }()
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

// servingSession is the session a delivery is opened in, released when the test ends.
func servingSession(t *testing.T, ffmpegPath string) *session {
	t.Helper()
	s := &session{
		cfg:      Config{FFmpegPath: ffmpegPath, Encoders: transcode.Encoders(ffmpegPath), Timelines: direct{}},
		ctx:      t.Context(),
		releases: &releases{},
	}
	t.Cleanup(func() { _ = s.releases.release() })
	return s
}

// openFixture opens one real delivery of a real encode over a real origin, in workDir.
func openFixture(t *testing.T, contentType, workDir string) delivery {
	t.Helper()
	ffmpegPath, _ := requireFFmpegTools(t)
	origin := serveFixture(t, ffmpegPath)
	s := servingSession(t, ffmpegPath)
	format, ok := container.FormatForContentType(contentType)
	if !ok {
		t.Fatalf("the format registry cannot produce %s", contentType)
	}
	opts := transcode.EncodeOptions{
		Format: format,
		Input:  transcode.FromSource(programSourceWithin(t, origin.stream().URL, media.MP4, 30*time.Second)),
		Probe:  media.ProbeInfo{VideoCodec: media.CodecH264},
		Video:  plan.CopyVideo(),
		Audio:  plan.EncodeAudio(plan.AudioEncode{Codec: media.CodecAAC}),
	}
	d, err := s.produce(probingRenderer{}, workspace{localIP: "127.0.0.1", dir: workDir}, feed{}, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	return d
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

func programSourceWithin(t *testing.T, sourceURL *url.URL, contentType string, rwTimeout time.Duration) transcode.ProgramSource {
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
	source, err := transcode.NewProgramSource(program, map[media.InputID]read.Policy{
		media.PrimaryInputID: read.For(media.Fetch{}, rwTimeout),
	}, ffmpeg.Binary{})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

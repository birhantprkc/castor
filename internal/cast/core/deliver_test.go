package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

// TestSubtitlesFollowTheTranscriberAndNothingElse pins the whole of the subtitle axis
// where it is now decided. The renderer clause a general rule would add (there must be a
// local encode to draw cues into) is structural instead: only the composition chosen for a
// renderer that never fetches for itself builds a burn-in, so a cast with no decoded frames
// cannot reach this question at all.
func TestSubtitlesFollowTheTranscriberAndNothingElse(t *testing.T) {
	cfg := Config{}
	if got := SubtitleForServed(cfg); got != SubtitleOff {
		t.Errorf("with the transcriber off: got %v, want SubtitleOff", got)
	}
	cfg.Whisper.Enable = true
	if got := SubtitleForServed(cfg); got != SubtitleBurnIn {
		t.Errorf("with the transcriber on: got %v, want SubtitleBurnIn", got)
	}
}

// TestEveryEncodeIsAskedToReportAndIsAlwaysRead covers the delivery driver's half of
// the encoder's telemetry, against a real ffmpeg.
//
// Both halves matter and they fail differently. A consumer that is never called leaves
// a cast with no measurement of its own encode (and, on the spool path, with no
// subtitles). A feed that is opened and NOT read is worse: ffmpeg writes -progress with
// a blocking write, so the encode runs for about two minutes and then stops dead with
// no exit status, no stderr line and nothing to attribute it to. Since EncodeArgs emits
// -progress unconditionally, the no-consumer case is the common one and it is the one
// that must not wedge.
func TestEveryEncodeIsAskedToReportAndIsAlwaysRead(t *testing.T) {
	ffmpegPath := requireFFmpeg(t)
	origin := serveFixture(t, ffmpegPath)

	policy, err := read.For(read.Shape{}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	format, ok := media.FormatForContentType(media.MP4)
	if !ok {
		t.Fatal("the format registry cannot produce mp4")
	}
	params := OpenParams{
		FFmpegPath: ffmpegPath,
		Opts: ffmpeg.EncodeOptions{
			Format: format,
			Source: ffmpeg.NetworkSource{URL: origin, ContentType: media.MP4, Read: policy},
			Probe:  media.ProbeInfo{VideoCodec: media.CodecH264},
			Video:  ffmpeg.CopyVideo(),
			Audio:  ffmpeg.EncodeAudio(ffmpeg.AudioEncode{Codec: media.CodecAAC}),
		},
	}

	t.Run("the samples reach the consumer", func(t *testing.T) {
		var (
			mu      sync.Mutex
			samples []media.Progress
		)
		p := params
		p.OnProgress = func(s media.Progress) {
			mu.Lock()
			defer mu.Unlock()
			samples = append(samples, s)
		}

		proc, joinProgress, err := startEncoder(t.Context(), p)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := io.Copy(io.Discard, proc.Stdout); err != nil || n == 0 {
			t.Fatalf("the encode produced %d bytes: %v\n%q", n, err, proc.StderrTail())
		}
		if err := proc.Wait(); err != nil {
			t.Fatalf("the encode failed: %v\n%q", err, proc.StderrTail())
		}
		// After this the last sample has been delivered, which is what lets the spool
		// path's consumer write into a work directory its caller removes next.
		joinProgress()

		if len(samples) == 0 {
			t.Fatal("the encode reported nothing about itself")
		}
		if last := samples[len(samples)-1]; last.Position <= 0 || last.Bytes <= 0 {
			t.Errorf("final sample = %+v; a completed encode reports a position and a size", last)
		}
	})

	t.Run("the join outlives the last sample", func(t *testing.T) {
		// The consumer is deliberately slow, so the last sample is still being handled
		// when the process has already been reaped. Every teardown joins after reaping
		// for exactly this reason: the spool path's consumer writes a cue file into a
		// work directory its caller removes the moment Serve returns, so "the encoder
		// has exited" is not on its own a statement that nobody is still writing.
		const handling = 200 * time.Millisecond
		var delivered atomic.Int64
		p := params
		p.OnProgress = func(media.Progress) {
			time.Sleep(handling)
			delivered.Add(1)
		}

		proc, joinProgress, err := startEncoder(t.Context(), p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, proc.Stdout); err != nil {
			t.Fatal(err)
		}
		if err := proc.Wait(); err != nil {
			t.Fatalf("the encode failed: %v\n%q", err, proc.StderrTail())
		}
		joinProgress()

		settled := delivered.Load()
		if settled == 0 {
			t.Fatal("the join returned before a single sample was handled")
		}
		time.Sleep(2 * handling)
		if got := delivered.Load(); got != settled {
			t.Errorf("%d sample(s) landed after the join returned", got-settled)
		}
	})

	t.Run("an encode nobody is measuring still finishes", func(t *testing.T) {
		proc, joinProgress, err := startEncoder(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := io.Copy(io.Discard, proc.Stdout); err != nil || n == 0 {
			t.Fatalf("the encode produced %d bytes: %v\n%q", n, err, proc.StderrTail())
		}
		if err := proc.Wait(); err != nil {
			t.Fatalf("the encode failed with no progress consumer: %v\n%q", err, proc.StderrTail())
		}
		joinProgress()
	})
}

// serveFixture generates a one second H.264/AAC mp4 and serves it, so the encode under
// test opens a network source on the same terms production does.
func serveFixture(t *testing.T, ffmpegPath string) *url.URL {
	t.Helper()
	path := filepath.Join(t.TempDir(), "video.mp4")
	gen := exec.CommandContext(t.Context(), ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=64x64:rate=10:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ac", "2", "-shortest",
		"-movflags", "+faststart", path,
	)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generating fixture: %v\n%s", err, out)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, path)
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL + "/video.mp4")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestDeliverLetsTheSupervisorOutrankACleanDelivery pins the race the in-flight window
// depends on. A sink's Wait is not a statement that the cast worked: it ends when nothing
// is left to serve, and "nothing is left to serve" is also what a renderer that never came
// for the bytes looks like from here. Whichever party answers first is the cast's answer.
func TestDeliverLetsTheSupervisorOutrankACleanDelivery(t *testing.T) {
	verdict := errors.New("the renderer accepted the stream URL and never requested it")

	t.Run("a verdict reached while the delivery is still running is the result", func(t *testing.T) {
		sess := &session{sink: blockingSink{}}
		err := deliver(t.Context(), sess, func(context.Context, Delivery) error { return verdict })
		if !errors.Is(err, verdict) {
			t.Fatalf("deliver = %v, want the supervisor's verdict", err)
		}
	})

	t.Run("a delivery that ran its course is not overruled", func(t *testing.T) {
		sess := &session{sink: doneSink{}}
		// The supervisor never returns on its own: only the cancellation deliver owns ends
		// it, which is what must not be mistaken for a verdict.
		err := deliver(t.Context(), sess, func(ctx context.Context, _ Delivery) error {
			<-ctx.Done()
			return ctx.Err()
		})
		if err != nil {
			t.Fatalf("deliver = %v, want nil for a delivery that completed", err)
		}
	})

	t.Run("a leg with no supervisor gets the sink's answer", func(t *testing.T) {
		sess := &session{sink: doneSink{}}
		if err := deliver(t.Context(), sess, nil); err != nil {
			t.Fatalf("deliver = %v, want nil", err)
		}
	})
}

// TestOnlyADeliveryThatRanItsCourseIsAskedWhetherTheRendererTookIt pins when the
// completeness statement is made, which is as load-bearing as the arithmetic behind it.
//
// It is asked of a Wait that ended CLEANLY, whether or not this leg supervises: the legs that
// do not supervise are the ones with nobody watching the renderer at all, so they are exactly
// where a cast nobody fetched used to be reported as delivered. It is never asked of a Wait
// that ended with an error, which is what keeps a user who presses Ctrl-C after ten minutes of
// a two hour film from being told their cast failed: the sink returns the context's error and
// the arithmetic that would convict is never run at all.
func TestOnlyADeliveryThatRanItsCourseIsAskedWhetherTheRendererTookIt(t *testing.T) {
	short := errors.New("the renderer was handed a fraction of what this cast produced")
	settled := func(sink Sink) *session {
		return &session{sink: sink, settled: func(time.Duration) error { return short }}
	}

	t.Run("a delivery that ran its course with no supervisor", func(t *testing.T) {
		if err := deliver(t.Context(), settled(doneSink{}), nil); !errors.Is(err, short) {
			t.Fatalf("deliver = %v, want the delivery's own account of what the renderer took", err)
		}
	})

	t.Run("a delivery that ran its course under a supervisor", func(t *testing.T) {
		err := deliver(t.Context(), settled(doneSink{}), func(ctx context.Context, _ Delivery) error {
			<-ctx.Done()
			return ctx.Err()
		})
		if !errors.Is(err, short) {
			t.Fatalf("deliver = %v, want the delivery's own account of what the renderer took", err)
		}
	})

	t.Run("a cast the user stopped is not asked", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := deliver(ctx, settled(blockingSink{}), nil)
		if errors.Is(err, short) {
			t.Fatal("a cancelled cast was told it failed to deliver: the user stopped it, and a cast the user stopped failed at nothing")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("deliver = %v, want the cancellation", err)
		}
	})

	t.Run("a mechanism that cannot say says nothing", func(t *testing.T) {
		if err := deliver(t.Context(), &session{sink: doneSink{}}, nil); err != nil {
			t.Fatalf("deliver = %v, want nil: a delivery with no completeness statement is reported as its sink left it", err)
		}
	})
}

// blockingSink is a delivery that never finishes, so only the supervisor can answer.
type blockingSink struct{}

func (blockingSink) URL() *url.URL { return &url.URL{} }
func (blockingSink) Wait(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// doneSink is a delivery that has already run its course.
type doneSink struct{}

func (doneSink) URL() *url.URL              { return &url.URL{} }
func (doneSink) Wait(context.Context) error { return nil }

// TestServeReportsTheSupervisorsVerdict is the wiring the in-flight window hangs off. The
// sink's Wait ends when nothing is left to serve, which is also what a renderer that never
// came for the bytes looks like from here, so a verdict reached while the delivery was
// running has to survive all the way out of Serve.
//
// It is also where the delivery hands over both of its facts, and the second one is what
// keeps a viewer who pauses from being convicted for it: a renderer that has stopped
// fetching is judged against how much media is still there for it to come back to, so a
// supervisor handed only the fetching has no way to tell a pause from a renderer that went
// away. A stream delivery can answer honestly, because it never takes back a byte it
// produced.
func TestServeReportsTheSupervisorsVerdict(t *testing.T) {
	ffmpegPath := requireFFmpeg(t)
	origin := serveFixture(t, ffmpegPath)

	policy, err := read.For(read.Shape{}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	format, ok := media.FormatForContentType(media.MP4)
	if !ok {
		t.Fatal("the format registry cannot produce mp4")
	}

	verdict := errors.New("the renderer accepted the stream URL and never requested it")
	err = Serve(t.Context(), stubRenderer{}, OpenParams{
		FFmpegPath: ffmpegPath,
		LocalIP:    "127.0.0.1",
		WorkDir:    t.TempDir(),
		Opts: ffmpeg.EncodeOptions{
			Format: format,
			Source: ffmpeg.NetworkSource{URL: origin, ContentType: media.MP4, Read: policy},
			Probe:  media.ProbeInfo{VideoCodec: media.CodecH264},
			Video:  ffmpeg.CopyVideo(),
			Audio:  ffmpeg.EncodeAudio(ffmpeg.AudioEncode{Codec: media.CodecAAC}),
		},
		Supervise: func(ctx context.Context, d Delivery) error {
			// The delivery has to hand over its own consumer: without it the supervisor can
			// see the read but not whether anybody is fetching what it produced, which is
			// exactly the half of the observed failure that reported success.
			if d.Consumer == nil {
				return errors.New("the delivery supervised nothing: no consumer was handed over")
			}
			if requests, last := d.Consumer.Fetched(); requests != 0 || !last.IsZero() {
				return fmt.Errorf("a renderer that never fetched reports %d requests at %v", requests, last)
			}
			if d.Delivered == nil {
				return errors.New("the delivery reported no fetchable media, so a renderer that stopped fetching cannot be told from one that paused")
			}
			// Polled rather than read once: the encoder states its position on its own report
			// cadence, and the supervisor's first tick can land between Play and the first
			// block. This fixture is one second of media, so any position at all is the whole
			// of it.
			if err := until(ctx, func() bool { return d.Delivered() > 0 }); err != nil {
				return fmt.Errorf("the delivery reported %s of fetchable media for an encode that ran to completion: %w", d.Delivered(), err)
			}
			return verdict
		},
	})
	if !errors.Is(err, verdict) {
		t.Fatalf("Serve = %v, want the supervisor's verdict to reach the caller", err)
	}
}

// until polls cond until it holds or ctx ends, which is how a test reads a figure the
// encoder publishes on its own cadence rather than on the test's.
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

// TestTheEncodersProgressIsFollowedWithoutTakingItsFeed covers the figures a stream delivery
// answers "how much has this cast made fetchable, and what did it cost in bytes" with, and
// both halves of it.
//
// The sample has to be followed whole: the position is what a paused renderer's remaining
// buffer is measured against, and it is only comparable with the bytes of the SAME sample,
// because that pair is the one byte-to-media mapping nobody had to invent. The caller's own
// consumer has to keep receiving every sample, because the feed has exactly one reader and the
// spool path places its subtitle cues off it: a follower that swallowed the samples would ship
// a cast that plays with no subtitles and no error anywhere.
func TestTheEncodersProgressIsFollowedWithoutTakingItsFeed(t *testing.T) {
	var seen []media.Progress
	follow, made := produced(func(s media.Progress) { seen = append(seen, s) })

	if got := made(); got.Position != 0 || got.Bytes != 0 {
		t.Errorf("an encode that has reported nothing states %+v", got)
	}
	for _, sample := range []media.Progress{
		{Position: 2 * time.Second, Bytes: 1 << 18},
		{Position: 90 * time.Second, Bytes: 1 << 20},
		{Position: 41 * time.Minute, Bytes: 1 << 30},
	} {
		follow(sample)
		if got := made(); got != sample {
			t.Errorf("made = %+v, want the sample the encoder last stated (%+v)", got, sample)
		}
	}
	if len(seen) != 3 {
		t.Errorf("the caller's consumer saw %d of 3 samples; the cue writer's samples arrive on this feed and nowhere else", len(seen))
	}

	// A delivery nobody is measuring still has to be followable: OnProgress is nil on every
	// cast that burns no subtitles, and the progress is read regardless.
	follow, made = produced(nil)
	follow(media.Progress{Position: time.Minute, Bytes: 4096})
	if got := made(); got.Position != time.Minute || got.Bytes != 4096 {
		t.Errorf("with no consumer of its own, made = %+v, want 1m0s over 4096 bytes", got)
	}
}

// TestACastNobodyTookTheStreamFromIsNotDelivered is the last account of the "exited 0 having
// cast nothing" family, and the one nothing else can give. A renderer that fetched once and
// went away mid-title leaves every other party reporting success: the encoder fills the spool
// to the end of the title whatever the renderer does, the sink severs the blocked write at its
// deadline, the client count reaches zero, the idle grace expires and Wait returns nil.
//
// The measurement is arithmetic over the whole cast rather than a state anybody was caught in.
// Playback runs at one second of media per second of wall clock, and a renderer cannot play
// media it was never handed, so a viewer watching from Play to the end consumed exactly the
// cast's own duration and anything the renderer was short of that reached nobody.
//
// The tolerance is the wall clock a cast legitimately spends with the renderer taking nothing:
// one reconnect ceiling before its first byte, which is what the unfetched verdict already
// allows it, and the sink's lingering after its last.
func TestACastNobodyTookTheStreamFromIsNotDelivered(t *testing.T) {
	// One film, as the encoder stated it: two hours of media in four gigabytes.
	film := media.Progress{Position: 2 * time.Hour, Bytes: 4 << 30}
	// The sink's lingering, as replay derives it, and the whole tolerance the arithmetic allows.
	const linger = watch.StallWindow + 30*time.Second
	const tolerance = read.BackoffMax + linger

	for _, tt := range []struct {
		name    string
		playing time.Duration
		sent    int64
		convict bool
	}{{
		// The observed shape: a renderer that took a couple of minutes of a two hour film and
		// went away, while the cast ran on for the hour its own read needed to finish.
		name:    "a renderer that fetched once and went away mid-title",
		playing: time.Hour,
		sent:    film.Bytes / 60,
		convict: true,
	}, {
		// The same with nothing fetched at all, which is what the unfetched verdict names on the
		// one leg that supervises and what nothing named on the legs that do not.
		name:    "a renderer that never came for the bytes",
		playing: time.Hour,
		sent:    0,
		convict: true,
	}, {
		// An hour of the film handed to nobody, on a cast that ran the whole two hours.
		name:    "a renderer that stopped taking the stream at half time",
		playing: 2 * time.Hour,
		sent:    film.Bytes / 2,
		convict: true,
	}, {
		// A viewer who watched the film. The renderer reads ahead of playback, so it has taken
		// the whole stream well before the cast ends.
		name:    "a renderer that read the stream to EOF",
		playing: 2 * time.Hour,
		sent:    film.Bytes,
	}, {
		// A renderer that read at playback rate and stopped one chunk short of the end: noticing
		// that costs the sink its whole lingering, so convicting inside it would name every cast
		// whose renderer closed the socket a moment early.
		name:    "a renderer that stopped just short of the end",
		playing: 2*time.Hour + linger,
		sent:    film.Bytes - 32<<10,
	}, {
		// The tolerance at its limit, from both ends at once: a renderer slow to come for the
		// bytes and a delivery lingering after its last one.
		name:    "a renderer that took everything, late and unhurried",
		playing: 2*time.Hour + tolerance,
		sent:    film.Bytes,
	}, {
		// Half the film taken while the cast had only run half an hour: the renderer is ahead of
		// the viewer, and the cast is not over.
		name:    "a renderer reading ahead of the viewer",
		playing: 30 * time.Minute,
		sent:    film.Bytes / 2,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			err := undelivered(tt.playing, tt.sent, film, linger)
			var short *Undelivered
			if got := errors.As(err, &short); got != tt.convict {
				t.Fatalf("undelivered(%s, %d bytes of %d) = %v, want convicted = %v", tt.playing, tt.sent, film.Bytes, err, tt.convict)
			}
			if tt.convict && short.Produced != film.Position {
				t.Errorf("the fault reports %s produced, want the %s the encoder stated: the numbers are the whole of what a user can act on", short.Produced, film.Position)
			}
		})
	}

	// A delivery that produced nothing is not the renderer's doing: the artifact gate and the
	// encoder's exit status own that failure, and answering here as well would blame the
	// renderer for a stream that never existed.
	if err := undelivered(time.Hour, 0, media.Progress{}, linger); err != nil {
		t.Errorf("a delivery that produced nothing blamed the renderer: %v", err)
	}
}

// TestOnlyTheDeliveryThatKeepsWhatItProducedStatesItsCompleteness is the wiring of the same
// property through the real openers, which is what makes the statement reachable rather than
// merely written: every non-segmented cast castor makes is opened by openStream, and each of
// them now ends by saying whether the renderer took the stream.
//
// The segmented mechanism deliberately says nothing, and that is the same measurement its
// missing buffer is: its muxer deletes behind a window, so a client that fetched every segment
// it was ever offered has still taken a fraction of the bytes the encoder wrote, and comparing
// the two would convict every HLS cast castor makes.
func TestOnlyTheDeliveryThatKeepsWhatItProducedStatesItsCompleteness(t *testing.T) {
	ffmpegPath := requireFFmpeg(t)
	origin := serveFixture(t, ffmpegPath)
	policy, err := read.For(read.Shape{}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name        string
		contentType string
		open        opener
		states      bool
	}{{
		name:        "a stream delivery replays every byte it produced",
		contentType: media.MPEGTS,
		open:        openStream,
		states:      true,
	}, {
		name:        "a rolling window has deleted most of what it produced",
		contentType: media.HLS,
		open:        openSegmented,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			format, ok := media.FormatForContentType(tt.contentType)
			if !ok {
				t.Fatalf("the format registry cannot produce %s", tt.contentType)
			}
			sess, err := tt.open(t.Context(), OpenParams{
				FFmpegPath: ffmpegPath,
				LocalIP:    "127.0.0.1",
				WorkDir:    t.TempDir(),
				Opts: ffmpeg.EncodeOptions{
					Format: format,
					Source: ffmpeg.NetworkSource{URL: origin, ContentType: media.MP4, Read: policy},
					Probe:  media.ProbeInfo{VideoCodec: media.CodecH264},
					Video:  ffmpeg.CopyVideo(),
					Audio:  ffmpeg.EncodeAudio(ffmpeg.AudioEncode{Codec: media.CodecAAC}),
				},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sess.teardown() })

			if got := sess.settled != nil; got != tt.states {
				t.Errorf("the delivery states its completeness = %v, want %v", got, tt.states)
			}
			if got := sess.delivered != nil; got != tt.states {
				t.Errorf("the delivery reports fetchable media = %v, want %v: what it keeps decides both answers", got, tt.states)
			}
		})
	}
}

// TestARollingWindowKeepsNothingASupervisorCouldHoldACastOpenOver is why the segmented
// delivery reports no fetchable buffer at all, and it is the property rather than a
// preference. That muxer deletes behind its window, so the most a renderer can ever have in
// hand there is the window itself, while both in-flight rules require silence for longer
// than the stall bound before they act. A renderer that has taken nothing for that long has
// lost the segment it would resume from whatever the encoder has since written, so there is
// nothing for a buffer to hold open.
//
// Grow the window past the bound and that stops being true: media a renderer could still
// fetch would start being ignored, and the fix is to report the window here rather than to
// leave the rules judging on silence alone.
func TestARollingWindowKeepsNothingASupervisorCouldHoldACastOpenOver(t *testing.T) {
	if ffmpeg.HLSWindow >= watch.StallWindow {
		t.Errorf("a segmented delivery keeps %s of media while a renderer's silence is acted on after %s: what it still has in hand now outlasts the verdict, so it has to be reported instead of assumed gone",
			ffmpeg.HLSWindow, watch.StallWindow)
	}
}

// stubRenderer accepts whatever it is pointed at and never fetches it, which is the exact
// renderer behaviour the supervisor exists to name.
type stubRenderer struct{}

func (stubRenderer) Play(context.Context, *url.URL, string) error { return nil }
func (stubRenderer) StreamHeaders(string) map[string]string       { return nil }

// TestTheArtifactGateReadsWhatWasWrittenAndNotWhetherAFileExists pins the fact the
// segmented delivery's readiness is judged on. A renderer handed a zero-byte m3u8 gets a
// 200 it cannot parse, and it does not come back for a second look.
func TestTheArtifactGateReadsWhatWasWrittenAndNotWhetherAFileExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream.m3u8")
	landed := written(path)

	if got := landed(); got != 0 {
		t.Errorf("a playlist that does not exist reports %d bytes", got)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := landed(); got != 0 {
		t.Errorf("an empty playlist reports %d bytes, so a renderer would be pointed at a document with nothing in it", got)
	}
	if err := os.WriteFile(path, []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := landed(); got != 8 {
		t.Errorf("a written playlist reports %d bytes, want 8", got)
	}
}

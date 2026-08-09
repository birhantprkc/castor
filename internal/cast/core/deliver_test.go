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
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/deliver/replay"
	"github.com/stupside/castor/internal/cast/deliver/spool"
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

		proc, joinProgress, err := startEncoder(t.Context(), p, t.TempDir())
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

		proc, joinProgress, err := startEncoder(t.Context(), p, t.TempDir())
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
		proc, joinProgress, err := startEncoder(t.Context(), params, t.TempDir())
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
// depends on. A mechanism's Wait is not a statement that the cast worked: it ends when nothing
// is left to serve, and "nothing is left to serve" is also what a renderer that never came
// for the bytes looks like from here. Whichever party answers first is the cast's answer.
func TestDeliverLetsTheSupervisorOutrankACleanDelivery(t *testing.T) {
	verdict := errors.New("the renderer accepted the stream URL and never requested it")

	t.Run("a verdict reached while the delivery is still running is the result", func(t *testing.T) {
		err := took(blocking).deliver(t.Context(), func(context.Context, Delivery) error { return verdict })
		if !errors.Is(err, verdict) {
			t.Fatalf("deliver = %v, want the supervisor's verdict", err)
		}
	})

	t.Run("a delivery that ran its course is not overruled", func(t *testing.T) {
		// The supervisor never returns on its own: only the cancellation deliver owns ends
		// it, which is what must not be mistaken for a verdict.
		err := took(over).deliver(t.Context(), func(ctx context.Context, _ Delivery) error {
			<-ctx.Done()
			return ctx.Err()
		})
		if err != nil {
			t.Fatalf("deliver = %v, want nil for a delivery that completed", err)
		}
	})

	t.Run("a mechanism nothing can judge in flight gets its own answer", func(t *testing.T) {
		unsupervisable := took(over)
		unsupervisable.mech.(*fakeMechanism).inFlight = nil
		if err := unsupervisable.deliver(t.Context(), nil); err != nil {
			t.Fatalf("deliver = %v, want nil", err)
		}
	})
}

// TestALegThatSuppliesNoSupervisorIsNotAnUnwatchedCast is the omission that left a whole window
// wired to one of three compositions. The remux leg passed no supervisor, so every cast of a
// header-gated source to a renderer that fetches for itself ran with no stall rule, no in-flight
// deliverability rule and no unfetched rule at all: a renderer could accept the URL, fetch
// nothing, and be reported as a delivered cast at the end of the title.
//
// Supervision is now the MECHANISM's property. A leg that opened a read of its own supplies the
// supervisor that watches it; a leg whose encode is the read supplies none, and this is what
// then happens instead of nothing.
func TestALegThatSuppliesNoSupervisorIsNotAnUnwatchedCast(t *testing.T) {
	// A renderer that took the URL and has been silent since before its grace window opened,
	// which is the one thing this window can say about a renderer, over a delivery that still
	// holds twenty minutes of media for it (so nothing else can be what ends the cast).
	sess := took(blocking)
	sess.mech.(*fakeMechanism).inFlight = inFlight(
		stoppedRenderer{},
		func() time.Duration { return 20 * time.Minute },
	)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := sess.deliver(ctx, nil)

	var fault *watch.Fault
	if !errors.As(err, &fault) {
		t.Fatalf("deliver = %v, want the in-flight verdict of a cast whose leg supplied no supervisor of its own", err)
	}
	if fault.Kind != watch.Unfetched {
		t.Errorf("verdict %s, want %s: %s", fault.Kind, watch.Unfetched, fault.Health)
	}
	if fault.Window != watch.Playing {
		t.Errorf("window %s, want the in-flight one", fault.Window)
	}
	// And it is judged as castor's own work rather than as a link: this encode may be decoding,
	// scaling under the height ceiling and re-encoding, so a pace here convicts the origin for
	// something castor chose to do.
	if fault.Health.Headroom != 0 {
		t.Errorf("the encode was judged against %gx of headroom; a deliverability verdict over castor's own encoder blames the source for it", fault.Health.Headroom)
	}
}

// stoppedRenderer is a renderer that accepted the URL and was handed no byte of the program, with
// its silence stated rather than waited out.
type stoppedRenderer struct{}

func (stoppedRenderer) Handed() (int64, time.Time) {
	return 0, time.Now().Add(-watch.StallWindow - time.Second)
}

// took is a delivery whose mechanism states that the renderer took what was made for it, and
// which can be judged in flight. Every mechanism states the first one way or the other: Settled
// is a method and not an optional field, because the nil is how a cast nobody fetched was
// reported as delivered.
func took(wait func(ctx context.Context) error) *session {
	return &session{mech: &fakeMechanism{
		wait:     wait,
		inFlight: inFlight(stoppedRenderer{}, func() time.Duration { return 0 }),
	}}
}

// blocking is a delivery that never finishes, so only a supervisor can answer.
func blocking(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// over is a delivery that has already run its course.
func over(context.Context) error { return nil }

// fakeMechanism is a delivery whose answers a test states outright: when its Wait ends, whether
// it can be judged in flight, and what it says about the renderer afterwards.
type fakeMechanism struct {
	wait     func(ctx context.Context) error
	inFlight *Delivery
	settled  error
	// asked counts the completeness statements this mechanism was asked for, because when the
	// question is put is as load-bearing as what it answers: a cast the user stopped must not be
	// asked at all.
	asked atomic.Int64
}

func (m *fakeMechanism) URL() *url.URL                  { return &url.URL{} }
func (m *fakeMechanism) Wait(ctx context.Context) error { return m.wait(ctx) }
func (m *fakeMechanism) Drained() <-chan struct{}       { return nil }
func (m *fakeMechanism) InFlight() *Delivery            { return m.inFlight }
func (m *fakeMechanism) Settled() error                 { m.asked.Add(1); return m.settled }
func (m *fakeMechanism) Close() error                   { return nil }

func (m *fakeMechanism) Artifact() Artifact {
	return Artifact{Subject: "a stated delivery", Landed: func() int64 { return 8 << 20 }}
}

// TestOnlyADeliveryThatRanItsCourseIsAskedWhetherTheRendererTookIt pins when the
// completeness statement is made, which is as load-bearing as the arithmetic behind it.
//
// It is asked of a Wait that ended CLEANLY, whether or not this leg supervises: the legs that
// do not supervise are the ones with nobody watching the renderer at all, so they are exactly
// where a cast nobody fetched used to be reported as delivered. It is never asked of a Wait
// that ended with an error, which is what keeps a user who presses Ctrl-C after ten minutes of
// a two hour film from being told their cast failed: the mechanism returns the context's error and
// the arithmetic that would convict is never run at all (see settle).
func TestOnlyADeliveryThatRanItsCourseIsAskedWhetherTheRendererTookIt(t *testing.T) {
	short := errors.New("the renderer was handed a fraction of what this cast produced")
	settled := func(wait func(context.Context) error) *session {
		sess := took(wait)
		sess.mech.(*fakeMechanism).settled = short
		return sess
	}

	t.Run("a delivery that ran its course with nothing judging it in flight", func(t *testing.T) {
		sess := settled(over)
		sess.mech.(*fakeMechanism).inFlight = nil
		if err := sess.deliver(t.Context(), nil); !errors.Is(err, short) {
			t.Fatalf("deliver = %v, want the delivery's own account of what the renderer took", err)
		}
	})

	t.Run("a delivery that ran its course under a supervisor", func(t *testing.T) {
		err := settled(over).deliver(t.Context(), func(ctx context.Context, _ Delivery) error {
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
		sess := settled(blocking)
		sess.mech.(*fakeMechanism).inFlight = nil
		err := sess.deliver(ctx, nil)
		if errors.Is(err, short) {
			t.Fatal("a cancelled cast was told it failed to deliver: the user stopped it, and a cast the user stopped failed at nothing")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("deliver = %v, want the cancellation", err)
		}
		// And it is not asked, rather than asked and disbelieved. While the two questions were one
		// cmp.Or, the arithmetic ran on every cancelled cast and only its answer was thrown away,
		// which is a statement the docs of both completeness rules make and the code did not keep.
		if asked := sess.mech.(*fakeMechanism).asked.Load(); asked != 0 {
			t.Errorf("a cancelled cast was asked %d time(s) what its renderer took: the answer is discarded today and counted, logged or clocked tomorrow", asked)
		}
	})

	t.Run("a delivery whose renderer took it is reported as its mechanism left it", func(t *testing.T) {
		err := took(over).deliver(t.Context(), func(ctx context.Context, _ Delivery) error {
			<-ctx.Done()
			return ctx.Err()
		})
		if err != nil {
			t.Fatalf("deliver = %v, want nil", err)
		}
	})

	// The row that used to sit here drove a session with no completeness statement at all and
	// asserted that it reported success, which is precisely what the segmented mechanism did on
	// every cast it ever served. Nothing may be silent here now: Settled is a method every
	// mechanism has, which is asserted of the real ones rather than of a hand-built session (see
	// TestEveryDeliveryStatesWhetherTheRendererTookIt).
}

// TestServeReportsTheSupervisorsVerdict is the wiring the in-flight window hangs off. A
// mechanism's Wait ends when nothing is left to serve, which is also what a renderer that never
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
	err = Serve(t.Context(), probingRenderer{}, OpenParams{
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
	}, func(ctx context.Context, d Delivery) error {
		// The delivery has to hand over its own consumer: without it the supervisor can
		// see the read but not whether anybody is fetching what it produced, which is
		// exactly the half of the observed failure that reported success.
		if d.Consumer == nil {
			return errors.New("the delivery supervised nothing: no consumer was handed over")
		}
		// Zero bytes, over a renderer that DID come to the door: probingRenderer's Play
		// HEADs the URL exactly as a real one does before deciding to fetch it. A mechanism
		// that answered this question by counting requests reports that probe as a fetch, and
		// the one verdict about a renderer then cannot fire on the run it exists for (a URL
		// accepted, a request in the log, bytes_sent=0).
		if handed, last := d.Consumer.Handed(); handed != 0 || !last.IsZero() {
			return fmt.Errorf("a renderer that only probed the URL is credited with %d bytes at %v", handed, last)
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
// to the end of the title whatever the renderer does, the delivery severs the blocked write at its
// deadline, the client count reaches zero, the idle grace expires and Wait returns nil.
//
// The measurement is two counts of one kind against each other, the bytes handed over against
// the bytes produced, and nothing else. Every row below is therefore a statement about a share
// of a program: how long the cast ran, how long the renderer held the URL and how fast the
// encoder went are not inputs, and TestTheCompletenessStatementCannotReadAClock is why they
// cannot become inputs again.
//
// The share is derived, from the claim the fault makes and from what a shortfall can be
// attributed to; the rows either side of it are the boundary that derivation names (see
// handedAtLeast).
func TestACastNobodyTookTheStreamFromIsNotDelivered(t *testing.T) {
	// One film, as the encoder stated it: two hours of media in four gigabytes.
	film := media.Progress{Position: 2 * time.Hour, Bytes: 4 << 30}

	for _, tt := range []struct {
		name    string
		sent    int64
		convict bool
	}{{
		// The observed shape: a renderer that took a couple of minutes of a two hour film and
		// went away while castor served the rest of it to nobody.
		name:    "a renderer that fetched once and went away mid-title",
		sent:    film.Bytes / 60,
		convict: true,
	}, {
		// The same with nothing fetched at all, which is what the unfetched verdict names on the
		// one leg that supervises and what nothing named on the legs that do not.
		name:    "a renderer that never came for the bytes",
		sent:    0,
		convict: true,
	}, {
		// Forty minutes of the film handed over and eighty to nobody: past the boundary in the
		// direction where "most of the program reached nobody" is the only reading left.
		name:    "a renderer that stopped taking the stream a third of the way in",
		sent:    film.Bytes / 3,
		convict: true,
	}, {
		// The boundary itself, and the reason it sits here rather than higher: a renderer handed
		// half a film and gone is the same evidence as a viewer who watched half a film and
		// switched the television off, and the fault's own sentence (most of the program reached
		// nobody) is not true of it either.
		name: "a renderer handed exactly half the program",
		sent: film.Bytes / 2,
	}, {
		// A viewer who watched the film. The renderer reads ahead of playback, so it has taken
		// the whole stream well before the cast ends.
		name: "a renderer that read the stream to EOF",
		sent: film.Bytes,
	}, {
		// The whole of the noise this comparison carries: a renderer that consumed the program
		// can be short by the final write chunk it never had to take (replay's sendChunkSize),
		// which is five orders of magnitude inside the share above, so none of that share is
		// paying for a measurement problem.
		name: "a renderer that stopped one write chunk short of the end",
		sent: film.Bytes - 32<<10,
	}, {
		// The encoder's last progress block is a moment behind the spool the handed bytes are
		// counted off, so a renderer that read to EOF can be credited with more bytes than the
		// encoder had claimed. Over-counting may only ever excuse a delivery.
		name: "a renderer credited with more bytes than the encoder had claimed",
		sent: film.Bytes + 1<<20,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			err := undelivered(tt.sent, film)
			var short *Undelivered
			if got := errors.As(err, &short); got != tt.convict {
				t.Fatalf("undelivered(%d bytes of %d) = %v, want convicted = %v", tt.sent, film.Bytes, err, tt.convict)
			}
			if !tt.convict {
				return
			}
			if short.Produced != film.Position {
				t.Errorf("the fault reports %s produced, want the %s the encoder stated: the numbers are the whole of what a user can act on", short.Produced, film.Position)
			}
			// The sentence the fault prints has to be true of the numbers it prints. This is what
			// keeps the share and the wording one decision rather than two that can drift apart,
			// and it is the check the wall-clock version failed: it convicted a cast with the words
			// "handed 2h0m0s of the 2h0m0s this cast produced ... so most of the program reached
			// nobody".
			if short.Handed*2 >= short.Produced {
				t.Errorf("the fault claims most of a %s program reached nobody while naming %s of it as handed over: the arithmetic and the sentence disagree", short.Produced, short.Handed)
			}
			if missed := (short.Produced - short.Handed).Round(time.Second).String(); !strings.Contains(short.Error(), missed) {
				t.Errorf("the fault does not name the %s of program that reached nobody, which is the figure a user acts on: %q", missed, short.Error())
			}
		})
	}

	// A delivery that produced nothing is not the renderer's doing: the artifact gate and the
	// encoder's exit status own that failure, and answering here as well would blame the
	// renderer for a stream that never existed.
	if err := undelivered(0, media.Progress{}); err != nil {
		t.Errorf("a delivery that produced nothing blamed the renderer: %v", err)
	}
}

// TestTheCompletenessStatementCannotReadAClock is the misattribution this measurement made
// while a wall clock was one of its terms, in both the shapes that proved it, plus the reason
// neither can come back.
//
// The term was `playing - handed`: the media a viewer would have consumed had they watched from
// Play to the end, less the media the renderer was handed. It is not arithmetic about a viewer,
// because wall clock is not a stand-in for media anybody consumed. A pause lands entirely in
// the elapsed side while the handed side does not move, and pauses accumulate; castor's own
// encoding pace does the same from the other end, and how long an encode takes is a property of
// the host rather than of the renderer. Both rows below were convicted while the renderer had
// taken every byte the cast produced.
//
// So the fix is not a wider allowance, and the rows assert that: each states the wall clock it
// spent beyond the program, and each is past the longest stretch castor lets any peer stay quiet
// for. Nothing derived from anything could have covered them, because the term was not too small,
// it was measuring the wrong thing.
//
// That bound is READ from the delivery rather than recomputed here, and the coupling is the point.
// This test's predecessor worked its own version of it out (a stall window plus a margin) and
// landed one idle grace away from the number production holds, so the boundary it called the
// limit was thirty seconds from the real one and a change to the delivery's deadline left this
// package green. Every duration below is now stated against replay.DefaultWriteDeadline, which
// is the figure that really decides whether a pause severs a connection.
func TestTheCompletenessStatementCannotReadAClock(t *testing.T) {
	film := media.Progress{Position: 2 * time.Hour, Bytes: 4 << 30}

	for _, tt := range []struct {
		name string
		// pause is one silence this cast's viewer produced and pauses how many of them there
		// were; beyond is wall clock it spent for reasons that are not a viewer's at all. The
		// three together are what used to convict it and are now not inputs at all.
		pause  time.Duration
		pauses int
		beyond time.Duration
		sent   int64
	}{{
		// A pause shorter than the delivery's write deadline never severs the connection, so the
		// renderer keeps its position and keeps reading; three of them across a film is a viewer
		// answering the door. A hundred seconds is a human being and not a threshold, which is why
		// it is written down rather than derived: what has to hold of it is checked below, against
		// the delivery's own bound, and it is the whole shape of the failure (pauses accumulate,
		// and it was their sum the term convicted).
		name:   "a film watched to the last byte through three pauses the delivery never severs",
		pause:  100 * time.Second,
		pauses: 3,
		sent:   film.Bytes,
	}, {
		// Castor's own doing, and nothing in flight convicts it there either, because no pace is
		// ever offered over castor's own encode: a burn-in pinned just above realtime that averages
		// 0.9x, or a remux binding the height ceiling on a software-only host, makes two hours of
		// media in two hours and thirteen minutes.
		name:   "an encode of castor's own that ran thirteen minutes longer than the film",
		beyond: 13 * time.Minute,
		sent:   film.Bytes,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			// A row about a viewer's pauses is only about them while each one really is a pause:
			// past the delivery's write deadline the socket is severed, and the renderer that comes
			// back is replayed from byte 0 rather than reading on, so the film was not watched to
			// its last byte and the row would be proving something else.
			if tt.pause >= replay.DefaultWriteDeadline {
				t.Fatalf("this row pauses for %s at a time, at or past the %s the delivery holds a quiet renderer to: the connection is severed there, so this is not a film anybody watched to its last byte",
					tt.pause, replay.DefaultWriteDeadline)
			}
			spent := tt.beyond + time.Duration(tt.pauses)*tt.pause
			if spent <= replay.DefaultWriteDeadline {
				t.Fatalf("this cast spends %s of wall clock beyond its program, inside the %s castor lets a peer stay quiet for: it is not the shape that convicted a healthy cast, so it proves nothing about the term that did",
					spent, replay.DefaultWriteDeadline)
			}
			if err := undelivered(tt.sent, film); err != nil {
				t.Fatalf("a cast whose renderer took all %d bytes was convicted: %v", tt.sent, err)
			}
		})
	}

	// And the clock is not reachable, which is what keeps this from being a tolerance somebody
	// widens back into the same failure. The statement takes two counts, and the field a
	// delivery makes it through takes nothing at all, so there is no parameter for a duration to
	// arrive by.
	clock := reflect.TypeOf(time.Duration(0))
	statement := reflect.TypeOf(undelivered)
	for i := range statement.NumIn() {
		// A variadic or pointed-to duration is the same clock arriving by a longer route, which
		// is exactly the shape a reader would reach for to keep the old term "available".
		in := statement.In(i)
		if in.Kind() == reflect.Slice || in.Kind() == reflect.Pointer {
			in = in.Elem()
		}
		if in == clock {
			t.Errorf("the completeness statement takes a %s: wall clock is not media anybody consumed, and a cast watched to its last byte through a pause is what a clock in this arithmetic convicts", statement.In(i))
		}
	}
	settled, _ := reflect.TypeOf((*mechanism)(nil)).Elem().MethodByName("Settled")
	if settled.Type.NumIn() != 0 {
		t.Errorf("a mechanism states its completeness through %s: it may rest on nothing but the counts the mechanism holds itself", settled.Type)
	}
}

// TestADeletingWindowStatesOnlyWhetherAnythingGotThrough is the completeness statement of the
// mechanism that cannot state a share, in the four shapes it has to tell apart.
//
// There is no threshold to derive here and that is the point of the measurement: zero is not a
// small share, it is the absence of one. No deletion excuses it, no pause produces it (a
// renderer that paused had fetched first, or it had nothing to pause), and it is exact, so
// there is nothing for a later reader to widen when a cast is convicted they think should not
// have been.
func TestADeletingWindowStatesOnlyWhetherAnythingGotThrough(t *testing.T) {
	program := media.Progress{Position: 90 * time.Minute, Bytes: 2 << 30}

	for _, tt := range []struct {
		name    string
		served  int
		made    media.Progress
		convict bool
	}{{
		// The failure the whole layer claims to own, surviving where nothing watched: this
		// mechanism can be judged by nobody in flight, so a renderer that accepted the playlist
		// URL and never asked for a segment ran the encoder to the end of the title and was
		// reported as delivered.
		name:    "a renderer that never came for the program",
		served:  0,
		made:    program,
		convict: true,
	}, {
		// The same cast as the row above as far as this mechanism can see, and the reason it is
		// keyed on Position: this muxer's output is a directory it deletes out of, so total_size
		// describes whatever file ffmpeg has open and can be nothing at all. A statement keyed on
		// the bytes produced would be dead on the exact leg it exists for.
		name:    "a renderer that never came, for a muxer that states no byte count",
		served:  0,
		made:    media.Progress{Position: program.Position},
		convict: true,
	}, {
		// One fragment is the whole of what this mechanism can ask for. It cannot say whether a
		// renderer that took some of the program took enough of it, because the program it
		// produced is mostly deleted, so past zero it says nothing rather than inventing a share.
		name:   "a renderer that took a fragment and stopped",
		served: 1,
		made:   program,
	}, {
		// Not the renderer's doing: the artifact gate and the encoder's exit status own a delivery
		// with nothing in it, and answering here as well would blame a renderer for a program that
		// never existed.
		name:   "a delivery that produced nothing",
		served: 0,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			err := unfetched(tt.served, tt.made)
			var short *Undelivered
			if got := errors.As(err, &short); got != tt.convict {
				t.Fatalf("unfetched(%d artifacts served, %+v) = %v, want convicted = %v", tt.served, tt.made, err, tt.convict)
			}
			if !tt.convict {
				return
			}
			if short.Handed != 0 {
				t.Errorf("the fault reports %s handed over on a delivery that handed over nothing", short.Handed)
			}
			if short.Produced != tt.made.Position {
				t.Errorf("the fault reports %s produced, want the %s the encoder stated", short.Produced, tt.made.Position)
			}
			// The sentence has to be true of the numbers beside it. A renderer that never came did
			// not stop taking a stream, and the previous wording said it did.
			if msg := short.Error(); !strings.Contains(msg, "never came") || !strings.Contains(msg, tt.made.Position.Round(time.Second).String()) {
				t.Errorf("the fault does not say that none of the %s produced reached the renderer: %q", tt.made.Position, msg)
			}
		})
	}
}

// TestASegmentedCastNobodyFetchedIsNotDelivered drives that statement through the real
// mechanism, because the arithmetic above proves nothing about the cast that shipped: this is
// the delivery nothing can judge in flight, so if its own count never reaches the statement, a
// Roku cast that never fetches a segment still exits 0.
//
// Everything here is production's: a real encoder writing a real rolling directory, the real
// artifact gate deciding when the playlist may be handed over, the real HTTP server answering
// real requests, and the delivery's own statement read back afterwards. Nothing constructs a
// count or a progress sample.
func TestASegmentedCastNobodyFetchedIsNotDelivered(t *testing.T) {
	workDir := t.TempDir()
	sess := openFixture(t, media.HLS, workDir)

	// The gate a renderer is really handed the playlist behind, so this cast reaches the state
	// production reaches: a URL that answers, pointed at by nobody.
	if err := sess.ready(t.Context()); err != nil {
		t.Fatalf("the playlist never appeared: %v", err)
	}

	// Polled because the encoder states its position on its own report cadence: the statement
	// cannot convict before the delivery has produced anything, and it must convict once it has.
	waiting, giveUp := context.WithTimeout(t.Context(), 30*time.Second)
	defer giveUp()
	var short *Undelivered
	if err := until(waiting, func() bool { return errors.As(sess.mech.Settled(), &short) }); err != nil {
		t.Fatalf("a segmented cast nobody fetched reports %v: it produced a whole program for a renderer that never asked for a byte of it", sess.mech.Settled())
	}
	if short.Produced <= 0 || short.Handed != 0 {
		t.Errorf("the fault reports %s of %s handed over, want none of a program the encoder stated", short.Handed, short.Produced)
	}

	// A renderer that polls the playlist and takes no segment is the same cast, and it is the
	// shape a family served the wrong transfer-mode header produces: every request 200s and none
	// of them is media.
	fetchFrom(t, sess.mech.URL())
	if err := sess.mech.Settled(); !errors.As(err, &short) {
		t.Errorf("a renderer that only polled the playlist was reported as delivered: %v", err)
	}

	// And one artifact of the program is the whole of what this mechanism can ask for: past zero
	// it has no share to judge, so it says nothing.
	artifacts := filepath.Join(workDir, "delivery")
	segment := sess.mech.URL()
	segment.Path = "/" + firstSegment(t, artifacts)
	fetchFrom(t, segment)
	if err := sess.mech.Settled(); err != nil {
		t.Errorf("a renderer that fetched %s was convicted: %v; this mechanism cannot state a share, so past zero it has nothing to say", segment.Path, err)
	}
}

// TestADeliveryPublishesItsOwnArtifactsAndNothingElseOfTheCasts is the file server's blast
// radius. It is mounted on a directory, and the directory it used to be given was the cast's
// whole work directory: the buffer a read-once leg fills and the live cue file a burn-in draws
// from sit in there, so a renderer (or anything else on the network) could fetch either. Worse
// for the cast itself, this mechanism counts anything it hands over that is not the playlist as
// program delivered, so a GET of the buffer would have acquitted a renderer of the only verdict
// this delivery can reach.
func TestADeliveryPublishesItsOwnArtifactsAndNothingElseOfTheCasts(t *testing.T) {
	workDir := t.TempDir()
	// A private file of the cast's own, written where the legs write theirs, before the delivery
	// is opened.
	private := filepath.Join(workDir, "spool.ts")
	if err := os.WriteFile(private, []byte("the cast's own buffer"), 0o600); err != nil {
		t.Fatal(err)
	}

	sess := openFixture(t, media.HLS, workDir)
	if err := sess.ready(t.Context()); err != nil {
		t.Fatalf("the playlist never appeared: %v", err)
	}

	leak := sess.mech.URL()
	leak.Path = "/spool.ts"
	if got := statusOf(t, leak); got == http.StatusOK {
		t.Errorf("GET %s = 200: the delivery publishes the cast's private files, and fetching one of them also reports the program as handed over", leak.Path)
	}
	// And the artifacts it does publish are still published, so this is isolation and not a
	// broken server.
	fetchFrom(t, sess.mech.URL())
	if got := sess.mech.Artifact().Landed(); got <= 0 {
		t.Errorf("the artifact gate reads %d bytes of playlist, so the muxer and the server disagree about where the artifacts are", got)
	}
}

// openFixture opens one real delivery of a real encode over a real origin, and stops it with the
// case. It is the production entry point (the mechanism table, the encoder, the one teardown),
// so nothing below it is a fixture except the media.
func openFixture(t *testing.T, contentType, workDir string) *session {
	t.Helper()
	ffmpegPath := requireFFmpeg(t)
	origin := serveFixture(t, ffmpegPath)
	policy, err := read.For(read.Shape{}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	format, ok := media.FormatForContentType(contentType)
	if !ok {
		t.Fatalf("the format registry cannot produce %s", contentType)
	}

	sess, err := open(t.Context(), OpenParams{
		FFmpegPath: ffmpegPath,
		LocalIP:    "127.0.0.1",
		WorkDir:    workDir,
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
	t.Cleanup(func() { _ = sess.stop() })
	return sess
}

// firstSegment is any artifact of the program the muxer has written into dir, which is
// whatever is in there other than the playlist itself.
func firstSegment(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() && e.Name() != media.HLSPlaylistName {
			return e.Name()
		}
	}
	t.Fatalf("the muxer wrote a playlist and no segment into %s: %v", dir, entries)
	return ""
}

// fetchFrom makes one request the way a renderer does, and reads what came back: a fetch
// nobody read is a fetch that handed nothing over.
func fetchFrom(t *testing.T, u *url.URL) {
	t.Helper()
	if got := statusOf(t, u); got != http.StatusOK {
		t.Fatalf("GET %s = %d, want the artifact a renderer would have been handed", u, got)
	}
}

// statusOf makes one request and reads the body to its end, reporting the status it was answered
// with.
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
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

// TestEveryDeliveryStatesWhatItCanBeJudgedOn is the wiring of the same property through the real
// mechanisms, which is what makes the statement reachable rather than merely written: every cast
// castor makes is served by one of these two.
//
// What each can state differs, and the difference is exactly what it KEEPS. A delivery that never
// takes back a byte can weigh what got through against what was made and can report how much
// media a paused renderer still has in hand, so it can be judged while it runs. One whose muxer
// deletes behind its window can do neither: a client that fetched every segment it was ever
// offered has still taken a fraction of the bytes the encoder wrote.
//
// The pair a supervisor reads is asserted as a PAIR, which is the state a leg used to be able to
// hand over half of: a supervisor given the fetching without the buffer ends a film at two and a
// half minutes of somebody standing up.
//
// What they cannot differ about is answering for their renderer, and it is asserted by driving
// the real one: a mechanism whose statement is written and never reached is the same silence as
// no statement at all, which is how a cast nobody fetched exited 0. The row that used to be here
// asserted the segmented mechanism says NOTHING about what the renderer took; that is now
// unwritable, because every mechanism has a Settled method and both of them are made to use it
// here.
func TestEveryDeliveryStatesWhatItCanBeJudgedOn(t *testing.T) {
	// Keyed on the delivery kind and walked from the dispatch table itself, so a third mechanism
	// cannot be added without a row here stating what it can answer: a mechanism nobody made
	// state anything is how this failure shipped.
	expected := map[media.DeliveryKind]struct {
		name        string
		contentType string
		// keeps reports whether this mechanism still holds what it produced, which is what
		// decides whether it can be judged in flight at all.
		keeps bool
	}{
		media.DeliverStream:    {name: "a stream delivery replays every byte it produced", contentType: media.MPEGTS, keeps: true},
		media.DeliverSegmented: {name: "a rolling window has deleted most of what it produced", contentType: media.HLS},
	}

	for kind := range mechanisms {
		tt, known := expected[kind]
		if !known {
			t.Fatalf("the %v delivery mechanism is not covered here, so nothing says what it can be judged on", kind)
		}
		t.Run(tt.name, func(t *testing.T) {
			sess := openFixture(t, tt.contentType, t.TempDir())

			d := sess.mech.InFlight()
			if got := d != nil; got != tt.keeps {
				t.Fatalf("the delivery can be judged in flight = %v, want %v: only a mechanism that keeps what it produced can be", got, tt.keeps)
			}
			if d != nil && (d.Consumer == nil || d.Delivered == nil) {
				t.Errorf("the delivery hands a supervisor half of what it is judged on (consumer %v, buffer %v): either fact alone convicts a viewer who paused",
					d.Consumer != nil, d.Delivered != nil)
			}
			// And whatever it keeps, it convicts the renderer that took nothing, from its own
			// counts rather than from arithmetic a test performed for it. Nobody is pointed at
			// this cast, so once the encoder has stated a program both mechanisms are looking at
			// the same run: a URL that answers, and no byte of it fetched.
			//
			// Polled, because the statement rests on the encoder's own progress and the first of
			// those samples arrives on ffmpeg's cadence, not on the mechanism being opened.
			waiting, giveUp := context.WithTimeout(t.Context(), 30*time.Second)
			defer giveUp()
			var short *Undelivered
			if err := until(waiting, func() bool { return errors.As(sess.mech.Settled(), &short) }); err != nil {
				t.Fatalf("a delivery nobody fetched reports %v: it produced a program for a renderer that never asked for a byte of it, which is the cast that used to exit 0", sess.mech.Settled())
			}
			if short.Handed != 0 || short.Produced <= 0 {
				t.Errorf("the fault reports %s of %s handed over, want none of a program the encoder stated", short.Handed, short.Produced)
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

// TestACastAbandonedInFlightTearsDownInsteadOfWaitingForever is the deadlock, as a case that
// fails in bounded time rather than hanging the suite.
//
// THE SHAPE, which is every in-flight verdict there is: the source was accepted and went quiet,
// so the buffer the encoder tails will never grow again, and a supervisor names the cast. The old
// teardown then closed the server and waited for a process it had never killed. Killing it would
// not have helped either: cmd.Stdin is an io.Reader, so os/exec's Wait also waits for the
// goroutine copying it, and that goroutine was parked in a read of the buffer nothing was going to
// grow. The one thing that would have freed it, the cast's own context, is cancelled by a defer
// that cannot run until this call returns. Measured on a real run: the verdict landed 154 seconds
// in and the cast stayed inside its teardown for another 5 minutes 27, until the CALLER's context
// expired. In production that context ends only on Ctrl+C, so the cast printed its fault and hung
// indefinitely.
//
// Everything here is production's: the real mechanism table, the real encoder over a real spool
// tail, the real teardown. Only the verdict is stated, because reaching one honestly takes two
// reconnect ceilings of wall clock.
func TestACastAbandonedInFlightTearsDownInsteadOfWaitingForever(t *testing.T) {
	ffmpegPath := requireFFmpeg(t)
	workDir := t.TempDir()
	format, ok := media.FormatForContentType(media.MPEGTS)
	if !ok {
		t.Fatal("the format registry cannot produce mpegts")
	}

	// A buffer holding the head of a real program, and a producer that never writes another byte.
	sp, err := spool.New(filepath.Join(workDir, "spool.ts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sp.CloseWrite(nil) })
	if _, err := sp.Write(programHead(t, ffmpegPath)); err != nil {
		t.Fatal(err)
	}
	tail, err := sp.Tail(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	verdict := errors.New("the source stopped feeding this cast")
	done := make(chan error, 1)
	go func() {
		done <- Serve(t.Context(), probingRenderer{}, OpenParams{
			FFmpegPath: ffmpegPath,
			LocalIP:    "127.0.0.1",
			WorkDir:    workDir,
			Input:      tail,
			Opts: ffmpeg.EncodeOptions{
				Format:     format,
				PipeFormat: ffmpeg.SpoolFormat,
				Probe:      media.ProbeInfo{VideoCodec: media.CodecH264, AudioCodec: media.CodecAAC},
				Video:      ffmpeg.CopyVideo(),
				Audio:      ffmpeg.CopyAudio(),
			},
		}, func(context.Context, Delivery) error { return verdict })
	}()

	// The budget is the teardown's, not the cast's: the verdict above is reached on the
	// supervisor's first tick, so everything after it is stopping an encoder, a server and a
	// progress feed. None of that waits on anything but the process it just killed.
	const teardown = 30 * time.Second
	select {
	case err := <-done:
		if !errors.Is(err, verdict) {
			t.Fatalf("Serve = %v, want the verdict that ended the cast", err)
		}
	case <-time.After(teardown):
		t.Fatalf("the cast reached its verdict and did not return within %s: its encoder is parked reading a buffer nothing will grow, and the teardown is waiting for it", teardown)
	}
}

// TestACastWhoseEncoderIsTheReadStopsThatEncoder is the same teardown over the other half of the
// deadlock, and it is the step the case above cannot reach: the KILL.
//
// On the composition where the encode IS the read there is no feed for the driver to close, so
// closing one frees nothing. The encoder is inside a socket that was accepted and then went
// quiet, which no amount of tidying up around it ends, and everything the teardown does next
// waits on it: the mechanism joins its own reading of the output, and the reap is the output
// pipe closing. Nothing but killing the process makes any of that return.
//
// It is the leg a Roku or Chromecast cast of a header-gated source takes, and it is now
// supervised (see watchTheEncode), so this is the state those casts reach the first time an
// origin goes quiet on them: a verdict, and then a teardown that has to come back on its own.
//
// The read's own mid-read deadline is set far past the budget deliberately. A pass may not come
// from ffmpeg giving up on the origin, because production withholds that deadline entirely from a
// source whose fragments must arrive whole (see read's segment-fragile row), and on those casts
// castor's own stop is the only thing there is.
func TestACastWhoseEncoderIsTheReadStopsThatEncoder(t *testing.T) {
	ffmpegPath := requireFFmpeg(t)
	format, ok := media.FormatForContentType(media.MPEGTS)
	if !ok {
		t.Fatal("the format registry cannot produce mpegts")
	}
	policy, err := read.For(read.Shape{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	verdict := errors.New("the source stopped feeding this cast")
	done := make(chan error, 1)
	go func() {
		done <- Serve(t.Context(), probingRenderer{}, OpenParams{
			FFmpegPath: ffmpegPath,
			LocalIP:    "127.0.0.1",
			WorkDir:    t.TempDir(),
			Opts: ffmpeg.EncodeOptions{
				Format: format,
				Source: ffmpeg.NetworkSource{
					URL:         quietOrigin(t, programHead(t, ffmpegPath)),
					ContentType: media.MPEGTS,
					Read:        policy,
				},
				Probe: media.ProbeInfo{VideoCodec: media.CodecH264, AudioCodec: media.CodecAAC},
				Video: ffmpeg.CopyVideo(),
				Audio: ffmpeg.CopyAudio(),
			},
		}, func(context.Context, Delivery) error { return verdict })
	}()

	const teardown = 30 * time.Second
	select {
	case err := <-done:
		if !errors.Is(err, verdict) {
			t.Fatalf("Serve = %v, want the verdict that ended the cast", err)
		}
	case <-time.After(teardown):
		t.Fatalf("the cast reached its verdict and did not return within %s: nothing stopped an encoder that is inside a read of an origin which has gone quiet, and the whole teardown is behind it", teardown)
	}
}

// quietOrigin hands over the head of a program and then goes quiet without ever ending the
// response, which is the origin behind every in-flight verdict: the socket was accepted, so
// nothing downstream sees a failure, and no byte follows.
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

// programHead is the first part of a real MPEG-TS program, which is what an upstream that was
// accepted and then went quiet leaves in the buffer: enough for the encoder to open the stream and
// produce output, and then nothing.
func programHead(t *testing.T, ffmpegPath string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "program.ts")
	gen := exec.CommandContext(t.Context(), ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=64x64:rate=10:duration=12",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=12",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ac", "2", "-shortest",
		"-f", "mpegts", path,
	)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generating fixture: %v\n%s", err, out)
	}
	program, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Cut mid-program, because a buffer that holds a whole program lets the encoder finish and
	// exit, and an encoder that has exited is not the state this case is about. Well past the
	// demuxer's analysis window, so the encoder really does write output before it runs out: a
	// prefix shorter than that window leaves it probing, and the artifact gate then waits out its
	// whole patience for a first byte before the cast even reaches the state under test.
	return program[:len(program)*2/3]
}

// probingRenderer accepts the URL, probes it the way a real firmware does, and then never takes
// a byte of the program. It is the observed run rather than a simplification of it: the failure
// arrives with a request already in the delivery's log, which is why what the renderer TOOK is the
// only fact that separates it from a renderer that is watching.
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

func (probingRenderer) StreamHeaders(string) map[string]string { return nil }

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

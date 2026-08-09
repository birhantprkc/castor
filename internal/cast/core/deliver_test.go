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
		err := deliver(t.Context(), took(blockingSink{}), func(context.Context, Delivery) error { return verdict })
		if !errors.Is(err, verdict) {
			t.Fatalf("deliver = %v, want the supervisor's verdict", err)
		}
	})

	t.Run("a delivery that ran its course is not overruled", func(t *testing.T) {
		// The supervisor never returns on its own: only the cancellation deliver owns ends
		// it, which is what must not be mistaken for a verdict.
		err := deliver(t.Context(), took(doneSink{}), func(ctx context.Context, _ Delivery) error {
			<-ctx.Done()
			return ctx.Err()
		})
		if err != nil {
			t.Fatalf("deliver = %v, want nil for a delivery that completed", err)
		}
	})

	t.Run("a leg with no supervisor gets the sink's answer", func(t *testing.T) {
		if err := deliver(t.Context(), took(doneSink{}), nil); err != nil {
			t.Fatalf("deliver = %v, want nil", err)
		}
	})
}

// took is a delivery whose mechanism states that the renderer took what was made for it, which
// every mechanism states one way or the other: there is no nil meaning "cannot say", because
// that nil is how a cast nobody fetched was reported as delivered.
func took(sink Sink) *session {
	return &session{sink: sink, settled: func() error { return nil }}
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
		return &session{sink: sink, settled: func() error { return short }}
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

	t.Run("a delivery whose renderer took it is reported as its sink left it", func(t *testing.T) {
		if err := deliver(t.Context(), took(doneSink{}), nil); err != nil {
			t.Fatalf("deliver = %v, want nil", err)
		}
	})

	// The row that used to sit here drove a session with no completeness statement at all and
	// asserted that it reported success, which is precisely what the segmented mechanism did on
	// every cast it ever served. Nothing may be silent here now, and that is asserted of the real
	// openers rather than of a hand-built session (see
	// TestEveryDeliveryStatesWhetherTheRendererTookIt).
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
		Supervise: func(ctx context.Context, d Delivery) error {
			// The delivery has to hand over its own consumer: without it the supervisor can
			// see the read but not whether anybody is fetching what it produced, which is
			// exactly the half of the observed failure that reported success.
			if d.Consumer == nil {
				return errors.New("the delivery supervised nothing: no consumer was handed over")
			}
			// Zero bytes, over a renderer that DID come to the door: probingRenderer's Play
			// HEADs the URL exactly as a real one does before deciding to fetch it. A sink that
			// answered this question by counting requests reports that probe as a fetch, and the
			// one verdict about a renderer then cannot fire on the run it exists for (a URL
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
// That bound is READ from the sink rather than recomputed here, and the coupling is the point.
// This test's predecessor worked its own version of it out (a stall window plus a margin) and
// landed one idle grace away from the number production holds, so the boundary it called the
// limit was thirty seconds from the real one and a change to the sink's deadline left this
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
		// Castor's own doing, on the leg with no supervisor to have caught it in flight: a burn-in
		// pinned just above realtime that averages 0.9x, or a remux binding the height ceiling on
		// a software-only host, makes two hours of media in two hours and thirteen minutes.
		name:   "an encode of castor's own that ran thirteen minutes longer than the film",
		beyond: 13 * time.Minute,
		sent:   film.Bytes,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			// A row about a viewer's pauses is only about them while each one really is a pause:
			// past the sink's write deadline the socket is severed, and the renderer that comes
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
	if made, _ := reflect.TypeOf(session{}).FieldByName("settled"); made.Type.NumIn() != 0 {
		t.Errorf("a delivery states its completeness through %s: it may rest on nothing but the counts the mechanism holds itself", made.Type)
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
		// The failure the whole layer claims to own, surviving where nothing watched: the leg
		// that opens this mechanism hands over no supervisor, so a renderer that accepted the
		// playlist URL and never asked for a segment ran the encoder to the end of the title and
		// was reported as delivered.
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
// mechanism, because the arithmetic above proves nothing about the cast that shipped: this
// delivery is opened by the one composition that hands over no supervisor, so if the sink's own
// count never reaches the statement, a Roku cast that never fetches a segment still exits 0.
//
// Everything here is production's: a real encoder writing a real rolling directory, the real
// artifact gate deciding when the playlist may be handed over, the real HTTP server answering
// real requests, and the delivery's own statement read back afterwards. Nothing constructs a
// count or a progress sample.
func TestASegmentedCastNobodyFetchedIsNotDelivered(t *testing.T) {
	ffmpegPath := requireFFmpeg(t)
	origin := serveFixture(t, ffmpegPath)
	policy, err := read.For(read.Shape{}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	format, ok := media.FormatForContentType(media.HLS)
	if !ok {
		t.Fatal("the format registry cannot produce HLS")
	}

	workDir := t.TempDir()
	sess, err := openSegmented(t.Context(), OpenParams{
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
	t.Cleanup(func() { _ = sess.teardown() })

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
	if err := until(waiting, func() bool { return errors.As(sess.settled(), &short) }); err != nil {
		t.Fatalf("a segmented cast nobody fetched reports %v: it produced a whole program for a renderer that never asked for a byte of it", sess.settled())
	}
	if short.Produced <= 0 || short.Handed != 0 {
		t.Errorf("the fault reports %s of %s handed over, want none of a program the encoder stated", short.Handed, short.Produced)
	}

	// A renderer that polls the playlist and takes no segment is the same cast, and it is the
	// shape a family served the wrong transfer-mode header produces: every request 200s and none
	// of them is media.
	fetchFrom(t, sess.sink.URL())
	if err := sess.settled(); !errors.As(err, &short) {
		t.Errorf("a renderer that only polled the playlist was reported as delivered: %v", err)
	}

	// And one artifact of the program is the whole of what this mechanism can ask for: past zero
	// it has no share to judge, so it says nothing.
	segment := sess.sink.URL()
	segment.Path = "/" + firstSegment(t, workDir)
	fetchFrom(t, segment)
	if err := sess.settled(); err != nil {
		t.Errorf("a renderer that fetched %s was convicted: %v; this mechanism cannot state a share, so past zero it has nothing to say", segment.Path, err)
	}
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
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %s, want the artifact a renderer would have been handed", u, resp.Status)
	}
}

// TestEveryDeliveryStatesWhetherTheRendererTookIt is the wiring of the same property through
// the real openers, which is what makes the statement reachable rather than merely written:
// every cast castor makes is opened by one of these two, and each of them ends by saying
// something about what the renderer took.
//
// What they can state differs, and that difference is exactly what each of them KEEPS. A
// delivery that never takes back a byte can weigh what got through against what was made, so it
// states a share and can also report how much media a paused renderer still has in hand. One
// whose muxer deletes behind its window can do neither: a client that fetched every segment it
// was ever offered has still taken a fraction of the bytes the encoder wrote, so it states only
// whether anything at all got through (see unfetched) and reports no buffer at all.
//
// The row that used to be here asserted the segmented mechanism says NOTHING, which is how a
// Roku cast nobody fetched exited 0: silence about the share was read as silence about
// everything, on the one leg that also hands over no supervisor.
func TestEveryDeliveryStatesWhetherTheRendererTookIt(t *testing.T) {
	ffmpegPath := requireFFmpeg(t)
	origin := serveFixture(t, ffmpegPath)
	policy, err := read.For(read.Shape{}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// Keyed on the delivery kind and walked from the dispatch table itself, so a third mechanism
	// cannot be added without a row here stating what it can answer: a mechanism nobody made
	// state anything is how this failure shipped.
	mechanisms := map[media.DeliveryKind]struct {
		name        string
		contentType string
		// keeps reports whether this mechanism still holds what it produced, which is what
		// decides both of the answers below: a share of the program, and how much of it a
		// renderer still has to come back for.
		keeps bool
	}{
		media.DeliverStream:    {name: "a stream delivery replays every byte it produced", contentType: media.MPEGTS, keeps: true},
		media.DeliverSegmented: {name: "a rolling window has deleted most of what it produced", contentType: media.HLS},
	}

	for kind, open := range deliveries {
		tt, known := mechanisms[kind]
		if !known {
			t.Fatalf("the %v delivery mechanism is not covered here, so nothing says whether it can state what its renderer took", kind)
		}
		t.Run(tt.name, func(t *testing.T) {
			format, ok := media.FormatForContentType(tt.contentType)
			if !ok {
				t.Fatalf("the format registry cannot produce %s", tt.contentType)
			}
			sess, err := open(t.Context(), OpenParams{
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

			if sess.settled == nil {
				t.Error("the delivery states nothing about what the renderer took, so a cast nobody fetched is reported exactly as its sink's Wait left it")
			}
			if got := sess.delivered != nil; got != tt.keeps {
				t.Errorf("the delivery reports fetchable media = %v, want %v: only a mechanism that keeps what it produced can say", got, tt.keeps)
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

// probingRenderer accepts the URL, probes it the way a real firmware does, and then never takes
// a byte of the program. It is the observed run rather than a simplification of it: the failure
// arrives with a request already in the sink's log, which is why what the renderer TOOK is the
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

package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stupside/castor/internal/cast/deliver/hlsserve"
	"github.com/stupside/castor/internal/cast/deliver/replay"
	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

// This file is the served-cast delivery driver. It is device-blind and carries no
// per-delivery code path in its control flow: Serve looks the delivery mechanism
// up from the format's DeliveryKind (data) and drives it uniformly. Each
// mechanism is one opener behind the deliveries table, so adding a delivery (or a
// caption sidecar, which is a second Sink at Serve's single Play step) is new data
// plus a small impl, not another Serve* function and not a content-type branch.

// Sink is a running local server fronting a produced stream for one cast: it
// exposes the URL the renderer fetches and blocks until the stream is fully
// delivered. Closing it is the opener's job (its teardown holds the concrete
// server), so the driver only ever needs these two. Both replay.Server and
// hlsserve.Server satisfy it unchanged.
type Sink interface {
	URL() *url.URL
	Wait(ctx context.Context) error
}

// Renderer is the two things a served cast needs from the connected device:
// where to point it, and what a response fronting that stream must say. It is
// declared here, at the consumer, rather than taken as the four-method
// device.Device: Serve neither discovers nor closes a renderer, and a parameter
// naming the two methods it drives is what says so. device.Device satisfies it
// as it stands.
type Renderer interface {
	Play(ctx context.Context, streamURL *url.URL, contentType string) error
	StreamHeaders(contentType string) map[string]string
}

// OpenParams are the device-blind inputs to open a delivery: the encoder to run
// and how its input/output is wired, and where to serve from. Headers are filled
// by Serve from the device, not here.
//
// There is no separate Format field. The format that selects the mechanism is
// Opts.Format, the same record the encode is built from: carrying it twice meant
// two copies of one value at both call sites with nothing enforcing agreement,
// and a divergence hangs the cast (Opts.Format.Delivery DeliverStream with a
// DeliverSegmented duplicate writes "-f mp4 pipe:1" while openSegmented waits
// forever for a playlist).
type OpenParams struct {
	FFmpegPath string
	Opts       ffmpeg.EncodeOptions
	StartOpts  []ffmpeg.StartOption
	LocalIP    string
	WorkDir    string
	// OnProgress, if set, is called with every sample the encoder reports about
	// itself: its output position, the bytes it has produced, and the speed it is
	// producing them at. The spool path places subtitle cues from it; nothing here
	// reads it, which is why it is a caller's callback rather than a field.
	//
	// It runs on the goroutine that drains the feed, so it must inspect the sample and
	// return. The feed is drained whether or not this is set, because ffmpeg writes it
	// with a blocking write: an unread progress pipe stops the encode dead once the
	// kernel buffer fills, with no exit status and no stderr line to say why.
	OnProgress func(media.Progress)

	// OnPlaying, if set, is called once the renderer has accepted the URL and before the
	// delivery is waited on. It is how a caller finds out that the line no recovery crosses has
	// been crossed, and the reason it is reported at all: a leg that answered "reading" for
	// everything past this point had a reader that died mid-title at exit 183 classified as a
	// broken copy, and the cast was run again from byte zero with a viewer watching.
	//
	// It runs on Serve's own goroutine, between Play returning and the delivery being waited
	// on, so a caller that reads what it recorded after Serve returns needs no
	// synchronisation of its own.
	OnPlaying func()

	// Supervise, if set, judges the cast for as long as the renderer is playing it, over
	// the delivery's own facts. It is a callback because the facts a health rule needs
	// live on both sides of this layer: what the renderer fetches and what is still
	// fetchable are the delivery's to report, while the read behind it belongs to the leg
	// that started it, and Serve is not the party that gets to hold them both.
	//
	// It returns when a verdict ends the cast, and its error becomes the cast's error
	// joined with whatever the encoder had to say. A leg that leaves it nil gets today's
	// behaviour: the delivery runs its course and nobody asks whether anyone was watching.
	Supervise func(ctx context.Context, d Delivery) error
}

// Delivery is one opened delivery as its supervisor reads it: who has come for the bytes,
// and how much media is still there to be come for.
//
// The two travel together because either one alone convicts a viewer who PAUSES. A paused
// renderer stops requesting segments, or stops draining the socket, and no sink can tell
// that from a renderer that went away, so silence alone ended a film at two and a half
// minutes of somebody standing up. What separates the two is whether castor still has
// anything for it to come back to.
type Delivery struct {
	// Consumer is the renderer's fetching as the sink fronting this delivery tracks it.
	Consumer watch.Consumer

	// Delivered is how much media this delivery can still hand over. It is nil where the
	// delivery cannot honestly say, which is never "nothing" by accident: a rule that reads
	// it must treat the absence as an unmeasured buffer and judge on the rest (see
	// openSegmented, whose muxer deletes behind its own window).
	Delivered func() time.Duration
}

// session is one opened delivery: the running server, an optional readiness gate
// (nil when the URL is usable immediately), and the teardown that stops the
// encoder and server AND reports why the encoder stopped. It lets Serve stay
// branch-free over the two mechanisms.
type session struct {
	sink Sink
	// consumer is the renderer's side of this delivery as a health rule reads it. It sits
	// beside the sink rather than widening Sink past URL+Wait, which is deliberately
	// narrow: a third delivery mechanism supplies one as data instead of every observer
	// reaching for a concrete *replay.Server the way the first-bytes gate used to.
	//
	// It is nil on a mechanism nothing supervises in flight, which is not a second way of
	// saying the same thing as delivered being nil, it is the same fact: a delivery that
	// deletes behind its own window has no buffer to weigh a renderer's silence against, so
	// there is nothing an in-flight rule could do with its fetching except convict every cast
	// it serves. Such a mechanism answers for its renderer once, afterwards (see settled).
	consumer watch.Consumer
	// delivered is how much media this delivery can still hand the renderer, nil where this
	// mechanism cannot say so honestly. It is the opener's answer and not the driver's
	// because it is a property of what the mechanism keeps: a delivery that never takes back
	// what it produced can offer the encoder's whole position, while one that rolls a window
	// has only the window.
	delivered func() time.Duration
	// settled states whether the renderer took what this mechanism made for it, asked once
	// the delivery has run its course. It takes nothing, and that is load-bearing: the only
	// facts the answer may rest on are counts this mechanism holds itself (what it handed over
	// and what it produced), so there is no parameter through which a clock could reach the
	// arithmetic again (see undelivered).
	//
	// EVERY mechanism answers, and there is no nil to mean "cannot say". Silence here is how a
	// cast nobody ever fetched exits 0: it was optional, the segmented mechanism left it out
	// because it cannot state a SHARE of a program it deletes, and the one thing it could
	// always have stated (whether anything at all got through) went unsaid on the only leg that
	// also hands over no supervisor. What a mechanism cannot measure it says nothing ABOUT (see
	// unfetched); what it cannot be is silent.
	settled func() error
	ready   func(ctx context.Context) error
	// teardown stops everything and returns the encoder's terminal error. Openers
	// build it with sync.OnceValue so it is idempotent and memoized: Serve both
	// defers it (for the early returns) and calls it on the happy path for its
	// value, and those must be one teardown reporting one result.
	//
	// Returning that error is the point. A dead encoder used to be indistinguishable
	// from a finished one: io.Copy sees a clean EOF, the replay server's Wait returns
	// nil once no client is left, and the exit status was logged at WARN after Serve
	// had already returned success. So an ADTS AAC track copied into the mp4 muxer,
	// which exits 255 with audio:0KiB, produced a castor run that exited 0 having
	// cast nothing. The encoder's exit status is now part of the cast's result,
	// together with ffmpeg.Process.SilentFailure and a probe of what the delivery
	// actually wrote, which between them cover the shapes that exit 0 and still
	// produced nothing playable: the container that refuses a track by writing it as
	// private data reports a plausible byte count and complains to nobody, so the
	// artifact is the only party that ever says so.
	teardown func() error
}

// opener starts an encoder and fronts it, returning the opened session. On
// failure it fully cleans up the encoder itself and returns the error, so Serve
// never has to tear down a half-open delivery.
type opener func(ctx context.Context, p OpenParams, headers map[string]string) (*session, error)

// deliveries maps a format's DeliveryKind to the opener that serves it. This is
// the whole per-delivery dispatch: no switch, no content-type conditional.
var deliveries = map[media.DeliveryKind]opener{
	media.DeliverStream:    openStream,
	media.DeliverSegmented: openSegmented,
}

// Serve runs one served cast end to end and is the single delivery entry point:
// pick the mechanism from the format's DeliveryKind, open it, wait until the
// device can be handed a URL, play, and block until delivered or ctx ends,
// tearing the encoder and server down on every return. It names no device family.
func Serve(ctx context.Context, dev Renderer, p OpenParams) error {
	format := p.Opts.Format
	open, ok := deliveries[format.Delivery]
	if !ok {
		return fmt.Errorf("no delivery mechanism for format %q", format.ContentType)
	}

	sess, err := open(ctx, p, dev.StreamHeaders(format.ContentType))
	if err != nil {
		return err
	}
	defer func() { _ = sess.teardown() }()

	if sess.ready != nil {
		if err := sess.ready(ctx); err != nil {
			// The gate reports that no playlist appeared; the teardown reports WHY the
			// encoder stopped. Returning only the first is how "encoder exited before
			// producing the HLS playlist" used to be the whole story on a run whose
			// ffmpeg had a real reason waiting in its exit status.
			return errors.Join(err, sess.teardown())
		}
	}

	streamURL := sess.sink.URL()
	slog.InfoContext(ctx, "starting playback", "url", streamURL.String(), "content_type", format.ContentType)
	if err := dev.Play(ctx, streamURL, format.ContentType); err != nil {
		return errors.Join(fmt.Errorf("starting playback: %w", err), sess.teardown())
	}
	// Announced before anything else can go wrong, because everything after this line fails
	// with a renderer holding a URL and that is the fact that decides what may be done about
	// it. Nothing between Play and the wait below is allowed to be the reason a caller is
	// told late.
	if p.OnPlaying != nil {
		p.OnPlaying()
	}
	slog.InfoContext(ctx, "streaming to device, press Ctrl+C to stop")

	// The sink's Wait says the delivery ran its course, the supervisor says whether it was
	// working while it did, and the teardown says whether the thing being delivered was
	// real. All three are part of the result, because a clean Wait over an encoder that
	// died on its first audio packet, or over a renderer that never fetched a byte, is
	// exactly the "exited 0 having cast nothing" shape this whole layer exists to prevent.
	waitErr := deliver(ctx, sess, p.Supervise)
	return errors.Join(waitErr, sess.teardown())
}

// deliver blocks until the delivery has run its course or the supervisor names a fault,
// whichever comes first, and returns that party's answer.
//
// A sink's Wait is not on its own a statement that the cast worked: it ends when nothing
// is left to serve, and "nothing is left to serve" is also what a renderer that never
// came for the bytes looks like from here. Racing the two is what makes the supervisor's
// verdict able to outrank a clean-looking delivery, and cancelling the loser is what
// stops either goroutine from outliving the cast.
//
// A Wait that ends CLEANLY is therefore asked one more question: did the renderer take what
// was made for it. That question is answerable only here, at the end, because it is
// arithmetic over the whole cast rather than a state anybody was ever in (see
// session.settled), and it is asked whether or not this leg supervises, because the legs that
// do not are the ones with nobody watching the renderer at all.
func deliver(ctx context.Context, sess *session, supervise func(context.Context, Delivery) error) error {
	if supervise == nil {
		return cmp.Or(sess.sink.Wait(ctx), sess.settled())
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	delivered := make(chan error, 1)
	go func() { delivered <- sess.sink.Wait(ctx) }()
	judged := make(chan error, 1)
	go func() {
		judged <- supervise(ctx, Delivery{Consumer: sess.consumer, Delivered: sess.delivered})
	}()

	select {
	case err := <-delivered:
		return cmp.Or(err, sess.settled())
	case err := <-judged:
		return err
	}
}

// Undelivered is a cast that ran its course while the renderer stopped taking the stream:
// the "exited 0 having cast nothing" shape at the one point it is still measurable, which is
// after the fact.
//
// It is a value and not a message because the numbers are the whole of what a user has to
// act on: a renderer handed two minutes of a two hour film failed differently from one handed
// the first forty of them.
//
// Both delivery mechanisms reach it, on the terms each of them can honestly state. One
// compares the bytes it handed over against the bytes it made, because it never takes a byte
// back (see undelivered); the other can only say whether anything at all got through, because
// it deletes behind its own live edge (see unfetched), and it reaches this with Handed at zero.
type Undelivered struct {
	// Handed is how much media the renderer was given and Produced how much this delivery
	// made. There is no third field, and no wall clock: how long the renderer held the URL is
	// not a statement about media anybody consumed, and reporting it beside these two invites
	// the subtraction that convicted casts watched to the last byte (see undelivered).
	Handed   time.Duration
	Produced time.Duration
}

// Error states the shortfall, in the words that are true of the numbers beside them. Nothing
// was handed over and something was is not a difference of degree: a renderer that never came
// did not "stop taking" a stream, and a fault able to print a sentence its own figures
// contradict is how the wall-clock version of this measurement announced itself as wrong.
func (u *Undelivered) Error() string {
	if u.Handed <= 0 {
		return fmt.Sprintf("the renderer was handed none of the %s this cast produced: it accepted the stream URL and never came for a byte of the program, all of which reached nobody",
			u.Produced.Round(time.Second))
	}
	return fmt.Sprintf("the renderer was handed %s of the %s this cast produced: it stopped taking the stream while castor was still serving it, so most of the program (%s) reached nobody",
		u.Handed.Round(time.Second), u.Produced.Round(time.Second), (u.Produced - u.Handed).Round(time.Second))
}

// handedAtLeast is the share of what a delivery produced that the renderer must have been
// handed for the cast to be reported as delivered. Falling short of it is the whole of what
// Undelivered names.
//
// A SHARE, because the same statement is made about a ninety second trailer and a three hour
// film, and what those two have in common is not a byte count or a duration but how much of
// themselves got through.
//
// DERIVED, from two readings that land on the same number:
//
//   - What the fault is entitled to SAY. It states that most of the program reached nobody.
//     Below a half that is arithmetically true and above it is arithmetically false, and a
//     fault that can print a false sentence is how the wall-clock version of this measurement
//     announced itself as wrong: it convicted a renderer handed every byte with the words
//     "handed 2h0m0s of the 2h0m0s this cast produced ... so most of the program reached
//     nobody".
//   - What a shortfall can be ATTRIBUTED to. A renderer handed most of a program and gone is
//     the same evidence as a viewer who watched most of the film and switched the television
//     off: requests stop, and nothing castor can read separates the two (it is the same
//     boundary the in-flight rules draw around a quiet renderer, see watch's unfetched row).
//     Once less than half got through that reading is gone as well, because nobody watches a
//     film by fetching a third of it.
//
// It pays for no measurement error, and saying so is the point of stating it here. A renderer
// that consumed the program falls short by at most the final write chunk it never had to take
// (replay's 32 KiB against a film's four gigabytes), because the bytes handed over are a max
// over connections that credits a resumed prefix and over-counts whatever the kernel accepted.
// The term this replaces spent its entire allowance on wall clock and still convicted healthy
// casts, so widening it was never going to be the fix.
const handedAtLeast = 0.5

// undelivered states whether the renderer took what this delivery made, from the two counts
// that answer it: the bytes the sink handed over (sent, the most any one connection got) and
// the bytes the encoder says it wrote (made, its last progress sample). Both are counts of the
// same kind, they are compared against each other, and there is nothing else in the
// arithmetic.
//
// NO CLOCK, and this is the whole shape of the measurement rather than a detail of it. The
// wall clock a renderer held the URL for reads like the obvious third term and was one, but it
// is not a stand-in for media a viewer consumed, and putting it on one side of a subtraction
// convicts casts that worked:
//
//   - Accumulated pauses. A pause shorter than the delivery's write deadline never severs the
//     connection, so it lands entirely in the elapsed time while the bytes handed over do not
//     move. Three hundred-second pauses across a two hour film, watched to its last byte, was
//     convicted.
//   - Castor's own encoding pace. A burn-in pinned just above realtime averaging 0.9x, or a
//     remux binding the height ceiling on a software-only host, makes two hours of media in
//     two hours and thirteen minutes. The renderer takes every byte and was convicted for
//     minutes that were castor's own doing, on the leg that has no supervisor to have caught
//     the misattribution earlier.
//
// No tolerance answers either, because neither is bounded by anything a tolerance could be
// derived from: pauses accumulate, and how long an encode runs is a property of the host.
// Comparing the two counts is immune to both by construction, since neither a pause nor a slow
// encoder moves either count.
//
// BYTES and not media seconds, because bytes are what both parties state exactly. A muxed VBR
// stream has no byte-to-time mapping of its own, so the media figures the fault reports are the
// encoder's own cumulative pair (one -progress block states total_size and out_time together)
// applied to the share that got through: honest as a report, an invented bitrate if it were the
// comparison.
//
// WHAT IT NAMES: a renderer that fetched once and went away mid-title. The encoder fills the
// spool to the end of the program whatever the renderer does, replay severs the blocked write
// at its deadline, the client count reaches zero, Wait's idle grace expires and the cast was
// reported as DELIVERED with the film's last hour delivered to nobody.
//
// WHAT IT MUST NOT NAME, and does not: a cast the user stopped, whose Wait returns the
// context's error so this is never asked at all; a renderer that read the stream to EOF, which
// has handed == produced; and a live source, whose producer never ends, so its Wait never
// reports a delivery over in the first place.
func undelivered(sent int64, made media.Progress) error {
	if made.Bytes <= 0 || made.Position <= 0 {
		// Nothing was produced to be taken. That is the artifact gate's verdict and the
		// encoder's exit status to report, and answering here as well would blame the renderer
		// for a stream that never existed.
		return nil
	}
	// A share above 1 is not clamped and needs no clamp: the encoder's last progress block is
	// a moment behind the spool it is measured against, so a renderer that read to EOF is
	// credited with marginally more bytes than the encoder had got round to claiming, and that
	// direction only ever excuses. The reported figure is derived inside the convicted branch,
	// where the share is under a half by definition.
	share := float64(sent) / float64(made.Bytes)
	if share >= handedAtLeast {
		return nil
	}
	return &Undelivered{Handed: time.Duration(float64(made.Position) * share), Produced: made.Position}
}

// unfetched states whether any of what this delivery produced reached the renderer at all,
// from the count of artifacts the sink handed over and the encoder's last progress sample. It
// is the completeness statement of a mechanism that deletes what it produced.
//
// NOT A SHARE, and no threshold: this mechanism has no share to state. Its muxer rolls a
// window and deletes behind it (ffmpeg.HLSWindow), so a renderer that fetched every segment it
// was ever offered has taken a fraction of the bytes the encoder wrote, and comparing the two
// would convict every segmented cast castor makes. Zero differs from that in kind and not in
// degree: no deletion excuses it, no pause produces it (a renderer that paused had fetched
// first, or it had nothing to pause), and it is exact rather than measured, which is why it
// needs no allowance and can have none widened.
//
// WHAT IT NAMES, and it is the failure this whole layer claims to own surviving in the one
// place nothing watched: the segmented delivery is reached by the leg that hands over no
// supervisor, so nothing judges its renderer in flight. The renderer accepts the playlist URL,
// never asks for a segment, the encoder runs the title to its end, the sink's idle grace
// expires against a timestamp seeded when it was created, its Wait returns nil, and castor
// exited 0 having cast nothing.
//
// WHAT IT MUST NOT NAME, and does not: a cast the user stopped, whose Wait returns the
// context's error so this is never asked at all; a live source, whose producer never ends, so
// its Wait never reports a delivery over; a renderer that took some of the program and stopped,
// which this mechanism cannot measure and therefore does not judge; and a delivery that
// produced nothing, which is the artifact gate's verdict and the encoder's exit status to
// report, not the renderer's doing.
//
// Position and not Bytes is what says something was produced, because it is the field this
// muxer states: total_size describes the file ffmpeg has open, and a muxer whose output is a
// directory of segments it deletes is not one to ask how large its output is.
func unfetched(served int, made media.Progress) error {
	if made.Position <= 0 || served > 0 {
		return nil
	}
	return &Undelivered{Produced: made.Position}
}

// openStream serves a single growing output over the replay-from-zero server: the
// encoder writes pipe:1, which the server spools and replays to every client from
// byte 0. The URL is handed over once the muxer has produced a byte, or once this
// delivery's own patience for one runs out. Teardown closes the server then the encoder,
// so nothing is left writing when the caller removes the work directory.
func openStream(ctx context.Context, p OpenParams, headers map[string]string) (*session, error) {
	// This mechanism never takes back a byte it produced (the spool is not truncated and
	// every connection replays it from 0), so everything the encoder has written is still
	// fetchable and its whole position is the honest answer to what a paused renderer has
	// left to play. Followed here rather than inside startEncoder because it is this
	// mechanism's guarantee that makes the figure true, and it is the same guarantee that
	// makes the completeness statement below arithmetic: what the renderer was handed can be
	// compared against what was made only where nothing made was ever withdrawn.
	follow, made := produced(p.OnProgress)
	p.OnProgress = follow
	proc, joinProgress, err := startEncoder(ctx, p)
	if err != nil {
		return nil, err
	}

	// The replay spool is this delivery's artifact as well as its buffer: it is the
	// complete output the renderer was handed, byte for byte, so probing it asks
	// what the muxer really wrote rather than what it was asked to write.
	format := p.Opts.Format
	artifact := filepath.Join(p.WorkDir, "out"+format.Extension)
	srv, err := replay.New(replay.Config{
		LocalIP:     p.LocalIP,
		ContentType: format.ContentType,
		Extension:   format.Extension,
		Headers:     headers,
		SpoolPath:   artifact,
	}, proc.Stdout)
	if err != nil {
		_ = finishEncoder(ctx, proc)
		joinProgress()
		return nil, fmt.Errorf("starting stream server: %w", err)
	}

	return &session{
		sink:      srv,
		consumer:  srv,
		delivered: func() time.Duration { return made().Position },
		settled: func() error {
			// The recency the sink states beside it is the in-flight window's business (see
			// watch.Consumer) and no term of this arithmetic: what is being answered here is how
			// much of the program got through, and a clock on either side of that subtraction is
			// what convicted casts watched to their last byte (see undelivered).
			handed, _ := srv.Handed()
			return undelivered(handed, made())
		},
		ready: func(ctx context.Context) error {
			return watch.Watch(ctx, watch.Monitor{
				Subject:  "the stream output",
				Window:   watch.Opening,
				Producer: producedBy(proc, srv.ProducerDone()),
				Landed:   srv.Produced,
				Grace:    firstBytesTimeout,
			})
		},
		teardown: sync.OnceValue(func() error {
			_ = srv.Close()
			err := finishEncoder(ctx, proc)
			joinProgress()
			return err
		}),
	}, nil
}

// openSegmented serves a live HLS directory: the encoder writes the playlist and
// rolling segments into the work directory, which the HLS server fronts. Because
// the output is files (not pipe:1), this fully owns the encoder lifecycle: a
// single goroutine drains the unused stdout to EOF and only then Waits (honoring
// os/exec's no-Wait-before-reads contract), then signals the server and the
// readiness gate. The device is handed the playlist only once the muxer has written
// something into it (the gate fails fast if the encoder dies first, and a zero-byte
// playlist is a 200 no renderer can parse). Teardown kills the encoder, joins the
// goroutine, then closes the server, so nothing writes into the work directory
// after the caller removes it.
//
// It reports no fetchable buffer, and that is a measurement rather than an omission. This
// muxer rolls a window and deletes behind it (ffmpeg.HLSWindow), so media the encoder wrote
// a minute ago is gone from the playlist: the most this delivery can ever have in hand is
// that window, which is a fraction of the silence either in-flight rule waits out
// (TestARollingWindowKeepsNothingASupervisorCouldHoldACastOpenOver), so there is nothing
// here to hold a cast open with. A renderer that stops fetching this for two and a half
// minutes has genuinely lost the segments it would resume from.
//
// For the same reason it states no SHARE of what it produced: what a renderer was handed cannot
// be weighed against what was made when most of what was made has been deleted, and a client
// that fetched every segment it was ever offered has still taken a fraction of the program's
// bytes. What it does state is whether anything at all got through (see unfetched), which no
// amount of deleting excuses and which is the whole of the failure this leg used to hide: it is
// reached by the composition that hands over no supervisor, so a renderer that took the URL and
// never asked for a segment was judged by nobody, in flight or afterwards.
func openSegmented(ctx context.Context, p OpenParams, headers map[string]string) (*session, error) {
	// Followed for one figure only: how much media this cast produced, which is what the
	// completeness statement below reports a renderer against. The segments themselves cannot
	// answer it, since the directory never holds more than a window of them.
	follow, made := produced(p.OnProgress)
	p.OnProgress = follow
	proc, joinProgress, err := startEncoder(ctx, p, ffmpeg.WithWorkDir(p.WorkDir))
	if err != nil {
		return nil, err
	}

	// The renderer's own headers travel with a segmented delivery too. Discarding them
	// here meant a family that only fetches what its transfer-mode header asks for was
	// served a playlist it had no reason to accept, on the one delivery where the fault
	// is invisible: the playlist 200s, every segment 200s, and the renderer simply never
	// asks for the second one.
	srv, err := hlsserve.New(hlsserve.Config{
		LocalIP:  p.LocalIP,
		Dir:      p.WorkDir,
		Playlist: media.HLSPlaylistName,
		Headers:  headers,
	})
	if err != nil {
		proc.Kill()
		_ = proc.Wait()
		joinProgress()
		return nil, fmt.Errorf("starting HLS server: %w", err)
	}

	var (
		wg       sync.WaitGroup
		exitErr  error
		exited   = make(chan struct{})
		producer = func() {
			// The output is files, not pipe:1, so stdout carries nothing; drain it to
			// EOF before Wait to honour os/exec's no-Wait-before-reads contract.
			_, _ = io.Copy(io.Discard, proc.Stdout)
			exitErr = encoderResult(ctx, proc, proc.Wait())
			srv.ProducerEnded()
			close(exited)
		}
	)
	wg.Go(producer)

	playlist := filepath.Join(p.WorkDir, media.HLSPlaylistName)
	return &session{
		sink: srv,
		// No consumer and no fetchable buffer: this mechanism is judged once its delivery has run
		// its course rather than while it runs, and both absences are the same measurement. A
		// supervisor here would have to read a buffer this muxer cannot state and a landed figure
		// that stops changing the moment the window is full, and the in-flight rules answer that
		// pair with a stall on every cast this mechanism serves, two and a half minutes in. What
		// can be said is said at the end, where zero is zero however much was deleted.
		settled: func() error {
			return unfetched(srv.Served(), made())
		},
		ready: func(ctx context.Context) error {
			return watch.Watch(ctx, watch.Monitor{
				Subject:  "the HLS playlist",
				Window:   watch.Opening,
				Producer: producedBy(proc, exited),
				Landed:   written(playlist),
			})
		},
		teardown: sync.OnceValue(func() error {
			proc.Kill()
			wg.Wait()
			joinProgress()
			_ = srv.Close()
			return exitErr
		}),
	}, nil
}

// produced follows what the encoder says it has written and hands back the latest sample of
// it: how much media has been made fetchable so far, and how many bytes that took. It wraps
// the caller's consumer instead of replacing it, because the feed has exactly one reader (see
// startEncoder) and the spool path places its subtitle cues off the same samples.
//
// The whole sample and not just the position, because the two numbers are only comparable
// TOGETHER: a muxed VBR stream has no byte-to-time mapping of its own, so the one honest
// conversion is the encoder's own cumulative pair, stated in one -progress block by the party
// that wrote both. Read separately they would be a bitrate somebody invented.
//
// What a mechanism may do with the sample follows from what it keeps. One that never withdraws
// a byte can read the position as media a renderer can still fetch and the bytes as what it
// should have been handed; one that deletes behind a window can read neither, and takes only
// what the encoder PRODUCED (ffmpeg states total_size for the file it has open, which for a
// directory of segments it is deleting out of is no statement about the program at all).
func produced(report func(media.Progress)) (func(media.Progress), func() media.Progress) {
	var latest atomic.Pointer[media.Progress]
	return func(sample media.Progress) {
			latest.Store(&sample)
			if report != nil {
				report(sample)
			}
		}, func() media.Progress {
			if s := latest.Load(); s != nil {
				return *s
			}
			return media.Progress{}
		}
}

// startEncoder builds and launches the encode and takes ownership of the feed it
// reports itself on. extra are start options this delivery adds to the caller's.
//
// The returned join blocks until the last sample has been delivered, and every
// teardown calls it after reaping the process. That ordering is what a progress
// consumer needs to be safe: the spool path's consumer writes a cue file into the
// work directory its caller removes as soon as Serve returns, so "the encoder has
// exited" is not on its own enough to say nobody is still writing.
func startEncoder(ctx context.Context, p OpenParams, extra ...ffmpeg.StartOption) (*ffmpeg.Process, func(), error) {
	args, err := ffmpeg.EncodeArgs(p.Opts)
	if err != nil {
		return nil, nil, fmt.Errorf("building encode args: %w", err)
	}
	startOpts := slices.Concat(p.StartOpts, extra, []ffmpeg.StartOption{
		ffmpeg.WithExtraPipes(ffmpeg.EncodeExtraPipes),
	})
	proc, err := ffmpeg.Start(ctx, p.FFmpegPath, args, startOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("starting transcode: %w", err)
	}

	// Drained unconditionally, and by exactly one reader. An encode emits -progress
	// whether or not anyone asked for the samples, so the alternative to draining is a
	// process that runs for two minutes and then blocks on a full pipe forever.
	report := p.OnProgress
	if report == nil {
		report = func(media.Progress) {}
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		feed := proc.ProgressFeed()
		defer func() { _ = feed.Close() }()
		ffmpeg.WatchProgress(feed, report)
	}()

	return proc, func() { <-drained }, nil
}

// finishEncoder tears down a pipe-fed encoder and reports why it stopped: close
// its output (the encoder gets EPIPE and exits), wait for exit, and judge what it
// left behind, because exit 0 is not evidence the output was playable.
func finishEncoder(ctx context.Context, proc *ffmpeg.Process) error {
	_ = proc.Stdout.Close()
	return encoderResult(ctx, proc, proc.Wait())
}

// encoderResult turns an encoder's exit into the cast's verdict on it. waitErr is
// what os/exec reported; a clean exit is still a failure when ffmpeg printed one
// of the lines that mean the output is not playable (see Process.SilentFailure),
// which is the whole point of asking. A cancelled context means castor killed
// ffmpeg itself (Ctrl+C), so that is not a failure and the tail is not dumped.
func encoderResult(ctx context.Context, proc *ffmpeg.Process, waitErr error) error {
	err := cmp.Or(waitErr, proc.SilentFailure())
	if err == nil || ctx.Err() != nil {
		return nil
	}
	proc.LogStderrTail(ctx, "ffmpeg stderr")
	return fmt.Errorf("encoder: %w", err)
}

// firstBytesTimeout is how long a stream delivery waits for its first byte before
// pointing the renderer at it anyway. It is generous because it is not a health check:
// the only thing it has to be shorter than is a user's patience, and the failure it
// exists to catch (a container refusing a track at header-write time, before a single
// byte) announces itself in milliseconds.
const firstBytesTimeout = 10 * time.Second

// encoderOutput is a delivery's own producer as a health rule reads it, so the wait for
// an artifact to appear is judged by the same table as everything else about a cast.
//
// Progress is deliberately empty. What this window judges is whether the artifact
// exists, and the encoder's own pace is not a statement about a link's carrying capacity:
// the subtitle-burning encode is pinned just above realtime by design, so offering its
// speed here is offering a number the deliverability rule would have to be taught to
// ignore. Err is nil for the same reason of ownership: the encoder's verdict belongs to
// this delivery's teardown, and Serve joins it into the result, so a gate that
// duplicated it would report one failure twice and could disagree about it.
type encoderOutput struct {
	proc  *ffmpeg.Process
	ended <-chan struct{}
}

func producedBy(proc *ffmpeg.Process, ended <-chan struct{}) encoderOutput {
	return encoderOutput{proc: proc, ended: ended}
}

func (o encoderOutput) Progress() media.Progress { return media.Progress{} }
func (o encoderOutput) Done() <-chan struct{}    { return o.ended }
func (o encoderOutput) Err() error               { return nil }
func (o encoderOutput) Evidence() []string       { return o.proc.Evidence().Lines }

// written reports how much of a file the muxer has put on disk, which is what a
// segmented delivery's artifact gate is waiting for. It is the SIZE and not merely the
// existence of the playlist: a renderer handed a zero-byte m3u8 gets a 200 it cannot
// parse, and it does not come back for a second look.
func written(path string) func() int64 {
	return func() int64 {
		info, err := os.Stat(path)
		if err != nil {
			return 0
		}
		return info.Size()
	}
}

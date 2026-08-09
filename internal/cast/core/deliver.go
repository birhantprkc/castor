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
// up from the format's DeliveryKind (data) and drives it uniformly. A mechanism is
// one interface with one constructor behind the table, so adding a delivery is new
// data plus a small adapter, not another Serve* function and not a content-type branch.
//
// It also owns one lifetime and owns it whole: the encoder and the mechanism fronting it are
// started here, stopped here, and stopped in one order no mechanism can restate (see open).

// mechanism is one delivery mechanism, whole: where it is fetched, when it is over, what a
// renderer may be pointed at, what can be judged about it while it runs and afterwards, and
// how it stops.
//
// One interface, because this used to be three things describing one: a two-method port for
// the URL and the wait, four closures on the driver's own session for everything else, and a
// third type repackaging two of those for the supervisor. Nothing kept them in agreement, and
// each mechanism then hand-wrote its own five steps in its own order.
//
// Both implementations are thin adapters over a server that already answered every one of these
// under a different name (Handed, Served, Spooled, ProducerDone, ProducerEnded).
type mechanism interface {
	// URL is the address the renderer fetches this delivery at.
	URL() *url.URL

	// Wait blocks until the delivery has run its course, or ctx ends. It states that nothing is
	// left to serve, which is NOT a statement that the cast worked: that is why the driver asks
	// two further questions of every mechanism (see InFlight and Settled).
	Wait(ctx context.Context) error

	// Artifact is what a renderer will be pointed at, as the gate waiting for it reads it.
	Artifact() Artifact

	// Drained is closed once this mechanism has read the encoder's output to its end.
	//
	// It is two things at once, and they are the same fact. It is what an artifact gate watches
	// to tell "still starting" from "already over", since a producer that ended having written
	// nothing will never write anything. And it is what makes reaping the encoder safe: os/exec
	// closes the output pipe inside Wait, so a reap racing a mechanism that is still reading it
	// truncates the very stream being delivered.
	Drained() <-chan struct{}

	// InFlight is this delivery as a supervisor reads it, or nil where this mechanism cannot
	// honestly be judged while it runs (see Delivery, and segmented.InFlight for the mechanism
	// that answers nil).
	InFlight() *Delivery

	// Settled states whether the renderer took what this mechanism made for it, asked once the
	// delivery has run its course. It takes nothing, and that is load-bearing: the only facts
	// the answer may rest on are counts the mechanism holds itself (what it handed over and what
	// it produced), so there is no parameter through which a clock could reach the arithmetic
	// again (see undelivered).
	//
	// EVERY mechanism answers, and it is a method rather than an optional field for that reason.
	// Silence here is how a cast nobody ever fetched exited 0: it was a closure the segmented
	// mechanism left nil because it cannot state a SHARE of a program it deletes, and the one
	// thing it could always have stated (whether anything at all got through) went unsaid on the
	// one mechanism nothing judges in flight either. What a mechanism cannot measure it says
	// nothing ABOUT (see unfetched); what it cannot be is silent.
	Settled() error

	// Close stops serving and finishes reading the encoder's output, joining whatever goroutine
	// of its own was doing the reading. The driver calls it as one step of one teardown; a
	// mechanism orders nothing itself.
	Close() error
}

// Artifact is what a renderer will be pointed at while it is still appearing: what to call it
// in a log line and in a fault, how much of it exists, and how long this mechanism is willing
// to wait for the first of it.
type Artifact struct {
	// Subject is the difference between "the stream output" and "the HLS playlist" in a message
	// a user reads.
	Subject string

	// Landed reports how much of the artifact exists now. It is polled, and it is the delivery's
	// own artifact rather than the encoder's report about itself, so a producer whose telemetry
	// pipe broke cannot hold a cast whose output is filling.
	Landed func() int64

	// Grace is how long the artifact may take to appear before the renderer is pointed at it
	// anyway. Zero never proceeds, which is the honest answer for a document a renderer cannot
	// be handed half of.
	Grace time.Duration
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
// DeliverSegmented duplicate writes "-f mp4 pipe:1" while the segmented mechanism
// waits forever for a playlist).
type OpenParams struct {
	FFmpegPath string
	Opts       ffmpeg.EncodeOptions
	LocalIP    string
	WorkDir    string

	// Input, if set, is the feed the encoder reads on stdin, and Serve OWNS it: it is closed as
	// one step of the teardown, on every path out, and a caller must not close it itself. The
	// ownership is what makes that teardown terminate rather than a tidiness (see open), and a
	// defer at the caller could not do the job, since it cannot run until the delivery has
	// returned, which is what it would be waiting for. A leg whose encoder reads the network
	// leaves it nil.
	Input io.ReadCloser

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
}

// Supervisor judges a cast for as long as the renderer is playing it, over the read behind the
// delivery. It is a parameter of Serve and not a field of OpenParams, so no leg can leave a cast
// unwatched by omitting one: the remux leg did exactly that, and every Chromecast cast of a
// header-gated source ran with no stall rule, no in-flight deliverability rule and no unfetched
// rule at all, on the composition where the encoder IS the read.
//
// nil says "this leg opened no read of its own", NOT "do not watch". The driver then judges the
// encode it started itself (see session.watchTheEncode), which on such a leg is the read.
//
// It is a callback because the facts a health rule needs live on both sides of this layer: what
// the renderer fetches and what is still fetchable are the delivery's to report, while a read
// castor opened belongs to the leg that opened it, and Serve is not the party that gets to hold
// them both. It returns when a verdict ends the cast, and its error becomes the cast's error
// joined with whatever the encoder had to say.
type Supervisor func(ctx context.Context, d Delivery) error

// Delivery is one opened delivery as its supervisor reads it: who has come for the bytes, and
// how much media is still there to be come for.
//
// The two travel as ONE value, built by one constructor, and that is what makes "half the pair"
// unrepresentable: it used to be two independent nilable closures with a paragraph elsewhere
// warning that a leg must not hand over one of them. Either fact alone convicts a viewer who
// PAUSES. A paused renderer stops requesting segments, or stops draining the socket, and no
// mechanism can tell that from a renderer that went away, so silence alone ended a film at two
// and a half minutes of somebody standing up. What separates the two is whether castor still has
// anything for it to come back to.
type Delivery struct {
	// Consumer is the renderer's fetching as the mechanism fronting this delivery tracks it.
	Consumer watch.Consumer

	// Delivered is how much media this delivery can still hand over.
	Delivered func() time.Duration
}

// inFlight is the only way a Delivery is built: both facts or no Delivery at all.
func inFlight(consumer watch.Consumer, delivered func() time.Duration) *Delivery {
	return &Delivery{Consumer: consumer, Delivered: delivered}
}

// opening is what a mechanism is built from, and all of it is decided before any mechanism
// exists: this cast's parameters, the directory its artifacts live in, the encoder's output, the
// headers the renderer asked for, and the encoder's own account of what it has made.
type opening struct {
	p       OpenParams
	dir     string
	out     io.Reader
	headers map[string]string
	made    func() media.Progress
}

// mechanisms maps a format's DeliveryKind to the constructor that fronts it. This is the whole
// per-delivery dispatch: no switch, no content-type conditional. A constructor starts no process
// and owns no lifetime, which is what leaves the ordering in one place (see open).
var mechanisms = map[media.DeliveryKind]func(opening) (mechanism, error){
	media.DeliverStream:    newStreamed,
	media.DeliverSegmented: newSegmented,
}

// session is one opened delivery: the mechanism serving it, the encode behind it, and the one
// stop that ends both.
type session struct {
	// mech is the delivery mechanism serving this cast, which is the whole of what a delivery
	// supplies. Nothing about it arrives here as a closure an opener could fill half of.
	mech mechanism

	// proc is the encode, held for the one thing the mechanism cannot state: what ffmpeg printed
	// while the artifact was failing to appear.
	proc *ffmpeg.Process

	// stop ends the encode and everything the driver started with it, in one order (see open).
	// It is memoized, because Serve both defers it (for the early returns) and calls it on the
	// happy path for its value, and those must be one teardown reporting one result.
	//
	// Returning that error is the point. A dead encoder used to be indistinguishable from a
	// finished one: io.Copy sees a clean EOF, the mechanism's Wait ends once no client is left,
	// and the exit status was logged at WARN after Serve had already returned success. It is now
	// part of the cast's result, beside what the mechanism says the renderer took (see Serve).
	stop func() error
}

// Serve runs one served cast end to end and is the single delivery entry point:
// pick the mechanism from the format's DeliveryKind, open it, wait until the
// device can be handed a URL, play, and block until delivered or ctx ends,
// tearing the encoder and mechanism down on every return. It names no device family.
func Serve(ctx context.Context, dev Renderer, p OpenParams, supervise Supervisor) error {
	format := p.Opts.Format
	sess, err := open(ctx, p, dev.StreamHeaders(format.ContentType))
	if err != nil {
		return err
	}
	defer func() { _ = sess.stop() }()

	if err := sess.ready(ctx); err != nil {
		// The gate reports that no artifact appeared; the teardown reports WHY the encoder
		// stopped. Returning only the first is how "encoder exited before producing the HLS
		// playlist" used to be the whole story on a run whose ffmpeg had a real reason waiting in
		// its exit status.
		return errors.Join(err, sess.stop())
	}

	streamURL := sess.mech.URL()
	slog.InfoContext(ctx, "starting playback", "url", streamURL.String(), "content_type", format.ContentType)
	if err := dev.Play(ctx, streamURL, format.ContentType); err != nil {
		return errors.Join(fmt.Errorf("starting playback: %w", err), sess.stop())
	}
	// Announced before anything else can go wrong, because everything after this line fails
	// with a renderer holding a URL and that is the fact that decides what may be done about
	// it. Nothing between Play and the wait below is allowed to be the reason a caller is
	// told late.
	if p.OnPlaying != nil {
		p.OnPlaying()
	}
	slog.InfoContext(ctx, "streaming to device, press Ctrl+C to stop")

	// The mechanism's Wait says the delivery ran its course, the supervisor says whether it was
	// working while it did, and the teardown says whether the thing being delivered was
	// real. All three are part of the result, because a clean Wait over an encoder that
	// died on its first audio packet, or over a renderer that never fetched a byte, is
	// exactly the "exited 0 having cast nothing" shape this whole layer exists to prevent.
	waitErr := sess.deliver(ctx, supervise)
	return errors.Join(waitErr, sess.stop())
}

// open starts the encode, fronts it with the mechanism the format names, and returns the opened
// session. It is the ONE place either is started or stopped, and the ordering below is used by
// every failure of its own as well as by the end of a healthy cast, so there is no second
// ordering to disagree with it.
func open(ctx context.Context, p OpenParams, headers map[string]string) (*session, error) {
	var (
		proc *ffmpeg.Process
		mech mechanism
		join = func() {}
	)
	// THE TEARDOWN, built before anything is started so that everything below can use it, and
	// total over the states where half of what it stops does not exist yet.
	stop := sync.OnceValue(func() error {
		// Killed first, because everything after this needs the encoder to have stopped writing:
		// a mechanism reading its output reaches EOF, and a reap can happen at all. Kill and not
		// a cancelled context, because os/exec turns a cancellation into the process's own error
		// and every healthy cast would then report "encoder: context canceled" and dump a stderr
		// tail.
		if proc != nil {
			proc.Kill()
		}
		// Then the feed it reads, which is what frees os/exec's stdin-copying goroutine: Wait
		// waits for that goroutine, and it parks in a read of a buffer that stops growing at
		// exactly the moment a fault ends a cast (see OpenParams.Input).
		if p.Input != nil {
			_ = p.Input.Close()
		}
		// Then the mechanism, which stops serving and joins its own reading of the output, so
		// nothing is left reading a pipe the reap below is about to close, and nothing is left
		// writing into a work directory the caller removes next.
		if mech != nil {
			_ = mech.Close()
		}
		// Then the reap, and only then the progress feed's join: a progress consumer writes a cue
		// file into that same work directory, so "the encoder has exited" is not on its own enough
		// to say nobody is still writing.
		if proc == nil {
			return nil
		}
		err := encoderResult(ctx, proc, proc.Wait())
		join()
		return err
	})

	build, ok := mechanisms[p.Opts.Format.Delivery]
	if !ok {
		return nil, errors.Join(fmt.Errorf("no delivery mechanism for format %q", p.Opts.Format.ContentType), stop())
	}
	// The mechanism's artifacts live in a directory of their own, never in the cast's work
	// directory itself: the file server that fronts a segmented delivery publishes everything in
	// the directory it is given, and the cast's own private files (the buffer a read-once leg
	// fills, the live cue file a burn-in draws from) are in the work directory beside it.
	dir := filepath.Join(p.WorkDir, "delivery")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.Join(fmt.Errorf("creating the delivery directory: %w", err), stop())
	}

	// The encoder's own account of what it has made, wrapping the caller's consumer rather than
	// replacing it, because the feed has exactly one reader and the spool path places its
	// subtitle cues off the same samples.
	follow, made := produced(p.OnProgress)
	p.OnProgress = follow

	var err error
	if proc, join, err = startEncoder(ctx, p, dir); err != nil {
		return nil, errors.Join(err, stop())
	}
	if mech, err = build(opening{p: p, dir: dir, out: proc.Stdout, headers: headers, made: made}); err != nil {
		return nil, errors.Join(fmt.Errorf("starting the delivery: %w", err), stop())
	}
	return &session{mech: mech, proc: proc, stop: stop}, nil
}

// ready holds until the renderer may be pointed at this delivery's artifact, or until the
// mechanism's own patience for one runs out.
//
// One gate for every mechanism, over the terms each of them states (see Artifact). The producer
// it watches is the encode, which answers two facts and not four: what it printed, and whether
// its output has ended. Its pace and its terminal error are deliberately not offered here (see
// watch.Telemetry).
func (s *session) ready(ctx context.Context) error {
	artifact := s.mech.Artifact()
	return watch.Watch(ctx, watch.Monitor{
		Subject:  artifact.Subject,
		Window:   watch.Opening,
		Producer: encoderOutput{proc: s.proc, ended: s.mech.Drained()},
		Landed:   artifact.Landed,
		Grace:    artifact.Grace,
	})
}

// deliver blocks until the delivery has run its course or a supervisor names a fault,
// whichever comes first, and returns that party's answer.
//
// A mechanism's Wait is not on its own a statement that the cast worked: it ends when nothing
// is left to serve, and "nothing is left to serve" is also what a renderer that never
// came for the bytes looks like from here. Racing the two is what makes the supervisor's
// verdict able to outrank a clean-looking delivery, and cancelling the loser is what
// stops either goroutine from outliving the cast.
//
// WHO IS SUPERVISED is a property of the mechanism and not of the leg: whatever can be judged in
// flight, is (see Supervisor). Only a mechanism that can state neither what the renderer fetched
// nor what is left for it runs unwatched, which it must, since the in-flight rules answer that
// pair with a stall on every cast (see segmented.InFlight).
//
// A Wait that ends CLEANLY is asked one more question: did the renderer take what was made for
// it. That question is answerable only here, at the end, because it is arithmetic over the whole
// cast rather than a state anybody was ever in (see mechanism.Settled), and it is asked whether
// or not this cast was supervised.
func (s *session) deliver(ctx context.Context, supervise Supervisor) error {
	d := s.mech.InFlight()
	if d == nil {
		return s.settle(s.mech.Wait(ctx))
	}
	if supervise == nil {
		supervise = s.watchTheEncode
	}

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	delivered := make(chan error, 1)
	go func() { delivered <- s.mech.Wait(ctx) }()
	judged := make(chan error, 1)
	go func() { judged <- supervise(ctx, *d) }()

	select {
	case err := <-delivered:
		return s.settle(err)
	case err := <-judged:
		return err
	}
}

// settle asks the mechanism what the renderer took, and asks it ONLY of a delivery that ran its
// course cleanly.
//
// The ordering is the whole of it. Both statements say they are never made about a cast the user
// stopped (see undelivered and unfetched), and while this was one cmp.Or the ordering was a claim
// rather than a fact: cmp.Or is a function, so its second argument is evaluated on every cancelled
// cast and only its answer discarded. Harmless while these two are pure arithmetic, and not
// harmless the day one of them logs, counts, or reads a clock that has been running since Play.
func (s *session) settle(delivered error) error {
	if delivered != nil {
		return delivered
	}
	return s.mech.Settled()
}

// watchTheEncode judges a cast whose producer is the encode this driver started, which is the
// composition where the encode IS the read: one ffmpeg reads the upstream and writes the very
// bytes the renderer fetches (see Supervisor for the casts this leaves watched).
//
// NO PACE, and it is not an omission. The deliverability verdict says the source cannot sustain
// the cast, and this encode is castor's own work: it may be decoding, scaling under the height
// ceiling and re-encoding, which needs hardware to hold realtime and sits far under it on a
// software-only host. Measured as a link, that ends a cast someone is watching and sends the
// user after their network. What is left is exactly what is honest here: an upstream that has
// stopped landing bytes while the renderer has nothing buffered, and a renderer that never came.
func (s *session) watchTheEncode(ctx context.Context, d Delivery) error {
	return watch.Watch(ctx, watch.Monitor{
		Subject:   "the playing cast",
		Window:    watch.Playing,
		Producer:  encoderOutput{proc: s.proc, ended: s.mech.Drained()},
		Landed:    s.mech.Artifact().Landed,
		Consumer:  d.Consumer,
		Delivered: d.Delivered,
	})
}

// streamed serves a single growing output over the replay-from-zero server: the encoder writes
// pipe:1, which the server spools and replays to every client from byte 0. The URL is handed
// over once the muxer has produced a byte, or once this delivery's own patience for one runs out.
//
// This mechanism never takes back a byte it produced (the spool is not truncated and every
// connection replays it from 0), so everything the encoder has written is still fetchable: its
// whole position is the honest answer to what a paused renderer has left to play, and what the
// renderer was handed can be weighed against what was made, which is arithmetic only where
// nothing made was ever withdrawn.
type streamed struct {
	srv  *replay.Server
	made func() media.Progress
}

func newStreamed(o opening) (mechanism, error) {
	// The replay spool is this delivery's artifact as well as its buffer: it is the complete
	// output the renderer was handed, byte for byte.
	format := o.p.Opts.Format
	srv, err := replay.New(replay.Config{
		LocalIP:     o.p.LocalIP,
		ContentType: format.ContentType,
		Extension:   format.Extension,
		Headers:     o.headers,
		SpoolPath:   filepath.Join(o.dir, "out"+format.Extension),
	}, o.out)
	if err != nil {
		return nil, fmt.Errorf("starting stream server: %w", err)
	}
	return streamed{srv: srv, made: o.made}, nil
}

func (m streamed) URL() *url.URL                  { return m.srv.URL() }
func (m streamed) Wait(ctx context.Context) error { return m.srv.Wait(ctx) }
func (m streamed) Drained() <-chan struct{}       { return m.srv.ProducerDone() }
func (m streamed) Close() error                   { return m.srv.Close() }

func (m streamed) Artifact() Artifact {
	return Artifact{
		Subject: "the stream output",
		Landed:  func() int64 { n, _ := m.srv.Spooled(); return n },
		Grace:   firstBytesTimeout,
	}
}

func (m streamed) InFlight() *Delivery {
	return inFlight(m.srv, func() time.Duration { return m.made().Position })
}

func (m streamed) Settled() error {
	// The recency the server states beside it is the in-flight window's business (see
	// watch.Consumer) and no term of this arithmetic: what is being answered here is how much of
	// the program got through, and a clock on either side of that subtraction is what convicted
	// casts watched to their last byte (see undelivered).
	handed, _ := m.srv.Handed()
	return undelivered(handed, m.made())
}

// segmented serves a live HLS directory: the encoder writes the playlist and rolling segments
// into the delivery's own directory, which the HLS server fronts. Because the output is files
// (not pipe:1), stdout carries nothing and is drained here to EOF, which is also what says the
// producer has ended. The device is handed the playlist only once the muxer has written
// something into it: a zero-byte playlist is a 200 no renderer can parse, and it does not come
// back for a second look.
//
// Both of the narrower answers it gives are measurements rather than omissions, and they have
// one cause: it deletes behind its own live edge. It reports no fetchable buffer (see InFlight)
// and states no SHARE of what it produced, only whether anything at all got through (see
// unfetched).
type segmented struct {
	srv      *hlsserve.Server
	made     func() media.Progress
	playlist string

	// drained is closed once the encoder's output has ended, which for this mechanism is the end
	// of a stdout nobody wanted: the artifacts are files.
	drained chan struct{}
	reader  sync.WaitGroup
}

func newSegmented(o opening) (mechanism, error) {
	// The renderer's own headers travel with a segmented delivery too. Discarding them
	// meant a family that only fetches what its transfer-mode header asks for was
	// served a playlist it had no reason to accept, on the one delivery where the fault
	// is invisible: the playlist 200s, every segment 200s, and the renderer simply never
	// asks for the second one.
	srv, err := hlsserve.New(hlsserve.Config{
		LocalIP:  o.p.LocalIP,
		Dir:      o.dir,
		Playlist: media.HLSPlaylistName,
		Headers:  o.headers,
	})
	if err != nil {
		return nil, fmt.Errorf("starting HLS server: %w", err)
	}

	m := &segmented{
		srv:      srv,
		made:     o.made,
		playlist: filepath.Join(o.dir, media.HLSPlaylistName),
		drained:  make(chan struct{}),
	}
	m.reader.Go(func() {
		// Drained to EOF whatever is on it: an unread output pipe stops ffmpeg dead once the
		// kernel buffer fills. Its end is the producer's end, which is what lets the server's
		// idle grace start running and what the artifact gate watches.
		defer close(m.drained)
		_, _ = io.Copy(io.Discard, o.out)
		srv.ProducerEnded()
	})
	return m, nil
}

func (m *segmented) URL() *url.URL                  { return m.srv.URL() }
func (m *segmented) Wait(ctx context.Context) error { return m.srv.Wait(ctx) }
func (m *segmented) Drained() <-chan struct{}       { return m.drained }
func (m *segmented) Settled() error                 { return unfetched(m.srv.Served(), m.made()) }

func (m *segmented) Artifact() Artifact {
	// No patience: the alternative is pointing a renderer at a playlist with nothing in it.
	return Artifact{Subject: "the HLS playlist", Landed: written(m.playlist)}
}

// InFlight is nil, and it is the same measurement as the missing share. This muxer rolls a
// window and deletes behind it (ffmpeg.HLSWindow), so media the encoder wrote a minute ago is
// gone from the playlist: the most this delivery can ever have in hand is that window, which is
// a fraction of the silence either in-flight rule waits out
// (TestARollingWindowKeepsNothingASupervisorCouldHoldACastOpenOver). A supervisor here would
// have to weigh a renderer's silence against a buffer that is never there, and would answer with
// a stall on every cast this mechanism serves, two and a half minutes in. What can be said is
// said at the end, where zero is zero however much was deleted (see Settled).
func (m *segmented) InFlight() *Delivery { return nil }

func (m *segmented) Close() error {
	err := m.srv.Close()
	m.reader.Wait()
	return err
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
// that answer it: the bytes the mechanism handed over (sent, the most any one connection got)
// and the bytes the encoder says it wrote (made, its last progress sample). Both are counts of
// the same kind, they are compared against each other, and there is nothing else in the
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
//     minutes that were castor's own doing.
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
// from the count of artifacts the mechanism handed over and the encoder's last progress sample.
// It is the completeness statement of a mechanism that deletes what it produced.
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
// place nothing watches: this mechanism cannot be judged in flight at all (see
// segmented.InFlight). The renderer accepts the playlist URL, never asks for a segment, the
// encoder runs the title to its end, the server's idle grace expires against a timestamp seeded
// when it was created, its Wait returns nil, and castor exited 0 having cast nothing.
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
// reports itself on. dir is where a muxer that writes files puts them, and where a
// relative output name resolves.
//
// The returned join blocks until the last sample has been delivered, and the teardown calls it
// after reaping the process. That ordering is what a progress consumer needs to be safe: the
// spool path's consumer writes a cue file into the work directory its caller removes as soon as
// Serve returns, so "the encoder has exited" is not on its own enough to say nobody is still
// writing.
func startEncoder(ctx context.Context, p OpenParams, dir string) (*ffmpeg.Process, func(), error) {
	args, err := ffmpeg.EncodeArgs(p.Opts)
	if err != nil {
		return nil, nil, fmt.Errorf("building encode args: %w", err)
	}
	startOpts := []ffmpeg.StartOption{
		ffmpeg.WithWorkDir(dir),
		ffmpeg.WithExtraPipes(ffmpeg.EncodeExtraPipes),
	}
	if p.Input != nil {
		startOpts = slices.Insert(startOpts, 0, ffmpeg.WithStdin(p.Input))
	}
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

// encoderOutput is the encode behind a delivery as a health rule reads it: whether its output
// has ended, and what it printed.
//
// Two facts and not four. It answers no telemetry at all, which is the point: those two methods
// used to exist and return constants (an empty progress sample, a nil error), and the
// reachability model credited them as facts this window supplied. The reasons the encoder has
// neither to offer are written once, where the port is declared (see watch.Telemetry).
type encoderOutput struct {
	proc  *ffmpeg.Process
	ended <-chan struct{}
}

func (o encoderOutput) Done() <-chan struct{} { return o.ended }
func (o encoderOutput) Evidence() []string    { return o.proc.Evidence().Lines }

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

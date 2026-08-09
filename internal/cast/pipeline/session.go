// Package pipeline is the cast executor: it runs exactly one decided attempt over the real
// machinery (read, gate, encode, serve) and is the only party in a cast that touches a
// process, a socket or a file. It is the production adapter behind attempt.Runner, so
// everything that varies between attempts arrives as data on one Attempt value and nothing
// here chooses which cast to make.
//
// What a cast is MADE of is data too. An ordered table of compositions carries the rule that
// selects each shape and how much its copy may refuse, and each row's wiring holds no decision
// of its own. Three rows cover every cast castor makes:
//
//   - read-once: a renderer that never fetches for itself, so one reader lands the program in
//     a local buffer an encoder tails, with an optional whisper burn-in. Chosen from the
//     family's static profile alone, which is what lets the read start before discovery has
//     finished;
//   - passthrough: the renderer fetches for itself and takes the source as it is, so it is
//     handed the URL and castor touches none of the media;
//   - remux: the renderer fetches for itself but cannot be handed this source, so one ffmpeg
//     reads the upstream and serves a container it takes.
//
// The one thing a device family shapes here is WHEN the renderer is connected, and that
// follows from which pass answered rather than from a branch or a column of its own: a cast
// whose shape is already fixed by the family's static profile reads first and connects
// alongside, so slow discovery does not age a short-lived signed URL, while a cast whose shape
// depends on what the renderer negotiates has to connect to be composed at all.
package pipeline

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/ffmpeg"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
)

// ConnectFunc discovers and connects the configured renderer. Production passes
// core.Connect; tests inject a fake so a cast can be driven without a live network.
type ConnectFunc func(context.Context, core.Config) (device.Device, error)

// Executor runs one decided attempt over the real machinery.
//
// Everything that varies between attempts arrives on the Attempt. Everything that does not
// (the binaries, the target renderer, the address a served stream is reachable at) is fixed
// here at construction, which is what leaves the loop above it with one method to drive and
// nothing to know about ffmpeg.
type Executor struct {
	cfg     core.Config
	connect ConnectFunc
	stage   StageFunc
	localIP string
}

// NewExecutor binds an executor to its host.
func NewExecutor(cfg core.Config, connect ConnectFunc, stage StageFunc, localIP string) *Executor {
	return &Executor{cfg: cfg, connect: connect, stage: stage, localIP: localIP}
}

// Compile-time proof that the resilience loop needs nothing of this package but this.
var _ attempt.Runner = (*Executor)(nil)

// Run casts one attempt to the configured renderer and reports what happened.
func (e *Executor) Run(ctx context.Context, a attempt.Attempt) attempt.Outcome {
	cfg := e.cfg
	// The attempt's delivery preference wins over the configured one. Configuration is only
	// where the first attempt's value came from, and a recovery is allowed to change it, so a
	// stage reading cfg.Delivery would be reading the answer to a question that has since
	// been asked again.
	cfg.Delivery = a.Delivery

	return run(ctx, cfg, configured{cfg: cfg, connect: e.connect}, e.stage, a, e.localIP).outcome(ctx)
}

// configured is the production Target: the renderer named in configuration, profiled from
// its family's registry entry before anything is discovered and connected through the
// prelude that owns discovery.
type configured struct {
	cfg     core.Config
	connect ConnectFunc
}

func (c configured) Profile() media.Renderer { return device.Profile(c.cfg.Device.Type) }

func (c configured) Acquire(ctx context.Context) (Renderer, error) {
	dev, err := c.connect(ctx, c.cfg)
	if err != nil {
		return nil, err
	}
	return dev, nil
}

// cast is one attempt's whole working material: what to try, where to run it, how much its
// composition's copy may refuse, and how to reach the renderer. A leg reads it and decides
// nothing from it.
type cast struct {
	cfg     core.Config
	attempt attempt.Attempt

	// policy is the composition's own column, carried here so the two served legs build their
	// encode from one function while still differing in the one way they differ.
	policy core.VideoPolicy

	localIP string
	workDir string

	// group owns every concurrent stage of this cast (the connect, a transcription). It is
	// cancelled and waited before the work directory is removed, so no goroutine is left
	// writing into a directory being deleted.
	group *errgroup.Group

	// renderer is the connected renderer, acquired at most once however many parties ask.
	renderer func(ctx context.Context) (Renderer, error)

	// stage builds the optional work this cast runs beside its read, in this cast's own work
	// directory. Only the composition that produces the picture asks for one.
	stage StageFunc
}

// run composes one cast, wires its lifecycle, and lets its composition run.
//
// Lifecycle: a cancellable context feeds an errgroup that owns the concurrent stages. Defer
// order is LIFO and load-bearing: cancel runs first (signalling every goroutine and killing
// every ffmpeg), g.Wait blocks until they have unwound, the renderer is closed (which needs
// the connect goroutine to have finished, so it cannot race one still acquiring), and only
// then is the work directory removed, so nothing is still writing a file into a directory
// being deleted.
func run(parent context.Context, cfg core.Config, t Target, stage StageFunc, a attempt.Attempt, localIP string) landing {
	workDir, err := os.MkdirTemp("", "castor-")
	if err != nil {
		return landing{err: fmt.Errorf("creating work directory: %w", err)}
	}
	defer func() { _ = os.RemoveAll(workDir) }() // registered first, runs last

	runCtx, cancel := context.WithCancel(parent)
	g, ctx := errgroup.WithContext(runCtx)
	renderer := newHeld(t.Acquire)
	defer renderer.close()
	defer func() { _ = g.Wait() }()
	defer cancel()

	row, shape, err := compose(ctx, compositions, t, renderer, core.Shape{
		Source:   a.Source,
		Delivery: cfg.Delivery,
		// Whatever was established about this attempt's picture, and both witnesses have to
		// arrive because the pass-through composition probes nothing ever: a height it is not
		// handed is a height it will never have (see media.Stream.Height). The declaration
		// leads where the source made one, since it describes the rung this cast will read
		// while a probe of a master reports whichever variant ffprobe opened.
		Height:    cmp.Or(a.Rendition.Height, a.Source.Height),
		MaxHeight: cfg.Resolver.MaxHeight,
	})
	if err != nil {
		return landing{err: err}
	}
	// When the renderer is acquired is a function of what the row's rule reads rather than a
	// column of its own: a row chosen from the family's static profile has connected nobody
	// yet, and a row chosen from what a renderer negotiated could only be chosen because
	// acquiring one is what answered it.
	concurrent := row.needs == profileOnly
	connected := "before the read"
	if concurrent {
		connected = "concurrently with the read"
	}
	slog.InfoContext(ctx, "cast composition",
		"composition", row.name,
		"why", row.why,
		"connect", connected,
		"shape", shape.String(),
	)

	if concurrent {
		// Nothing needs the renderer until the buffer is playable, and discovery plus connect
		// can take seconds the single-use source URL cannot spare, so it is acquired in the
		// group: a connect failure cancels the group with its own error as the cause, which is
		// what the wait for a playable buffer then reports rather than a bare cancellation.
		g.Go(func() error { _, err := renderer.get(ctx); return err })
	}

	return row.run(ctx, &cast{
		cfg:      cfg,
		attempt:  a,
		policy:   row.policy,
		localIP:  localIP,
		workDir:  workDir,
		group:    g,
		renderer: renderer.get,
		stage:    stage,
	})
}

// held is the cast's one renderer: acquired at most once however many parties ask for it, and
// closed exactly once even when the party that asked never got to use it (the wait for a
// playable buffer failed before the await).
type held struct {
	acquire func(context.Context) (Renderer, error)

	mu      sync.Mutex
	claimed bool
	dev     Renderer
	err     error
	ready   chan struct{}
}

func newHeld(acquire func(context.Context) (Renderer, error)) *held {
	return &held{acquire: acquire, ready: make(chan struct{})}
}

// get acquires the renderer, or waits for the acquisition already under way, or reports the
// one that already failed.
//
// A caller waiting on someone else's acquisition gives up when ctx ends, with
// context.Cause rather than a bare cancellation: on the concurrent path the acquisition runs
// in the errgroup, so the reason the group was cancelled is usually the connect's own failure
// and that is the answer worth reporting.
func (h *held) get(ctx context.Context) (Renderer, error) {
	if h.claim() {
		dev, err := h.acquire(ctx)
		h.mu.Lock()
		h.dev, h.err = dev, err
		h.mu.Unlock()
		close(h.ready)
		return dev, err
	}
	select {
	case <-h.ready:
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.dev, h.err
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

// claim answers true for exactly one caller, which is the one that does the connecting.
func (h *held) claim() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.claimed {
		return false
	}
	h.claimed = true
	return true
}

// close releases the renderer if one was ever acquired. Call it only after the goroutine that
// might be acquiring has been waited for.
func (h *held) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dev != nil {
		_ = h.dev.Close()
	}
}

// encodeInput is what a served leg measured and where its bytes come from: the two things the
// two served legs differ in beyond their row's policy.
type encodeInput struct {
	// facts is the measurement every copy decision in the resulting command line is made
	// from, of the subject this leg actually reads.
	facts core.Facts
	// source is the network input, zero on a leg fed over stdin.
	source ffmpeg.NetworkSource
	// pipe is the container fed over stdin, zero on a leg reading the network.
	pipe media.FormatInfo
	// burnIn is the live cue file to draw into every frame, empty for none.
	burnIn string
}

// encode is the one encode a served cast runs, built the same way on both served legs
// because what differs between them is stated rather than restated: what was measured (the
// upstream for a remux, the local buffer for a read-once), where the bytes arrive from, and
// how much the copy may refuse, which is the composition's own column.
//
// The two axes are decided independently and neither reads the other: audio can be copied,
// preserving 5.1, even where the video must be re-encoded, and the other way round. ctx is
// threaded because proving an encoder exists runs a real one-frame encode, and a wedged one
// must unwind with the cast rather than run detached.
func (c *cast) encode(ctx context.Context, caps media.Renderer, into media.FormatInfo, in encodeInput) ffmpeg.EncodeOptions {
	opts := ffmpeg.EncodeOptions{
		Format:     into,
		Source:     in.source,
		PipeFormat: in.pipe,
		Probe:      in.facts.Probe,
		Video: core.DecideVideo(ctx, core.VideoInputs{
			Caps:      caps,
			Probe:     in.facts.Probe,
			Into:      into,
			Policy:    c.policy,
			MaxHeight: c.cfg.Resolver.MaxHeight,
			// The attempt's own evidence, on both served legs at once: an axis a reader of this
			// cast already died copying is not handed to a second process to copy again. On the
			// buffered leg that second process reads the very packets the first produced.
			Decode:     c.attempt.Decode,
			GOPSeconds: keyframeSeconds,
			BurnIn:     in.burnIn,
			FFmpegPath: c.cfg.Transcode.FFmpegPath,
		}),
		Audio: core.DecideAudio(ctx, core.AudioInputs{
			Caps:   caps,
			Probe:  in.facts.Probe,
			Into:   into,
			Decode: c.attempt.Decode,
		}),
	}

	slog.InfoContext(ctx, "encode decision",
		"video_codec", opts.Video.Name(),
		"audio_codec", opts.Audio.Name(),
		"source_video_codec", string(in.facts.Probe.VideoCodec),
		"source_video_profile", in.facts.Probe.VideoProfile,
		"source_video_height", in.facts.Probe.VideoHeight,
		"source_audio_codec", string(in.facts.Probe.AudioCodec),
		"source_audio_channels", in.facts.Probe.AudioChannels,
		"measured", in.facts.Measured,
		"output_content_type", into.ContentType,
		"burn_in", in.burnIn != "",
		"decode", c.attempt.Decode.String(),
	)
	return opts
}

// serve hands one produced stream to the renderer, filling in what every delivery of this cast
// shares: the binary that produces it, the address it is reachable at, and the directory it is
// produced in. What differs (the encode, how it is fed, who follows it) is the caller's to state,
// and so is the supervisor, which is a parameter rather than a field of the params so that a leg
// states what watches its read instead of being able to omit one.
//
// It reports how far the cast got as well as what it ended with, and that phase is the whole
// input to decision 1: PhasePlaying once the renderer has accepted the URL, and otherwise
// before, which is what the caller states because only the caller knows what it had already
// established. Everything a delivery does after Play (the wait, the supervisor, the encoder's
// teardown) fails with a viewer watching, and a leg that reported those as reading is what let
// a reader's exit 1 forty minutes into a film be answered by casting it again from zero.
func (c *cast) serve(ctx context.Context, dev Renderer, before attempt.Phase, p core.OpenParams, supervise core.Supervisor) (attempt.Phase, error) {
	p.FFmpegPath = c.cfg.Transcode.FFmpegPath
	p.LocalIP = c.localIP
	p.WorkDir = c.workDir

	// Written by the delivery on this goroutine before Serve returns, and read after it, so
	// the ordering is established by the call itself.
	reached := before
	p.OnPlaying = func() { reached = attempt.PhasePlaying }

	err := core.Serve(ctx, dev, p, supervise)
	return reached, err
}

// keyframeSeconds caps the encoded GOP so a renderer joining mid-stream resyncs within a
// couple seconds, and so a segmented delivery can cut where it was asked to: the HLS muxer
// only ever starts a segment on a keyframe, so an encode left on libx264's 250-frame default
// answers "-hls_time 4" with roughly ten-second segments, and the window a paced encoder is
// bursting to fill is counted in segments it never gets.
//
// It bounds every re-encode castor produces, on both served legs. A stream copy is unaffected
// on either: the source's own keyframes are what they are, and a copy carries no GOP field to
// set.
const keyframeSeconds = 2

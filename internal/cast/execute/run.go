package execute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/engine/deliver"
	"github.com/stupside/castor/internal/cast/engine/ffmpeg"
	"github.com/stupside/castor/internal/cast/policy/compose"
	"github.com/stupside/castor/internal/device"
)

// Executor runs decided attempt over real machinery (varies: Attempt; fixed: binaries/renderer/address).
type Executor struct {
	cfg Config
}

func NewExecutor(cfg Config) *Executor { return &Executor{cfg: cfg} }

var _ attempt.Runner = (*Executor)(nil)

func (e *Executor) Run(ctx context.Context, a attempt.Attempt) attempt.Outcome {
	return run(ctx, e.cfg, a)
}

// cast is one attempt: working material and record of what each step established.
type cast struct {
	cfg     Config
	attempt attempt.Attempt

	row compose.Row

	localIP string
	workDir string

	ctx      context.Context
	cancel   context.CancelFunc
	group    *errgroup.Group               // Owns concurrent stages (cancelled before work dir removed).
	connect  func() (device.Device, error) // Renderer acquisition (memo, not lock).
	dev      device.Device
	stop     func() error
	burn     Burn // Nil if cast runs none (only buffering cast needs it).
	spool    *deliver.Spool
	reader   *pull
	opts     ffmpeg.EncodeOptions
	proc     *ffmpeg.Process
	sink     Sink
	tail     io.ReadCloser // Stdin feed for encoder (cast owns and closes).
	evidence attempt.Evidence
	ran      bool // Reports delivery ran to completion cleanly.
}

// run casts attempt end to end (teardown called here, not deferred; result is cast's own).
func run(parent context.Context, cfg Config, a attempt.Attempt) attempt.Outcome {
	c := &cast{cfg: cfg, attempt: a}
	c.stop = sync.OnceValue(c.teardown)

	err := errors.Join(c.play(parent), c.stop())
	if err == nil {
		c.evidence.Reached = attempt.PhaseDelivered
	}
	return c.outcome(parent, err)
}

func (c *cast) play(parent context.Context) error {
	ctx, err := c.open(parent)
	if err != nil {
		return err
	}
	if err := c.compose(ctx); err != nil {
		return err
	}
	for _, step := range sequences[c.row.Kind] {
		if err := step(c, ctx); err != nil {
			return err
		}
	}
	return nil
}

type step func(*cast, context.Context) error

// served is every step after the renderer is connected for a cast castor produces and serves.
var served = []step{(*cast).encode, (*cast).produce, (*cast).opened, (*cast).hand, (*cast).supervising, (*cast).settled}

// sequences is the steps each composition runs, in order.
var sequences = map[compose.Kind][]step{
	compose.Handoff:  {(*cast).connected, (*cast).hand},
	compose.Remux:    slices.Concat([]step{(*cast).workspace, (*cast).connected}, served),
	compose.ReadOnce: slices.Concat([]step{(*cast).workspace, (*cast).read, (*cast).transcribe, (*cast).playable, (*cast).connected}, served),
}

func (c *cast) open(parent context.Context) (context.Context, error) {
	if err := c.attempt.Program.Validate(); err != nil {
		return nil, fmt.Errorf("attempt has no valid media program: %w", err)
	}
	runCtx, cancel := context.WithCancel(parent)
	g, ctx := errgroup.WithContext(runCtx)
	c.ctx, c.cancel, c.group = ctx, cancel, g
	c.connect = sync.OnceValues(func() (device.Device, error) {
		dev, err := c.cfg.Renderer.Connect(ctx)
		c.dev = dev
		return dev, err
	})
	return ctx, nil
}

func (c *cast) teardown() error {
	// Killed first, because everything after this needs the encoder to have stopped writing.
	if c.proc != nil {
		c.proc.Kill()
	}
	if c.tail != nil {
		_ = c.tail.Close()
	}
	if c.sink != nil {
		_ = c.sink.Close()
	}
	var encoded error
	if c.proc != nil {
		encoded = encoderResult(c.ctx, c.proc, c.proc.Wait())
	}
	if c.cancel != nil {
		c.cancel()
	}
	if c.group != nil {
		_ = c.group.Wait()
	}
	if c.dev != nil {
		_ = c.dev.Close()
	}
	if c.workDir != "" {
		_ = os.RemoveAll(c.workDir)
	}
	return encoded
}

func (c *cast) compose(ctx context.Context) error {
	shape := compose.Shape{
		Program:   c.attempt.Program,
		Delivery:  c.attempt.Delivery,
		Height:    c.attempt.SelfFetchHeight(),
		MaxHeight: c.cfg.MaxHeight,
	}

	shape.Renderer = c.cfg.Renderer.Profile()
	row, fixed := compose.Compose(shape, compose.ProfileOnly)
	if !fixed {
		dev, err := c.connect()
		if err != nil {
			return err
		}
		negotiated := dev.Capabilities()
		// The family states whether it fetches for itself once, in its profile.
		negotiated.SelfFetch = shape.Renderer.SelfFetch
		shape.Renderer = negotiated
		row, _ = compose.Compose(shape, compose.Negotiated)
	}
	c.row = row

	connected := "before the read"
	if fixed {
		connected = "concurrently with the read"
	}
	slog.InfoContext(ctx, "cast composition",
		"composition", row.Name,
		"why", row.Why,
		"connect", connected,
		"shape", shape.String(),
	)

	if fixed {
		c.group.Go(func() error { _, err := c.connect(); return err })
	}
	return nil
}

func (c *cast) workspace(ctx context.Context) error {
	localIP, err := c.cfg.Addresses.LocalIPv4(ctx)
	if err != nil {
		return fmt.Errorf("resolving local relay address: %w", err)
	}
	workDir, err := os.MkdirTemp("", "castor-")
	if err != nil {
		return fmt.Errorf("creating work directory: %w", err)
	}
	c.localIP, c.workDir = localIP, workDir
	return nil
}

func (c *cast) connected(context.Context) error {
	_, err := c.connect()
	return err
}

func (c *cast) hand(ctx context.Context) error {
	// Present because open validated this program before any step of the sequence ran.
	primary, _ := c.attempt.Program.PrimaryInput()
	target, contentType := primary.URL, primary.ContentType
	if c.row.Kind.Produces() {
		target, contentType = c.sink.URL(), c.opts.Format.ContentType
	}

	slog.InfoContext(ctx, "starting playback", "url", target.String(), "content_type", contentType)
	if err := c.dev.Play(ctx, target, contentType); err != nil {
		c.evidence.PlayErr = err
		return fmt.Errorf("starting playback: %w", err)
	}
	c.evidence.Reached = attempt.PhasePlaying
	if c.row.Kind.Produces() {
		slog.InfoContext(ctx, "streaming to device, press Ctrl+C to stop")
		return nil
	}
	slog.InfoContext(ctx, "playback handed off to device")
	return nil
}

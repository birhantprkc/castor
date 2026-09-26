// Package execute runs one attempt of a cast over real processes, servers and devices.
package execute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/compose"
	"github.com/stupside/castor/internal/cast/deliver"
	"github.com/stupside/castor/internal/cast/transcode"
	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/ffmpeg"
	"github.com/stupside/castor/internal/media"
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

	// program is what this cast reads: the attempt's, with each followed input pointed at castor's timeline.
	program  media.Program
	unfollow func() error

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
	opts     transcode.EncodeOptions
	proc     *ffmpeg.Process
	sink     sink
	tail     io.ReadCloser // Stdin feed for encoder (cast owns and closes).
	evidence attempt.Evidence
	output   watch.Producer // What writes the served bytes: the encoder, or the read standing in for it.
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
	switch c.row.Kind {
	case compose.Handoff:
		return c.handoff(ctx)
	case compose.Remux:
		return c.remux(ctx)
	case compose.ReadOnce:
		return c.readOnce(ctx)
	default:
		return fmt.Errorf("composition %q has no execution for kind %d", c.row.Name, c.row.Kind)
	}
}

func (c *cast) handoff(ctx context.Context) error {
	if _, err := c.connect(); err != nil {
		return err
	}
	// Present because open validated this program before the composition ran.
	primary, _ := c.attempt.Program.PrimaryInput()
	if err := c.hand(ctx, primary.URL, primary.ContentType); err != nil {
		return err
	}
	slog.InfoContext(ctx, "playback handed off to device")
	return nil
}

func (c *cast) remux(ctx context.Context) error {
	if err := c.workspace(ctx); err != nil {
		return err
	}
	if err := c.follow(ctx); err != nil {
		return err
	}
	if _, err := c.connect(); err != nil {
		return err
	}
	return c.serve(ctx, c.fromSource())
}

func (c *cast) readOnce(ctx context.Context) error {
	if err := c.workspace(ctx); err != nil {
		return err
	}
	if err := c.follow(ctx); err != nil {
		return err
	}
	if err := c.read(ctx); err != nil {
		return err
	}
	if err := c.transcribe(ctx); err != nil {
		return err
	}
	if err := c.playable(ctx); err != nil {
		return err
	}
	if _, err := c.connect(); err != nil {
		return err
	}
	return c.serve(ctx, c.fromBuffer())
}

func (c *cast) open(parent context.Context) (context.Context, error) {
	if err := c.attempt.Program.Validate(); err != nil {
		return nil, fmt.Errorf("attempt has no valid media program: %w", err)
	}
	c.program = c.attempt.Program
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
	switch {
	case c.proc != nil:
		encoded = encoderResult(c.ctx, c.proc, c.proc.Wait())
	case c.output == watch.Producer(c.reader) && c.ctx.Err() == nil:
		// The read stands in for the encoder, so a read that failed fails the cast as that encoder would have.
		encoded = c.reader.Err()
	}
	if c.cancel != nil {
		c.cancel()
	}
	if c.group != nil {
		_ = c.group.Wait()
	}
	// Only once every reader is gone: a timeline closed under one would read as the source failing.
	if c.unfollow != nil {
		_ = c.unfollow()
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

// follow points the inputs whose timelines castor keeps at castor's republished playlists.
func (c *cast) follow(ctx context.Context) error {
	program, unfollow, err := c.cfg.Timelines.Republish(ctx, c.attempt.Program)
	if err != nil {
		return fmt.Errorf("following the source's timeline: %w", err)
	}
	c.program, c.unfollow = program, unfollow
	return nil
}

func (c *cast) hand(ctx context.Context, target *url.URL, contentType string) error {
	slog.InfoContext(ctx, "starting playback", "url", target.String(), "content_type", contentType)
	if err := c.dev.Play(ctx, target, contentType); err != nil {
		c.evidence.PlayErr = err
		return fmt.Errorf("starting playback: %w", err)
	}
	c.evidence.Reached = attempt.PhasePlaying
	return nil
}

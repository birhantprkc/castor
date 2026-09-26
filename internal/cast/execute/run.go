// Package execute runs one attempt of a cast over real processes, servers and devices.
package execute

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/compose"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// Executor runs decided attempt over real machinery (varies: Attempt; fixed: binaries/renderer/address).
type Executor struct {
	cfg Config
}

func NewExecutor(cfg Config) *Executor { return &Executor{cfg: cfg} }

var _ attempt.Runner = (*Executor)(nil)

// Run casts a end to end; everything it acquired is released before its outcome is read.
func (e *Executor) Run(parent context.Context, a attempt.Attempt) attempt.Outcome {
	if err := a.Program.Validate(); err != nil {
		err = fmt.Errorf("attempt has no valid media program: %w", err)
		return attempt.Outcome{Err: err, Evidence: evidence(ran{}, err, parent.Err() != nil)}
	}
	s := open(parent, e.cfg, a)
	r, err := s.play()
	err = errors.Join(err, s.releases.release())
	return attempt.Outcome{Err: err, Evidence: evidence(r, err, parent.Err() != nil)}
}

// session is what one attempt runs on and never changes once opened; every step returns what it established.
type session struct {
	cfg     Config
	attempt attempt.Attempt

	ctx   context.Context
	group *errgroup.Group // Owns the steps that run beside the pipeline (the concurrent connect).
	// device acquires the renderer once, whichever step asks first.
	device func() (device.Device, error)
	// releases undoes everything the attempt acquired, once it ends.
	releases *releases
}

// ran is how far an attempt's pipeline got, and the read it started on the way (nil where it read nothing).
type ran struct {
	reached attempt.Phase
	reader  *pull
}

func open(parent context.Context, cfg Config, a attempt.Attempt) *session {
	runCtx, cancel := context.WithCancel(parent)
	g, ctx := errgroup.WithContext(runCtx)
	rel := &releases{}
	// Released last: whatever still runs beside the pipeline stops before the attempt is over.
	rel.push(func() error {
		cancel()
		_ = g.Wait()
		return nil
	})
	connect := sync.OnceValues(func() (device.Device, error) {
		dev, err := cfg.Renderer.Connect(ctx)
		if dev != nil {
			rel.push(func() error { _ = dev.Close(); return nil })
		}
		return dev, err
	})
	return &session{cfg: cfg, attempt: a, ctx: ctx, group: g, device: connect, releases: rel}
}

func (s *session) play() (ran, error) {
	row, err := s.compose()
	if err != nil {
		return ran{}, err
	}
	switch row.Kind {
	case compose.Handoff:
		return s.handoff()
	case compose.Remux:
		return s.remux()
	case compose.ReadOnce:
		return s.readOnce()
	default:
		return ran{}, fmt.Errorf("composition %q has no execution for kind %d", row.Name, row.Kind)
	}
}

func (s *session) handoff() (ran, error) {
	dev, err := s.device()
	if err != nil {
		return ran{}, err
	}
	// Present because run validated this program before the composition ran.
	primary, _ := s.attempt.Program.PrimaryInput()
	if err := hand(s.ctx, dev, primary.URL, primary.ContentType, true); err != nil {
		return ran{}, err
	}
	slog.InfoContext(s.ctx, "playback handed off to device")
	return ran{reached: attempt.PhasePlaying}, nil
}

func (s *session) remux() (ran, error) {
	ws, err := s.workspace()
	if err != nil {
		return ran{}, err
	}
	program, err := s.follow(s.attempt.Program)
	if err != nil {
		return ran{}, err
	}
	dev, err := s.device()
	if err != nil {
		return ran{}, err
	}
	reached, err := s.serve(dev, ws, feed{program: program}, nil)
	return ran{reached: reached}, err
}

func (s *session) readOnce() (ran, error) {
	ws, err := s.workspace()
	if err != nil {
		return ran{}, err
	}
	program, err := s.follow(s.attempt.Program)
	if err != nil {
		return ran{}, err
	}
	buf, err := s.read(ws, program)
	if err != nil {
		return ran{}, err
	}
	reading := ran{reached: attempt.PhaseReading, reader: buf.reader}
	if err := gate(s.ctx, buf); err != nil {
		return reading, err
	}
	dev, err := s.device()
	if err != nil {
		return reading, err
	}
	reached, err := s.serve(dev, ws, feed{buffered: buf}, buf.burn)
	return ran{reached: max(reading.reached, reached), reader: buf.reader}, err
}

func (s *session) compose() (compose.Row, error) {
	shape := compose.Shape{
		Program:   s.attempt.Program,
		Delivery:  s.attempt.Delivery,
		Height:    source.SelfFetchHeight(s.attempt.Program, s.attempt.Origin, s.attempt.Rendition),
		MaxHeight: s.cfg.MaxHeight,
	}

	shape.Renderer = s.cfg.Renderer.Profile()
	row, fixed := compose.Compose(shape, compose.ProfileOnly)
	if !fixed {
		dev, err := s.device()
		if err != nil {
			return compose.Row{}, err
		}
		negotiated := dev.Capabilities()
		// The family states whether it fetches for itself once, in its profile.
		negotiated.SelfFetch = shape.Renderer.SelfFetch
		shape.Renderer = negotiated
		row, _ = compose.Compose(shape, compose.Negotiated)
	}

	connected := "before the read"
	if fixed {
		connected = "concurrently with the read"
	}
	slog.InfoContext(s.ctx, "cast composition",
		"composition", row.Name,
		"why", row.Why,
		"connect", connected,
		"shape", shape.String(),
	)

	if fixed {
		s.group.Go(func() error { _, err := s.device(); return err })
	}
	return row, nil
}

// workspace is where an attempt keeps its files, and the address its renderer reaches it at.
type workspace struct {
	localIP string
	dir     string
}

func (s *session) workspace() (workspace, error) {
	localIP, err := s.cfg.Addresses.LocalIPv4(s.ctx)
	if err != nil {
		return workspace{}, fmt.Errorf("resolving local relay address: %w", err)
	}
	dir, err := os.MkdirTemp("", "castor-")
	if err != nil {
		return workspace{}, fmt.Errorf("creating work directory: %w", err)
	}
	s.releases.push(func() error { _ = os.RemoveAll(dir); return nil })
	return workspace{localIP: localIP, dir: dir}, nil
}

// follow points the inputs whose timelines castor keeps at castor's republished playlists.
func (s *session) follow(program media.Program) (media.Program, error) {
	followed, unfollow, err := s.cfg.Timelines.Republish(s.ctx, program)
	if err != nil {
		return media.Program{}, &timelineUnreadable{err: err}
	}
	s.releases.push(func() error { _ = unfollow(); return nil })
	return followed, nil
}

// timelineUnreadable is a source whose timeline castor must keep and could not read.
type timelineUnreadable struct{ err error }

func (e *timelineUnreadable) Error() string {
	return "following the source's timeline: " + e.err.Error()
}
func (e *timelineUnreadable) Unwrap() error { return e.err }

// hand points the renderer at target; source says whether that is the source itself rather than what castor serves.
func hand(ctx context.Context, dev device.Device, target *url.URL, contentType string, source bool) error {
	slog.InfoContext(ctx, "starting playback", "url", target.String(), "content_type", contentType)
	if err := dev.Play(ctx, target, contentType); err != nil {
		return &playRefused{err: err, source: source}
	}
	return nil
}

// playRefused is the renderer's own refusal of the URL it was handed.
type playRefused struct {
	err    error
	source bool
}

func (e *playRefused) Error() string { return "starting playback: " + e.err.Error() }
func (e *playRefused) Unwrap() error { return e.err }

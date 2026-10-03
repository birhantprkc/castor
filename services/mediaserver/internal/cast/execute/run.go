// Package execute runs one attempt of a cast over real processes, servers and devices.
package execute

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"

	"github.com/stupside/castor/services/mediaserver/internal/cast/attempt"
	"github.com/stupside/castor/services/mediaserver/internal/cast/compose"
	"github.com/stupside/castor/services/mediaserver/internal/cast/health"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// Executor runs each attempt of one cast over real machinery.
type Executor struct {
	cfg config
}

func NewExecutor(m Machinery, c Cast) *Executor { return &Executor{cfg: config{Machinery: m, Cast: c}} }

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

// pipeline is what one attempt runs on and never changes once opened; every step returns what it established.
type pipeline struct {
	cfg     config
	attempt attempt.Attempt

	ctx context.Context
	// releases undoes everything the attempt acquired, once it ends.
	releases *releases
}

// ran is how far an attempt's pipeline got, and the read it started on the way (nil where it read nothing).
type ran struct {
	reached health.Phase
	reader  *pull
}

func open(parent context.Context, cfg config, a attempt.Attempt) *pipeline {
	ctx, cancel := context.WithCancel(parent)
	rel := &releases{}
	// Released last: whatever the attempt started stops before it is over.
	rel.push(func() error { cancel(); return nil })
	return &pipeline{cfg: cfg, attempt: a, ctx: ctx, releases: rel}
}

func (s *pipeline) play() (ran, error) {
	row := s.compose()
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

func (s *pipeline) handoff() (ran, error) {
	// Present because run validated this program before the composition ran.
	primary, _ := s.attempt.Program.PrimaryInput()
	if err := hand(s.ctx, s.cfg.Device, primary.URL, primary.ContentType, true); err != nil {
		return ran{}, err
	}
	slog.InfoContext(s.ctx, "playback handed off to the device")
	return ran{reached: health.Playing}, nil
}

func (s *pipeline) remux() (ran, error) {
	work, err := s.workdir()
	if err != nil {
		return ran{}, err
	}
	program, err := s.follow(s.attempt.Program)
	if err != nil {
		return ran{}, err
	}
	reached, err := s.serve(work, feed{program: program}, nil)
	return ran{reached: reached}, err
}

func (s *pipeline) readOnce() (ran, error) {
	work, err := s.workdir()
	if err != nil {
		return ran{}, err
	}
	program, err := s.follow(s.attempt.Program)
	if err != nil {
		return ran{}, err
	}
	buf, err := s.read(work, program)
	if err != nil {
		return ran{}, err
	}
	reading := ran{reached: health.Reading, reader: buf.reader}
	if err := gate(s.ctx, buf); err != nil {
		return reading, err
	}
	reached, err := s.serve(work, feed{buffered: buf}, buf.burn)
	return ran{reached: max(reading.reached, reached), reader: buf.reader}, err
}

func (s *pipeline) compose() compose.Row {
	shape := compose.Shape{
		Device:    s.cfg.Device.Capabilities(),
		Program:   s.attempt.Program,
		Delivery:  s.attempt.Delivery,
		Height:    s.attempt.SelfFetchHeight(),
		MaxHeight: s.cfg.MaxHeight,
	}
	row := compose.Compose(shape)
	slog.InfoContext(s.ctx, "cast composition", "composition", row.Name, "why", row.Why, "shape", shape.String())
	return row
}

// workdir is where an attempt keeps its files, removed when it is released.
func (s *pipeline) workdir() (string, error) {
	dir, err := os.MkdirTemp("", "castor-")
	if err != nil {
		return "", fmt.Errorf("creating work directory: %w", err)
	}
	s.releases.push(func() error { _ = os.RemoveAll(dir); return nil })
	return dir, nil
}

// follow points the inputs whose timelines castor keeps at castor's republished playlists.
func (s *pipeline) follow(program media.Program) (media.Program, error) {
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

// hand points the device at target; source says whether that is the source itself rather than what castor serves.
func hand(ctx context.Context, dev Device, target *url.URL, contentType string, source bool) error {
	slog.InfoContext(ctx, "starting playback", "url", target.String(), "content_type", contentType)
	if err := dev.Play(ctx, target, contentType); err != nil {
		return &playRefused{err: err, source: source}
	}
	return nil
}

// playRefused is the device's own refusal of the URL it was handed.
type playRefused struct {
	err    error
	source bool
}

func (e *playRefused) Error() string { return "starting playback: " + e.err.Error() }
func (e *playRefused) Unwrap() error { return e.err }

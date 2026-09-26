// Package watch judges whether a cast is working: the rules, and the loop that applies them.
package watch

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/stupside/castor/internal/media"
)

// Monitor is one subject under watch (a value; each watch owns derived state independently).
type Monitor struct {
	// Subject is what's being waited on (e.g. "playback gate", "HLS playlist").
	Subject string

	// Window is which side of the playback gate (determines what verdicts are allowed).
	Window Window

	// Producer is the source read or encoder (nil ports mean unmeasured, not "no").
	Producer Producer

	// Telemetry is the producer's pace (nil where rate is not judged).
	Telemetry Telemetry

	// Audience is the renderer side (nil until renderer holds URL).
	Audience Audience

	// Lead is the transcription frontier (nil if no burn-in).
	Lead Lead

	// Landed is supplied (not taken from producer) so gate opens on artifact itself.
	Landed func() int64

	// Headroom is the pace the read was allowed (zero if not a source read).
	Headroom float64

	// Grace is how long artifact may take; zero never proceeds.
	Grace time.Duration
}

// Watch polls one subject until a rule ends the wait: nil = proceed, *Fault = cast ends.
func Watch(ctx context.Context, m Monitor) error {
	t := &tracker{m: m, start: time.Now(), grew: time.Now()}
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	for {
		h := t.read()
		r, act, err := judge(m.Window, h)
		if err != nil {
			return err
		}
		switch act {
		case open:
			slog.InfoContext(ctx, "watch cleared",
				"gate", m.Subject,
				"rule", r.Name,
				"waited", time.Since(t.start).Round(time.Millisecond),
				"landed_bytes", h.Landed,
				"transcribed_lead_seconds", int(h.Lead),
			)
			return nil
		case revise, abandon:
			return t.fault(ctx, r, act, h)
		default:
			t.report(ctx, r, h)
		}

		select {
		case <-ctx.Done():
			// context.Cause surfaces real reason (not bare "context canceled").
			return context.Cause(ctx)
		case <-tick.C:
		}
	}
}

// Fault is a verdict that ended a watch (value not string; attempt loop keys recovery on Kind).
type Fault struct {
	// Kind is the verdict; Why is the row's reasoning.
	Kind Kind
	Why  string

	// Revise: reached before the renderer held the URL, so the attempt may change.
	Revise bool

	// Subject is what was watched (gate, output, or cast).
	Subject string

	// Health is the measurements the verdict was reached on.
	Health Health

	// Err is the producer's terminal error (so errors.Is still finds cancellation).
	Err error

	// Evidence is what the producer printed (retained even if castor killed it).
	Evidence []string
}

func (f *Fault) Error() string {
	msg := fmt.Sprintf("%s: %s (%s)", f.Subject, f.Why, f.Health)
	if f.Err != nil {
		return msg + ": " + f.Err.Error()
	}
	return msg
}

func (f *Fault) Unwrap() error { return f.Err }

// tracker is one Watch's derived state (how long something has been true).
type tracker struct {
	m     Monitor
	start time.Time

	// landed and grew: artifact size, and when the producer last grew.
	landed int64
	grew   time.Time

	// trail is the media position over the last paceSpan, oldest first.
	trail []mark

	// sample is last progress; samples counts stated speeds (not blocks/N/A noise).
	sample  media.Progress
	samples int
	fresh   bool

	// deficitFrom: when read fell under playback rate (zero if not yet).
	deficitFrom time.Time

	// reported: when last cadence line was emitted.
	reported time.Time
}

// read takes one reading of every port and folds it into judge-able Health.
func (t *tracker) read() Health {
	h := Health{Headroom: t.m.Headroom, Subtitles: t.m.Lead != nil}

	if t.m.Landed != nil {
		h.Landed = t.m.Landed()
	}

	if l := t.m.Lead; l != nil {
		h.Lead, h.LeadDone = l.LatestEnd(), l.Done()
	}

	if tm := t.m.Telemetry; tm != nil {
		sample := tm.Progress()
		t.fresh = sample != t.sample
		if t.fresh && sample.Speed > 0 {
			t.samples++
		}
		t.sample = sample
		h.Position, h.Speed, h.Samples = sample.Position, sample.Speed, t.samples
	}

	// Growth is media at a pace: a muxer pads bytes with no media behind them, and a trickle never goes silent.
	grew := h.Landed != t.landed
	if t.m.Telemetry != nil {
		grew = t.keepsPace(time.Now(), h.Position)
	}
	if grew {
		t.landed, t.grew = h.Landed, time.Now()
	}
	h.SinceGrowth = time.Since(t.grew)

	if p := t.m.Producer; p != nil {
		select {
		case <-p.Done():
			h.Ended = true
			// Asked only of ended producer (terminal error published with Done, not before).
			if tm := t.m.Telemetry; tm != nil {
				h.Failed = tm.Err() != nil
			}
		default:
		}
	}

	if a := t.m.Audience; a != nil {
		handed, last := a.Handed()
		h.Handed = handed
		// Renderer fetch measured from watch start (zero time = "eternity ago" before watch opened).
		h.SinceFetch = time.Since(cmp.Or(last, t.start))
		// Watch is the clock (starts at Play return); buffers before first frame understates media on hand.
		h.Delivered, h.SincePlay = a.Buffered(), time.Since(t.start)
	}

	if t.m.Grace > 0 {
		h.Overdue = time.Since(t.start) > t.m.Grace
	}

	if h.starving() {
		if t.deficitFrom.IsZero() {
			t.deficitFrom = time.Now()
		}
		h.SinceDeficit = time.Since(t.deficitFrom)
	} else {
		t.deficitFrom = time.Time{}
	}

	return h
}

// mark is the media position read at one instant.
type mark struct {
	at       time.Time
	position time.Duration
}

// keepsPace records position and reports whether it moved minPace of paceSpan since the last mark paceSpan old.
func (t *tracker) keepsPace(now time.Time, position time.Duration) bool {
	t.trail = append(t.trail, mark{at: now, position: position})
	for len(t.trail) > 1 && now.Sub(t.trail[1].at) >= paceSpan {
		t.trail = t.trail[1:]
	}
	return position-t.trail[0].position >= time.Duration(minPace*float64(paceSpan))
}

// fault ends the watch with producer's own words (stderr dumped only while NOT ended).
func (t *tracker) fault(ctx context.Context, r rule, act action, h Health) error {
	f := &Fault{
		Kind:    r.Kind,
		Why:     r.Why,
		Revise:  act == revise,
		Subject: t.m.Subject,
		Health:  h,
	}
	if r.Blames == theProducer && t.m.Producer != nil {
		if tm := t.m.Telemetry; tm != nil {
			f.Err = tm.Err()
		}
		f.Evidence = t.m.Producer.Evidence()
		if !h.Ended {
			if len(f.Evidence) > 0 {
				for _, line := range f.Evidence {
					slog.WarnContext(ctx, "producer stderr", "gate", t.m.Subject, "line", line)
				}
			} else {
				slog.WarnContext(ctx, "the producer emitted no stderr: the connection was accepted and neither bytes nor warnings arrived (silent upstream)", "gate", t.m.Subject)
			}
		}
	}
	slog.WarnContext(ctx, "cast abandoned",
		"gate", t.m.Subject,
		"window", t.m.Window.String(),
		"verdict", r.Kind.String(),
		"rule", r.Name,
		"revisable", f.Revise,
		"health", h.String(),
	)
	return f
}

// report says what watch is waiting on; deficit case is deliberately louder (nameROW not just verdict).
func (t *tracker) report(ctx context.Context, r rule, h Health) {
	deficit := t.fresh && h.Headroom > 1 && h.Speed > 0 && h.Speed < playbackRate
	if !deficit && time.Since(t.reported) < reportInterval {
		return
	}
	t.reported = time.Now()
	slog.InfoContext(ctx, "watch",
		"gate", t.m.Subject,
		"window", t.m.Window.String(),
		"verdict", r.Kind.String(),
		"rule", r.Name,
		"landed_bytes", h.Landed,
		"media_position", h.Position.Round(time.Second),
		"speed", float64(h.Speed),
		"readrate", h.Headroom,
		"speed_samples", h.Samples,
		"under_playback_rate_for", h.SinceDeficit.Round(time.Second),
		"transcribed_lead_seconds", int(h.Lead),
		"need_lead_seconds", transcriptionLeadSeconds,
	)
}

// Package health judges whether a cast is working: the rules, and the loop that applies them.
package health

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// Watch polls one subject until a rule ends the wait: nil to proceed, a *Fault to end.
func Watch(ctx context.Context, m Monitor) error {
	t := &tracker{m: m, start: time.Now(), grew: time.Now()}
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	for {
		h := t.read()
		r, act, err := judge(m.Phase, h)
		if err != nil {
			return err
		}
		switch act {
		case open:
			slog.InfoContext(ctx, "watch cleared",
				"gate", m.Subject,
				"rule", r.name,
				"waited", time.Since(t.start).Round(time.Millisecond),
				"landed_bytes", h.Landed,
				"transcribed_lead_seconds", int(h.lead),
			)
			return nil
		case revise, abandon:
			return t.fault(ctx, r, act, h)
		default:
			t.report(ctx, r, h)
		}

		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-tick.C:
		}
	}
}

// Fault is a verdict that ended a watch; recovery keys on its Kind.
type Fault struct {
	Kind Kind
	// why is the reasoning of the row that reached it.
	why string

	// Revise is a verdict reached before the device held the URL, so the attempt may change.
	Revise bool

	// Subject is what was watched (gate, output, or cast).
	Subject string

	// Vitals is the measurements the verdict was reached on.
	Vitals Vitals

	// Err is the producer's terminal error (so errors.Is still finds cancellation).
	Err error

	// Evidence is what the producer printed (retained even if castor killed it).
	Evidence []string
}

func (f *Fault) Error() string {
	msg := fmt.Sprintf("%s: %s (%s)", f.Subject, f.why, f.Vitals)
	if f.Err != nil {
		return msg + ": " + f.Err.Error()
	}
	return msg
}

func (f *Fault) Unwrap() error { return f.Err }

// tracker is one Watch's derived state: how long each thing has been true.
type tracker struct {
	m     Monitor
	start time.Time

	// landed is the artifact's size, and grew when the producer last grew.
	landed int64
	grew   time.Time

	// trail is the media position over the last paceSpan, oldest first.
	trail []mark

	// samples counts the speeds the producer stated, not the N/A it prints before.
	sample  media.Progress
	samples int
	fresh   bool

	// deficitFrom is when the read fell under playback rate.
	deficitFrom time.Time

	reported time.Time
}

// read takes one reading of every port.
func (t *tracker) read() Vitals {
	h := Vitals{Headroom: t.m.Headroom, subtitles: t.m.Lead != nil}

	if t.m.Landed != nil {
		h.Landed = t.m.Landed()
	}

	if l := t.m.Lead; l != nil {
		h.lead, h.leadDone = l.LatestEnd(), l.Done()
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
	h.sinceGrowth = time.Since(t.grew)

	if p := t.m.Producer; p != nil {
		select {
		case <-p.Done():
			h.ended = true
			// A terminal error is published with Done, never before.
			if tm := t.m.Telemetry; tm != nil {
				h.failed = tm.Err() != nil
			}
		default:
		}
	}

	if a := t.m.Audience; a != nil {
		handed, last := a.Handed()
		h.handed = handed
		// A device that fetched nothing yet is measured from the watch's start, not from the epoch.
		h.sinceFetch = time.Since(cmp.Or(last, t.start))
		// The watch starts as Play returns, which understates the media on hand.
		h.delivered, h.sincePlay = a.Buffered(), time.Since(t.start)
	}

	if t.m.Grace > 0 {
		h.overdue = time.Since(t.start) > t.m.Grace
	}

	if h.starving() {
		if t.deficitFrom.IsZero() {
			t.deficitFrom = time.Now()
		}
		h.sinceDeficit = time.Since(t.deficitFrom)
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

// fault ends the watch in the producer's own words, logged only while it still runs.
func (t *tracker) fault(ctx context.Context, r rule, act action, h Vitals) error {
	f := &Fault{
		Kind:    r.kind,
		why:     r.why,
		Revise:  act == revise,
		Subject: t.m.Subject,
		Vitals:  h,
	}
	if r.blames == theProducer && t.m.Producer != nil {
		if tm := t.m.Telemetry; tm != nil {
			f.Err = tm.Err()
		}
		f.Evidence = t.m.Producer.Evidence()
		if !h.ended {
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
		"phase", t.m.Phase.String(),
		"verdict", r.kind.String(),
		"rule", r.name,
		"revisable", f.Revise,
		"health", h.String(),
	)
	return f
}

// report says what the watch is waiting on, at once for a fresh deficit.
func (t *tracker) report(ctx context.Context, r rule, h Vitals) {
	deficit := t.fresh && h.Headroom > 1 && h.Speed > 0 && h.Speed < playbackRate
	if !deficit && time.Since(t.reported) < reportInterval {
		return
	}
	t.reported = time.Now()
	slog.InfoContext(ctx, "watch",
		"gate", t.m.Subject,
		"phase", t.m.Phase.String(),
		"verdict", r.kind.String(),
		"rule", r.name,
		"landed_bytes", h.Landed,
		"media_position", h.Position.Round(time.Second),
		"speed", float64(h.Speed),
		"readrate", h.Headroom,
		"speed_samples", h.Samples,
		"under_playback_rate_for", h.sinceDeficit.Round(time.Second),
		"transcribed_lead_seconds", int(h.lead),
		"need_lead_seconds", transcriptionLead.Seconds(),
	)
}

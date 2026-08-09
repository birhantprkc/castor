package watch

import (
	"cmp"
	"context"
	"log/slog"
	"time"

	"github.com/stupside/castor/internal/media"
)

// Monitor is one subject under watch: the ports the facts are read through, and the two
// terms the reading depends on. It is a value, so a Watch owns its own derived state and
// two windows over the same reader cannot corrupt each other's history.
//
// Every port is optional and absent means unmeasured, never "no": a nil Lead is a cast
// that burns no subtitles, a nil Consumer is a delivery no renderer has been pointed at
// yet, and the rules that read those facts are confined to the windows where the ports
// exist (see Rule.Windows).
type Monitor struct {
	// Subject names what is being waited on, in log lines and in the fault. It is the
	// difference between "playback gate" and "the HLS playlist" in a message a user reads.
	Subject string

	// Window is which side of the playback gate this watch is on, and therefore what its
	// verdicts are allowed to do.
	Window Window

	// Producer is the side delivering the bytes: the source read before playback, the
	// encoder behind a delivery being opened or played.
	Producer Producer

	// Telemetry is that producer's account of its own pace, nil where its rate is not a
	// statement about anything a rule may judge (see Telemetry).
	Telemetry Telemetry

	// Consumer is the renderer's side, nil until a renderer holds a URL.
	Consumer Consumer

	// Lead is the transcription's frontier, nil when this cast burns no subtitles.
	Lead Lead

	// Landed reports how many bytes of the artifact a renderer would fetch exist now. It
	// is a function rather than a number because it is polled, and it is supplied rather
	// than taken from the producer's own report so that the gate a cast opens on is the
	// artifact itself: a reader whose telemetry pipe broke must not be able to hold a
	// cast whose buffer is filling.
	Landed func() int64

	// Headroom is the pace the source read was allowed (read.Pace.Realtime). Left zero
	// where the subject is not a source read, which is what confines the deliverability
	// verdict to the reads it means anything about.
	Headroom float64

	// Delivered reports how much media the renderer can already fetch, as the position of
	// the encoder producing the delivery. It is nil in every window before a renderer holds
	// a URL, where there is nobody it could be a lead over, and it is supplied by the
	// delivery rather than by the leg that started the read: the leg watches the upstream,
	// while what a viewer still has to play is a fact about the artifact.
	//
	// It is honest only where nothing produced is ever taken away, which is what the sink
	// that supplies it today guarantees (the replay spool is never truncated and every
	// connection replays it from byte 0). A rolling-window delivery deletes behind the live
	// edge, so one that is ever supervised must cap this at its window rather than hand over
	// a position, or the rules below will hold a cast open over segments that are gone.
	Delivered func() time.Duration

	// Grace is how long the artifact may take to appear before the watch proceeds without
	// it. Zero never proceeds: a delivery whose renderer cannot be pointed at a
	// half-written playlist has no such patience to offer.
	Grace time.Duration
}

// Watch polls one subject until a rule's action ends the wait: nil when the verdict is
// that it may proceed, a *Fault when a verdict ends the cast, or the context's cause.
//
// It is the only loop in the cast path that asks whether a cast is working. It replaced
// three bespoke tickers, each with its own inline predicates, its own logging and its own
// idea of what an ended producer meant, and none of them able to see the window the
// other was in.
func Watch(ctx context.Context, m Monitor) error {
	t := &tracker{m: m, start: time.Now(), grew: time.Now()}
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	for {
		h := t.read()
		rule, act, err := judge(m.Window, h)
		if err != nil {
			return err
		}
		switch act {
		case open:
			slog.InfoContext(ctx, "watch cleared",
				"gate", m.Subject,
				"waited", time.Since(t.start).Round(time.Millisecond),
				"landed_bytes", h.Landed,
				"transcribed_lead_seconds", int(h.Lead),
			)
			return nil
		case revise, abandon:
			return t.fault(ctx, rule, act, h)
		default:
			t.report(ctx, rule, h)
		}

		select {
		case <-ctx.Done():
			// context.Cause surfaces the real reason when a concurrent stage cancelled the
			// group, not a bare "context canceled".
			return context.Cause(ctx)
		case <-tick.C:
		}
	}
}

// tracker is one Watch's derived state: the facts that are not readable at an instant
// because they are about how long something has been true.
type tracker struct {
	m     Monitor
	start time.Time

	// landed and grew are the last artifact size seen and when it last changed.
	landed int64
	grew   time.Time

	// sample is the last progress the producer reported, and samples how many times it
	// has stated a speed at all. Counting stated speeds and not blocks is what keeps
	// ffmpeg's opening run of "N/A" from being read as four seconds of zero throughput.
	// fresh reports that the last reading saw a new one, which is what keeps a report
	// cadence tied to the producer rather than to the polling rate.
	sample  media.Progress
	samples int
	fresh   bool

	// deficitFrom is when the read last fell under playback rate, zero while it has not.
	deficitFrom time.Time

	// reported is when the last cadence line was emitted.
	reported time.Time
}

// read takes one reading of every port and folds it into the facts the rules judge.
func (t *tracker) read() Health {
	h := Health{Headroom: t.m.Headroom, Subtitles: t.m.Lead != nil}

	if t.m.Landed != nil {
		h.Landed = t.m.Landed()
	}
	if h.Landed != t.landed {
		t.landed, t.grew = h.Landed, time.Now()
	}
	h.SinceGrowth = time.Since(t.grew)

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

	if p := t.m.Producer; p != nil {
		select {
		case <-p.Done():
			h.Ended = true
			// Asked only of a producer that has ended, because that is when the answer exists:
			// a read publishes its terminal error with the close of that channel and not
			// before, so a reading taken earlier is of a field the download is still writing.
			if tm := t.m.Telemetry; tm != nil {
				h.Failed = tm.Err() != nil
			}
		default:
		}
	}

	if c := t.m.Consumer; c != nil {
		handed, last := c.Handed()
		h.Handed = handed
		// A renderer that has taken nothing is measured from the watch, not from a zero
		// time: "an eternity ago" is not something a watch that just opened knows.
		h.SinceFetch = time.Since(cmp.Or(last, t.start))
	}

	if d := t.m.Delivered; d != nil {
		// The watch is the clock here because it starts the instant Play returns, so its own
		// age is how long the renderer has held the URL. It over-counts by whatever the
		// renderer spends buffering before the first frame, which understates the media it
		// still has in hand, and the rules only ever act on that figure being gone.
		h.Delivered, h.SincePlay = d(), time.Since(t.start)
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

// fault ends the watch, and explains it with the producer's own words where the verdict
// is about a producer.
//
// The evidence is dumped only while the producer has NOT ended, which is exactly when
// nobody else will: a stalled reader is killed by context cancellation, so its own error
// path never runs and these lines would be lost, whereas a producer that ended reported
// them itself on the way out. The common case is a 404 storm ("Failed to open segment",
// "HTTP error 404"), meaning the source handed castor a signed playlist whose segments
// have already expired, dead for everyone and not just for castor. Empty stderr means
// ffmpeg connected and neither bytes nor warnings arrived: a genuinely silent upstream.
func (t *tracker) fault(ctx context.Context, r Rule, act action, h Health) error {
	f := &Fault{
		Kind:    r.Kind,
		Why:     r.Why,
		Window:  t.m.Window,
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

// report says what the watch is waiting on, at the reporting cadence, and on every fresh
// sample while the read is short of playback rate.
//
// The deficit case is deliberately louder, and it is tied to the producer's own report
// period rather than to the polling rate: an in-flight deficit is acted on only after it
// has outlasted two reconnect ceilings, and two and a half minutes of a cast quietly
// deciding to abandon itself is exactly the stretch a user needs the numbers for.
func (t *tracker) report(ctx context.Context, r Rule, h Health) {
	deficit := t.fresh && h.Headroom > 1 && h.Speed > 0 && h.Speed < playbackRate
	if !deficit && time.Since(t.reported) < reportInterval {
		return
	}
	t.reported = time.Now()
	slog.InfoContext(ctx, "watch",
		"gate", t.m.Subject,
		"window", t.m.Window.String(),
		"verdict", r.Kind.String(),
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

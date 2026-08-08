package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/media"
)

// fakeProber is a measurement without a subprocess, which is what makes the rule this
// package owns testable at all: the rule is about what a cast believes when a probe fails,
// and today's failures need a 403ing CDN or a wedged ffprobe to produce.
type fakeProber struct {
	info  media.ProbeInfo
	err   error
	calls int

	// budget is how long the probe was actually given, so a test can assert whose clock it
	// was measured against.
	budget time.Duration
}

func (p *fakeProber) Probe(ctx context.Context) (media.ProbeInfo, error) {
	p.calls++
	if deadline, ok := ctx.Deadline(); ok {
		p.budget = time.Until(deadline)
	}
	return p.info, p.err
}

// TestMeasureKeepsWhatAnsweredAndSaysWhatDidNot covers both halves of the fallback rule
// that used to be restated as three inline warnings in three different wordings.
//
// The partial case is the load-bearing one: a demuxed program whose audio rendition could
// not be read still measured its video, and dropping that half would cost the source its
// video stream copy for a failure on the other axis. It is still Measured false, because a
// diagnosis reading "this was never measured" must not be answered with half a measurement.
func TestMeasureKeepsWhatAnsweredAndSaysWhatDidNot(t *testing.T) {
	whole := &fakeProber{info: media.ProbeInfo{VideoCodec: media.CodecH264, AudioCodec: media.CodecAAC, AudioChannels: 6}}
	facts := Measure(t.Context(), "the source", whole)
	if !facts.Measured {
		t.Error("a probe that answered reports Measured false")
	}
	if facts.Probe.AudioChannels != 6 {
		t.Errorf("Probe = %+v, want what the prober answered", facts.Probe)
	}
	if whole.calls != 1 {
		t.Errorf("the subject was probed %d times, want exactly one", whole.calls)
	}

	half := &fakeProber{
		info: media.ProbeInfo{VideoCodec: media.CodecH264},
		err:  errors.New("probing audio rendition: ffprobe: exit status 1"),
	}
	facts = Measure(t.Context(), "the source", half)
	if facts.Measured {
		t.Error("a partial measurement reports Measured true, so a cast that dies against it cannot be told from one castor measured")
	}
	if facts.Probe.VideoCodec != media.CodecH264 {
		t.Errorf("Probe = %+v, want the half that answered: the video axis is still decided from it", facts.Probe)
	}

	// And nothing known against a subject is what a total failure has to answer, because
	// every copy decision below reads the probe: a zero one matches no carriage rule and no
	// copy adaptation, so it costs a re-encode and never a cast.
	facts = Measure(t.Context(), "the local buffer", &fakeProber{err: errors.New("ffprobe: exit status 1")})
	if facts.Measured || facts.Probe != (media.ProbeInfo{}) {
		t.Errorf("a failed probe reports %+v, want nothing known against the subject", facts)
	}
}

// TestMeasureImposesItsOwnDeadline covers the shape of dead source an unbounded measurement
// outlives the whole cast for: an origin that accepts the connection and then answers nothing
// leaves ffprobe walking a playlist for minutes (199 seconds, measured on a real one),
// printing nothing at all. The deadline has to be castor's own, so the cast reads on rather
// than waiting it out, and it is asserted here rather than waited out.
func TestMeasureImposesItsOwnDeadline(t *testing.T) {
	p := &fakeProber{}
	Measure(t.Context(), "the source", p)

	if p.budget == 0 {
		t.Fatal("the probe was given no deadline of its own, so a subject that answers nothing outlives the cast's patience")
	}
	if p.budget > probeBudget {
		t.Errorf("the probe was given %s, which is more than the budget this layer derived (%s)", p.budget, probeBudget)
	}

	// And a caller already closer to its own deadline keeps it: the bound is a ceiling, not a
	// grant.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	tight := &fakeProber{}
	Measure(ctx, "the source", tight)
	if tight.budget > 50*time.Millisecond {
		t.Errorf("the probe was given %s, outliving the cast that asked for it", tight.budget)
	}
}

// TestTheProbeBudgetIsHalfAReconnectCeiling pins the derivation rather than the number. The
// reader this measurement informs is allowed to spend a whole reconnect ceiling waiting out
// a rate limiter before the retry that lands, so a measurement outliving half of one costs
// more than the read it is protecting.
func TestTheProbeBudgetIsHalfAReconnectCeiling(t *testing.T) {
	if probeBudget*2 != read.BackoffMax {
		t.Errorf("probeBudget = %s, want half of the reader's reconnect ceiling %s", probeBudget, read.BackoffMax)
	}
}

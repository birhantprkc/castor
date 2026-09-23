package execute

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
)

type fakeProber struct {
	info   media.ProbeInfo
	err    error
	budget time.Duration
}

func (p *fakeProber) Probe(ctx context.Context) (media.ProbeInfo, media.Reach, error) {
	if deadline, ok := ctx.Deadline(); ok {
		p.budget = time.Until(deadline)
	}
	return p.info, media.ReachUnproven, p.err
}

// TestMeasureKeepsWhatAnsweredWithinItsOwnDeadline: a partial probe is used but never called measured.
func TestMeasureKeepsWhatAnsweredWithinItsOwnDeadline(t *testing.T) {
	half := &fakeProber{
		info: media.ProbeInfo{VideoCodec: media.CodecH264},
		err:  errors.New("probing audio rendition: ffprobe: exit status 1"),
	}
	facts := measure(t.Context(), "the source", half)
	if facts.Measured {
		t.Error("a partial measurement reports Measured true")
	}
	if facts.Probe.VideoCodec != media.CodecH264 {
		t.Errorf("Probe = %+v, want the half that answered", facts.Probe)
	}
	if half.budget == 0 || half.budget > probeBudget {
		t.Errorf("the probe was given %s, want a deadline within %s", half.budget, probeBudget)
	}

	if whole := measure(t.Context(), "the source", &fakeProber{info: media.ProbeInfo{AudioChannels: 6}}); !whole.Measured || whole.Probe.AudioChannels != 6 {
		t.Errorf("a probe that answered reports %+v", whole)
	}
}

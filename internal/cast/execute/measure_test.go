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

func TestARenditionThatStartsLaterPlaysLater(t *testing.T) {
	program := media.Program{
		Inputs:     []media.Input{{ID: "video"}, {ID: "audio"}, {ID: "commentary"}},
		ClockInput: "video",
		Offsets:    map[media.InputID]time.Duration{"commentary": 500 * time.Millisecond},
	}
	got := aligned(program, map[media.InputID]time.Duration{
		"video": 1400 * time.Millisecond, "audio": 2400 * time.Millisecond, "commentary": 9 * time.Second,
	})
	if got.Offsets["audio"] != time.Second {
		t.Errorf("audio offset %v, want the 1s it starts after the picture", got.Offsets["audio"])
	}
	if got.Offsets["commentary"] != 500*time.Millisecond {
		t.Errorf("commentary offset %v, want the offset the source declared kept", got.Offsets["commentary"])
	}
	if _, set := program.Offsets["audio"]; set {
		t.Error("aligning changed the program it was given")
	}
	if same := aligned(program, map[media.InputID]time.Duration{"video": time.Second, "audio": time.Second + time.Millisecond}); same.Offsets["audio"] != 0 {
		t.Errorf("inputs a millisecond apart were offset by %v, want them left together", same.Offsets["audio"])
	}
}

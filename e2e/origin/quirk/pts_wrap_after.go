package quirk

import (
	"fmt"
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/strategy"
)

// wrap is where MPEG-TS's 33-bit 90 kHz timestamps roll over, in seconds.
const wrap = float64(1<<33) / 90000

// muxDelay is the head start ffmpeg's mpegts muxer gives every timestamp by default.
const muxDelay = 1.4

// PTSWrapAfter builds a quirk whose timestamps roll over partway through, as long DVB recordings do, as in `pts-wrap-after: 7s`.
type PTSWrapAfter struct{}

func (PTSWrapAfter) Name() string { return "pts-wrap-after" }

func (PTSWrapAfter) Build(settings yaml.Node) (origin.Quirk, error) {
	var after time.Duration
	if err := strategy.Decode(settings, &after); err != nil {
		return nil, err
	}
	if after <= 0 {
		return nil, fmt.Errorf("want a positive duration such as 7s, not %s", after)
	}
	return ptsWrapAfter{after: after}, nil
}

type ptsWrapAfter struct{ after time.Duration }

func (ptsWrapAfter) Name() string { return "pts-wrap-after" }

func (p ptsWrapAfter) Bend(s *origin.Stream) error {
	if s.Packager.Muxes() != origin.MPEGTS {
		return fmt.Errorf("only MPEG-TS timestamps wrap, not %s's", s.Packager.Name())
	}
	if p.after >= time.Duration(s.Seconds)*time.Second {
		return fmt.Errorf("a %ds stream ends before a wrap at %s", s.Seconds, p.after)
	}
	offset := wrap - muxDelay - p.after.Seconds()
	s.MuxOut = append(s.MuxOut, "-output_ts_offset", strconv.FormatFloat(offset, 'f', 6, 64))
	return nil
}

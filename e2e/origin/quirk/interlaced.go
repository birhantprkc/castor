package quirk

import (
	"fmt"
	"math"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/strategy"
)

// weaves is the tinterlace mode that weaves each field order.
var weaves = map[string]string{"tff": "interleave_top", "bff": "interleave_bottom"}

// Interlaced builds a quirk that weaves the picture into fields coded interlaced, as broadcasts are, as in `interlaced: tff`.
type Interlaced struct{}

func (Interlaced) Name() string { return "interlaced" }

func (Interlaced) Build(settings yaml.Node) (origin.Quirk, error) {
	var order string
	if err := strategy.Decode(settings, &order); err != nil {
		return nil, err
	}
	if weaves[order] == "" {
		return nil, fmt.Errorf("want the field order, tff or bff, not %q", order)
	}
	return interlaced{order: order}, nil
}

type interlaced struct{ order string }

func (interlaced) Name() string { return "interlaced" }

func (i interlaced) Bend(s *origin.Stream) error {
	if s.Video.Name() != "h264" {
		return fmt.Errorf("only h264 is encoded interlaced here, not %s", s.Video.Name())
	}
	// Weaving halves the rate, so the source runs at twice it and the GOP stays a second of woven frames.
	fps := s.FPS()
	s.Rate = strconv.FormatFloat(2*fps, 'f', -1, 64)
	s.Filters = append(s.Filters, "tinterlace=mode="+weaves[i.order], "setfield="+i.order)
	// x264 codes top field first whatever the frames say, so the order is told to it outright.
	s.VideoOut = append(s.VideoOut, "-flags", "+ildct+ilme", "-x264-params", i.order+"=1", "-g", strconv.Itoa(int(math.Round(fps))))
	s.Interlaced = true
	return nil
}

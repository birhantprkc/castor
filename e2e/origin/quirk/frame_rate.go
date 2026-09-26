package quirk

import (
	"errors"
	"fmt"
	"math"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/strategy"
)

// FrameRate builds a quirk that runs the source at another frame rate, as in `frame-rate: 120` or `frame-rate: 24000/1001`.
type FrameRate struct{}

func (FrameRate) Name() string { return "frame-rate" }

func (FrameRate) Build(settings yaml.Node) (origin.Quirk, error) {
	var rate string
	if err := strategy.Decode(settings, &rate); err != nil {
		return nil, err
	}
	if fps := (origin.Stream{Rate: rate}).FPS(); rate == "" || !(fps > 0) || math.IsInf(fps, 1) {
		return nil, fmt.Errorf("want a positive rate such as 120, 59.94 or 24000/1001, not %q", rate)
	}
	return frameRate{rate: rate}, nil
}

type frameRate struct{ rate string }

func (frameRate) Name() string { return "frame-rate" }

// Bend only sets the rate: the encoder already keys its GOP to it, one keyframe a second.
func (f frameRate) Bend(s *origin.Stream) error {
	if s.Interlaced {
		return errors.New("interlaced has already woven the picture at its rate: list frame-rate before it")
	}
	s.Rate = f.rate
	return nil
}

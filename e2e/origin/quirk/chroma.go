package quirk

import (
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/strategy"
)

// Chroma builds a quirk that encodes the picture with fuller chroma than 4:2:0, as screen captures are, as in `chroma: 444`.
type Chroma struct{}

func (Chroma) Name() string { return "chroma" }

func (Chroma) Build(settings yaml.Node) (origin.Quirk, error) {
	var subsampling int
	if err := strategy.Decode(settings, &subsampling); err != nil {
		return nil, err
	}
	if !slices.Contains([]int{422, 444}, subsampling) {
		return nil, fmt.Errorf("want 422 or 444, not %d", subsampling)
	}
	return chroma{subsampling: subsampling}, nil
}

type chroma struct{ subsampling int }

func (chroma) Name() string { return "chroma" }

func (c chroma) Bend(s *origin.Stream) error {
	// SVT-AV1 silently falls back to 4:2:0 rather than refusing.
	if slices.Contains([]string{"vp8", "av1"}, s.Video.Name()) {
		return fmt.Errorf("%s is encoded 4:2:0 only, not %d", s.Video.Name(), c.subsampling)
	}
	format := fmt.Sprintf("yuv%dp", c.subsampling)
	if s.Depth == 10 {
		format += "10le"
	}
	s.VideoOut = append(s.VideoOut, "-pix_fmt", format)
	s.Chroma = c.subsampling
	return nil
}

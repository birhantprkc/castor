package quirk

import (
	"fmt"
	"slices"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/strategy"
)

// Rotated builds a quirk that stores the picture unrotated under a display matrix, as phones do, as in `rotated: 90`.
type Rotated struct{}

func (Rotated) Name() string { return "rotated" }

func (Rotated) Build(settings yaml.Node) (origin.Quirk, error) {
	var degrees int
	if err := strategy.Decode(settings, &degrees); err != nil {
		return nil, err
	}
	if !slices.Contains([]int{90, 180, 270}, degrees) {
		return nil, fmt.Errorf("want 90, 180 or 270 degrees, not %d", degrees)
	}
	return rotated{degrees: degrees}, nil
}

type rotated struct{ degrees int }

func (rotated) Name() string { return "rotated" }

func (r rotated) Bend(s *origin.Stream) error {
	// MPEG-TS has nowhere to put a display matrix.
	if slices.Contains([]string{"file-ts", "hls-ts"}, s.Packager.Name()) {
		return fmt.Errorf("%s cannot carry a rotation", s.Packager.Name())
	}
	s.VideoIn = append(s.VideoIn, "-noautorotate", "-display_rotation", strconv.Itoa(r.degrees))
	s.Rotation = r.degrees
	return nil
}

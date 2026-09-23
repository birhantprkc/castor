package source

import "github.com/stupside/castor/internal/media"

// Config is what ranking and resolution read of the operator's resolver section.
type Config struct {
	ProbeMaxConcurrency int             `yaml:"probe_max_concurrency" validate:"required,min=1"` // Bound concurrent measurements (origin courtesy).
	MaxHeight           media.HeightCap `yaml:"max_height" validate:"required,min=2"`            // Tallest video (caps renderer, not encoder).
}

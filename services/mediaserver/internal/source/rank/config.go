package rank

// Config is what ranking reads of the operator's resolver section.
type Config struct {
	ProbeMaxConcurrency int `yaml:"probe_max_concurrency" validate:"required,min=1"` // Bound concurrent measurements (origin courtesy).
}

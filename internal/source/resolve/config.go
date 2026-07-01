package resolve

import "time"

type Config struct {
	HLSTimeout          time.Duration `yaml:"hls_timeout" validate:"required"`
	FFprobePath         string        `yaml:"ffprobe_path" validate:"required"`
	ProbeTimeout        time.Duration `yaml:"probe_timeout" validate:"required"`
	ProbeMaxConcurrency int           `yaml:"probe_max_concurrency" validate:"required,min=1"`
}

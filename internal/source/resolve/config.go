package resolve

import "time"

// Config is the resolver section of the operator's configuration. It holds two
// kinds of value and the split matters: MaxHeight and ProbeMaxConcurrency are read
// by the policy in this package, while the paths and budgets are spent by the
// composition root building the adapters the policy drives (see internal/config,
// ./ffprobe and ./httpfetch). Nothing in here names an ffprobe flag or an HTTP
// client, which is why a decision made in this package can be tested without
// either.
type Config struct {
	// HLSTimeout bounds one playlist fetch, body included. It is the httpfetch
	// client's budget.
	HLSTimeout time.Duration `yaml:"hls_timeout" validate:"required"`

	// FFprobePath and ProbeTimeout are the ffprobe prober's binary and the budget
	// one measurement gets.
	FFprobePath  string        `yaml:"ffprobe_path" validate:"required"`
	ProbeTimeout time.Duration `yaml:"probe_timeout" validate:"required"`

	// ProbeMaxConcurrency bounds how many candidates are measured at once. It is a
	// courtesy to the origin as much as to this machine: an embed proxy answers a
	// burst of probes behind one signature with HTTP 429, which poisons the whole
	// ranking (see maxProbePerHost).
	ProbeMaxConcurrency int `yaml:"probe_max_concurrency" validate:"required,min=1"`

	// MaxHeight is the tallest video the user wants cast, and it is a maximum on what
	// reaches the RENDERER rather than on what castor's encoder produces: source
	// selection prefers the largest HLS variant no taller than this, the encoder scales
	// its output down to it, and a source declared taller than it is not handed to a
	// self-fetching renderer at all, because castor cannot scale bytes it never reads
	// (see core.Shape.Passthrough). Set it to your renderer's native height (e.g. 2160
	// for a 4K TV). Required, so it is always an explicit ceiling.
	MaxHeight int `yaml:"max_height" validate:"required,min=1"`
}

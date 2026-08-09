// Package subtitle holds configuration shared across the subtitle mechanisms,
// deliberately free of any mechanism implementation (and its cgo) so the pure
// planner in core can read it without importing the whisper transcriber.
package subtitle

// Whisper holds settings for the in-process whisper.cpp transcriber. It lives
// here, not in the whisper package, so core.Config can carry it (the planner
// reads Enable to choose the subtitle axis) without pulling whisper's cgo into
// the decision core. It is the sole mechanism for enabling subtitle generation;
// there is no CLI override. Set Enable: true in config.yaml (or
// CASTOR_WHISPER__ENABLE=true) to opt in. Everything else is self-managed: the
// transcription and VAD models auto-download to the user cache, and the streaming
// pipeline needs no tuning.
type Whisper struct {
	Enable    bool     `yaml:"enable"`
	ModelPath string   `yaml:"model_path"` // override the auto-downloaded tiny.en model
	Language  Language `yaml:"language"`   // pin a BCP-47 code, or LanguageAuto to detect
}

// SampleRate is the rate the PCM feed a transcription reads must be produced at:
// mono s16le at 16 kHz, which is what the model works in. It lives here for the same
// reason Whisper does, and the party it matters most to is the READ, which has to ask
// ffmpeg for that rate without linking the mechanism (see internal/cast/burnin).
const SampleRate = 16000

// Language is a subtitle language: a BCP-47 code (e.g. "en", "fr") the transcriber
// is pinned to, or LanguageAuto to detect it from the audio. It is a named type so
// the "detect it" sentinel is expressed once, via AutoDetect, instead of scattered
// as bare "" and "auto" string comparisons at each call site.
type Language string

// LanguageAuto lets the transcriber detect the language per buffer. An unset
// (zero-value) Language means the same, so both resolve to auto-detection.
const LanguageAuto Language = "auto"

// AutoDetect reports whether the language should be detected from the audio rather
// than pinned. Pinning is preferred where a code is known: per-buffer detection
// misfires on music and quiet stretches.
func (l Language) AutoDetect() bool {
	return l == "" || l == LanguageAuto
}

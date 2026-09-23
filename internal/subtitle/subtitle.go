package subtitle

// Whisper holds settings for the in-process whisper.cpp transcriber.
type Whisper struct {
	Enable    bool     `yaml:"enable"`
	ModelPath string   `yaml:"model_path"` // override the auto-downloaded tiny.en model
	Language  Language `yaml:"language"`   // pin a BCP-47 code, or LanguageAuto to detect
}

// SampleRate is the rate the PCM feed a transcription reads must be produced at: mono s16le at 16 kHz.
const SampleRate = 16000

// Language is a subtitle language: a BCP-47 code (e.g. "en", "fr") or LanguageAuto to detect it.
type Language string

// LanguageAuto lets the transcriber detect the language per buffer.
const LanguageAuto Language = "auto"

// AutoDetect reports whether the language should be detected from the audio rather than pinned.
func (l Language) AutoDetect() bool {
	return l == "" || l == LanguageAuto
}

// Package subtitle turns a cast's transcribed speech into the captions burnt into its picture.
package subtitle

// Whisper holds settings for the in-process whisper.cpp transcriber.
type Whisper struct {
	Enable    bool   `yaml:"enable"`
	ModelPath string `yaml:"model_path"` // override the auto-downloaded tiny.en model
	// Language is a BCP-47 code (e.g. "en", "fr"), or auto to detect it from the audio.
	Language string `yaml:"language"`
}

// SampleRate is the rate the PCM feed a transcription reads must be produced at: mono s16le at 16 kHz.
const SampleRate = 16000

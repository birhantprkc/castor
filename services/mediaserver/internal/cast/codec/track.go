package codec

import (
	"github.com/stupside/castor/services/mediaserver/internal/ffmpeg"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// copyName is "-c copy" on the command line (unexported: copying leaves this package through Name).
const copyName = "copy"

// encode is the re-encode one half of a program can be given.
type encode interface {
	VideoEncode | AudioEncode
	name() string
}

// Track is one half of an encode: a stream copy or a re-encode (not the zero value).
type Track[E encode] struct {
	decided bool
	enc     *E
}

func CopyVideo() Track[VideoEncode] { return Track[VideoEncode]{decided: true} }

func CopyAudio() Track[AudioEncode] { return Track[AudioEncode]{decided: true} }

func Encode[E encode](e E) Track[E] { return Track[E]{decided: true, enc: &e} }

// Decided reports whether any decision was taken for this half (false only for the zero value).
func (t Track[E]) Decided() bool { return t.decided }

// Encode returns the re-encode parameters, with ok false for a stream copy.
func (t Track[E]) Encode() (E, bool) {
	if t.enc == nil {
		var none E
		return none, false
	}
	return *t.enc, true
}

// Name is what this track puts on -c: the encoder, or the copy keyword (for decision logs).
func (t Track[E]) Name() string {
	if t.enc != nil {
		return (*t.enc).name()
	}
	return copyName
}

// VideoEncode is the video re-encode: the encoder and the picture it produces.
type VideoEncode struct {
	Encoder ffmpeg.Encoder

	// Bitrate is the average target (e.g. "4M"); Maxrate and Bufsize bound the instantaneous rate via VBV.
	Bitrate string
	Maxrate string
	Bufsize string

	// Quality is a constant-quality target (-crf); only set with rate ceilings (see floorVideoQuality).
	Quality int

	// MaxHeight caps the output height while preserving aspect ratio.
	MaxHeight media.HeightCap

	// KeyframeIntervalSec caps GOP length so a device joining mid-stream resyncs within this bound.
	KeyframeIntervalSec int

	// SubtitleTextFile burns the file's current contents into every frame via drawtext with reload=1.
	SubtitleTextFile string

	// Deinterlace weaves fields into frames for a device that would show them combed.
	Deinterlace bool
	// ToneMap maps an HDR picture to BT.709 SDR, since no device is known to engage HDR.
	ToneMap bool
	// MaxFrameRate drops frames above it; 0 keeps the source's rate.
	MaxFrameRate float64
}

func (e VideoEncode) name() string { return e.Encoder.Name }

// AudioEncode is the audio re-encode: the codec to produce and the shape to produce it in.
type AudioEncode struct {
	// Codec is the encoder to run, e.g. aac, ac3, eac3 (the abstract codec, not the encoder name).
	Codec media.Codec
	// Bitrate is the target (e.g. "256k").
	Bitrate string
	// SampleRate is the output rate in Hz; 0 keeps the source rate.
	SampleRate int
	// Channels is the output layout; 0 keeps the source layout.
	Channels int
	// Resync retimes samples by their count, a clock that runs straight through seams where timestamps restart.
	Resync bool
}

func (e AudioEncode) name() string { return string(e.Codec) }

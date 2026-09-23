package plan

import "github.com/stupside/castor/internal/media"

// copyName is "-c copy" on the command line (unexported: copying leaves this package through Name).
const copyName = "copy"

// VideoTrack is the video half of an encode: a stream copy or a re-encode (not the zero value).
type VideoTrack struct {
	decided bool
	enc     *VideoEncode
}

// Encoder is one concrete way to produce a codec: the ffmpeg -c:v name plus its command fragments.
type Encoder struct {
	Name     string      // -c:v value, e.g. "libx264", "hevc_videotoolbox"
	Codec    media.Codec // the abstract codec produced, independent of the name
	Hardware bool        // GPU-backed: trusted only after a real test encode
	InitArgs []string    // emitted before the input: hardware device setup
	Filters  []string    // appended to the -vf chain: e.g. the GPU upload
	Flags    []string    // encoder-specific -c:v flags: preset, pix_fmt, GOP
}

type VideoEncode struct {
	Encoder Encoder

	// Bitrate is the average target (e.g. "4M"); Maxrate and Bufsize bound the instantaneous rate via VBV.
	Bitrate string
	Maxrate string
	Bufsize string

	// Quality is a constant-quality target (-crf); only set with rate ceilings (see floorVideoQuality).
	Quality int

	// MaxHeight caps the output height while preserving aspect ratio.
	MaxHeight media.HeightCap

	// KeyframeIntervalSec caps GOP length so a renderer joining mid-stream resyncs within this bound.
	KeyframeIntervalSec int

	// SubtitleTextFile burns the file's current contents into every frame via drawtext with reload=1.
	SubtitleTextFile string
}

func CopyVideo() VideoTrack { return VideoTrack{decided: true} }

func EncodeVideo(e VideoEncode) VideoTrack { return VideoTrack{decided: true, enc: &e} }

// Decided reports whether any decision was taken for this axis (false only for the zero value).
func (t VideoTrack) Decided() bool { return t.decided }

// Encode returns the re-encode parameters, with ok false for a stream copy.
func (t VideoTrack) Encode() (VideoEncode, bool) {
	if t.enc == nil {
		return VideoEncode{}, false
	}
	return *t.enc, true
}

// Name is what this track puts on -c:v: the encoder's ffmpeg name, or the copy keyword (for decision logs).
func (t VideoTrack) Name() string {
	if t.enc != nil {
		return t.enc.Encoder.Name
	}
	return copyName
}

// AudioTrack is the audio half of an encode, mirroring VideoTrack exactly.
type AudioTrack struct {
	decided bool
	enc     *AudioEncode
}

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
}

func CopyAudio() AudioTrack { return AudioTrack{decided: true} }

func EncodeAudio(e AudioEncode) AudioTrack { return AudioTrack{decided: true, enc: &e} }

// Decided reports whether any decision was taken for this axis.
func (t AudioTrack) Decided() bool { return t.decided }

// Encode returns the re-encode parameters, with ok false for a stream copy.
func (t AudioTrack) Encode() (AudioEncode, bool) {
	if t.enc == nil {
		return AudioEncode{}, false
	}
	return *t.enc, true
}

// Name is what this track puts on -c:a: the codec to encode, or the copy keyword.
func (t AudioTrack) Name() string {
	if t.enc != nil {
		return string(t.enc.Codec)
	}
	return copyName
}

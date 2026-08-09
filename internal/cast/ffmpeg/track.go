package ffmpeg

import "github.com/stupside/castor/internal/media"

// This file is the shape of an encode decision. Each axis of a command line is
// one of exactly two things, a stream copy or a re-encode, and every parameter
// that only means something on a re-encode lives inside the re-encode. That is
// the whole point: the flat struct these types replace carried eleven fields of
// which six were documented "Ignored when VideoEncoder is nil", i.e. six states
// the type could represent and the encoder could not. A copy carrying a bitrate,
// a scale filter, a GOP bound or a burn-in is now unspellable rather than
// documented-against, and a decision no resolver ever took is distinguishable
// from a copy instead of silently becoming one.

// codecCopy is the ffmpeg "-c copy" keyword: stream-copy a track instead of
// re-encoding it. It is unexported because copying is no longer a value any
// caller assigns: it is one of the two states a track is constructed in, so the
// keyword only ever reaches a command line from inside this package.
const codecCopy = "copy"

// VideoTrack is the video half of an encode: a stream copy, or a re-encode with
// the parameters that belong to it. Neither is the zero value. A track no
// decision ever produced is not a copy, it is nothing, and EncodeArgs refuses it
// rather than emitting "-c:v copy" for a decision nobody took.
type VideoTrack struct {
	decided bool
	enc     *VideoEncode
}

// VideoEncode is everything that only means something on a re-encode. It is a
// separate record so a copied bitstream cannot carry any of it: filters run on
// decoded frames and rate control drives an encoder, so on a copy every field
// here would be a flag ffmpeg either ignores or dies on.
type VideoEncode struct {
	// Encoder is the concrete way this host produces the target codec. It carries
	// its own device setup, filters and flags, so EncodeArgs never branches on the
	// encoder kind.
	Encoder Encoder

	// Bitrate is the average target (e.g. "4M"). Maxrate is the VBV peak-rate cap
	// and Bufsize the VBV buffer: together they bound the instantaneous bitrate so
	// a complex scene cannot spike past what the renderer decodes and buffers.
	// Empty leaves the encoder in unbounded ABR.
	Bitrate string
	Maxrate string
	Bufsize string

	// MaxHeight caps the output height while preserving aspect ratio. It is the cast's own
	// ceiling, carried as the type every party that honours it reads (see media.HeightCap).
	MaxHeight media.HeightCap

	// KeyframeIntervalSec caps the GOP length in seconds via force_key_frames, so
	// a renderer joining mid-stream resyncs within this bound regardless of source
	// fps. 0 leaves the encoder default.
	KeyframeIntervalSec int

	// SubtitleTextFile burns the file's current contents into every frame via
	// drawtext with reload=1: ffmpeg re-opens the file by path before each frame,
	// so an external writer can swap the active subtitle line live (atomic rename
	// only, a failed read kills ffmpeg). The file must exist before ffmpeg starts, and
	// the writer swaps it as the encoder's -progress feed ticks (Process.ProgressFeed),
	// which every encode emits whether or not it burns anything in.
	//
	// It lives inside the re-encode and nowhere else, because drawtext needs
	// decoded frames. The cross-field contract EncodeArgs used to check at runtime
	// ("burn-in with a nil encoder") is now a state that cannot be built, and so is
	// the statement-ordering hazard on the caller's side: a burn-in is an argument
	// to the decision rather than a field the decision reads back.
	SubtitleTextFile string
}

// CopyVideo decides the video axis is a stream copy.
func CopyVideo() VideoTrack { return VideoTrack{decided: true} }

// EncodeVideo decides the video axis is a re-encode with e.
func EncodeVideo(e VideoEncode) VideoTrack { return VideoTrack{decided: true, enc: &e} }

// Decided reports whether any decision was taken for this axis. It is false only
// for the zero value, which is what lets EncodeArgs refuse an encode whose
// resolver never ran instead of quietly stream-copying for it.
func (t VideoTrack) Decided() bool { return t.decided }

// Encode returns the re-encode parameters, with ok false for a stream copy.
func (t VideoTrack) Encode() (VideoEncode, bool) {
	if t.enc == nil {
		return VideoEncode{}, false
	}
	return *t.enc, true
}

// Name is what this track puts on -c:v: the encoder's ffmpeg name, or the copy
// keyword. It exists for the decision log lines, which are the one place a
// caller outside this package needs to say what the encode is doing without
// rebuilding the command line to find out.
func (t VideoTrack) Name() string {
	if t.enc != nil {
		return t.enc.Encoder.Name
	}
	return codecCopy
}

// AudioTrack is the audio half of an encode, mirroring VideoTrack exactly. The
// two axes used to spell one union two different ways, a nil *Encoder for video
// and the literal string "copy" for audio, which is why "-c:a" could be emitted
// with an empty value and "-c:v" could not.
type AudioTrack struct {
	decided bool
	enc     *AudioEncode
}

// AudioEncode is the audio re-encode: the codec to produce and the shape to
// produce it in.
//
// There is deliberately no bitstream-filter field here either. What a copy needs
// to survive muxing is derived inside EncodeArgs from Probe and Format, because a
// filter is a property of a copy and nothing else: a stray "-bsf:a aac_adtstoasc"
// alongside "-c:a ac3" aborts at filter init with nothing written.
type AudioEncode struct {
	// Codec is the encoder to run, e.g. aac, ac3, eac3. It is the abstract codec
	// rather than an encoder name because ffmpeg's built-in audio encoders are
	// named after their codec, and the copy adaptation tables ask about the codec.
	Codec media.Codec
	// Bitrate is the target (e.g. "256k").
	Bitrate string
	// SampleRate is the output rate in Hz; 0 keeps the source rate.
	SampleRate int
	// Channels is the output layout; 0 keeps the source layout. The audio decision
	// sets this: 2 to downmix to the stereo floor every renderer decodes, or the
	// source layout capped at the codec's ceiling to keep 5.1/7.1.
	Channels int
}

// CopyAudio decides the audio axis is a stream copy.
func CopyAudio() AudioTrack { return AudioTrack{decided: true} }

// EncodeAudio decides the audio axis is a re-encode with e.
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
	return codecCopy
}

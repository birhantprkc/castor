package core

// DeliveryPreference is what the operator asks of the delivery axis. It exists for the one
// case castor cannot infer: a source that lies about itself (a playlist whose segments are
// served under a disguised extension, say) is fetchable as far as castor can tell, yet the
// renderer refuses it. No evidence distinguishes that source from a well-formed one, so
// only the operator can say.
//
// Nothing else about a cast is configurable this way on purpose. Every other axis is
// inferred from evidence castor holds (a probe, advertised capabilities), and a renderer
// that misbehaves there is a capability-data fix, not a knob.
type DeliveryPreference string

const (
	// DeliveryAuto leaves the decision to the evidence (see Shape.Passthrough). It is also
	// what an unset key means, so the zero value needs no default.
	DeliveryAuto DeliveryPreference = "auto"
	// DeliveryServe refuses pass-through for every cast: castor reads the source and serves
	// the renderer a local stream, whatever the source looks like.
	DeliveryServe DeliveryPreference = "serve"
)

// SubtitleMode is the subtitle axis of a cast. It carries only the two modes today's code
// produces; a future SubtitleSidecar (a separately served caption track the device loads)
// is a new value here plus one more implementation of the stage port, not a reshuffle of
// the existing ones.
type SubtitleMode int

const (
	// SubtitleOff ships no subtitles: every cast a renderer fetches for itself, and every
	// served cast with the transcriber disabled.
	SubtitleOff SubtitleMode = iota
	// SubtitleBurnIn transcribes the audio and draws the cues into the video frames during
	// the encode (whisper hardsubs). It forces a video re-encode (drawtext needs decoded
	// frames) and so is only reachable on a cast castor produces the picture for.
	SubtitleBurnIn
)

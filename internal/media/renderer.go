package media

import "slices"

// Renderer describes what a target device can play without help from us: the
// containers it accepts as-is over the network (so the source URL can be handed
// to it directly), and the video envelopes it decodes natively (so a matching
// source can be stream-copied instead of re-encoded). It is the single capability
// model every device type describes itself with; each Device resolves its own by
// whatever means its protocol allows, while this package owns the type and the
// matching rules. Nothing here names or is specialized for any device family.
type Renderer struct {
	Containers []string
	Video      []VideoSupport
	Audio      []AudioSupport

	// SelfFetch reports whether the renderer fetches an arbitrary stream URL
	// itself once it is handed one, versus needing castor to serve the bytes to
	// it. The planner reads this to choose pass-through (hand the renderer the
	// source URL and let it pull directly) over a castor-served stream: a
	// self-fetching renderer can pass the source through, while a push-only
	// renderer only plays what castor serves it and so is always served locally.
	SelfFetch bool

	// ServedContainer is the content type castor produces on a served cast that
	// remuxes: the container the local ffmpeg muxes and the renderer is told it is
	// fetching. A renderer that will be served a live remux declares the container
	// it wants (a fragmented single-file container for a smart client, or a
	// segmented live format for one that cannot play a growing single-file URL);
	// the read-once spool path serves its own append-only container. Inert on a
	// pass-through cast, which reads the source's own content type.
	ServedContainer string
}

// VideoSupport is one video envelope a renderer decodes natively. A probed
// source is copy-eligible when it matches at least one on the things that
// black-screen a TV outright: codec, profile and bit depth.
//
// Two facts about a picture are deliberately NOT here, and for the same reason:
// neither is anything a renderer said about itself, so a device declaring one
// would be answering someone else's question. Resolution is the user's
// cast-quality preference (HeightCap is the one type that says where it binds).
// Dynamic range is a policy castor holds for every renderer alike, HDR playback
// not being something that can be assumed to engage on an arbitrary set; both are
// refused where the copy is actually decided (see core.DecideVideo), which is what
// makes them bind on every leg rather than only on the legs that consult an
// envelope at all.
type VideoSupport struct {
	Codec     Codec
	Profiles  []string // nil or empty = any profile
	BitDepths []int    // nil or empty = {8}
}

// AudioSupport is one audio codec a renderer decodes natively, up to MaxChannels
// channels. A probed audio track is copy-eligible when its codec matches and its
// channel count fits, so a 5.1/7.1 track passes through instead of being
// downmixed to stereo. MaxChannels 0 means "no advertised ceiling": trusted at
// full channel count, which is right for the inherently-surround Dolby codecs
// (AC-3/E-AC-3) whose advertised support already implies multichannel decode.
type AudioSupport struct {
	Codec       Codec
	MaxChannels int // highest channel count the renderer decodes; 0 = no ceiling
}

// DeliveryPreference is what the operator asks of the delivery axis: whether a renderer
// that fetches for itself may be handed the source URL at all. It exists for the one case
// castor cannot infer: a source that lies about itself (a playlist whose segments are
// served under a disguised extension, say) is fetchable as far as castor can tell, yet the
// renderer refuses it. No evidence distinguishes that source from a well-formed one, so
// only the operator can say.
//
// Nothing else about a cast is configurable this way on purpose. Every other axis is
// inferred from evidence castor holds (a probe, advertised capabilities), and a renderer
// that misbehaves there is a capability-data fix, not a knob.
//
// It lives beside Renderer.SelfFetch because it is the operator's answer to the same
// question, and it lives in this leaf package because the recovery loop that flips it
// performs no I/O of its own: a spine that had to import the delivery driver to name this
// value would link an ffmpeg, a device adapter and two HTTP servers to say "serve".
type DeliveryPreference string

const (
	// DeliveryAuto leaves the decision to the evidence (see core.Shape.Passthrough). It is
	// also what an unset key means, so the zero value needs no default.
	DeliveryAuto DeliveryPreference = "auto"
	// DeliveryServe refuses pass-through for every cast: castor reads the source and serves
	// the renderer a local stream, whatever the source looks like.
	DeliveryServe DeliveryPreference = "serve"
)

// AcceptsContainer reports whether the device plays contentType directly over
// the network (the pass-through decision).
func (r Renderer) AcceptsContainer(contentType string) bool {
	return slices.Contains(r.Containers, contentType)
}

// CanCopyVideo reports whether a probed source video can be stream-copied to
// this renderer instead of re-encoded.
func (r Renderer) CanCopyVideo(v ProbeInfo) bool {
	return slices.ContainsFunc(r.Video, func(s VideoSupport) bool { return s.accepts(v) })
}

// SupportsCodec reports whether the renderer decodes video codec c natively, and
// so whether the pipeline may target it when re-encoding.
func (r Renderer) SupportsCodec(c Codec) bool {
	return slices.ContainsFunc(r.Video, func(s VideoSupport) bool { return s.Codec == c })
}

// CanCopyAudio reports whether a probed source audio track can be stream-copied
// to this renderer instead of re-encoded.
func (r Renderer) CanCopyAudio(p ProbeInfo) bool {
	return slices.ContainsFunc(r.Audio, func(s AudioSupport) bool { return s.accepts(p) })
}

// SupportsAudioCodec reports whether the renderer decodes audio codec c natively,
// and so whether the pipeline may re-encode a multichannel source to it rather
// than downmix to stereo.
func (r Renderer) SupportsAudioCodec(c Codec) bool {
	return slices.ContainsFunc(r.Audio, func(s AudioSupport) bool { return s.Codec == c })
}

func (s AudioSupport) accepts(p ProbeInfo) bool {
	if p.AudioCodec != s.Codec {
		return false
	}
	// An unknown source channel count (0) is trusted: a matching codec with no
	// probed layout is not worth force-transcoding. A known count above the
	// renderer's advertised ceiling is not copy-eligible.
	if s.MaxChannels > 0 && p.AudioChannels > s.MaxChannels {
		return false
	}
	return true
}

func (s VideoSupport) accepts(v ProbeInfo) bool {
	if v.VideoCodec != s.Codec {
		return false
	}
	if len(s.Profiles) > 0 && !slices.Contains(s.Profiles, v.VideoProfile) {
		return false
	}
	depths := s.BitDepths
	if len(depths) == 0 {
		depths = []int{8}
	}
	return slices.Contains(depths, v.VideoBitDepth)
}

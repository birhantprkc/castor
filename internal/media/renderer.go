package media

import (
	"fmt"
	"slices"
)

type Capabilities struct {
	Containers []string
	Video      []VideoSupport
	Audio      []AudioSupport

	// SelfFetch reports whether the renderer fetches a stream URL itself.
	SelfFetch bool

	// ServedContainer is what castor muxes on a served cast that remuxes.
	ServedContainer string
}

// VideoSupport is one video envelope a renderer decodes natively (codec, profile, bit depth only).
type VideoSupport struct {
	Codec     Codec
	Profiles  []Profile // nil or empty = any profile
	BitDepths []int     // nil or empty = {8}
}

// AudioSupport is one audio codec a renderer decodes natively, up to MaxChannels channels.
type AudioSupport struct {
	Codec Codec
	// MaxChannels is the highest channel count the renderer decodes (0 = no advertised ceiling).
	MaxChannels int
}

// AcceptsContainer reports whether the device plays contentType directly (the pass-through decision).
func (r Capabilities) AcceptsContainer(contentType string) bool {
	return slices.Contains(r.Containers, contentType)
}

func (r Capabilities) CanCopyVideo(v ProbeInfo) bool {
	return slices.ContainsFunc(r.Video, func(s VideoSupport) bool { return s.accepts(v) })
}

// SupportsCodec reports whether the renderer decodes video codec c natively.
func (r Capabilities) SupportsCodec(c Codec) bool {
	return slices.ContainsFunc(r.Video, func(s VideoSupport) bool { return s.Codec == c })
}

func (r Capabilities) CanCopyAudio(p ProbeInfo) bool {
	return slices.ContainsFunc(r.Audio, func(s AudioSupport) bool { return s.accepts(p) })
}

// SupportsAudioCodec reports whether the renderer decodes audio codec c natively.
func (r Capabilities) SupportsAudioCodec(c Codec) bool {
	return slices.ContainsFunc(r.Audio, func(s AudioSupport) bool { return s.Codec == c })
}

func (s AudioSupport) accepts(p ProbeInfo) bool {
	if p.AudioCodec != s.Codec {
		return false
	}
	// Unknown source channel count (0) is trusted: don't force-transcode a matching codec with no layout.
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

type Gone struct {
	// Renderer is the set's name (or where castor reached it if it has none).
	Renderer string

	// Observed is the family's account of how it established this (for humans to read).
	Observed string

	// Err is the last failure the family saw (the only account of HOW).
	Err error
}

func (g *Gone) Error() string {
	msg := fmt.Sprintf("renderer %q is unreachable", g.Renderer)
	if g.Observed != "" {
		msg += ": " + g.Observed
	}
	if g.Err != nil {
		msg += ": " + g.Err.Error()
	}
	return msg
}

func (g *Gone) Unwrap() error { return g.Err }

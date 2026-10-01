package compose

import (
	"cmp"
	"fmt"

	"github.com/stupside/castor/internal/media"
)

// Shape: renderer, source, ceiling, operator preference; pure data, no I/O.
type Shape struct {
	// Static profile or negotiated capabilities; zeros mark unmeasured fields.
	Renderer media.Capabilities

	// Program is the normalized set of inputs and selected tracks this cast reads.
	Program media.Program

	// Picture height from source declaration or probe; 0 means unmeasured.
	Height int

	// Height ceiling; binds composition choice, not just encode.
	MaxHeight media.HeightCap

	// Operator preference on delivery axis.
	Delivery DeliveryPreference
}

// DeliveryPreference is whether a self-fetching renderer may be handed the source URL.
type DeliveryPreference string

const (
	// DeliveryAuto leaves the decision to the evidence (also the zero value).
	DeliveryAuto DeliveryPreference = "auto"
	// DeliveryServe refuses pass-through: castor reads the source and serves the renderer locally.
	DeliveryServe DeliveryPreference = "serve"
)

// passthrough: renderer self-fetches, source reachable, container accepted, height admits.
func (s Shape) passthrough() bool {
	primary, ok := s.Program.PrimaryInput()
	if !ok || s.Delivery == DeliveryServe {
		return false
	}
	probe, measured := s.Program.Measurement()
	return s.Renderer.SelfFetch && s.Program.SelfFetchable() &&
		s.Renderer.AcceptsContainer(primary.ContentType) && measured &&
		canPlay(s.Renderer, probe) && s.MaxHeight.Admits(s.Height)
}

// String reports composition and refusal reasons; heights load-bearing for users.
func (s Shape) String() string {
	// Handles invalid programs; no container name if clock names missing input.
	sourceContentType := "unknown"
	if primary, ok := s.Program.PrimaryInput(); ok {
		sourceContentType = cmp.Or(primary.ContentType, "unknown")
	}
	return fmt.Sprintf("self_fetch=%v served_container=%s source_content_type=%s source_self_fetchable=%v source_header_keys=%v source_height=%d max_height=%d delivery=%s",
		s.Renderer.SelfFetch,
		cmp.Or(s.Renderer.ServedContainer, "none"),
		sourceContentType,
		s.Program.SelfFetchable(),
		s.Program.HeaderKeys(),
		s.Height, s.MaxHeight,
		cmp.Or(s.Delivery, DeliveryAuto))
}

// canPlay reports whether the whole measured program is safe to hand over: HDR is never assumed to engage.
func canPlay(r media.Capabilities, p media.ProbeInfo) bool {
	if p.VideoCodec == "" || p.VideoHDR || !r.CanCopyVideo(p) {
		return false
	}
	return p.AudioCodec == "" || r.CanCopyAudio(p)
}

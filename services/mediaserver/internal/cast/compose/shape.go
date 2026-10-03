package compose

import (
	"cmp"
	"fmt"

	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// Shape is the device, the source, the ceiling and the operator's preference: pure data, no I/O.
type Shape struct {
	// Device is what the lent device decodes, and whether it fetches for itself.
	Device media.Capabilities

	// Program is the normalized set of inputs and selected tracks this cast reads.
	Program media.Program

	// Height is the picture the device would fetch, declared or probed; 0 is unmeasured.
	Height int

	// MaxHeight binds the composition, not just the encode.
	MaxHeight media.HeightCap

	Delivery DeliveryPreference
}

// DeliveryPreference is whether a self-fetching device may be handed the source URL.
type DeliveryPreference string

const (
	// DeliveryAuto leaves the decision to the evidence (also the zero value).
	DeliveryAuto DeliveryPreference = "auto"
	// DeliveryServe refuses the handoff: castor reads the source and serves the device locally.
	DeliveryServe DeliveryPreference = "serve"
)

// handoff is a device that fetches for itself and plays the source as it is, within the ceiling.
func (s Shape) handoff() bool {
	primary, ok := s.Program.PrimaryInput()
	if !ok || s.Delivery == DeliveryServe {
		return false
	}
	probe, measured := s.Program.Measurement()
	return s.Device.SelfFetch && s.Program.SelfFetchable() &&
		s.Device.AcceptsContainer(primary.ContentType) && measured &&
		canPlay(s.Device, probe) && s.MaxHeight.Admits(s.Height)
}

// String is the shape as the composition line logs it.
func (s Shape) String() string {
	sourceContentType := "unknown"
	if primary, ok := s.Program.PrimaryInput(); ok {
		sourceContentType = cmp.Or(primary.ContentType, "unknown")
	}
	return fmt.Sprintf("self_fetch=%v served_container=%s source_content_type=%s source_self_fetchable=%v source_header_keys=%v source_height=%d max_height=%d delivery=%s",
		s.Device.SelfFetch,
		cmp.Or(s.Device.ServedContainer, "none"),
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

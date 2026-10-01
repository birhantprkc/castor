// Package wire translates castor's own types to the cast contract and back, with no state and no I/O.
package wire

import (
	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/media"
)

// Capabilities is c on the wire.
func Capabilities(c media.Capabilities) *castorv1.Capabilities {
	out := &castorv1.Capabilities{
		Containers:      c.Containers,
		SelfFetch:       c.SelfFetch,
		ServedContainer: c.ServedContainer,
		Deinterlaces:    c.Deinterlaces,
	}
	for _, v := range c.Video {
		vs := &castorv1.VideoSupport{Codec: string(v.Codec), MaxLevel: int32(v.MaxLevel)}
		for _, p := range v.Profiles {
			vs.Profiles = append(vs.Profiles, string(p))
		}
		for _, b := range v.BitDepths {
			vs.BitDepths = append(vs.BitDepths, int32(b))
		}
		out.Video = append(out.Video, vs)
	}
	for _, a := range c.Audio {
		out.Audio = append(out.Audio, &castorv1.AudioSupport{Codec: string(a.Codec), MaxChannels: int32(a.MaxChannels)})
	}
	return out
}

// FromCapabilities is what the wire says a renderer decodes.
func FromCapabilities(c *castorv1.Capabilities) media.Capabilities {
	out := media.Capabilities{
		Containers:      c.GetContainers(),
		SelfFetch:       c.GetSelfFetch(),
		ServedContainer: c.GetServedContainer(),
		Deinterlaces:    c.GetDeinterlaces(),
	}
	for _, v := range c.GetVideo() {
		vs := media.VideoSupport{Codec: media.Codec(v.GetCodec()), MaxLevel: int(v.GetMaxLevel())}
		for _, p := range v.GetProfiles() {
			vs.Profiles = append(vs.Profiles, media.Profile(p))
		}
		for _, b := range v.GetBitDepths() {
			vs.BitDepths = append(vs.BitDepths, int(b))
		}
		out.Video = append(out.Video, vs)
	}
	for _, a := range c.GetAudio() {
		out.Audio = append(out.Audio, media.AudioSupport{Codec: media.Codec(a.GetCodec()), MaxChannels: int(a.GetMaxChannels())})
	}
	return out
}

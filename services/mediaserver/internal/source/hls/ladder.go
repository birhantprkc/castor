package hls

import (
	"cmp"
	"slices"

	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/source"
)

// byBitrate prefers the richest rung, then the taller: BANDWIDTH is the one attribute every variant must state, while RESOLUTION is optional.
func byBitrate(a, b source.Rendition) int {
	return cmp.Or(cmp.Compare(a.Bitrate, b.Bitrate), cmp.Compare(a.Height, b.Height))
}

// castable narrows a variant list to those that carry video (audio-only variants must not be picked).
func castable(doc document) []variant {
	withVideo := slices.DeleteFunc(slices.Clone(doc.variants), func(v variant) bool {
		audio := doc.audioFor(v)
		return !v.hasVideo || (audio != nil && v.url.String() == audio.String())
	})
	if len(withVideo) == 0 {
		return doc.variants
	}
	return withVideo
}

// ladder is the choice the source offered: publication order preserved, audio-only rungs excluded.
func ladder(doc document, probe *media.ProbeInfo) []source.Rendition {
	measured := measuredHeights(doc, probe)
	rungs := castable(doc)
	out := make([]source.Rendition, len(rungs))
	for i, v := range rungs {
		out[i] = rendition(doc, v, measured[v.program])
	}
	return out
}

// measuredHeights is each variant's picture as a probe of this very master measured it, by program.
func measuredHeights(doc document, probe *media.ProbeInfo) map[int]int {
	if probe == nil || !doc.multivariant {
		return nil
	}
	// A probe that saw another program count read another document than the one parsed.
	variants := 0
	for _, v := range doc.variants {
		variants = max(variants, v.program+1)
	}
	if len(probe.ProgramHeights) != variants {
		return nil
	}
	return probe.ProgramHeights
}

// rendition translates one variant without dropping its companion audio; a probe of this very master wins over RESOLUTION, since it numbers variants as the master does.
func rendition(doc document, v variant, measured int) source.Rendition {
	height := cmp.Or(measured, v.height)
	declared := v.declared
	if declared != nil && declared.VideoHeight != height {
		stated := declared.Clone()
		stated.VideoHeight = height
		declared = &stated
	}
	return source.Rendition{
		URL:      v.url,
		AudioURL: doc.audioFor(v),
		Bitrate:  media.Bitrate(v.bandwidth),
		Height:   height,
		Declared: declared,
	}
}

// backedBy is r, with what the caller already knew of the rung filling what this document left unsaid.
func backedBy(r, prior source.Rendition) source.Rendition {
	if r.AudioURL == nil {
		r.AudioURL = prior.AudioURL
	}
	if r.Declared == nil {
		r.Declared = prior.Declared
	}
	return r
}

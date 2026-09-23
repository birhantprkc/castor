package dash

import (
	"cmp"
	"context"
	"log/slog"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// DASH: ladder published as streams, not documents; rendition pick = stream index.

// representations: ladder from probe heights; no URL (at manifest), no bitrate.
func representations(probe *media.ProbeInfo) []source.Rendition {
	if probe == nil {
		return nil
	}
	rungs := make([]source.Rendition, 0, len(probe.VideoHeights))
	for i, height := range probe.VideoHeights {
		if height <= 0 {
			continue
		}
		// i is original 0:V:N position; compacting unknown-height must not renumber.
		rungs = append(rungs, source.Rendition{Index: i, Height: height})
	}
	return rungs
}

// byHeight prefers the tallest rung: a DASH rung is a stream index, not a document with a declared rate.
func byHeight(a, b source.Rendition) int { return cmp.Compare(a.Height, b.Height) }

// reportNoRendition: INFO when no measure (nothing failed, cost stated).
func reportNoRendition(ctx context.Context, stream source.Candidate, ceiling media.HeightCap) {
	slog.InfoContext(ctx, "adaptive source with no measured representation: castor picked no rendition, so the height cap did not bind the read",
		"url", stream.URL.String(), "cap", int(ceiling), "content_type", stream.ContentType)
}

package recovery

import (
	"cmp"
	"context"
	"log/slog"

	"github.com/stupside/castor/services/mediaserver/internal/cast/compose"
	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/source"
)

// change provides everything a strategy needs to decide what to try next.
type change struct {
	intent   Intent
	attempt  Attempt
	outcome  Outcome
	resolver SourceResolver
}

// strategy is one named recovery, applying only where the facts allow it.
type strategy struct {
	name string
	why  string
	// apply only ever moves an attempt down a finite order, so recovery ends.
	apply func(context.Context, change) (Attempt, bool)
}

// switchCandidate reads the next link the ranker admitted.
var switchCandidate = strategy{
	name: "switch-candidate",
	why:  "read the next link the ranker admitted, which was measured and opened like this one",
	apply: func(ctx context.Context, c change) (Attempt, bool) {
		a, ok := nextReadable(ctx, c.intent, c.resolver, c.attempt)
		if !ok {
			return c.attempt, false
		}
		// Packets the last link broke on do not condemn this one's.
		a.Decode = media.Axes{}
		return a, true
	},
}

// nextReadable resolves the links after a's in rank order, moving past any that cannot be resolved.
func nextReadable(ctx context.Context, in Intent, resolver SourceResolver, a Attempt) (Attempt, bool) {
	for next := a.candidate + 1; next < len(in.Candidates); next++ {
		link := in.Candidates[next]
		resolved, err := resolver.Resolve(ctx, link, source.Rendition{})
		if err != nil {
			slog.WarnContext(ctx, "a link could not be resolved; moving past it",
				"url", link.URL.String(), "error", err)
			continue
		}
		a.candidate = next
		return a.reading(resolved, in.Deadline), true
	}
	return a, false
}

// degradeRendition reads the heaviest rung the measured link can carry, not merely the next one down.
var degradeRendition = strategy{
	name: "degrade-rendition",
	why:  "read the heaviest rung of the same program the measured link can carry",
	apply: func(ctx context.Context, c change) (Attempt, bool) {
		a := c.attempt
		speed := c.outcome.Evidence.Vitals.Speed
		if a.Origin.Sole() || a.Rendition.Bitrate <= 0 || speed <= 0 {
			return a, false
		}

		// Capped at the current rung, so the move is down even when the measurement says more.
		carried := media.Bitrate(float64(a.Rendition.Bitrate) * float64(speed))
		lighter := a.Origin.Lighter(min(carried, a.Rendition.Bitrate))
		if len(lighter) == 0 {
			return a, false
		}

		rung := lighter[0]
		primary, ok := a.Program.PrimaryInput()
		// A rung is reached by its own URL, or by name inside the manifest the program already reads.
		if !ok || (rung.URL == nil && rung.Representation == "") {
			return a, false
		}
		source := source.Stream{
			URL: cmp.Or(rung.URL, primary.URL), Headers: primary.Headers.Clone(), ContentType: primary.ContentType,
		}
		resolved, err := c.resolver.Resolve(ctx, &source, rung)
		if err != nil {
			slog.WarnContext(ctx, "the lighter rendition's documents could not be read; declining an unsafe fallback",
				"url", source.URL.String(), "representation", rung.Representation, "error", err)
			return a, false
		}

		// Resolve reads the selected media playlist, which reports itself as a sole rendition.
		origin := resolved.Origin
		origin.Renditions = a.Origin.Renditions
		origin.Segmented = a.Origin.Segmented
		origin.Live = origin.Live || a.Origin.Live
		if origin.Duration == 0 {
			origin.Duration = a.Origin.Duration
		}

		a = a.reading(resolved, c.intent.Deadline)
		a.Origin, a.Rendition = origin, rung
		return a, true
	},
}

// decodeAxis decodes the packets the reader died copying.
var decodeAxis = strategy{
	name: "decode-axis",
	why:  "stop copying the packets the reader died on and decode them, which is what a truncated bitstream needs",
	apply: func(_ context.Context, c change) (Attempt, bool) {
		a := c.attempt
		next := a.Decode.Or(c.outcome.Evidence.Copied)
		if next == a.Decode {
			return a, false
		}
		a.Decode = next
		return a, true
	},
}

// relaxRead asks for the same link at playback pace, with no burst for an origin to stall on.
var relaxRead = strategy{
	name: "relax-read",
	why:  "ask for the same link at playback pace with no wire-speed burst, in case the burst is what it stopped answering",
	apply: func(_ context.Context, c change) (Attempt, bool) {
		a := c.attempt
		plan, ok := a.Fetch.Cautious()
		if !ok {
			return a, false
		}
		a.Fetch = plan
		return a, true
	},
}

// serveInstead serves a device that refused to fetch the source itself.
var serveInstead = strategy{
	name: "serve-instead",
	why:  "read the source and serve it locally, since the device would not fetch it itself",
	apply: func(_ context.Context, c change) (Attempt, bool) {
		a := c.attempt
		// A device already served would refuse the next attempt's identical serve.
		if a.Delivery == compose.DeliveryServe || !c.outcome.Evidence.Handoff {
			return a, false
		}
		a.Delivery = compose.DeliveryServe
		return a, true
	},
}

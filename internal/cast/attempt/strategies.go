package attempt

import (
	"cmp"
	"context"
	"log/slog"

	"github.com/stupside/castor/internal/cast/compose"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

// change provides everything a strategy needs to decide what to try next.
type change struct {
	intent   Intent
	attempt  Attempt
	outcome  Outcome
	resolver SourceResolver // Re-establishes media URL; only I/O provider here.
}

// strategy is one named recovery; applicability is fact not condition.
type strategy struct {
	name  string                                        // Identifies strategy in revision line and refusal list.
	why   string                                        // Describes intended change benefit.
	apply func(context.Context, change) (Attempt, bool) // Moves attempt strictly DOWN finite order.
}

// switchCandidate reads the next link the ranker admitted (load-bearing recovery).
var switchCandidate = strategy{
	name: "switch-candidate",
	why:  "read the next link the ranker admitted, which was measured and opened like this one",
	apply: func(ctx context.Context, c change) (Attempt, bool) {
		a, ok := nextReadable(ctx, c.intent, c.resolver, c.attempt)
		if !ok {
			return c.attempt, false
		}
		// Different bitstream, so clear decode axes (packets from last link can't condemn this one).
		a.Decode = media.Axes{}
		return a, true
	},
}

// nextReadable resolves the links after a's in rank order, moving past any that cannot be resolved.
func nextReadable(ctx context.Context, in Intent, resolver SourceResolver, a Attempt) (Attempt, bool) {
	for next := a.candidate + 1; next < len(in.Candidates); next++ {
		link := in.Candidates[next]
		resolved, err := resolver.RefetchProgram(ctx, link, source.Rendition{})
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

// degradeRendition reads the heaviest rung measured to be achievable (not just one lower).
var degradeRendition = strategy{
	name: "degrade-rendition",
	why:  "read the heaviest rung of the same program the measured link can carry",
	apply: func(ctx context.Context, c change) (Attempt, bool) {
		// Stated upfront; the three ways this recovery fails are its whole contract.
		a := c.attempt
		speed := c.outcome.Evidence.Health.Speed
		if a.Origin.Sole() || a.Rendition.Bitrate <= 0 || speed <= 0 {
			return a, false
		}

		// Ceiling capped by current rung, so move is strictly down even if measurement says higher.
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
		source := source.Candidate{
			URL: cmp.Or(rung.URL, primary.URL), Headers: primary.Headers.Clone(), ContentType: primary.ContentType,
		}
		resolved, err := c.resolver.RefetchProgram(ctx, &source, rung)
		if err != nil {
			slog.WarnContext(ctx, "the lighter rendition's documents could not be read; declining an unsafe fallback",
				"url", source.URL.String(), "representation", rung.Representation, "error", err)
			return a, false
		}

		// Refetch reads the selected media playlist, which naturally reports itself as a sole rendition.
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

// decodeAxis stops copying packets reader failed on and decodes instead (recovery for unresumed bitstreams).
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

// relaxRead asks for same link at playback pace with no wire-speed burst (recovery for tarpit stalls).
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

// serveInstead serves local stream instead of handing URL (recovery for renderer refusal).
var serveInstead = strategy{
	name: "serve-instead",
	why:  "read the source and serve it locally, since the renderer would not fetch it itself",
	apply: func(_ context.Context, c change) (Attempt, bool) {
		a := c.attempt
		// A renderer already served would refuse the next attempt's identical serve.
		if a.Delivery == compose.DeliveryServe || !c.outcome.Evidence.Handoff {
			return a, false
		}
		a.Delivery = compose.DeliveryServe
		return a, true
	},
}

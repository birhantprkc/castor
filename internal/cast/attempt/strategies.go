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
	Intent   Intent
	Attempt  Attempt
	Outcome  Outcome
	Resolver SourceResolver // Re-establishes media URL; only I/O provider here.
}

// strategy is one named recovery; applicability is fact not condition.
type strategy struct {
	Name  string                                        // Identifies strategy in revision line and refusal list.
	Why   string                                        // Describes intended change benefit.
	Apply func(context.Context, change) (Attempt, bool) // Moves attempt strictly DOWN finite order.
}

// switchCandidate reads the next link the ranker admitted (load-bearing recovery).
var switchCandidate = strategy{
	Name: "switch-candidate",
	Why:  "read the next link the ranker admitted, which was measured and opened like this one",
	Apply: func(ctx context.Context, c change) (Attempt, bool) {
		a, ok := nextReadable(ctx, c.Intent, c.Resolver, c.Attempt)
		if !ok {
			return c.Attempt, false
		}
		// Different bitstream, so clear decode axes (packets from last link can't condemn this one).
		a.Decode = media.Axes{}
		return a, true
	},
}

// nextReadable resolves the links after a's in rank order, moving past any that cannot be resolved.
func nextReadable(ctx context.Context, in Intent, resolver SourceResolver, a Attempt) (Attempt, bool) {
	for next := a.Candidate + 1; next < len(in.Candidates); next++ {
		link := in.Candidates[next]
		resolved, err := resolver.RefetchProgram(ctx, link, source.Rendition{})
		if err != nil {
			slog.WarnContext(ctx, "a link could not be resolved; moving past it",
				"url", link.URL.String(), "error", err)
			continue
		}
		a.Candidate = next
		return a.reading(resolved, in.Deadline), true
	}
	return a, false
}

// degradeRendition reads the heaviest rung measured to be achievable (not just one lower).
var degradeRendition = strategy{
	Name: "degrade-rendition",
	Why:  "read the heaviest rung of the same program the measured link can carry",
	Apply: func(ctx context.Context, c change) (Attempt, bool) {
		// Stated upfront; the three ways this recovery fails are its whole contract.
		a := c.Attempt
		speed := c.Outcome.Evidence.Health.Speed
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
		resolved, err := c.Resolver.RefetchProgram(ctx, &source, rung)
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

		a = a.reading(resolved, c.Intent.Deadline)
		a.Origin, a.Rendition = origin, rung
		return a, true
	},
}

// decodeAxis stops copying packets reader failed on and decodes instead (recovery for unresumed bitstreams).
var decodeAxis = strategy{
	Name: "decode-axis",
	Why:  "stop copying the packets the reader died on and decode them, which is what a truncated bitstream needs",
	Apply: func(_ context.Context, c change) (Attempt, bool) {
		a := c.Attempt
		next := a.Decode.Or(c.Outcome.Evidence.Copied)
		if next == a.Decode {
			return a, false
		}
		a.Decode = next
		return a, true
	},
}

// relaxRead asks for same link at playback pace with no wire-speed burst (recovery for tarpit stalls).
var relaxRead = strategy{
	Name: "relax-read",
	Why:  "ask for the same link at playback pace with no wire-speed burst, in case the burst is what it stopped answering",
	Apply: func(_ context.Context, c change) (Attempt, bool) {
		a := c.Attempt
		plan, ok := a.Read.Cautious()
		if !ok {
			return a, false
		}
		a.Read = plan
		return a, true
	},
}

// serveInstead serves local stream instead of handing URL (recovery for renderer refusal).
var serveInstead = strategy{
	Name: "serve-instead",
	Why:  "read the source and serve it locally, since the renderer would not fetch it itself",
	Apply: func(_ context.Context, c change) (Attempt, bool) {
		a := c.Attempt
		// A renderer already served would refuse the next attempt's identical serve.
		if a.Delivery == compose.DeliveryServe || !c.Outcome.Evidence.Handoff {
			return a, false
		}
		a.Delivery = compose.DeliveryServe
		return a, true
	},
}

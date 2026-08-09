// Package cast turns the links a ranker admitted for one title into playback on a
// renderer.
//
// It is device-agnostic and carries no device-type switch. Play resolves the head of
// that ordering (renderer-independent), states the whole cast as an attempt.Intent,
// and hands it to the attempt loop over the pipeline executor: the loop decides what
// to try and what to try instead, and the executor composes the cast from what the
// renderer can do and runs it. Every device concern lives either in the device
// adapter (internal/device) or is expressed as capability data a composition rule
// reads, so no device family is a distinct code path here.
package cast

import (
	"context"
	"errors"
	"slices"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/burnin"
	"github.com/stupside/castor/internal/cast/core"
	"github.com/stupside/castor/internal/cast/pipeline"
	"github.com/stupside/castor/internal/cast/read"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source/resolve"
)

// Play casts a title to the configured device, given the ordering the ranker
// admitted for it, best link first. Source resolution is renderer-independent and
// happens here; connecting the renderer is left to the executor, which times it
// against the delivery path: a non-self-fetching renderer connects concurrently
// with the pull, so slow discovery does not age a short-lived signed URL before
// the first byte. There is deliberately no device-type branch: adding a renderer
// family is a device adapter plus a set of capability values.
//
// It takes the whole ordering rather than its head, and that is what arms the
// recovery the resilience loop was built around. The ranker measures every
// candidate and returns them in the order a cast should walk them precisely so
// that a link which cannot deliver can be abandoned for one which was measured
// alongside it: the run this exists for held four alternatives, two of them
// probing cleanly, while the head delivered 33 KB in thirty seconds and the
// renderer read nothing at all.
//
// Only the head is resolved here. What each of the others publishes is established
// if and when a cast moves onto it (see attempt.Program), because a playlist GET
// per candidate up front is a burst against exactly the hosts the ranker limits
// its probes per host to protect, spent on links most casts never read.
//
// Resolution yields three facts a cast needs: the stream castor will read, what
// the source published about the program behind it (see media.Origin), and which
// rung of that ladder was chosen. All three are renderer-independent, which is why
// they are established here and stated on the intent rather than re-derived
// downstream.
func Play(ctx context.Context, cfg Config, candidates []*media.Stream) error {
	if len(candidates) == 0 {
		return errors.New("nothing to cast: the ranker admitted no link")
	}
	resolved, origin, rung, localIP, err := core.ResolveSource(ctx, cfg.Config, candidates[0])
	if err != nil {
		return err
	}

	// How the source is fetched, decided once from what it published and then carried
	// by every reader that touches it. A shape no row answers stops the cast rather
	// than reading on whatever terms a zero policy renders, which are none.
	policy, err := read.For(read.ShapeOf(origin), cfg.Transcode.RWTimeout)
	if err != nil {
		return err
	}

	// The resolved head takes its own place in the ordering, so the record of what a cast
	// tried stays the ranking it was given: candidate 0 is the link that was read, spelled
	// as resolution left it, and every index below it is still the ranker's own.
	links := slices.Clone(candidates)
	links[0] = resolved

	// How many attempts this can make is a property of the material and not a limit inside
	// the loop: the links the ranker admitted, and the rungs published below the one being
	// read.
	return attempt.Cast(ctx, attempt.Intent{
		Candidates: links,
		Origin:     origin,
		Rendition:  rung,
		Read:       policy,
		Deadline:   cfg.Transcode.RWTimeout,
		Delivery:   cfg.Delivery,
	}, pipeline.NewExecutor(cfg.Config, core.Connect, burnInStage, localIP), resolve.NewPrograms(cfg.Source))
}

// burnInStage is the one Stage a production cast can run, named here because the composition
// root is the only party that may name a cgo mechanism (see internal/cast/burnin). Both ways of
// ending up with none are decided here and only here, so nothing downstream re-reads the answer
// off a nil pointer: the operator did not ask for subtitles, or whisper could not start, and the
// second downgrades to a subtitle-less cast rather than blocking playback.
func burnInStage(ctx context.Context, cfg core.Config, workDir string) pipeline.Stage {
	if core.SubtitleForServed(cfg) != core.SubtitleBurnIn {
		return nil
	}
	// Returned through the interface only once it is known to be non-nil: a typed nil pointer
	// would be a Stage that exists as far as every caller is concerned.
	if s := burnin.New(ctx, cfg.Whisper, workDir); s != nil {
		return s
	}
	return nil
}

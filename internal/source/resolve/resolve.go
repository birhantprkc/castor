package resolve

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/stupside/castor/internal/media"
)

// Source resolution runs in two phases and this file holds the entry point to each,
// because they answer different questions at prices that must not be mixed up.
// RankStreams asks which of many candidates is worth attempting and pays in
// measurements, so it fetches no documents at all (rank.go): one GET per candidate
// against the hosts the per-host probe cap exists to protect is how a whole ranking
// earns a 429. Resolve asks what the one chosen source publishes and pays in one or
// two plain GETs of documents the reader opens next anyway (program.go).

// Resolver is this package's policy with its adapters bound: which candidate is
// worth casting, which rendition of it to read, and whether a renderer could
// fetch it unaided. It measures nothing and fetches nothing itself (see ports.go),
// so every decision below is reachable from a test holding a scripted Measurer and
// a fixture playlist, which is what the same decisions welded to an exec call and
// an http.Client were not.
type Resolver struct {
	cfg       Config
	measurer  Measurer
	playlists Playlists
}

// New binds the policy to the adapters the composition root built. Config is
// carried for the two things the policy itself reads, the height ceiling and the
// probe fan-out; the paths and budgets in it belong to the adapters and are spent
// building them (see internal/config).
func New(cfg Config, measurer Measurer, playlists Playlists) *Resolver {
	return &Resolver{cfg: cfg, measurer: measurer, playlists: playlists}
}

// Resolve establishes the facts a cast needs about its source, in the order they
// depend on each other: what the source is, which of its renditions to read,
// whether a renderer could fetch it unaided, and what the source published about the
// program behind all of that. Only the fields resolution establishes are rewritten
// on the stream; everything else is preserved.
//
// The media.Origin is returned beside the stream rather than folded into it because
// the two are different kinds of fact with different lifetimes. The stream is what
// castor will read, and every stage rewrites it; the Origin is what the source
// offered, and it must stay true after the choice was made. Folding the ladder into
// the stream is how it used to be lost: the variant list was reduced to one URL and
// the alternatives ceased to exist.
//
// The chosen rendition is a third kind of fact, and a third value for the same reason:
// it is neither published nor read, it is the choice made from the ladder, and it is the
// only statement of which rung a cast is reading (see program). It is zero for a source
// that published no ladder, which a caller must read as the absence of evidence and
// never as a free rendition.
func (r *Resolver) Resolve(ctx context.Context, stream *media.Stream) (*media.Stream, media.Origin, media.Rendition, error) {
	info, err := r.identify(ctx, stream)
	if err != nil {
		return nil, media.Origin{}, media.Rendition{}, err
	}
	origin := media.Origin{Live: stream.Live}
	if info != nil {
		// Free, and the only duration a non-segmented source has: the measurement was
		// already spent naming the container, and a whole file publishes no document to
		// state its runtime.
		origin.Duration = info.Duration
	}
	// The remaining facts are HLS's alone: it is the only container that publishes one
	// program across renditions, the only one whose document states how its segments
	// are framed, and the only one castor opens with relaxed checks.
	var chosen media.Rendition
	if stream.ContentType == media.HLS {
		origin, chosen = r.program(ctx, stream, origin)
		r.verifyRendererCanFetch(ctx, stream)
	}
	return stream, origin, chosen, nil
}

// RankStreams measures every candidate and returns the order a cast walks them in,
// best first. It is thin on purpose: the measurement is a port (see ports.go), the
// admission of a candidate is a table and the ordering is one comparison, all of
// them in rank.go, so what remains here is the sequence those three run in.
//
// An ordering rather than a winner, because the head is not the only answer worth
// having: --dry-run prints exactly what a cast would attempt, in the order it would
// attempt it, instead of a second probe loop that answered a different question.
//
// Nothing is admitted on hope. A candidate the origin refused outright is dropped,
// and one castor merely failed to measure is admitted below every measured
// candidate, so the pool can never present a link castor has proved dead as its
// best stream. When nothing is admitted the failure names the shapes it saw, which
// is the difference between "extraction is broken" and "these links have expired,
// extract again".
func (r *Resolver) RankStreams(ctx context.Context, streams []*media.Stream) ([]*media.Stream, error) {
	slog.InfoContext(ctx, "ranking streams", "count", len(streams))
	if len(streams) == 0 {
		return nil, fmt.Errorf("no streams to rank")
	}
	streams = limitPerHost(ctx, streams)

	pool := make([]candidate, 0, len(streams))
	rejected := make(map[reason]int)
	for _, m := range r.measureAll(ctx, streams) {
		v := admit(m)
		if !v.admit {
			rejected[v.reason]++
			logRejection(ctx, m, v)
			continue
		}
		pool = append(pool, admitted(m, v))
	}
	if len(pool) == 0 {
		return nil, fmt.Errorf("no castable stream: none of %d candidates was admitted (%s)", len(streams), tally(rejected))
	}
	if len(rejected) > 0 {
		slog.InfoContext(ctx, "rejected candidates", "kept", len(pool), "detail", tally(rejected))
	}

	order := ranked(pool, r.cfg.MaxHeight)
	best := order[0]
	slog.InfoContext(ctx, "best stream selected", "url", best.stream.URL.String(),
		"bitrate", best.stream.Bandwidth, "height", best.height, "last_resort", best.lastResort,
		// The renditions the captured document advertised, which is the first thing worth
		// knowing when a cast later reports it has nothing lighter to fall back to.
		"renditions", best.stream.Ladder, "alternatives", len(order)-1)

	out := make([]*media.Stream, len(order))
	for i, c := range order {
		out[i] = c.stream
	}
	return out, nil
}

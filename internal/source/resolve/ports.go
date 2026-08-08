package resolve

import (
	"context"
	"net/http"
	"net/url"

	"github.com/stupside/castor/internal/media"
)

// This file holds the two things this package needs done for it. Both are
// declared here, at the consumer, and both name only the methods the policy above
// drives, in the manner of core.Renderer: the interesting part of source
// resolution is the judgement (which candidate is real, which rendition to read,
// whether a renderer could fetch it unaided), and a judgement welded to a
// subprocess and a socket is a judgement nothing can test. The production
// adapters live in ./ffprobe and ./httpfetch and are bound at the composition
// root.

// Measurer measures one source. The two methods are the whole of what the rules
// above read about a candidate, which is what makes them exercisable with no
// ffprobe on PATH.
//
// OpensUnaided is a second method rather than a flag on Measure because its answer
// is a different fact about the source (whether a reader applying its own defaults
// can open it at all, i.e. media.Stream.NeedsLeniency) and because it is skipped
// whenever it cannot change the outcome: see verifyRendererCanFetch.
//
// A failed Measure returns a nil StreamInfo. Callers must read that as "nothing is
// known about this source", never as "this source carries nothing": the two lead
// to opposite decisions, since a candidate castor never managed to measure may
// still be readable by a puller that reconnects where ffprobe gave up.
//
// The media.Reach comes back beside the info rather than folded into the error
// because the admissions table keys its harshest row on it: an origin that answered
// and said no is dead for every reader, while a measurement castor abandoned at its
// own deadline is unproven and the puller may still land it. An implementation that
// cannot tell the two apart must answer ReachUnproven, which is the zero value, so
// silence here can only ever be lenient.
type Measurer interface {
	Measure(ctx context.Context, s *media.Stream) (*media.StreamInfo, media.Reach, error)
	OpensUnaided(ctx context.Context, s *media.Stream) bool
}

// Playlists fetches an HLS document and reports what the origin said. It is
// declared here so that rendition reduction stays exercised over fixture
// documents with no network.
//
// The status comes back separately from the error because the caller has to be
// able to tell a 403 (a dead signed link, which only a fresh extraction fixes)
// from a fetch that never got an answer (transient, and the reader's own
// reconnects may still land). A zero status means no answer arrived.
type Playlists interface {
	Fetch(ctx context.Context, u *url.URL, h http.Header) (body string, status int, err error)
}

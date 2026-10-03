package origin

import (
	"net/http"
	"path"
	"time"

	"github.com/stupside/castor/e2e/strategy"
)

// Behaviour is how the origin serves what it published: faithfully, as a live edge, or with hostility.
type Behaviour interface {
	strategy.Named
	Wrap(next http.Handler, p Published) http.Handler
}

// Edge marks a behaviour that serves the stream as a live edge, which a player joins wherever the edge is when it arrives.
type Edge interface{ LiveEdge() }

// Published is what a behaviour needs to know about the files it serves.
type Published struct {
	SegmentExt string
	// Since is when the origin started serving, the zero of any timeline.
	Since time.Time
	// Closing closes before the origin shuts down, releasing any request a behaviour holds.
	Closing <-chan struct{}
}

// Held blocks until the client leaves or the origin closes, whichever comes first.
func (p Published) Held(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-p.Closing:
	}
}

// IsSegment reports whether a request is for a media segment rather than a playlist or init segment.
func (p Published) IsSegment(r *http.Request) bool { return path.Ext(r.URL.Path) == p.SegmentExt }

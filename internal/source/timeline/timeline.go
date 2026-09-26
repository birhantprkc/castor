// Package timeline is the timeline castor keeps for a segmented origin: what the origin lists, merged into an HLS media playlist whose sequence only moves forward.
package timeline

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Segment is one piece of media as the origin lists it, every reference absolute.
type Segment struct {
	URI      string
	Duration time.Duration
	Range    Range
	Map      *Map
	Key      Key
	// Seam is a break the origin itself declares before this segment.
	Seam bool
	// Place is where the origin puts the segment on its own clock.
	Place Place
}

// Range is a byte range of a resource; a zero Length is the whole resource.
type Range struct{ Offset, Length int64 }

// Header is the Range request header that asks for r, which must have a length.
func (r Range) Header() string { return fmt.Sprintf("bytes=%d-%d", r.Offset, r.Offset+r.Length-1) }

// Map is the initialization section a segment is decoded with, encrypted under Key when one was in force.
type Map struct {
	URI   string
	Range Range
	Key   Key
}

// Key is how a segment is encrypted; the zero Key is clear.
type Key struct {
	Method, URI, IV, Format string
}

// IdentityFormat is the KEYFORMAT of a key whose URI serves the raw key bytes, as opposed to a DRM system's.
const IdentityFormat = "identity"

// Place names the run of numbering a segment belongs to and the span it covers, End being where the next begins.
type Place struct {
	Period     string
	Start, End int64
}

// Window is what the origin lists now.
type Window struct {
	Segments []Segment
	// Closed is an origin that will publish nothing more.
	Closed bool
	Start  *Start
}

// Start is where the origin asks playback to begin, from the first segment or, when negative, from the end.
type Start struct {
	Offset  time.Duration
	Precise bool
}

// Source yields the origin's current window each time it is asked, never twice at once, and reads the media it lists.
type Source interface {
	Window(ctx context.Context) (Window, error)
	// Read opens a resource the window names, as the input's own reader would ask for it.
	Read(ctx context.Context, uri string, r Range) (io.ReadCloser, error)
}

// Failure is an origin that answered a window request with a status.
type Failure struct {
	Status int
	Err    error
}

func (f *Failure) Error() string { return fmt.Sprintf("origin answered %d: %v", f.Status, f.Err) }

func (f *Failure) Unwrap() error { return f.Err }

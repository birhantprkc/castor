package source

import (
	"net/url"
	"time"
)

// Ladder is what a body says of the renditions it offers.
type Ladder int

const (
	// LadderUnknown is the zero value because reading the body is best-effort.
	LadderUnknown Ladder = iota
	// LadderMultivariant is a document advertising renditions: a master.
	LadderMultivariant
	// LadderSole is a playlist advertising no renditions: it IS the rendition.
	LadderSole
)

func (l Ladder) String() string {
	switch l {
	case LadderMultivariant:
		return "multivariant"
	case LadderSole:
		return "sole"
	default:
		return "unknown"
	}
}

// Reading is what a body says of itself in the grammar that recognised it.
type Reading struct {
	Ladder Ladder
	// Refs is every resource the body names, as written.
	Refs []string
	// Runtime is how long a document that ended plays; zero when it may still grow or never says.
	Runtime time.Duration
}

// Document is a Reading whose names are resolved against where the body came from.
type Document struct {
	Ladder  Ladder
	Names   []*url.URL
	Runtime time.Duration
}

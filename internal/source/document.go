package source

import (
	"net/url"
	"time"
)

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

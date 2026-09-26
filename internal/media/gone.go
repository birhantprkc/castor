package media

import "fmt"

type Gone struct {
	// Renderer is the set's name (or where castor reached it if it has none).
	Renderer string

	// Observed is the family's account of how it established this (for humans to read).
	Observed string

	// Err is the last failure the family saw (the only account of HOW).
	Err error
}

func (g *Gone) Error() string {
	msg := fmt.Sprintf("renderer %q is unreachable", g.Renderer)
	if g.Observed != "" {
		msg += ": " + g.Observed
	}
	if g.Err != nil {
		msg += ": " + g.Err.Error()
	}
	return msg
}

func (g *Gone) Unwrap() error { return g.Err }

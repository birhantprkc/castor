package attempt

import (
	"errors"

	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

// Phase is how far one attempt got: ORDER is the safety property, nothing seeks in castor.
type Phase int

const (
	// phaseUnstarted is an attempt that ended before anything was read.
	phaseUnstarted Phase = iota
	// PhaseReading is the source read landing media with no renderer pointed at anything yet.
	PhaseReading
	// PhaseOpening is the artifact a renderer will be pointed at being produced.
	PhaseOpening
	// PhasePlaying is the renderer holding the URL. This is the line a recovery does not cross.
	PhasePlaying
	// PhaseDelivered is the cast having run its course.
	PhaseDelivered
)

func (p Phase) String() string {
	switch p {
	case PhaseReading:
		return "reading"
	case PhaseOpening:
		return "opening"
	case PhasePlaying:
		return "playing"
	case PhaseDelivered:
		return "delivered"
	default:
		return "unstarted"
	}
}

// Outcome is what one attempt did: whether it worked, and the material to say why not.
type Outcome struct {
	Err error

	// Evidence is what the attempt left behind: the only material a classification rule reads.
	Evidence Evidence
}

// attribute decides whose account of a failure the cast reports: a verdict is left alone.
func attribute(err, readErr error) error {
	_, judged := errors.AsType[*watch.Fault](err)
	switch {
	case err == nil || readErr == nil || judged:
		return err
	case errors.Is(err, readErr):
		return readErr
	default:
		return errors.Join(readErr, err)
	}
}

type Evidence struct {
	// Reached is how far the attempt got, which decides whether the loop may still change its mind.
	Reached Phase

	Cancelled bool

	Verdict watch.Kind
	Health  watch.Health

	ReadErr error

	// ReadExit is the exit status that error came with.
	ReadExit int

	// ReadIncomplete is a read that ended short of what the source declared, whatever its exit status.
	ReadIncomplete bool

	Copied media.Axes

	// Buffered is a delivery that read castor's own buffer, which the playback gate had already proven.
	Buffered bool

	// PlayErr is the renderer's own refusal of the URL it was handed.
	PlayErr error

	// Handoff is a renderer pointed at the source itself rather than at what castor serves.
	Handoff bool

	// TimelineErr is a source whose timeline castor must keep and could not read when the read was set up.
	TimelineErr error

	Undelivered error

	// RendererGone is what an unplugged, crashed or switched-off set looks like from here.
	RendererGone *media.Gone

	// Lines is the retained stderr of the party being blamed. No rule DECIDES on it: prose is not a contract.
	Lines []string
}

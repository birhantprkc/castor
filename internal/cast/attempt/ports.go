package attempt

import (
	"context"

	"github.com/stupside/castor/internal/media"
)

// Runner executes one fully decided attempt and reports what happened. It is this
// layer's only outbound port: the loop knows how to choose the next attempt and nothing
// at all about pulling, encoding, serving or connecting.
//
// It is declared here, at the consumer, and names one method because one method is all
// the loop drives. That is what makes the recovery playbook exercisable with a scripted
// fake: the executor's own suite drives every case through real fixtures and two real
// ffmpeg processes and skips entirely on a host with none on PATH, so a rule that fires
// on the fifth minute of a starving 4K read had nowhere to be tested.
//
// It answers with an Outcome rather than an error because an error is one party's account
// of a cast and an attempt has three: the read that lands the media, the judgement that
// watches it, and the delivery that hands it over. Making the adapter produce one value
// is what forces those to be joined here instead of one of them travelling out under
// another's name.
type Runner interface {
	Run(ctx context.Context, a Attempt) Outcome
}

// Program is what the source layer establishes about ONE link: the stream to read, what
// that link publishes about the program behind it, and which rung of it was chosen.
//
// It is declared here because a cast that changes which link it reads has to ask, and
// because nothing else in this layer may: which rendition of a master to narrow to, under
// which height ceiling, and whether the source's tracks are published separately are the
// source layer's policy, and a loop that carried its own answers would be a second
// resolver disagreeing with the first.
//
// The ranker's ordering is measured but unresolved past its head, and reading an
// unresolved master is not a smaller version of reading a resolved one: it hands ffmpeg
// the variant list and lets the demuxer pick, which is how a cast escaping a 4K rung that
// cannot deliver arrives at another 4K rung, with no height cap applied, no companion
// audio rendition paired and nothing said about how the segments are framed.
//
// It answers on a COPY of the link it is given (see resolve.Programs). The ordering is
// the record of what a cast tried, and resolving in place would rewrite it.
type Program interface {
	Refetch(ctx context.Context, s *media.Stream) (*media.Stream, media.Origin, media.Rendition, error)
}

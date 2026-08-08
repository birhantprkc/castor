package watch

import (
	"time"

	"github.com/stupside/castor/internal/media"
)

// Producer is the upstream side of a cast as a health rule reads it: what it has
// delivered, whether it is over, why it ended, and the evidence that names a stall.
// Declared here because those four are all any rule below touches, and naming them is
// what lets the rules be exercised without an ffmpeg, a spool or a network.
//
// Progress carries media position and speed as well as bytes. That is the whole fix
// for the starved-upstream failure: bytes per second cannot be compared against
// anything on a source that publishes no bitrate, while media seconds per wall-clock
// second (ffmpeg's own speed=) is the comparison already made. A read that moved
// 14.4 MB and then nothing printed the same two numbers as one that had finished.
//
// The producer still decides nothing: this is an observation surface, so the pull's
// contract (a download that made its own decisions would have to be able to take them
// back, which means rewinding a spool something may already be reading) stays literally
// true.
type Producer interface {
	Progress() media.Progress
	Done() <-chan struct{}
	Err() error
	Evidence() []string
}

// Consumer is the renderer's side of a delivery as a health rule reads it: how many
// times it has come for bytes, and when it last did. Two numbers, because that is the
// whole of the question "is anybody actually watching this".
//
// It is deliberately requests-and-recency rather than clients-and-bytes. A byte counter
// cannot be had from the segmented sink without wrapping every ResponseWriter
// (http.FileServer writes directly), and a live connection count is nearly always zero
// there because HLS segment GETs are transient, so a rule keyed on either would be
// honest for one sink and false for the other. Both sinks already track these two.
//
// It exists because a renderer that accepted Play and never fetched the URL was
// reported as SUCCESS: the replay sink's idle grace starts running the moment it is
// created and its finished condition needs no client at all, so castor encoded an
// entire title and called it delivered. The segmented sink has the identical hole. The
// observed run's renderer was handed a URL and read bytes_sent=0 from it.
//
// last is the zero time until the first request, which a rule must not read as "an
// eternity ago": a watch that has only just opened has established nothing about a
// renderer yet, so the elapsed time is measured from the watch instead.
type Consumer interface {
	Fetched() (requests int, last time.Time)
}

// Lead is how far a transcription has committed: two numbers, so the readiness rule is
// exercisable without a whisper model and a cgo build.
//
// A nil Lead means this cast burns no subtitles, which is the ruleset's own way of
// asking whether a transcription lead is part of being playable at all. A typed nil
// would answer "yes, and it has committed nothing", forever.
type Lead interface {
	LatestEnd() float64
	Done() bool
}

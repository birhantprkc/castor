package watch

import (
	"time"

	"github.com/stupside/castor/internal/media"
)

// Producer is the party making the bytes, as every window reads it: whether it has reached
// its terminal state, and the lines it printed getting there. Declared here because those
// two are all a rule needs of it, which is what lets the rules be exercised without an
// ffmpeg, a spool or a network.
//
// The producer decides nothing: this is an observation surface, so the pull's contract (a
// download that made its own decisions would have to be able to take them back, which means
// rewinding a spool something may already be reading) stays literally true.
type Producer interface {
	Done() <-chan struct{}
	Evidence() []string
}

// Telemetry is what a producer says about its own pace, and why it stopped. It is a second
// port and not two more methods on Producer because only a subject whose RATE means something
// answers it: a source read does, and a delivery's own encoder does not.
//
// That absence used to be two methods answering constants (Progress an empty sample, Err nil
// forever), and the reachability model credited them as facts supplied: an artifact gate's rule
// keyed on a speed or on a failure read a structural zero on every cast and passed for
// reachable. A port nothing implements for that window is absence a supply check can see.
//
// The encoder's own two reasons for staying out of it, kept here because this is where a future
// third subject will ask: its pace is not a statement about a link's carrying capacity (the
// subtitle-burning encode is pinned just above realtime by design, so offering its speed is
// offering a number the deliverability rule would have to be taught to ignore), and its
// terminal error belongs to the teardown that reaps it, which the delivery driver joins into
// the cast's result, so a gate reporting it too would state one failure twice and could
// disagree with itself about it.
//
// Progress carries media position and speed as well as bytes. That is the whole fix for the
// starved-upstream failure: bytes per second cannot be compared against anything on a source
// that publishes no bitrate, while media seconds per wall-clock second (ffmpeg's own speed=) is
// the comparison already made. A read that moved 14.4 MB and then nothing printed the same two
// numbers as one that had finished.
type Telemetry interface {
	Progress() media.Progress
	Err() error
}

// Consumer is the renderer's side of a delivery as a health rule reads it: how much of the
// program it has been handed, and when a byte of it last moved. Two numbers, because that is
// the whole of the question "is anybody actually watching this".
//
// BYTES TAKEN and not requests made, and that is the difference between naming the failure
// below and reading as if it did. A sink counts a request the moment one arrives, before a
// byte of the response is written, and a renderer comes for a stream by probing it first: a
// HEAD, then a short GET, then the real GET (see the replay package). So a count answers "the
// renderer fetched" for one that asked and took nothing, which is the observed run to the
// letter: a URL accepted, a request in the log, and bytes_sent=0. Bytes moved are what
// separate asking from taking, and nothing else a sink tracks does.
//
// It exists because a renderer that accepted Play and never fetched the URL was
// reported as SUCCESS: the replay sink's idle grace starts running the moment it is
// created and its finished condition needs no client at all, so castor encoded an
// entire title and called it delivered.
//
// The delivery that KEEPS what it produces supplies it, and that is what is supervised in
// flight, on every composition that serves one. A delivery that deletes behind its own window
// cannot be given one honestly, because it has no buffer figure to weigh a renderer's silence
// against; it answers the same question once its delivery has run its course instead (see core's
// completeness statement), which is the only place a mechanism that deletes its program can
// answer it at all.
//
// last is the zero time until a byte has really moved, which a rule must not read as "an
// eternity ago": a watch that has only just opened has established nothing about a
// renderer yet, so the elapsed time is measured from the watch instead. A connection merely
// ENDING may not touch it either, or a renderer that probes on a cadence and takes nothing
// keeps restarting the clock it is about to be judged on.
type Consumer interface {
	Handed() (bytes int64, last time.Time)
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

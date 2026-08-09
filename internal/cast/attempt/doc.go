// Package attempt is the cast spine: what one try at a cast IS, what went wrong with
// it, and what to try instead. It performs no I/O itself and drives two outbound ports
// (Runner, which runs an attempt, and Program, which says what a link publishes), which
// is what makes every recovery path a table-driven unit test with no ffmpeg, no network
// and no renderer.
//
// That is a compile-time fact and not a habit: this package links internal/media, carriage,
// read and watch and nothing else in the tree, so nothing it imports can start a process or
// open a socket (see TestTheSpineLinksNothingThatRunsAProcessOrOpensASocket). It was one
// identifier away from being false: naming the operator's delivery preference in the
// delivery driver pulled ffmpeg, the device adapters, two HTTP servers, the spool and the
// source resolver in behind it, so that value lives in the leaf that owns the vocabulary.
//
// It exists because castor held no value for a cast at all. The shape of a try (which
// link, which rung of the program, how it is read, how the renderer is asked to receive
// it) was spread across a function's parameters and two nested booleans, so nothing
// could print what was being attempted and nothing could change it. A failure was
// whatever string the stage that noticed happened to produce, so a dead source reached a
// user under the name of the process that merely read the wreckage (Outcome.Err states
// the join that fixed it).
//
// An Attempt is that shape as data, an Outcome is what happened to it, and a Fault is
// that outcome classified. The tables are read the way every other table in castor is:
// declaration order, first match, ending in a row that carries no predicate, so a failure
// nobody recognised is NAMED as unrecognised rather than reported as a table with a hole in
// it (see classes and playbook, whose agreement is asserted in both directions).
//
// Two rules live here and nowhere else. A cancelled cast is not a failed cast, which is
// the first row of the classification, because everything castor kills reports a broken
// pipe on the way out and a loop that recovered from those would answer Ctrl+C by
// starting the cast over. And the join that makes an Outcome combines the landing's
// terminal state, the judgement's verdict and the delivery's result, so the party
// actually at fault is the party named.
//
// A recovery is offered only while the attempt has not reached PhasePlaying, and Phase
// carries why. Termination is structural rather than a maximum-attempts knob: every
// strategy moves the attempt strictly down a finite order (candidate index up a bounded ordering, rung
// bitrate down a published ladder, a copy cleared and never reinstated, a pace lowered
// to a floor, a delivery axis that flips one way), and the ledger refuses an attempt this
// cast has already run, so a table edit that broke that monotonicity stalls the loop
// instead of spinning it.
//
// What a refusal SAYS is part of the job and not a postscript: a fault carries the
// arithmetic castor already held and never said (see arithmetic).
package attempt

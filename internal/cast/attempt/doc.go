// Package attempt is the cast spine: what one try at a cast IS, what went wrong with
// it, and what to try instead. It performs no I/O itself and drives two outbound ports
// (Runner, which runs an attempt, and Program, which says what a link publishes), which
// is what makes every recovery path a table-driven unit test with no ffmpeg, no network
// and no renderer.
//
// It exists because castor held no value for a cast at all. The shape of a try (which
// link, which rung of the program, how it is read, how the renderer is asked to receive
// it) was spread across a function's parameters and two nested booleans, so nothing
// could print what was being attempted and nothing could change it. A failure was
// whatever string the stage that noticed happened to produce: a source that exited 183
// mid-copy reached a user as "encoder: spool producer failed: upstream pull: exit status
// 183", blaming the process that merely read the wreckage.
//
// An Attempt is that shape as data, an Outcome is what happened to it, and a Fault is
// that outcome classified. Both tables are read the way every other table in castor is:
// declaration order, first match, with a missing key an error naming the shape rather
// than a fall-through (see classes and playbook).
//
// Two rules live here and nowhere else. A cancelled cast is not a failed cast, which is
// the first row of the classification, because everything castor kills reports a broken
// pipe on the way out and a loop that recovered from those would answer Ctrl+C by
// starting the cast over. And the join that makes an Outcome combines the landing's
// terminal state, the judgement's verdict and the delivery's result, so the party
// actually at fault is the party named.
//
// A recovery is offered only while the attempt has not reached PhasePlaying. Past that
// there is no going back: nothing in castor seeks, each attempt owns a fresh buffer, and
// replaying a film from the beginning at minute forty is worse than a clear error.
// Termination is structural rather than a maximum-attempts knob: every strategy moves
// the attempt strictly down a finite order (candidate index up a bounded ordering, rung
// bitrate down a published ladder, a copy cleared and never reinstated, a pace lowered
// to a floor, a delivery axis that flips one way), and the ledger refuses an attempt this
// cast has already run, so a table edit that broke that monotonicity stalls the loop
// instead of spinning it.
//
// What a refusal SAYS is part of the job and not a postscript. The run this layer was
// built for ended as a TV error and bytes_sent=0 while castor held every term of the
// sentence a user needed, so a fault carries the arithmetic: the measured speed, how long
// the program would take at it, whether the source offered anything lighter, and how much
// of the ranker's ordering was spent.
package attempt

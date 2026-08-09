// Package watch owns one question and everything about it: is this cast working,
// and if not, what kind of not-working. Not how the bytes are fetched (internal/cast/read),
// not what is inside them (internal/media), and not what to try instead, which is the
// attempt loop's job over the verdicts decided here.
//
// It exists because castor could only ever ask that question once, in one place, about
// one window. The playback gate judged four facts inline in a poll loop, and after the
// gate opened nothing watched anything at all: the executor touched the reader for the
// last time on its way to Play, so a cast that starved, or a renderer that accepted a
// URL and never came for the bytes, ran to the end of the title and was reported as
// success. The run that motivates this package landed 33 KB and one second of media in
// thirty seconds against a reader allowed twice realtime, opened the gate on those
// 33 KB because readiness was "any byte at all", and handed a renderer a stream that
// delivered bytes_sent=0.
//
// Health is those facts as one value. The verdict is a table keyed on it, walked in
// order, first match, with the window a FIELD on the row rather than a second table:
// the same rules judge the pre-playback window and the in-flight one, and only the
// ACTION differs by window, which is what keeps a revision unreachable from a cast a
// viewer is already watching. An unmatched Health, or a verdict with no action for its
// window, is an error naming the shape rather than a silent fall-through.
//
// One thing is deliberately NOT judged here, and it is worth naming so it is not added
// back as a missing rule: a renderer that took some of the program and then went quiet. A viewer
// who paused, a viewer who walked away and a renderer that crashed are one fact from outside
// (nothing more being taken), and nothing this package can read separates them, because what castor still
// holds for the renderer is filled by the encoder and keeps growing through the silence. A
// verdict over that evidence states something castor cannot know. The delivery's write
// deadline is what answers a quiet renderer (see replay's DefaultWriteDeadline), and whether
// it ever took what was made for it is arithmetic done once the cast has ended (see
// core.Undelivered), where the numbers exist.
//
// Every threshold here is derived from something that already had to be chosen, in the
// manner of the playback gate's stall bound: the stall window and both deficit windows
// from the read policy's backoff ceiling (one before playback, two after), the
// deliverability floor from playback itself, the confidence window from the reader's own
// report period and the startup lag every figure it states carries. None of it is
// configurable, because none of it is an operator's decision.
//
// The dependency direction is deliberate: this package imports internal/media and
// internal/cast/read and nothing else. A judgement about whether a cast is healthy must
// not be computed from an argument builder's flag choices, and it must be exercisable
// over a Health value with no ffmpeg, no network and no renderer.
package watch

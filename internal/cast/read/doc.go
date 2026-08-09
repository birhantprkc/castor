// Package read owns one question and everything about it: how the bytes of a
// source are fetched. Not what is inside them (internal/media), not what is done
// with them afterwards (the pipeline's stages), and not how a command line spells
// any of it, which is the ffmpeg adapter's job over the values decided here.
//
// It exists because those terms were eight literals in the middle of an argument
// builder. One mid-read deadline was applied to every input of both readers, the
// retry status set was the single token "429", and the backoff ceiling was a
// constant the arg builder owned and the playback gate derived its own stall bound
// from. Nothing about a read could be stated, compared or tested without building
// a command line, and a rule about a hostile CDN could only be written as a string.
//
// A read is a policy: how long one read may stall before it is abandoned (or whether
// it may be abandoned at all), how long a retry may be waited out, which HTTP answers
// are worth retrying, how many times a segment whose open failed is re-fetched, and how
// fast the source may be consumed. Which policy a source gets is a function of what
// the source itself published (see media.Origin, harvested from a playlist the
// reader is about to open anyway), so the answer is a table keyed on those facts,
// walked in order, first match, ending in a row that carries no predicate at all: a
// source no other row claimed is read on the careful terms rather than on the zero
// value's, which are no deadline, no reconnection and no pace.
//
// The ownership direction is deliberate. The two thresholds other stages derive
// their own bounds from (the backoff ceiling and the encoder's burst) live here,
// and nothing here imports the ffmpeg adapter: a judgement about whether a cast is
// healthy must not be computed from the flag renderer's choices, which is what a
// playback gate deriving its stall bound from the arg builder's backoff constant
// was. This package imports internal/media and the standard library, nothing else.
package read

// Package receiver is the protocol-agnostic half of a fake device: a Session takes one hand-off and plays it.
// Device families, players and viewers are strategies behind its interfaces, listed at the composition root.
// It shares no code with castor, so a castor bug cannot also blind the judge.
package receiver

// Tools are the binaries a receiver plays and measures with.
type Tools struct{ FFmpeg, FFprobe string }

// UserAgent marks the receiver's own fetches, so an origin can tell them from castor's.
const UserAgent = "castor-e2e-receiver"

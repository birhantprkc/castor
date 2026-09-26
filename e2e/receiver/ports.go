// Package receiver is the protocol-agnostic half of a fake renderer: a Session takes one hand-off and plays it.
// Device families, players and viewers are strategies behind the ports below, listed at the composition root.
// It shares no code with castor, so a castor bug cannot also blind the judge.
package receiver

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stupside/castor/e2e/strategy"
)

// Family builds a Device from a case's settings; its name is the device.type castor pins the receiver with.
type Family = strategy.Factory[Device]

// Device is one configured renderer: it serves its protocol and turns its hand-off into a Session's.
type Device interface {
	// Type is the device.type castor pins it with.
	Type() string
	Start(t *testing.T, s *Session) (Endpoint, error)
}

// Endpoint is where castor reaches a started receiver, and what that receiver decodes.
type Endpoint struct {
	Host  string
	Plays Plays
	// EndsWithin is how long castor may take to notice playback ended, as far as this protocol lets it.
	EndsWithin time.Duration
}

// Reported is EndsWithin for a protocol that tells castor about the end: polled every 2s, so anything longer is a hang.
const Reported = 15 * time.Second

// Plays is what a receiver decodes, codecs named as ffprobe names them.
type Plays struct {
	// Video maps each codec to the deepest bit depth it decodes.
	Video map[string]int `yaml:"video"`
	// Audio maps each codec to its channel ceiling, 0 for none.
	Audio map[string]int `yaml:"audio"`
	// HDR is whether the receiver engages a PQ or HLG picture; castor declares no receiver that does.
	HDR bool `yaml:"hdr"`
	// Levels maps a codec to the highest level it decodes, in ffprobe's units (h264 x10); absent means no ceiling.
	Levels map[string]int `yaml:"levels"`
	// MaxSampleRate is the highest audio sample rate the receiver plays, 0 for no ceiling.
	MaxSampleRate int `yaml:"max_sample_rate"`
	// Deinterlaces is whether the receiver shows an interlaced picture without combing.
	Deinterlaces bool `yaml:"deinterlaces"`
}

// Player consumes a handed stream onto a tape, the way one kind of media is played.
type Player interface {
	strategy.Named
	// Plays reports whether this player takes a response whose body opens with head.
	Plays(head []byte) bool
	Record(ctx context.Context, rec Recording) (tape string, err error)
}

// Recording is one handed stream a Player consumes.
type Recording struct {
	URL string
	// Body is the response already opened on URL.
	Body   io.Reader
	Tape   string
	FFmpeg string
	Viewer Viewer
}

// Viewer is how the person in front of the receiver watches: to the end, or until they stop it.
type Viewer interface {
	strategy.Named
	// Watch bounds playback; the returned context ends when the viewer stops.
	Watch(ctx context.Context) (context.Context, context.CancelFunc)
	// Stopped reports whether a playback watched with ctx ended at the viewer's hand.
	Stopped(ctx context.Context) bool
	// WillStop reports whether this viewer means to stop before the media ends.
	WillStop() bool
	// Pace wraps a stream read the way this viewer consumes it.
	Pace(r io.Reader) io.Reader
	// Realtime reports whether the viewer consumes at playback speed, which a demuxer must be told.
	Realtime() bool
}

// ResponseCheck holds the response to the receiver's fetch to a protocol's demands; it returns violations.
type ResponseCheck func(declared string, h http.Header) []string

// Tools are the binaries a receiver plays and measures with.
type Tools struct{ FFmpeg, FFprobe string }

// UserAgent marks the receiver's own fetches, so an origin can tell them from castor's.
const UserAgent = "castor-e2e-receiver"

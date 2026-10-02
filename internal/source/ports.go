package source

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source/timeline"
)

// Client reads an origin over HTTP: its documents, and the media they list, on the session the documents opened.
type Client interface {
	Fetch(ctx context.Context, u *url.URL, h http.Header) (body string, from *url.URL, status int, err error)
	// Session is what the origin set while documents were read, which every later read of u must replay.
	Session(u *url.URL) http.Header
	// Read opens media bytes, within r when it has a length, on the same session; a status the origin answered comes as a timeline.Failure.
	Read(ctx context.Context, u *url.URL, h http.Header, r timeline.Range) (io.ReadCloser, error)
}

// Reader reads the links of one shape.
type Reader interface {
	Resolve(ctx context.Context, env Env, s Subject) (Resolution, error)

	// Timeline follows an input whose timeline castor keeps, read for the kind of track given; nil for one ffmpeg reads directly.
	Timeline(c Client, in media.Input, reads media.TrackKind) timeline.Source
}

// Format is a Reader for a document grammar, which links announce and bodies show.
type Format interface {
	Reader

	// Identity is the content type this format reads and the names a link announces it under.
	Identity() Identity

	// Recognize reads a body in this format's grammar; LadderUnknown means it is not this format.
	Recognize(body string) Reading
}

// Identity is how a link says it carries one content type: a file extension, or a server-confirmed MIME type.
type Identity struct {
	ContentType string
	Extensions  []string
	MIMETypes   []string
}

type Env struct {
	Client    Client
	MaxHeight media.HeightCap
}

type Subject struct {
	Stream Stream
	Origin Origin
	Chosen Rendition
}

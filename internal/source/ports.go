package source

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/stupside/castor/internal/media"
)

type Probes func(*Candidate) media.Prober

type Playlists interface {
	Fetch(ctx context.Context, u *url.URL, h http.Header) (body string, from *url.URL, status int, err error)
}

type Format interface {
	// Name is the shape resolution reports having read.
	Name() string

	// Identity is the content type this format reads and the names a link announces it under.
	Identity() Identity

	Resolve(ctx context.Context, env Env, s Subject) (Resolution, error)

	// Recognize reads a body in this format's grammar; LadderUnknown means it is not this format.
	Recognize(body string) (Ladder, []string)
}

// Identity is how a link says it carries one content type: a file extension, or a server-confirmed MIME type.
type Identity struct {
	ContentType string
	Extensions  []string
	MIMETypes   []string
}

type Env struct {
	Playlists Playlists
	MaxHeight media.HeightCap
}

type Subject struct {
	Stream Candidate
	Origin Origin
	Chosen Rendition
}

// Formats is every format castor reads, in order, first match.
type Formats []Format

func (fs Formats) claiming(c *Candidate) Format {
	for _, f := range fs {
		if f.Identity().ContentType == c.ContentType {
			return f
		}
	}
	return opaque{}
}

// ContentTypeOf names what a link carries: its extension first, then a confirmed MIME type, else "".
func (fs Formats) ContentTypeOf(u *url.URL, mime string) string {
	ids := fs.identities()
	if u != nil {
		ext := strings.ToLower(path.Ext(u.Path))
		for _, id := range ids {
			if ext != "" && slices.Contains(id.Extensions, ext) {
				return id.ContentType
			}
		}
	}
	mime = strings.ToLower(mime)
	for _, id := range ids {
		if mime != "" && slices.Contains(id.MIMETypes, mime) {
			return id.ContentType
		}
	}
	return ""
}

func (fs Formats) identities() []Identity {
	ids := make([]Identity, 0, len(fs)+len(containers))
	for _, f := range fs {
		ids = append(ids, f.Identity())
	}
	return append(ids, containers...)
}

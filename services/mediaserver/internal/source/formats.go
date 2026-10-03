package source

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/stupside/castor/services/mediaserver/internal/source/timeline"
)

// Formats is every format castor reads, in order, first match.
type Formats []Format

// Claiming is the reader of contentType: its format, or opaque when none claims it.
func (fs Formats) Claiming(contentType string) Reader {
	for _, f := range fs {
		if f.Identity().ContentType == contentType {
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

// sniffBytes is enough of a body to read a playlist's or manifest's grammar, and far less than a film.
const sniffBytes = 64 << 10

// Identify names what a link carries: its name first, else the grammar of its body, since a script serves a playlist under any name.
func (fs Formats) Identify(ctx context.Context, c Client, u *url.URL) string {
	if ct := fs.ContentTypeOf(u, ""); ct != "" {
		return ct
	}
	body, _, _, err := c.Fetch(ctx, u, http.Header{"Range": {timeline.Range{Length: sniffBytes}.Header()}})
	if err != nil {
		return ""
	}
	return fs.sniff(body)
}

// InputArgs is how ffmpeg and ffprobe open an input of contentType, as its format says.
func (fs Formats) InputArgs(contentType string, segmentRetries int) []string {
	return fs.Claiming(contentType).InputArgs(segmentRetries)
}

// sniff names the content type of the first grammar that recognises body, "" when none does.
func (fs Formats) sniff(body string) string {
	for _, f := range fs {
		if f.Recognize(body).Ladder != LadderUnknown {
			return f.Identity().ContentType
		}
	}
	return ""
}

// Parse reads a body in the first grammar that recognises it, what it names resolved against base.
func (fs Formats) Parse(body string, base *url.URL) Document {
	for _, f := range fs {
		read := f.Recognize(body)
		if read.Ladder == LadderUnknown {
			continue
		}
		doc := Document{Ladder: read.Ladder, Runtime: read.Runtime}
		if base == nil {
			return doc
		}
		for _, ref := range read.Refs {
			if resolved, err := base.Parse(ref); err == nil {
				doc.Names = append(doc.Names, resolved)
			}
		}
		return doc
	}
	return Document{}
}

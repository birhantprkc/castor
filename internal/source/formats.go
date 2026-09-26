package source

import (
	"net/url"
	"path"
	"slices"
	"strings"
)

// Formats is every format castor reads, in order, first match.
type Formats []Format

// Claiming is the format that reads contentType, opaque when none does.
func (fs Formats) Claiming(contentType string) Format {
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

// Sniff names the content type of the first grammar that recognises body, "" when none does.
func (fs Formats) Sniff(body string) string {
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

// Package dash is castor's DASH reader: it translates a presentation into the segments castor republishes.
package dash

import (
	"strings"

	"github.com/Eyevinn/dash-mpd/mpd"

	"github.com/stupside/castor/services/mediaserver/internal/ffmpeg"
	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/source"
)

// Format is DASH as a source.Format.
type Format struct{}

var _ source.Format = Format{}

func (Format) Identity() source.Identity {
	return source.Identity{ContentType: media.DASH, Extensions: []string{".mpd"}, MIMETypes: []string{media.DASH}}
}

func (Format) InputArgs(int) []string {
	return []string{"-f", ffmpeg.FormatDASH, "-allowed_extensions", "ALL"}
}

// signature is the root element every presentation is written under.
const signature = "<MPD"

// Recognize reads a presentation written as a document whose root element is a required signature.
func (Format) Recognize(body string) source.Reading {
	if !strings.Contains(body, signature) {
		return source.Reading{}
	}
	read := source.Reading{Ladder: source.LadderMultivariant}
	// A sniffed head is cut short and parses as nothing, but sniffing only asks for the ladder.
	if m, ok := parse(body); ok {
		read.Refs = m.references()
	}
	return read
}

// references is every resource the presentation names outright, templated names aside.
func (m presentation) references() []string {
	var out []string
	add := func(refs ...string) {
		for _, ref := range refs {
			if ref = strings.TrimSpace(ref); ref != "" && !strings.Contains(ref, "$") {
				out = append(out, ref)
			}
		}
	}
	level := func(bases []*mpd.BaseURLType, t *mpd.SegmentTemplateType, l *mpd.SegmentListType, b *mpd.SegmentBaseType) {
		for _, u := range bases {
			add(string(u.Value))
		}
		var inits []*mpd.URLType
		if t != nil {
			add(t.Media, t.Initialization)
			inits = append(inits, t.SegmentBaseType.Initialization)
		}
		if l != nil {
			for _, u := range l.SegmentURL {
				add(string(u.Media))
			}
			inits = append(inits, l.SegmentBaseType.Initialization)
		}
		if b != nil {
			inits = append(inits, b.Initialization)
		}
		for _, i := range inits {
			if i != nil {
				add(string(i.SourceURL))
			}
		}
	}
	level(m.BaseURL, nil, nil, nil)
	for _, p := range m.Periods {
		level(p.BaseURLs, p.SegmentTemplate, p.SegmentList, p.SegmentBase)
		for _, s := range p.AdaptationSets {
			level(s.BaseURLs, s.SegmentTemplate, s.SegmentList, s.SegmentBase)
			for _, r := range s.Representations {
				level(r.BaseURLs, r.SegmentTemplate, r.SegmentList, r.SegmentBase)
			}
		}
	}
	return out
}

package serving

import (
	"iter"
	"strings"
)

// Line is one line of an HLS playlist, each with its ending; an entry is an #EXTINF line and the line after it.
type Line struct {
	Text string
	// URI is the line after an entry's #EXTINF, empty for any other line.
	URI string
}

// Entry reports whether the line is a segment entry.
func (l Line) Entry() bool { return l.URI != "" }

// Lines walks a playlist, joining each #EXTINF to the line it introduces.
func Lines(playlist string) iter.Seq[Line] {
	return func(yield func(Line) bool) {
		info := ""
		for text := range strings.Lines(playlist) {
			switch {
			case info != "":
				if !yield(Line{Text: info, URI: text}) {
					return
				}
				info = ""
			case strings.HasPrefix(text, "#EXTINF"):
				info = text
			default:
				if !yield(Line{Text: text}) {
					return
				}
			}
		}
		if info != "" {
			yield(Line{Text: info})
		}
	}
}

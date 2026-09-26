// Package index reads where a container file keeps its segments: an ISO BMFF sidx, or WebM Cues.
package index

import (
	"bytes"
	"errors"
	"fmt"
	"iter"
	"slices"

	"github.com/Eyevinn/mp4ff/mp4"
)

// Reference is one subsegment an index names: its bytes, and how long it plays in the index's timescale.
type Reference struct {
	Offset, Length, Duration int64
}

// boxed is one ISO BMFF box: its header, the offset it starts at, and its payload.
type boxed struct {
	mp4.BoxHeader
	at      int
	payload []byte
}

// boxes walks the ISO BMFF boxes laid end to end in b by their headers alone, so no body has to decode to be skipped.
func boxes(b []byte) iter.Seq[boxed] {
	return func(yield func(boxed) bool) {
		for at := 0; at < len(b); {
			h, err := mp4.DecodeHeader(bytes.NewReader(b[at:]))
			if err != nil || h.Size > uint64(len(b)-at) {
				return
			}
			end := at + int(h.Size)
			if !yield(boxed{BoxHeader: h, at: at, payload: b[at+h.Hdrlen : end]}) {
				return
			}
			at = end
		}
	}
}

// containers are the boxes a sample description is nested in.
var containers = []string{"moov", "trak", "mdia", "minf", "stbl"}

// SampleEntries is the codec of every track an init section describes, by its sample entry's type.
func SampleEntries(init []byte) []string {
	var out []string
	var walk func([]byte)
	walk = func(b []byte) {
		for bx := range boxes(b) {
			switch {
			case slices.Contains(containers, bx.Name):
				walk(bx.payload)
			case bx.Name == "stsd" && len(bx.payload) >= 8:
				// A full box: version and flags, then an entry count ahead of the entries.
				for entry := range boxes(bx.payload[8:]) {
					out = append(out, entry.Name)
				}
			}
		}
	}
	walk(init)
	return out
}

// Sidx reads the segment index box in b, which holds bytes of its resource from at on.
func Sidx(b []byte, at int64) ([]Reference, int64, error) {
	for bx := range boxes(b) {
		if bx.Name != "sidx" {
			continue
		}
		decoded, err := mp4.DecodeSidx(bx.BoxHeader, uint64(at)+uint64(bx.at), bytes.NewReader(bx.payload))
		if err != nil {
			return nil, 0, fmt.Errorf("reading the sidx box: %w", err)
		}
		return references(decoded.(*mp4.SidxBox))
	}
	return nil, 0, errors.New("the index range holds no sidx box")
}

func references(sidx *mp4.SidxBox) ([]Reference, int64, error) {
	if sidx.Timescale == 0 {
		return nil, 0, errors.New("the sidx box states no timescale")
	}
	// The anchor is the byte after the box moved on by the first offset.
	offset := int64(sidx.AnchorPoint)
	out := make([]Reference, len(sidx.SidxRefs))
	for i, r := range sidx.SidxRefs {
		if r.ReferenceType == 1 {
			return nil, 0, errors.New("the sidx box references another index, which castor does not follow")
		}
		out[i] = Reference{Offset: offset, Length: int64(r.ReferencedSize), Duration: int64(r.SubSegmentDuration)}
		offset += int64(r.ReferencedSize)
	}
	return out, int64(sidx.Timescale), nil
}

package index

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"slices"

	ebmlgo "github.com/at-wat/ebml-go"
	"github.com/at-wat/ebml-go/webm"
)

// defaultNanoseconds is the Matroska timecode scale a Segment that states none ticks at.
const defaultNanoseconds = 1_000_000

// head is what castor reads of a WebM init section; the read stops at Info, ahead of the tracks.
type head struct {
	Segment struct {
		Info struct {
			// A slice tells an absent scale, which defaults, from a zero one, which is refused.
			TimecodeScale []uint64
			Duration      float64
		} `ebml:"Info,stop"`
	}
}

// segmentData is where the Segment's children start: after its id and its size's variable-length integer.
func segmentData(init []byte, segment *ebmlgo.Element) int64 {
	at := int(segment.Position) + len(ebmlgo.ElementSegment.Bytes())
	return int64(at + bits.LeadingZeros8(init[at]) + 1)
}

// Cues reads a WebM file's index into its clusters: init is the file from its first byte, cues the Cues element found at byte at.
func Cues(init, cues []byte, at int64) ([]Reference, int64, error) {
	var h head
	var segment *ebmlgo.Element
	// A hook sees an element once read, so the Segment an init section cuts short is found through its children.
	root := func(e *ebmlgo.Element) {
		for e.Parent != nil {
			e = e.Parent
		}
		if e.Type == ebmlgo.ElementSegment {
			segment = e
		}
	}
	if err := ebmlgo.Unmarshal(bytes.NewReader(init), &h, ebmlgo.WithElementReadHooks(root)); err != nil && !errors.Is(err, ebmlgo.ErrReadStopped) {
		return nil, 0, fmt.Errorf("reading the WebM init section: %w", err)
	}
	if segment == nil {
		return nil, 0, errors.New("the WebM init section holds no Segment")
	}
	scale := int64(defaultNanoseconds)
	if stated := h.Segment.Info.TimecodeScale; len(stated) > 0 {
		scale = int64(stated[len(stated)-1])
	}
	if scale <= 0 {
		return nil, 0, errors.New("the WebM Segment states no timecode scale")
	}

	var index struct {
		Cues webm.Cues `ebml:"Cues,stop"`
	}
	if err := ebmlgo.Unmarshal(bytes.NewReader(cues), &index); err != nil && !errors.Is(err, ebmlgo.ErrReadStopped) {
		return nil, 0, fmt.Errorf("reading the WebM index: %w", err)
	}
	type point struct{ time, cluster int64 }
	var points []point
	for _, p := range index.Cues.CuePoint {
		if len(p.CueTrackPositions) > 0 {
			points = append(points, point{time: int64(p.CueTime), cluster: int64(p.CueTrackPositions[0].CueClusterPosition)})
		}
	}
	if len(points) == 0 {
		return nil, 0, errors.New("the WebM index lists no cue points")
	}
	slices.SortFunc(points, func(a, b point) int { return cmp.Or(cmp.Compare(a.cluster, b.cluster), cmp.Compare(a.time, b.time)) })
	// Each track may cue the same cluster; the cluster starts at its earliest cue.
	points = slices.CompactFunc(points, func(a, b point) bool { return a.cluster == b.cluster })

	base := segmentData(init, segment)
	// The last cluster runs to whatever follows it: the index when it comes after the clusters, else the Segment's end.
	end := int64(math.MaxInt64)
	if segment.Size != ebmlgo.SizeUnknown {
		end = base + int64(segment.Size)
	}
	if at > base+points[len(points)-1].cluster {
		end = min(end, at)
	}
	if end == math.MaxInt64 {
		return nil, 0, errors.New("the WebM Segment is open-ended, so its last cluster has no end")
	}
	duration := int64(h.Segment.Info.Duration)
	refs := make([]Reference, len(points))
	for i, p := range points {
		next, until := end, duration
		if i+1 < len(points) {
			next, until = base+points[i+1].cluster, points[i+1].time
		}
		// A Segment that states no Duration leaves its last cluster's length unknown (zero), never negative.
		refs[i] = Reference{Offset: base + p.cluster, Length: next - base - p.cluster, Duration: max(until-p.time, 0)}
	}
	return refs, int64(math.Round(1e9 / float64(scale))), nil
}

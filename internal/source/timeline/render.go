package timeline

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Render writes the ledger as an HLS media playlist, each segment as view shows it to the reader; nil shows the origin's.
func (l *Ledger) Render(view func(s Segment, sequence int64) Segment) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s:6\n", TagHeader, tagVersion)
	fmt.Fprintf(&b, "%s:%d\n", tagTargetDuration, l.target())
	first := l.next
	if len(l.entries) > 0 {
		first = l.entries[0].sequence
	}
	fmt.Fprintf(&b, "%s:%d\n", TagMediaSequence, first)
	if l.dropped > 0 {
		fmt.Fprintf(&b, "%s:%d\n", tagDiscontinuitySequence, l.dropped)
	}
	if s := l.start; s != nil {
		fmt.Fprintf(&b, "%s:TIME-OFFSET=%s", TagStart, seconds(s.Offset))
		if s.Precise {
			b.WriteString(",PRECISE=YES")
		}
		b.WriteString("\n")
	}

	var key Key
	var init *Map
	for i, e := range l.entries {
		seen := view(e.Segment, e.sequence)
		if seen.Seam && i > 0 {
			b.WriteString(TagDiscontinuity + "\n")
		}
		if seen.Key != key {
			writeKey(&b, seen.Key)
			key = seen.Key
		}
		if seen.Map != nil && (init == nil || *seen.Map != *init) {
			fmt.Fprintf(&b, "%s:URI=%q%s\n", TagMap, seen.Map.URI, byteRange(",BYTERANGE=", seen.Map.Range, true))
			init = seen.Map
		}
		if seen.Range.Length > 0 {
			fmt.Fprintf(&b, "%s\n", byteRange(TagByteRange+":", seen.Range, false))
		}
		fmt.Fprintf(&b, "%s:%s,\n%s\n", TagInf, seconds(seen.Duration), seen.URI)
	}
	if l.closed {
		b.WriteString(TagEndList + "\n")
	}
	return []byte(b.String())
}

// target is the longest segment ever published, rounded up: the specification lets TARGETDURATION never shrink.
func (l *Ledger) target() int {
	return int(math.Ceil(max(l.longest, time.Second).Seconds()))
}

func writeKey(b *strings.Builder, k Key) {
	if k == (Key{}) {
		b.WriteString(TagKey + ":METHOD=NONE\n")
		return
	}
	fmt.Fprintf(b, "%s:METHOD=%s", TagKey, k.Method)
	if k.URI != "" {
		fmt.Fprintf(b, ",URI=%q", k.URI)
	}
	if k.IV != "" {
		fmt.Fprintf(b, ",IV=%s", k.IV)
	}
	if k.Format != "" {
		fmt.Fprintf(b, ",KEYFORMAT=%q", k.Format)
	}
	b.WriteString("\n")
}

// byteRange writes length@offset, quoted where the tag carries it as an attribute.
func byteRange(prefix string, r Range, quoted bool) string {
	if r.Length == 0 {
		return ""
	}
	value := fmt.Sprintf("%d@%d", r.Length, r.Offset)
	if quoted {
		value = `"` + value + `"`
	}
	return prefix + value
}

func seconds(d time.Duration) string { return fmt.Sprintf("%.6f", d.Seconds()) }

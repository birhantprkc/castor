package playlist

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/origin/serving"
	"github.com/stupside/castor/e2e/strategy"
)

// Sequence is how a restarted encoder numbers what it publishes next.
type Sequence string

const (
	// Reset drops everything before the restart and numbers it from 0 again.
	Reset Sequence = "reset"
	// Continue keeps the numbering and marks the restart with EXT-X-DISCONTINUITY.
	Continue Sequence = "continue"
)

// RestartsAt builds an encoder that restarts at a segment with fresh timestamps, as in `restarts-at: {segment: 10, sequence: reset}`.
type RestartsAt struct{}

func (RestartsAt) Name() string { return "restarts-at" }

func (RestartsAt) Build(settings yaml.Node) (origin.Behaviour, error) {
	var r restart
	if err := strategy.Decode(settings, &r); err != nil {
		return nil, fmt.Errorf("restarts-at: %w", err)
	}
	if r.Segment < 1 || (r.Sequence != Reset && r.Sequence != Continue) {
		return nil, fmt.Errorf("restarts-at: want segment >= 1 and sequence one of %s, %s", Reset, Continue)
	}
	return r, nil
}

type restart struct {
	Segment  int      `yaml:"segment"`
	Sequence Sequence `yaml:"sequence"`
}

func (restart) Name() string { return "restarts-at" }

func (s restart) Wrap(next http.Handler, p origin.Published) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch index, numbered := serving.SegmentIndex(r.URL.Path); {
		case p.IsSegment(r) && numbered && index >= s.Segment:
			serving.Rewrite(next, w, r, func(ts []byte) ([]byte, bool, error) {
				return ts, true, rebase(ts, uint64(s.Segment)*90000)
			})
		case path.Ext(r.URL.Path) == ".m3u8":
			serving.Rewrite(next, w, r, func(playlist []byte) ([]byte, bool, error) { return s.restarted(string(playlist)), true, nil })
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// restarted is a media playlist as the encoder publishes it once it has restarted; one that has not reached it passes through.
func (s restart) restarted(playlist string) []byte {
	lines := strings.SplitAfter(playlist, "\n")
	// listed maps each #EXTINF line to the index of the segment it introduces.
	listed := map[int]int{}
	for i, line := range lines[:len(lines)-1] {
		if index, ok := serving.SegmentIndex(uriPath(lines[i+1])); ok && strings.HasPrefix(line, "#EXTINF") {
			listed[i] = index
		}
	}
	indices := slices.Collect(maps.Values(listed))
	if !slices.Contains(indices, s.Segment) {
		// Once the restart slides out of a window, the encoder still numbers from where it restarted.
		if s.Sequence == Reset && len(indices) > 0 && slices.Min(indices) > s.Segment {
			return []byte(sequencePattern.ReplaceAllString(playlist, fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d", slices.Min(indices)-s.Segment)))
		}
		return []byte(playlist)
	}
	var out strings.Builder
	for i := 0; i < len(lines); i++ {
		index, entry := listed[i]
		switch {
		case strings.HasPrefix(lines[i], "#EXT-X-MEDIA-SEQUENCE") && s.Sequence == Reset:
			out.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
		case !entry:
			out.WriteString(lines[i])
		case index < s.Segment && s.Sequence == Reset:
			i++
		case index == s.Segment && s.Sequence == Continue:
			out.WriteString("#EXT-X-DISCONTINUITY\n")
			fallthrough
		default:
			out.WriteString(lines[i] + lines[i+1])
			i++
		}
	}
	return []byte(out.String())
}

var sequencePattern = regexp.MustCompile(`#EXT-X-MEDIA-SEQUENCE:\d+`)

// uriPath is the path of a playlist's URI line, without its query.
func uriPath(line string) string {
	uri, _, _ := strings.Cut(strings.TrimSpace(line), "?")
	return uri
}

const (
	tsPacket = 188
	// ptsWrap is where the 33-bit PTS, DTS and PCR base roll over.
	ptsWrap = 1 << 33
)

// rebase moves every PES timestamp and PCR base of a TS back by ticks of the 90 kHz clock, in place.
func rebase(ts []byte, ticks uint64) error {
	if len(ts) == 0 || len(ts)%tsPacket != 0 || ts[0] != 0x47 {
		return errors.New("restarts-at: the segment is not an MPEG-TS")
	}
	back := func(v uint64) uint64 { return (v + ptsWrap - ticks%ptsWrap) % ptsWrap }
	for off := 0; off < len(ts); off += tsPacket {
		pkt := ts[off : off+tsPacket]
		if pkt[0] != 0x47 {
			return fmt.Errorf("restarts-at: lost TS sync at byte %d", off)
		}
		control, payload := pkt[3]>>4&3, 4
		if control&2 != 0 {
			length := int(pkt[4])
			if length > 0 && pkt[5]&0x10 != 0 && length >= 7 {
				writePCR(pkt[6:], back(readPCR(pkt[6:])))
			}
			payload += 1 + length
		}
		if control&1 == 0 || pkt[1]&0x40 == 0 || payload+9 > tsPacket {
			continue
		}
		pes := pkt[payload:]
		// Only a PES start code with the optional header carries timestamps; PSI sections begin with a pointer field.
		if pes[0] != 0 || pes[1] != 0 || pes[2] != 1 || pes[6]&0xc0 != 0x80 {
			continue
		}
		flags := pes[7] >> 6
		if flags&2 != 0 && len(pes) >= 14 {
			writeTimestamp(pes[9:], back(readTimestamp(pes[9:])))
		}
		if flags == 3 && len(pes) >= 19 {
			writeTimestamp(pes[14:], back(readTimestamp(pes[14:])))
		}
	}
	return nil
}

func readTimestamp(b []byte) uint64 {
	return uint64(b[0]>>1&7)<<30 | uint64(b[1])<<22 | uint64(b[2]>>1)<<15 | uint64(b[3])<<7 | uint64(b[4]>>1)
}

// writeTimestamp keeps the prefix nibble and the marker bits.
func writeTimestamp(b []byte, v uint64) {
	b[0] = b[0]&0xf1 | byte(v>>30&7)<<1
	b[1] = byte(v >> 22)
	b[2] = b[2]&1 | byte(v>>15&0x7f)<<1
	b[3] = byte(v >> 7)
	b[4] = b[4]&1 | byte(v&0x7f)<<1
}

func readPCR(b []byte) uint64 {
	return uint64(b[0])<<25 | uint64(b[1])<<17 | uint64(b[2])<<9 | uint64(b[3])<<1 | uint64(b[4]>>7)
}

// writePCR keeps the reserved bits and the 27 MHz extension.
func writePCR(b []byte, v uint64) {
	b[0], b[1], b[2], b[3] = byte(v>>25), byte(v>>17), byte(v>>9), byte(v>>1)
	b[4] = b[4]&0x7f | byte(v&1)<<7
}

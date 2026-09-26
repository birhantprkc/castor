package ffmpeg

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

// silentFailure is one stderr line that means the output is not playable, or that media is missing from it.
type silentFailure struct {
	marker string
	reason string
	// lost is media the source never delivered, which a second read of the same link may still deliver.
	lost bool
}

// silentFailures are markers ffmpeg emits on exit 0 with unplayable or incomplete output.
var silentFailures = []silentFailure{
	{
		// aac_adtstoasc with ADTS input: muxer rejects every packet, output unplayable.
		marker: "AAC bitstream not in ADTS format and extradata missing",
		reason: "an audio repack was applied toward a container that frames its streams in band, and the muxer discarded the packets",
	},
	{
		// A response shorter than its Content-Length; ffmpeg keeps what arrived and moves on.
		marker: "Stream ends prematurely",
		reason: "the origin cut a response short of the length it declared, so media is missing from the read",
		lost:   true,
	},
	{
		// The HLS demuxer drops a segment it could not open and carries on to exit 0.
		marker: "failed too many times, skipping",
		reason: "the origin refused a segment on every retry, so the read skipped media",
		lost:   true,
	},
	{
		// Exits non-zero; free to catch early.
		marker: "Malformed AAC bitstream detected",
		reason: "an ADTS-framed AAC track reached a container that declares its decoder configuration up front, with no repack",
	},
}

// SilentFailure returns a non-nil error once ffmpeg printed unplayable-output line, or nil.
func (p *Process) SilentFailure() error { return p.markers.failure() }

// LostMedia reports that the source delivered less than it declared, whatever ffmpeg's exit status says.
func (p *Process) LostMedia() bool { return p.markers.lostMedia() }

// markerWatch scans every line for unplayable-output markers (survives tail scrolling).
type markerWatch struct {
	mu sync.Mutex
	// silent is the FIRST silent-failure line (nil until one appears).
	silent error
	// seen are markers that fired, in order and without repeats.
	seen []string
	lost bool
}

func (w *markerWatch) Observe(line string) {
	for _, f := range silentFailures {
		if !strings.Contains(line, f.marker) {
			continue
		}
		w.mu.Lock()
		if !slices.Contains(w.seen, f.marker) {
			w.seen = append(w.seen, f.marker)
		}
		if w.silent == nil {
			w.silent = fmt.Errorf("ffmpeg produced unplayable output: %s (%s)", f.reason, line)
		}
		w.lost = w.lost || f.lost
		w.mu.Unlock()
		return
	}
}

func (w *markerWatch) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.silent
}

func (w *markerWatch) lostMedia() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lost
}

func (w *markerWatch) snapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.seen)
}

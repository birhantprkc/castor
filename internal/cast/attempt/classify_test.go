package attempt

import (
	"errors"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/watch"
	"github.com/stupside/castor/internal/media"
)

func TestClassify(t *testing.T) {
	gone := &media.Gone{Renderer: "Living Room TV"}
	for _, tt := range []struct {
		name string
		in   Evidence
		want Kind
	}{
		{"cancelled beats every symptom", Evidence{Cancelled: true, Reached: PhaseReading, Verdict: watch.Undeliverable}, Cancelled},
		{"cancelled beats a renderer that stopped answering", Evidence{Cancelled: true, Reached: PhasePlaying, RendererGone: gone}, Cancelled},
		{"dead having established nothing is unreachable", Evidence{Reached: PhaseReading, Verdict: watch.Dead}, Unreachable},
		{"dead having landed media is not unreachable", Evidence{Reached: PhaseReading, Verdict: watch.Dead, Health: watch.Health{Landed: 33088}}, Unclassified},
		{"a reader exit on copied packets broke upstream", Evidence{Reached: PhaseReading, Verdict: watch.Dead, ReadExit: 183, Copied: media.Axes{Video: true}, Health: watch.Health{Landed: 4 << 20, Samples: 12}}, CopyBrokeUpstream},
		{"a reader castor killed is a stall, not a broken copy", Evidence{Reached: PhaseReading, Verdict: watch.Stalled, ReadExit: -1, Copied: media.Axes{Video: true}, Health: watch.Health{Landed: 4 << 20}}, SourceStalled},
		{"under-delivering", Evidence{Reached: PhaseReading, Verdict: watch.Undeliverable, Health: starving}, UnderDelivering},
		{"a clean read that lost media before play is re-read", Evidence{Reached: PhaseReading, ReadIncomplete: true, Copied: media.Axes{Video: true}}, SourceStalled},
		{"a read that lost media after the hand-off is not re-read", Evidence{Reached: PhasePlaying, ReadIncomplete: true}, Unclassified},
		{"a renderer that stopped answering is gone", Evidence{Reached: PhasePlaying, RendererGone: gone}, RendererGone},
		{"gone beats unfetched", Evidence{Reached: PhasePlaying, Verdict: watch.Unfetched, RendererGone: gone}, RendererGone},
		{"a refused URL is a refusing renderer", Evidence{PlayErr: errors.New("SOAP 714")}, RendererRefused},
		{"a renderer that took a fraction is refusing", Evidence{Reached: PhasePlaying, Undelivered: &watch.Undelivered{Handed: 2 * time.Minute, Produced: 2 * time.Hour}}, RendererRefused},
		{"a delivery with no artifact produced nothing", Evidence{Reached: PhaseOpening, Verdict: watch.Dead}, ProducedNothing},
		{"a delivery silent over a proven buffer produced nothing", Evidence{Reached: PhaseOpening, Verdict: watch.Stalled, Buffered: true}, ProducedNothing},
		{"a delivery silent over the source itself is the source stalling", Evidence{Reached: PhaseOpening, Verdict: watch.Stalled}, SourceStalled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := classFor(tt.in).Kind; got != tt.want {
				t.Errorf("classified %s, want %s", got, tt.want)
			}
		})
	}
}

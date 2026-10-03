package recovery

import (
	"errors"
	"testing"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/cast/health"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

func TestClassify(t *testing.T) {
	gone := &media.Gone{Device: "Living Room"}
	for _, tt := range []struct {
		name string
		in   Evidence
		want kind
	}{
		{"cancelled beats every symptom", Evidence{Cancelled: true, Reached: health.Reading, Verdict: health.Undeliverable}, cancelled},
		{"cancelled beats a device that stopped answering", Evidence{Cancelled: true, Reached: health.Playing, DeviceGone: gone}, cancelled},
		{"dead having established nothing is unreachable", Evidence{Reached: health.Reading, Verdict: health.Dead}, unreachable},
		{"dead having landed media is not unreachable", Evidence{Reached: health.Reading, Verdict: health.Dead, Vitals: health.Vitals{Landed: 33088}}, unclassified},
		{"a reader exit on copied packets broke upstream", Evidence{Reached: health.Reading, Verdict: health.Dead, ReadExit: 183, Copied: media.Axes{Video: true}, Vitals: health.Vitals{Landed: 4 << 20, Samples: 12}}, copyBrokeUpstream},
		{"a reader castor killed is a stall, not a broken copy", Evidence{Reached: health.Reading, Verdict: health.Stalled, ReadExit: -1, Copied: media.Axes{Video: true}, Vitals: health.Vitals{Landed: 4 << 20}}, sourceStalled},
		{"under-delivering", Evidence{Reached: health.Reading, Verdict: health.Undeliverable, Vitals: starving}, underDelivering},
		{"a clean read that lost media before play is re-read", Evidence{Reached: health.Reading, ReadIncomplete: true, Copied: media.Axes{Video: true}}, sourceStalled},
		{"a read that lost media after the hand-off is not re-read", Evidence{Reached: health.Playing, ReadIncomplete: true}, unclassified},
		{"a device that stopped answering is gone", Evidence{Reached: health.Playing, DeviceGone: gone}, deviceGone},
		{"gone beats unfetched", Evidence{Reached: health.Playing, Verdict: health.Unfetched, DeviceGone: gone}, deviceGone},
		{"a refused URL is a refusing device", Evidence{PlayErr: errors.New("SOAP 714")}, deviceRefused},
		{"a device that took a fraction is refusing", Evidence{Reached: health.Playing, Undelivered: &health.Undelivered{Handed: 2 * time.Minute, Produced: 2 * time.Hour}}, deviceRefused},
		{"a delivery with no artifact produced nothing", Evidence{Reached: health.Opening, Verdict: health.Dead}, producedNothing},
		{"a delivery silent over a proven buffer produced nothing", Evidence{Reached: health.Opening, Verdict: health.Stalled, Buffered: true}, producedNothing},
		{"a delivery silent over the source itself is the source stalling", Evidence{Reached: health.Opening, Verdict: health.Stalled}, sourceStalled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := classFor(tt.in).kind; got != tt.want {
				t.Errorf("classified %s, want %s", got, tt.want)
			}
		})
	}
}

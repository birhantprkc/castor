package health

import (
	"testing"
	"time"
)

func TestVerdicts(t *testing.T) {
	past := StallWindow + time.Second
	for _, tt := range []struct {
		name   string
		phase  Phase
		vitals Vitals
		want   Kind
	}{
		{"any landed byte opens a cast", Reading, Vitals{Landed: 1}, ready},
		{"an empty buffer waits", Reading, Vitals{}, starting},
		{"a burn-in waits for the transcription lead", Reading, Vitals{Landed: 4 << 20, subtitles: true, lead: transcriptionLead.Seconds() - 1}, starting},
		{"the transcription lead opens the gate", Reading, Vitals{Landed: 1, subtitles: true, lead: transcriptionLead.Seconds()}, ready},
		{"a finished transcription over an empty buffer does not open", Reading, Vitals{subtitles: true, leadDone: true}, starting},
		{"an ended read is never held, even empty under burn-in", Reading, Vitals{ended: true, subtitles: true}, ready},
		{"silence past the stall window is a stall", Reading, Vitals{sinceGrowth: past}, Stalled},
		{"a finished read never stalls", Reading, Vitals{ended: true, sinceGrowth: 10 * StallWindow}, ready},
		{"a read that failed before playback is dead", Reading, Vitals{Landed: 1 << 20, ended: true, failed: true}, Dead},
		{"a sustained 0.0627x against 2x is undeliverable", Reading, Vitals{Landed: 33088, Speed: 0.0627, Headroom: 2, Samples: minSpeedSamples, sinceDeficit: deficitWindow + time.Second}, Undeliverable},
		{"a fresh deficit holds the gate", Reading, Vitals{Landed: 33088, Speed: 0.0627, Headroom: 2, Samples: minSpeedSamples, sinceDeficit: time.Second}, starting},
		{"too few stated speeds convict nothing", Reading, Vitals{Landed: 33088, Speed: 0.0627, Headroom: 2, Samples: minSpeedSamples - 1, sinceDeficit: 10 * StallWindow}, starting},
		{"a withheld pace is never undeliverable", Reading, Vitals{Landed: 1 << 20, Speed: 0.0627, Samples: 100, sinceDeficit: 10 * StallWindow}, ready},
		{"the artifact appearing opens a delivery", Opening, Vitals{Landed: 1}, ready},
		{"a delivery without its artifact waits", Opening, Vitals{}, starting},
		{"a delivery past its patience proceeds", Opening, Vitals{overdue: true}, ready},
		{"a producer that ended with no artifact is dead", Opening, Vitals{ended: true}, Dead},
		{"a producer silent past the stall window with no artifact is stalled", Opening, Vitals{sinceGrowth: past}, Stalled},
		{"a delivery whose artifact landed is not stalled by later quiet", Opening, Vitals{Landed: 1, sinceGrowth: past}, ready},
		{"a playing cast with nothing against it is healthy", Playing, Vitals{Landed: 1 << 20, handed: 1 << 20, Speed: 2, Headroom: 2, Samples: 100}, healthy},
		{"a device handed nothing past the fetch window is unfetched", Playing, Vitals{Landed: 8 << 20, sinceFetch: fetchWindow + time.Second, delivered: 30 * time.Minute}, Unfetched},
		{"a device that fetched and went quiet is not a verdict", Playing, Vitals{Landed: 8 << 20, handed: 4 << 20, sinceFetch: past}, healthy},
		{"a silent producer while the device has media is not stalled", Playing, Vitals{Landed: 8 << 20, handed: 4 << 20, sinceGrowth: past, delivered: 45 * time.Minute, sincePlay: 15 * time.Minute}, healthy},
		{"a silent producer once the device played it out is stalled", Playing, Vitals{Landed: 8 << 20, handed: 4 << 20, sinceGrowth: past, delivered: 15 * time.Minute, sincePlay: 16 * time.Minute}, Stalled},
		{"an in-flight deficit past the stall window is undeliverable", Playing, Vitals{Landed: 8 << 20, handed: 1 << 20, Speed: 0.3, Headroom: 2, Samples: 100, sinceDeficit: past}, Undeliverable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rule, _, err := judge(tt.phase, tt.vitals)
			if err != nil {
				t.Fatalf("judge: %v", err)
			}
			if rule.kind != tt.want {
				t.Errorf("verdict = %s (rule %q), want %s", rule.kind, rule.name, tt.want)
			}
		})
	}
}

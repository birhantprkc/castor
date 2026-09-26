package watch

import (
	"testing"
	"time"
)

func TestVerdicts(t *testing.T) {
	past := StallWindow + time.Second
	for _, tt := range []struct {
		name   string
		window Window
		health Health
		want   Kind
	}{
		{"any landed byte opens a cast", BeforePlay, Health{Landed: 1}, Ready},
		{"an empty buffer waits", BeforePlay, Health{}, Starting},
		{"a burn-in waits for the transcription lead", BeforePlay, Health{Landed: 4 << 20, Subtitles: true, Lead: transcriptionLeadSeconds - 1}, Starting},
		{"the transcription lead opens the gate", BeforePlay, Health{Landed: 1, Subtitles: true, Lead: transcriptionLeadSeconds}, Ready},
		{"a finished transcription over an empty buffer does not open", BeforePlay, Health{Subtitles: true, LeadDone: true}, Starting},
		{"an ended read is never held, even empty under burn-in", BeforePlay, Health{Ended: true, Subtitles: true}, Ready},
		{"silence past the stall window is a stall", BeforePlay, Health{SinceGrowth: past}, Stalled},
		{"a finished read never stalls", BeforePlay, Health{Ended: true, SinceGrowth: 10 * StallWindow}, Ready},
		{"a read that failed before playback is dead", BeforePlay, Health{Landed: 1 << 20, Ended: true, Failed: true}, Dead},
		{"a sustained 0.0627x against 2x is undeliverable", BeforePlay, Health{Landed: 33088, Speed: 0.0627, Headroom: 2, Samples: minSpeedSamples, SinceDeficit: deficitWindow + time.Second}, Undeliverable},
		{"a fresh deficit holds the gate", BeforePlay, Health{Landed: 33088, Speed: 0.0627, Headroom: 2, Samples: minSpeedSamples, SinceDeficit: time.Second}, Starting},
		{"too few stated speeds convict nothing", BeforePlay, Health{Landed: 33088, Speed: 0.0627, Headroom: 2, Samples: minSpeedSamples - 1, SinceDeficit: 10 * StallWindow}, Starting},
		{"a withheld pace is never undeliverable", BeforePlay, Health{Landed: 1 << 20, Speed: 0.0627, Samples: 100, SinceDeficit: 10 * StallWindow}, Ready},
		{"the artifact appearing opens a delivery", Opening, Health{Landed: 1}, Ready},
		{"a delivery without its artifact waits", Opening, Health{}, Starting},
		{"a delivery past its patience proceeds", Opening, Health{Overdue: true}, Ready},
		{"a producer that ended with no artifact is dead", Opening, Health{Ended: true}, Dead},
		{"a playing cast with nothing against it is healthy", Playing, Health{Landed: 1 << 20, Handed: 1 << 20, Speed: 2, Headroom: 2, Samples: 100}, Healthy},
		{"a renderer handed nothing past the fetch window is unfetched", Playing, Health{Landed: 8 << 20, SinceFetch: fetchWindow + time.Second, Delivered: 30 * time.Minute}, Unfetched},
		{"a renderer that fetched and went quiet is not a verdict", Playing, Health{Landed: 8 << 20, Handed: 4 << 20, SinceFetch: past}, Healthy},
		{"a silent producer while the renderer has media is not stalled", Playing, Health{Landed: 8 << 20, Handed: 4 << 20, SinceGrowth: past, Delivered: 45 * time.Minute, SincePlay: 15 * time.Minute}, Healthy},
		{"a silent producer once the renderer played it out is stalled", Playing, Health{Landed: 8 << 20, Handed: 4 << 20, SinceGrowth: past, Delivered: 15 * time.Minute, SincePlay: 16 * time.Minute}, Stalled},
		{"an in-flight deficit past the stall window is undeliverable", Playing, Health{Landed: 8 << 20, Handed: 1 << 20, Speed: 0.3, Headroom: 2, Samples: 100, SinceDeficit: past}, Undeliverable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rule, _, err := judge(tt.window, tt.health)
			if err != nil {
				t.Fatalf("judge: %v", err)
			}
			if rule.Kind != tt.want {
				t.Errorf("verdict = %s (rule %q), want %s", rule.Kind, rule.Name, tt.want)
			}
		})
	}
}

// Revising a playing cast would restart it under a viewer.
func TestNoPlayingVerdictRevises(t *testing.T) {
	for v, act := range actions {
		if v.Window == Playing && act == revise {
			t.Errorf("a %s verdict while playing asks for a revision", v.Kind)
		}
	}
}

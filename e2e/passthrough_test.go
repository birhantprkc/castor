package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/cast/policy/read"
	"github.com/stupside/castor/internal/config"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/probe"
	"github.com/stupside/castor/internal/source"
	"github.com/stupside/castor/internal/source/web"
)

// Tests hand-off on HLS master declarations: granted by declared codecs alone, not measurement.

// pointedWithin bounds cast time to point renderer (pass-through has minimal overhead).
const pointedWithin = probeBudget + schedulingSlack

func TestAnHLSMasterIsHandedOverOnWhatItDeclared(t *testing.T) {
	t.Parallel()
	tl := newTools(t)

	origin := startHostile(t, tl, masterLadder, servesEverything)
	resolved := resolveOrigin(t, tl, origin)

	// Verify hand-off licensed by declaration, not by hope (test would silently pass after master changed).
	if len(resolved.Origin.Renditions) != 2 {
		t.Fatalf("the origin published %d renditions, want the two rungs the fixture writes: this case is about the rung castor CHOSE rather than the one a probe opened",
			len(resolved.Origin.Renditions))
	}
	if got := primaryInput(t, resolved.Program).URL.String(); got == origin.URL {
		t.Fatalf("resolution left the cast pointed at the master itself (%s), so nothing was narrowed and nothing threw a measurement away", got)
	}
	if got := resolved.Rendition.Height; got != masterTallRung {
		t.Fatalf("castor chose the %dpx rung, want the %dpx one: the ceiling admits both and the tall one declares four times the bandwidth", got, masterTallRung)
	}
	declared, measured := resolved.Program.Measurement()
	if !measured {
		t.Fatal("the resolved program carries no envelope at all, so every renderer below is served a remux whatever it advertises and neither row is about the master's declaration")
	}
	// Declaration vs measurement differ in duration, channels, container (probe attributes CODECS lacks).
	if declared.VideoHeight != masterTallRung {
		t.Errorf("the program's envelope is %dpx tall, want the %dpx the master declared for the chosen rung: %dpx is what a probe of THIS master reports, so this program is carrying a measurement of media it will not read",
			declared.VideoHeight, masterTallRung, masterShortRung)
	}
	if declared.Duration != 0 || declared.AudioChannels != 0 || declared.ContentType != "" {
		t.Errorf("the envelope states a runtime (%s), a channel count (%d) or a container (%q), none of which a CODECS attribute can say: this is a measurement standing where the declaration should be",
			declared.Duration, declared.AudioChannels, declared.ContentType)
	}
	t.Logf("the master declared %s/%s %dpx %s for the rung castor chose (%s)",
		declared.VideoCodec, declared.VideoProfile, declared.VideoHeight, declared.AudioCodec,
		primaryInput(t, resolved.Program).URL)

	for _, tt := range []struct {
		name       string
		decodes    media.VideoSupport // Only differing field between rows.
		handedOver bool               // Expected outcome.
	}{{
		// Master declared avc1.42c0__ (Constrained Baseline at 8 bits).
		name:       "a renderer advertising what the master declared is handed the URL",
		decodes:    media.VideoSupport{Codec: media.CodecH264, Profiles: []media.Profile{"Constrained Baseline", "Baseline", "Main", "High"}},
		handedOver: true,
	}, {
		// Only field differs; success means decision ignores declaration.
		name:    "a renderer advertising a codec the master did not declare is served instead",
		decodes: media.VideoSupport{Codec: media.CodecHEVC, Profiles: []media.Profile{"Main"}},
	}} {
		t.Run(tt.name, func(t *testing.T) {
			dev := selfFetching(tt.decodes)
			ctx, cancel := context.WithTimeout(t.Context(), pointedWithin)
			defer cancel()

			done := make(chan attempt.Outcome, 1)
			go func() {
				done <- newExecutor(castConfig(tl), dev, noStage).
					Run(ctx, attempt.Attempt{
						Try:       1,
						Program:   resolved.Program.Clone(),
						Origin:    resolved.Origin,
						Rendition: resolved.Rendition,
						Read:      read.ForProgram(resolved.Program, rwTimeout),
					})
			}()

			// Pass-through finishes when renderer accepts URL; transcode runs until media ends.
			var out attempt.Outcome
			returned := false
			if !tt.handedOver {
				select {
				case <-dev.played:
					cancel()
				case out = <-done:
					// Early return is failure; read outcome to avoid waiting on finished cast.
					returned = true
				case <-ctx.Done():
					t.Error("no renderer was pointed at anything within the bound")
				}
			}
			if !returned {
				out = <-done
			}

			played := dev.snapshot()
			if len(played) != 1 {
				t.Fatalf("the renderer was played %d times, want exactly 1: the cast reached %s and reported %v",
					len(played), out.Evidence.Reached, out.Err)
			}
			at, target := played[0], primaryInput(t, resolved.Program).URL.String()
			switch {
			case tt.handedOver && at != target:
				t.Errorf("the renderer was pointed at %q, want the source URL %q: it advertises everything the master declared for that rung, so every byte castor puts between the two is a byte nobody needed",
					at, target)
			case !tt.handedOver && at == target:
				t.Errorf("the renderer was handed the source URL %q though it advertises none of the codecs the master declared for that rung: nothing downstream reads the stream again, so this is a black screen with no cast left running to report it",
					at)
			case !tt.handedOver && !strings.HasPrefix(at, "http://"+localAddress+":"):
				t.Errorf("the renderer was pointed at %q, which is neither the source nor a stream served from the address this cast was given", at)
			}
			// Pass-through completes upon hand-off (no read, no encode, no delivery after URL given).
			if tt.handedOver {
				if out.Err != nil {
					t.Fatalf("the cast failed after handing over a URL the renderer accepted: %v", out.Err)
				}
				if out.Evidence.Reached != attempt.PhaseDelivered {
					t.Errorf("reached %s, want %s: a cast castor produces nothing for is over when the renderer takes the URL",
						out.Evidence.Reached, attempt.PhaseDelivered)
				}
			}
		})
	}
}

// primaryInput returns the program's clock owner, failing test if none exists.
func primaryInput(t *testing.T, program media.Program) media.Input {
	t.Helper()
	input, ok := program.PrimaryInput()
	if !ok {
		t.Fatalf("the resolved program has no primary input: its clock names %q, and it carries %+v", program.ClockInput, program.Inputs)
	}
	return input
}

// selfFetching creates a self-fetching renderer that drains both served and handed-off URLs.
func selfFetching(decodes media.VideoSupport) *servedRenderer {
	dev := &servedRenderer{decodes: media.Capabilities{
		SelfFetch:       true,
		Containers:      []string{media.HLS, media.MPEGTS, media.MP4},
		ServedContainer: media.MP4,
		Video:           []media.VideoSupport{decodes},
		Audio:           []media.AudioSupport{{Codec: media.CodecAAC, MaxChannels: 6}},
	}}
	dev.drain, dev.played = true, make(chan string, 1)
	return dev
}

// resolveOrigin measures the master before narrowing, then discards measurement to use declared codecs.
func resolveOrigin(t *testing.T, tl tools, o hostileOrigin) source.Resolution {
	t.Helper()

	cfg := source.Config{ProbeMaxConcurrency: 1, MaxHeight: castConfig(tl).MaxHeight}
	measured, err := source.NewRanker(cfg, probe.Candidate(tl.ffprobe, probeWithin)).
		Measure(t.Context(), &source.Candidate{URL: mustURL(t, o.URL)})
	if err != nil {
		t.Fatalf("measuring the origin's master: %v", err)
	}
	resolved, err := source.NewResolver(cfg, web.Playlists(probeWithin), config.Formats).RefetchProgram(t.Context(), measured, source.Rendition{})
	if err != nil {
		t.Fatalf("resolving the origin's master: %v", err)
	}
	return resolved
}

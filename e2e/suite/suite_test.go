// Package suite is the e2e framework's composition root, the only place strategies are listed, and runs every case in cases/:
// an origin streams the case's spec, castor casts it to a fake receiver on the device's own protocol, and the judge holds
// what was played to the case. Nothing here imports castor; its binary is the only boundary crossed.
package suite

import (
	"cmp"
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stupside/castor/e2e/judge"
	"github.com/stupside/castor/e2e/origin"
	"github.com/stupside/castor/e2e/receiver"
)

func TestEveryCaseCastsWhatItsReceiverCanPlay(t *testing.T) {
	tools := findTools(t)
	files, err := filepath.Glob("cases/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no cases found in cases/ (%v)", err)
	}
	for _, file := range files {
		t.Run(strings.TrimSuffix(filepath.Base(file), ".yaml"), func(t *testing.T) {
			t.Parallel()
			p := load(t, file)
			src := origin.Start(t, tools.FFmpeg, p.stream, p.behaviours)
			session := receiver.NewSession(t, receiver.Setup{Tools: tools, Players: players, Viewer: p.viewer})
			endpoint, err := p.device.Start(t, session)
			if err != nil {
				t.Fatal(err)
			}
			invocation := p.command.Invoke(t, src)
			launch, err := p.carrier.Carry(t, configDoc(t, p.castor, invocation.Config, p.device.Type(), endpoint.Host))
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), castTimeout)
			defer cancel()
			out, castErr := castor.Cast(ctx, launch, invocation.Args)
			exited := time.Now()
			t.Cleanup(func() {
				if t.Failed() {
					t.Logf("castor's output:\n%s", out)
				}
			})

			got, handed := session.Received(t, handOff)
			e := judge.Evidence{
				Origin: src, Endpoint: endpoint, Viewer: p.viewer, Received: got, Handed: handed,
				CastErr: castErr, Exited: exited, Killed: ctx.Err() != nil, Ceiling: p.ceiling,
			}
			played := got.Played
			t.Logf("castor exited %v; handed %q (%s): %s %dp %d-bit %s, %s %dch, %v",
				castErr, got.URL, got.ContentType, played.Video, played.Height, played.Depth, cmp.Or(played.Transfer, "untagged"),
				played.Audio, played.Channels, played.Duration)
			for _, check := range slices.Concat([]judge.Check{p.outcome}, p.checks, invocation.Checks) {
				for _, failure := range check.Judge(e) {
					t.Errorf("%s: %s", check.Name(), failure)
				}
			}
		})
	}
}

func findTools(t *testing.T) receiver.Tools {
	t.Helper()
	if testing.Short() {
		t.Skip("every case casts in real time; skipped in -short mode")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatalf("this suite drives real ffmpeg: %v", err)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatalf("this suite drives real ffprobe: %v", err)
	}
	return receiver.Tools{FFmpeg: ffmpeg, FFprobe: ffprobe}
}

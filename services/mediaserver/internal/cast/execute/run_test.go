package execute

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupside/castor/services/mediaserver/internal/cast/health"
	"github.com/stupside/castor/services/mediaserver/internal/cast/recovery"
	"github.com/stupside/castor/services/mediaserver/internal/media"
	"github.com/stupside/castor/services/mediaserver/internal/probe"
	"github.com/stupside/castor/services/mediaserver/internal/source"
)

// countedListeners is the delivery port a handed-off cast must never reach.
type countedListeners struct{ asked *atomic.Int64 }

func (a countedListeners) Listen(context.Context) (net.Listener, error) {
	a.asked.Add(1)
	return nil, errors.New("this host has no local route")
}

func handoffStream() *source.Stream {
	c := &source.Stream{URL: &url.URL{Scheme: "https", Host: "cdn.example", Path: "/movie.mp4"}, ContentType: media.MP4}
	c.Probe = &media.ProbeInfo{VideoCodec: media.CodecH264, VideoBitDepth: 8, AudioCodec: media.CodecAAC, AudioChannels: 2}
	return c
}

func TestAHandoffBuildsNoLocalMachinery(t *testing.T) {
	var asked atomic.Int64
	dev := &fakeDevice{caps: chromecastLike(media.MP4)}
	got := Cast{Timelines: direct{}, MaxHeight: 1080, Device: dev, Listeners: countedListeners{asked: &asked}}.Run(t.Context(),
		recovery.Attempt{Program: programFromStream(t, handoffStream())})
	if got.Err != nil {
		t.Fatalf("a handoff depended on local relay resources: %v", got.Err)
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("delivery listener opened %d time(s), want none for a handoff", n)
	}
	if got.Evidence.Reached != health.Delivered {
		t.Errorf("reached %s, want %s", got.Evidence.Reached, health.Delivered)
	}
}

func TestADeviceThatRefusesPlayIsBlamed(t *testing.T) {
	refused := errors.New("SOAP SetAVTransportURI: 714")
	dev := &fakeDevice{caps: chromecastLike(media.MP4), refuse: refused}
	out := realCast(dev, "", "").
		Run(t.Context(), recovery.Attempt{Try: 1, Program: programFromStream(t, handoffStream())})

	if out.Err == nil {
		t.Fatal("the cast reported success though the device refused the URL")
	}
	if !errors.Is(out.Evidence.PlayErr, refused) {
		t.Errorf("evidence carries PlayErr %v, want the device's own refusal %v", out.Evidence.PlayErr, refused)
	}
	if out.Evidence.Reached == health.Playing {
		t.Error("a cast whose device refused the URL reached Playing")
	}
}

// TestEveryAttemptOwnsAFreshWorkDirectoryAndLeavesNoneBehind is what makes a revised cast safe to offer.
func TestEveryAttemptOwnsAFreshWorkDirectoryAndLeavesNoneBehind(t *testing.T) {
	var dirs []string
	watchDir := func(_ context.Context, workDir string) Burn {
		dirs = append(dirs, workDir)
		if _, err := os.Stat(workDir); err != nil {
			t.Errorf("the read started with no work directory to buffer into: %v", err)
		}
		return nil
	}

	candidate := &source.Stream{URL: &url.URL{Scheme: "https", Host: "cdn.example"}, ContentType: media.MP4}
	for range 2 {
		// No ffmpeg configured, so the read fails after the work directory exists.
		program := programFromStream(t, candidate)
		// Sound the read must carry, so an unprobed read is still transcribed and the hook sees its directory.
		for i := range program.Tracks {
			program.Tracks[i].Optional = false
		}
		out := Cast{
			Probes: probe.FFprobe(""), Timelines: direct{}, InputArgs: formats.InputArgs,
			Device: &fakeDevice{caps: dlnaLike()}, Subtitles: watchDir, Listeners: loopback{},
		}.Run(t.Context(), recovery.Attempt{Program: program, Fetch: sourceFetchPlan(t, program, 30*time.Second)})
		if out.Err == nil {
			t.Fatal("a cast with no ffmpeg to read with reported success")
		}
	}

	if len(dirs) != 2 || dirs[0] == dirs[1] {
		t.Fatalf("two attempts ran in %v, want two different directories", dirs)
	}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("the abandoned attempt's directory %s is still on disk (%v)", dir, err)
		}
	}
}

// TestABufferCopiedWholeIsServedAsItIs: an encoder that would change nothing is not run, so the film is on disk once.
func TestABufferCopiedWholeIsServedAsItIs(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)
	origin := serveFixture(t, ffmpegPath)

	dev := &fakeDevice{caps: dlnaLike(), drain: true}
	c := realCast(dev, ffmpegPath, ffprobePath)
	program := programFromStream(t, origin.stream())

	ctx, cancel := context.WithTimeout(t.Context(), castTimeout)
	defer cancel()
	s := open(ctx, c, recovery.Attempt{Try: 1, Program: program, Fetch: sourceFetchPlan(t, program, testReadDeadline)})
	t.Cleanup(func() { _ = s.releases.release() })
	r, err := s.play()
	if err != nil {
		t.Fatalf("casting a program the device takes as it is: %v", err)
	}

	// An encoder would have written its delivery beside the buffer.
	spool := r.reader.spool.Path()
	entries, err := os.ReadDir(filepath.Dir(spool))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(spool) {
		t.Errorf("the work directory holds %v, want the read's buffer alone", entries)
	}
	spooled, err := os.ReadFile(spool)
	if err != nil {
		t.Fatal(err)
	}
	if len(spooled) == 0 || !bytes.Equal(dev.served, spooled) {
		t.Errorf("the device was served %d bytes, want exactly the %d the read buffered", len(dev.served), len(spooled))
	}
	if copied := evidence(r, nil, false).Copied; !copied.Video || !copied.Audio {
		t.Errorf("the outcome says the read copied %s, want both halves", copied)
	}
}

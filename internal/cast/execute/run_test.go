package execute

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/attempt"
	"github.com/stupside/castor/internal/device"
	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/probe"
	"github.com/stupside/castor/internal/source"
)

// countedListeners is the delivery port a passthrough cast must never reach.
type countedListeners struct{ asked *atomic.Int64 }

func (a countedListeners) Listen(context.Context) (net.Listener, error) {
	a.asked.Add(1)
	return nil, errors.New("this host has no local route")
}

func passthroughCandidate() *source.Stream {
	c := &source.Stream{URL: &url.URL{Scheme: "https", Host: "cdn.example", Path: "/movie.mp4"}, ContentType: media.MP4}
	c.Probe = &media.ProbeInfo{VideoCodec: media.CodecH264, VideoBitDepth: 8, AudioCodec: media.CodecAAC, AudioChannels: 2}
	return c
}

func TestPassthroughBuildsNoLocalMachinery(t *testing.T) {
	var asked atomic.Int64
	dev := &fakeDevice{caps: chromecastLike(media.MP4)}
	got := NewExecutor(Config{MaxHeight: 1080, Renderer: rendererOf(selfFetching(), dev), Listeners: countedListeners{asked: &asked}, Timelines: direct{}}).Run(t.Context(),
		attempt.Attempt{Program: programFromStream(t, passthroughCandidate())})
	if got.Err != nil {
		t.Fatalf("passthrough depended on local relay resources: %v", got.Err)
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("delivery listener opened %d time(s), want none for passthrough", n)
	}
	if got.Evidence.Reached != attempt.PhaseDelivered {
		t.Errorf("reached %s, want %s", got.Evidence.Reached, attempt.PhaseDelivered)
	}
}

func TestARendererThatRefusesPlayIsBlamedAndReleased(t *testing.T) {
	refused := errors.New("SOAP SetAVTransportURI: 714")
	dev := &fakeDevice{caps: chromecastLike(media.MP4), refuse: refused}
	out := newExecutor(castConfig(selfFetching(), "", ""), connectTo(dev), noStage).
		Run(t.Context(), attempt.Attempt{Try: 1, Program: programFromStream(t, passthroughCandidate())})

	if out.Err == nil {
		t.Fatal("the cast reported success though the renderer refused the URL")
	}
	if !errors.Is(out.Evidence.PlayErr, refused) {
		t.Errorf("evidence carries PlayErr %v, want the renderer's own refusal %v", out.Evidence.PlayErr, refused)
	}
	if out.Evidence.Reached == attempt.PhasePlaying {
		t.Error("a cast whose renderer refused the URL reached Playing")
	}
	if !dev.closed.Load() {
		t.Error("the renderer was never closed")
	}
}

func TestAReadOnceCastConnectsWhileItReads(t *testing.T) {
	ffmpegPath, ffprobePath := requireFFmpegTools(t)

	release := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(origin.Close)
	sourceURL, err := url.Parse(origin.URL + "/movie.mp4")
	if err != nil {
		t.Fatal(err)
	}

	connecting := make(chan struct{})
	connect := func(context.Context) (device.Device, error) {
		close(connecting)
		return &fakeDevice{caps: dlnaLike()}, nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- castOnce(ctx, t, castConfig(pushOnly(), ffmpegPath, ffprobePath), connect, &source.Stream{URL: sourceURL, ContentType: media.MP4})
	}()

	select {
	case <-connecting:
	case err := <-done:
		t.Fatalf("the cast ended before the renderer was connected: %v", err)
	case <-time.After(castTimeout):
		t.Fatal("the renderer was not connected while the source was still answering nothing")
	}
	close(release)
	cancel()
	<-done
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
		cfg := Config{
			Renderer:  rendererOf(pushOnly(), &fakeDevice{caps: dlnaLike()}),
			Subtitles: watchDir, Listeners: loopback{}, Probes: probe.FFprobe(""), Timelines: direct{},
		}
		out := NewExecutor(cfg).Run(t.Context(), attempt.Attempt{Program: program, Fetch: sourceReadPlan(t, program, 30*time.Second)})
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
	cfg := castConfig(pushOnly(), ffmpegPath, ffprobePath)
	cfg.Renderer = rendererOf(pushOnly(), dev)
	cfg.Listeners = loopback{}
	program := programFromStream(t, origin.stream())

	ctx, cancel := context.WithTimeout(t.Context(), castTimeout)
	defer cancel()
	s := open(ctx, cfg, attempt.Attempt{Try: 1, Program: program, Fetch: sourceReadPlan(t, program, testReadDeadline)})
	t.Cleanup(func() { _ = s.releases.release() })
	r, err := s.play()
	if err != nil {
		t.Fatalf("casting a program the renderer takes as it is: %v", err)
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
		t.Errorf("the renderer was served %d bytes, want exactly the %d the read buffered", len(dev.served), len(spooled))
	}
	if copied := evidence(r, nil, false).Copied; !copied.Video || !copied.Audio {
		t.Errorf("the outcome says the read copied %s, want both halves", copied)
	}
}

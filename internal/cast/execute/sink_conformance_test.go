package execute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/engine/deliver"
	"github.com/stupside/castor/internal/cast/policy/watch"
	"github.com/stupside/castor/internal/container"
	"github.com/stupside/castor/internal/media"
)

// madeBytes is what the encoder claims it made: the bytes the producer below writes.
const madeBytes = 1 << 16

// made returns the encoder's account; production binds it to running encoder progress.
func made() media.Progress {
	return media.Progress{Bytes: madeBytes, Position: 10 * time.Second}
}

// This file is the Sink port's specification, and every mechanism is judged by it.

type mechanism struct {
	name string
	// open builds the mechanism over a producer the case controls, and returns that producer.
	open func(t *testing.T) (sink Sink, producer *io.PipeWriter)
	// judged states whether this mechanism can be judged while it runs.
	judged bool
	// media is where the program's bytes are fetched, relative to URL.
	media string
}

// opened builds a sink the way a cast does, for the format named.
func opened(t *testing.T, contentType string) (Sink, *io.PipeWriter) {
	t.Helper()
	format, ok := container.FormatForContentType(contentType)
	if !ok {
		t.Fatalf("castor produces no %s", contentType)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, container.HLSPlaylistName), []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatalf("writing the playlist the mechanism serves: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf(container.HLSSegmentPattern, 0)), make([]byte, 188), 0o600); err != nil {
		t.Fatalf("writing the segment the mechanism serves: %v", err)
	}
	pr, pw := io.Pipe()
	sink, err := sinkFor(deliver.Opening{
		Format:        format,
		LocalIP:       "127.0.0.1",
		Dir:           dir,
		Out:           pr,
		IdleGrace:     50 * time.Millisecond,
		WriteDeadline: 300 * time.Millisecond,
	}, made)
	if err != nil {
		t.Fatalf("opening the %s delivery: %v", contentType, err)
	}
	return sink, pw
}

// mechanisms is every implementation of Sink. A new delivery is a row here.
func mechanisms() []mechanism {
	return []mechanism{
		{name: "streamed", judged: true, open: func(t *testing.T) (Sink, *io.PipeWriter) { return opened(t, media.MP4) }},
		{name: "segmented", judged: false, media: fmt.Sprintf(container.HLSSegmentPattern, 0), open: func(t *testing.T) (Sink, *io.PipeWriter) { return opened(t, media.HLS) }},
	}
}

// each runs one case per mechanism over a fresh sink and its producer.
func each(t *testing.T, check func(t *testing.T, m mechanism, sink Sink, producer *io.PipeWriter)) {
	for _, m := range mechanisms() {
		t.Run(m.name, func(t *testing.T) {
			sink, producer := m.open(t)
			t.Cleanup(func() { _ = producer.Close(); _ = sink.Close() })
			check(t, m, sink, producer)
		})
	}
}

func get(t *testing.T, u *url.URL) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("nothing answers at %q: %v", u, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestEveryMechanismAnswersAtItsURLAndSaysWhetherItIsJudgedInFlight(t *testing.T) {
	each(t, func(t *testing.T, m mechanism, sink Sink, _ *io.PipeWriter) {
		u := sink.URL()
		if u == nil || !u.IsAbs() {
			t.Fatalf("URL %v is not an absolute address to point a renderer at", u)
		}
		if resp := get(t, u); resp.StatusCode != http.StatusOK {
			t.Errorf("the address a renderer is pointed at answered %s", resp.Status)
		}
		if judged := sink.Audience() != nil; judged != m.judged {
			t.Errorf("audience present = %t, want %t: what a mechanism cannot measure it answers nil", judged, m.judged)
		}
	})
}

func TestEveryMechanismSettlesOnlyWhatTheRendererTook(t *testing.T) {
	each(t, func(t *testing.T, _ mechanism, sink Sink, producer *io.PipeWriter) {
		_ = producer.Close()
		<-sink.Drained()
		if _, ok := errors.AsType[*watch.Undelivered](sink.Settled()); !ok {
			t.Errorf("nobody fetched a delivery and Settled = %v, want *watch.Undelivered", sink.Settled())
		}
	})
	each(t, func(t *testing.T, m mechanism, sink Sink, producer *io.PipeWriter) {
		go func() {
			_, _ = producer.Write(make([]byte, madeBytes))
			_ = producer.Close()
		}()
		_, _ = io.Copy(io.Discard, get(t, sink.URL().ResolveReference(&url.URL{Path: m.media})).Body)
		<-sink.Drained()
		if err := sink.Settled(); err != nil {
			t.Errorf("the renderer fetched the whole delivery and it reports %v", err)
		}
	})
}

func TestEveryMechanismSaysWhenItHasReadTheProducerOut(t *testing.T) {
	each(t, func(t *testing.T, _ mechanism, sink Sink, producer *io.PipeWriter) {
		select {
		case <-sink.Drained():
			t.Fatal("Drained is closed while the producer is still running")
		default:
		}
		_ = producer.Close()
		select {
		case <-sink.Drained():
		case <-time.After(5 * time.Second):
			t.Fatal("the producer ended and Drained never closed")
		}
	})
}

// TestEveryMechanismFinishesReadingBeforeCloseReturns: teardown reaps the producer and removes its directory next.
func TestEveryMechanismFinishesReadingBeforeCloseReturns(t *testing.T) {
	each(t, func(t *testing.T, _ mechanism, sink Sink, producer *io.PipeWriter) {
		_ = producer.Close()
		_ = sink.Close()
		select {
		case <-sink.Drained():
		default:
			t.Error("Close returned while the producer was still being read")
		}
	})
}

func TestEveryMechanismStopsWhenTheCallerDoes(t *testing.T) {
	each(t, func(t *testing.T, _ mechanism, sink Sink, _ *io.PipeWriter) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		done := make(chan error, 1)
		go func() { done <- sink.Wait(ctx) }()
		select {
		case err := <-done:
			if err == nil {
				t.Error("Wait returned nil for a cancelled context")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Wait ignored a cancelled context")
		}
	})
}

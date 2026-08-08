package hlsserve

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContentTypeFor(t *testing.T) {
	tests := map[string]string{
		"/stream.m3u8":   "application/vnd.apple.mpegurl",
		"/seg_00001.m4s": "video/iso.segment",
		"/init.mp4":      "video/mp4",
		"/unknown.txt":   "",
	}
	for path, want := range tests {
		if got := contentTypeFor(path); got != want {
			t.Errorf("contentTypeFor(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestServe is what a renderer fetching the playlist gets back. The server hands
// out a directory a live encoder is writing into, so the file it is asked for may
// not exist yet, and both answers have to be right.
func TestServe(t *testing.T) {
	for _, tt := range []struct {
		name string
		// write, when set, is the playlist's content. Leaving it unset is the state
		// a cast starts in: the directory exists and the encoder has not cut
		// anything into it yet.
		write      string
		wantStatus int
		wantType   string
	}{{
		name:       "a playlist that exists is served with the type a player expects",
		write:      "#EXTM3U\n",
		wantStatus: http.StatusOK,
		wantType:   "application/vnd.apple.mpegurl",
	}, {
		name:       "a playlist the encoder has not written yet is a 404",
		wantStatus: http.StatusNotFound,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.write != "" {
				if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(tt.write), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			srv, err := New(Config{LocalIP: "127.0.0.1", Dir: dir, Playlist: "stream.m3u8"})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer srv.Close()

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL().String(), nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			if ct := resp.Header.Get("Content-Type"); ct != tt.wantType {
				t.Errorf("content-type = %q, want %q", ct, tt.wantType)
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != tt.write {
				t.Errorf("body = %q, want the playlist's contents %q", body, tt.write)
			}
		})
	}
}

func TestWaitReturnsOnContextCancel(t *testing.T) {
	srv, err := New(Config{LocalIP: "127.0.0.1", Dir: t.TempDir(), Playlist: "stream.m3u8"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer srv.Close()

	// Producer never finishes (live), so Wait blocks until ctx ends.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := srv.Wait(ctx); err == nil {
		t.Error("Wait() should return ctx error when the cast is cancelled mid-stream")
	}
}

func TestWaitReturnsAfterProducerDoneAndIdle(t *testing.T) {
	srv, err := New(Config{LocalIP: "127.0.0.1", Dir: t.TempDir(), Playlist: "stream.m3u8", IdleGrace: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer srv.Close()

	// The producer is still going, so Wait must NOT return yet even past the grace.
	early, earlyCancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer earlyCancel()
	if err := srv.Wait(early); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() returned %v before ProducerDone; want it to keep waiting", err)
	}

	// Once the producer is done and the client has been idle past the grace, the
	// finite cast completes cleanly (nil), not on ctx cancellation.
	srv.ProducerEnded()
	done, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := srv.Wait(done); err != nil {
		t.Errorf("Wait() after ProducerDone = %v, want nil (clean completion)", err)
	}
}

// TestFetchedReportsWhetherARendererCameAtAll is what makes "the renderer accepted Play and
// never fetched" sayable about a segmented delivery. Segment GETs are transient, so a live
// connection count here is nearly always zero and could never answer it; the request count
// can, and the seeded idle-grace timestamp must not be reported as a fetch nobody made.
func TestFetchedReportsWhetherARendererCameAtAll(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{LocalIP: "127.0.0.1", Dir: dir, Playlist: "stream.m3u8"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	if requests, last := srv.Fetched(); requests != 0 || !last.IsZero() {
		t.Fatalf("a directory nobody has fetched reports %d requests at %v", requests, last)
	}

	before := time.Now()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}

	requests, last := srv.Fetched()
	if requests != 1 {
		t.Errorf("requests = %d after one playlist GET, want 1", requests)
	}
	if last.Before(before) {
		t.Errorf("last fetch = %v, which predates the GET at %v", last, before)
	}
}

// TestHeadersTheRendererAskedForTravelWithEverySegment covers the field this delivery used to
// discard. The fault is invisible without it: the playlist 200s, every segment 200s, and a
// family that only fetches what its transfer-mode header announces simply never comes back
// for the second segment.
func TestHeadersTheRendererAskedForTravelWithEverySegment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{
		LocalIP:  "127.0.0.1",
		Dir:      dir,
		Playlist: "stream.m3u8",
		Headers:  map[string]string{"transferMode.dlna.org": "Streaming", "Content-Type": "text/plain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("transferMode.dlna.org"); got != "Streaming" {
		t.Errorf("transferMode.dlna.org = %q, want the value the renderer asked for", got)
	}
	// What a .m3u8 IS cannot be overridden by a device's preferences, so the artifact's own
	// type wins the collision.
	if got := resp.Header.Get("Content-Type"); got != "application/vnd.apple.mpegurl" {
		t.Errorf("content-type = %q, want the playlist's own type", got)
	}
}

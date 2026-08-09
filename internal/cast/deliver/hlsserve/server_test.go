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

	"github.com/stupside/castor/internal/media"
)

// TestThePlaylistIsServedAsWhatTheRendererWasToldToExpect is one file with one name for what it
// is. This server kept a table of its own and answered a playlist with a different spelling of the
// HLS type from the one the cast announces at Play, so the renderer was told to expect one thing
// and handed another by the same program.
func TestThePlaylistIsServedAsWhatTheRendererWasToldToExpect(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, media.HLSPlaylistName), []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{LocalIP: "127.0.0.1", Dir: dir, Playlist: media.HLSPlaylistName})
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
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != media.HLS {
		t.Errorf("the playlist is served as %q while the renderer is played %q: the same file has two names for what it is", got, media.HLS)
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
		wantType:   media.HLS,
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

// TestWaitReturnsAfterProducerDoneAndIdle is what Wait answers, and the second half of it is
// deliberate rather than an oversight: nothing was ever fetched here and Wait still reports the
// delivery over, because it reports that there is nothing left to serve and not that anybody
// took it. Whether any of the program reached the renderer is stated once, at the end of the
// cast, from Served. Teaching this loop to refuse as well would put the same judgement in two
// places and leave the one that carries the fault unreachable.
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

// TestServedCountsWhatOfTheProgramWentOut is what makes "the renderer accepted Play and never
// came for the program" sayable about a segmented delivery, and it is the only thing here that
// can say it: the producer finishes whatever the renderer does, and the idle grace's clock is
// seeded when the server is created, so both are satisfied by a cast nobody ever fetched.
//
// Every row is a request a real renderer makes, and each one either handed a piece of the
// program over or did not. The four that did not are the ways this count could quietly report a
// cast as watched: polling a playlist while refusing every segment (which is what a family
// served the wrong transfer-mode header does), probing with a HEAD, asking for a segment the
// rolling window already deleted, and asking for something that was never there.
func TestServedCountsWhatOfTheProgramWentOut(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"stream.m3u8":   "#EXTM3U\n#EXTINF:4,\nseg_00001.m4s\n",
		"init.mp4":      "\x00\x00\x00\x18ftyp",
		"seg_00001.m4s": "\x00\x00\x00\x18moof",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := New(Config{LocalIP: "127.0.0.1", Dir: dir, Playlist: "stream.m3u8"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	if got := srv.Served(); got != 0 {
		t.Fatalf("a directory nobody has fetched reports %d artifacts handed over", got)
	}

	for _, tt := range []struct {
		name   string
		method string
		path   string
		// handed reports whether this request put a piece of the program in the renderer's
		// hands, which is the only thing the count may go up on.
		handed bool
	}{{
		// Metadata, and the failure shape that makes this distinction load-bearing: a renderer
		// polling the playlist and never taking a segment has been served no media at all, while
		// looking from every other angle like one that is watching.
		name:   "the playlist a renderer polls",
		method: http.MethodGet,
		path:   "/stream.m3u8",
	}, {
		// A probe is not a fetch, for the reason the other sink gives: a renderer that asks
		// whether the URL exists and never gets the program is the exact failure being reported.
		name:   "a HEAD of a segment",
		method: http.MethodHead,
		path:   "/seg_00001.m4s",
	}, {
		// The segment the muxer deleted behind its window. A renderer that has fallen too far
		// behind asks for exactly this and gets nothing, so counting the request would report a
		// cast that handed the renderer nothing but errors as one that delivered.
		name:   "a segment the window already rolled off",
		method: http.MethodGet,
		path:   "/seg_00000.m4s",
	}, {
		name:   "a path nothing ever wrote",
		method: http.MethodGet,
		path:   "/master.m3u8",
	}, {
		// The directory itself, which the file server answers with a listing. It is a 200 carrying
		// no program at all, and anything on the network can ask for it.
		name:   "the directory the program is served out of",
		method: http.MethodGet,
		path:   "/",
	}, {
		// The decoder configuration. It is counted with the fragments, which acquits a renderer
		// that took the headers and then refused the picture: erring towards acquittal is the
		// only direction available to a delivery that cannot state a share.
		name:   "the initialisation segment",
		method: http.MethodGet,
		path:   "/init.mp4",
		handed: true,
	}, {
		name:   "a fragment of the program",
		method: http.MethodGet,
		path:   "/seg_00001.m4s",
		handed: true,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			before := srv.Served()
			fetch(t, srv, tt.method, tt.path)
			got := srv.Served() - before
			want := 0
			if tt.handed {
				want = 1
			}
			if got != want {
				t.Errorf("%s %s moved the count by %d, want %d", tt.method, tt.path, got, want)
			}
		})
	}
}

// TestTheIdleGraceStartsBeforeTheFirstRequest pins what the timestamp seeded at New is FOR,
// which is the thing a reader tempted to delete it would break. A renderer is pointed at the
// playlist and takes a moment to come; if the grace only started at the first request, a
// producer that finished before then (a short title, or an encode that outran the renderer's
// first fetch) would be reported as drained on the spot and the cast torn down under a renderer
// about to ask for the tail.
//
// The seed says "the grace starts now", and nothing else. It says nothing about whether anybody
// fetched, which is the count's job, and treating it as a fetch is what reported casts nobody
// ever came for as delivered.
// Both durations are derived from Wait's own polling interval rather than picked: the grace is
// three polls, so the wait is observed inside it, and the deadline is two, so a Wait that
// declares this delivery drained at its first look fails while one honouring the grace does not.
func TestTheIdleGraceStartsBeforeTheFirstRequest(t *testing.T) {
	srv, err := New(Config{LocalIP: "127.0.0.1", Dir: t.TempDir(), Playlist: "stream.m3u8", IdleGrace: 3 * pollInterval})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	srv.ProducerEnded()
	waiting, cancel := context.WithTimeout(t.Context(), 2*pollInterval)
	defer cancel()
	if err := srv.Wait(waiting); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v inside the grace that follows the producer's exit; a renderer coming for the tail segments has just had its window taken away", err)
	}
}

// TestARendererPollingThePlaylistIsNotIdle is the other half of the same distinction, and it
// is why the metadata requests above are still requests. A renderer between segments refreshes
// the playlist and nothing else; read as idleness, that ends the cast under a viewer who is
// watching it.
func TestARendererPollingThePlaylistIsNotIdle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The grace is six poll intervals, so a machine that loses five of them still does not
	// read this renderer as gone.
	const poll = 50 * time.Millisecond
	srv, err := New(Config{LocalIP: "127.0.0.1", Dir: dir, Playlist: "stream.m3u8", IdleGrace: 6 * poll})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.ProducerEnded()

	polling, stop := context.WithCancel(t.Context())
	defer stop()
	go func() {
		for polling.Err() == nil {
			// A failed request here is the test unwinding rather than a fact about the server, and
			// this goroutine is not the one that may end the test: what is being asserted is what
			// Wait does while the asking continues.
			_ = get(polling, srv, http.MethodGet, "/stream.m3u8")
			time.Sleep(poll)
		}
	}()

	// Long enough that a Wait reading the seeded timestamp alone, or one restarted only by
	// media requests, would have called this delivery over several graces ago.
	watching, giveUp := context.WithTimeout(t.Context(), 20*poll)
	defer giveUp()
	if err := srv.Wait(watching); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v while the renderer was refreshing the playlist; a cast someone is watching was reported as drained", err)
	}
	if got := srv.Served(); got != 0 {
		t.Errorf("the playlist polling handed over %d pieces of the program, want 0", got)
	}

	stop()
	drained, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := srv.Wait(drained); err != nil {
		t.Errorf("Wait = %v once the renderer stopped asking, want nil: the idle grace still ends a finished cast", err)
	}
}

// fetch makes one request of the server under test and reads whatever came back, which is what
// makes it a fetch as far as the server is concerned.
func fetch(t *testing.T, srv *Server, method, path string) {
	t.Helper()
	if err := get(t.Context(), srv, method, path); err != nil {
		t.Fatal(err)
	}
}

func get(ctx context.Context, srv *Server, method, path string) error {
	u := srv.URL()
	u.Path = path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return err
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
	if got := resp.Header.Get("Content-Type"); got != media.HLS {
		t.Errorf("content-type = %q, want the playlist's own type", got)
	}
}

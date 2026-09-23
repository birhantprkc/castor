package segments

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stupside/castor/internal/cast/engine/deliver"
	"github.com/stupside/castor/internal/media"
)

// Only a GET that hands over a present media artifact counts; the playlist, a HEAD and a 404 do not.
func TestServedCountsOnlyMediaHandedOver(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"stream.m3u8":   "#EXTM3U\n#EXTINF:4,\nseg_00001.m4s\n",
		"seg_00001.m4s": "\x00\x00\x00\x18moof",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv, _ := serving(t, config{
		LocalIP: "127.0.0.1", Dir: dir, Playlist: "stream.m3u8",
		Headers: map[string]string{"transferMode.dlna.org": "Streaming", "Content-Type": "text/plain"},
	})

	for _, tt := range []struct {
		method, path string
		want         int
		contentType  string
	}{
		{http.MethodGet, "/stream.m3u8", 0, media.HLS},
		{http.MethodHead, "/seg_00001.m4s", 0, ""},
		{http.MethodGet, "/seg_00000.m4s", 0, ""},
		{http.MethodGet, "/", 0, ""},
		{http.MethodGet, "/seg_00001.m4s", 1, "video/iso.segment"},
	} {
		t.Run(tt.method+tt.path, func(t *testing.T) {
			before := srv.Served()
			resp, err := get(t.Context(), srv, tt.method, tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := srv.Served() - before; got != tt.want {
				t.Errorf("the count moved by %d, want %d", got, tt.want)
			}
			if tt.contentType == "" {
				return
			}
			// The artifact's own type wins over the renderer's; the renderer's other headers travel.
			if got := resp.Header.Get("Content-Type"); got != tt.contentType {
				t.Errorf("content-type = %q, want %q", got, tt.contentType)
			}
			if got := resp.Header.Get("transferMode.dlna.org"); got != "Streaming" {
				t.Errorf("transferMode.dlna.org = %q, want the renderer's header", got)
			}
		})
	}
}

// The grace is seeded at open, so a renderer coming late for the tail segments still has its window.
func TestTheIdleGraceStartsBeforeTheFirstRequest(t *testing.T) {
	srv, ended := serving(t, config{LocalIP: "127.0.0.1", Dir: t.TempDir(), Playlist: "stream.m3u8", IdleGrace: 3 * deliver.SettleInterval})
	ended()
	waiting, cancel := context.WithTimeout(t.Context(), 2*deliver.SettleInterval)
	defer cancel()
	if err := srv.Wait(waiting); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v inside the grace that follows the producer's exit", err)
	}
}

func TestARendererPollingThePlaylistIsNotIdle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const poll = 50 * time.Millisecond
	srv, ended := serving(t, config{LocalIP: "127.0.0.1", Dir: dir, Playlist: "stream.m3u8", IdleGrace: 6 * poll})
	ended()

	polling, stop := context.WithCancel(t.Context())
	defer stop()
	go func() {
		for polling.Err() == nil {
			_, _ = get(polling, srv, http.MethodGet, "/stream.m3u8")
			time.Sleep(poll)
		}
	}()

	watching, giveUp := context.WithTimeout(t.Context(), 20*poll)
	defer giveUp()
	if err := srv.Wait(watching); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v while the renderer was polling the playlist", err)
	}

	stop()
	drained, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := srv.Wait(drained); err != nil {
		t.Errorf("Wait = %v once the renderer stopped asking, want nil", err)
	}
}

// serving starts a server; end closes the producer, which starts the idle grace.
func serving(t *testing.T, cfg config) (srv *Server, end func()) {
	t.Helper()
	pr, pw := io.Pipe()
	cfg.IdleGrace = cmp.Or(cfg.IdleGrace, 30*time.Second)
	srv, err := open(cfg, pr)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	end = sync.OnceFunc(func() { _ = pw.Close() })
	t.Cleanup(func() {
		end()
		_ = srv.Close()
	})
	return srv, end
}

func get(ctx context.Context, srv *Server, method, path string) (*http.Response, error) {
	u := srv.URL()
	u.Path = path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return resp, err
}

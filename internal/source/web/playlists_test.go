package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stupside/castor/internal/source/sourcetest"
)

func TestFetchReportsWhatTheOriginSaid(t *testing.T) {
	const body = "#EXTM3U\n#EXT-X-ENDLIST\n"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hls/master.m3u8":
			http.Redirect(w, r, "/edge/a/b/master.m3u8", http.StatusFound)
		case "/edge/a/b/master.m3u8":
			if got := r.Header.Get("Referer"); got != "https://player.example/" {
				t.Errorf("Referer = %q, want the captured header replayed", got)
			}
			_, _ = w.Write([]byte(body))
		case "/spent.m3u8":
			http.Error(w, "expired signature", http.StatusForbidden)
		case "/film.m3u8":
			_, _ = w.Write(make([]byte, documentLimit+1))
		}
	}))
	t.Cleanup(origin.Close)
	client := Playlists(5 * time.Second)

	// Relative URIs resolve against where the document was served from, not the URL asked for.
	got, from, status, err := client.Fetch(t.Context(), sourcetest.URL(t, origin.URL+"/hls/master.m3u8"), http.Header{"Referer": {"https://player.example/"}})
	if err != nil || status != http.StatusOK || got != body {
		t.Fatalf("Fetch = (%q, %d, %v), want the body with 200", got, status, err)
	}
	if from.Path != "/edge/a/b/master.m3u8" {
		t.Errorf("from = %q, want the redirected path", from.Path)
	}
	if _, _, status, err := client.Fetch(t.Context(), sourcetest.URL(t, origin.URL+"/spent.m3u8"), nil); err == nil || status != http.StatusForbidden {
		t.Errorf("Fetch = (%d, %v), want a 403 error", status, err)
	}
	if _, _, _, err := client.Fetch(t.Context(), sourcetest.URL(t, origin.URL+"/film.m3u8"), nil); err == nil {
		t.Error("a body past the document limit was read whole")
	}
}

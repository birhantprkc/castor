package httpfetch

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// TestFetchReportsWhatTheOriginSaid pins the reason this adapter returns a status
// at all: the caller has to be able to tell a refusal from silence. Collapsing
// both into one error is how a spent signed link (403, fatal, only a fresh
// extraction helps) and an origin that never answered (transient, worth another
// read) became the same single warn.
func TestFetchReportsWhatTheOriginSaid(t *testing.T) {
	const body = "#EXTM3U\n#EXT-X-ENDLIST\n"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.m3u8":
			if got := r.Header.Get("Referer"); got != "https://player.example/" {
				t.Errorf("Referer = %q, want the captured header replayed to the origin", got)
			}
			_, _ = w.Write([]byte(body))
		case "/spent.m3u8":
			http.Error(w, "expired signature", http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(origin.Close)

	client := New(5 * time.Second)
	headers := http.Header{"Referer": {"https://player.example/"}}

	t.Run("a served document comes back with its status", func(t *testing.T) {
		got, status, err := client.Fetch(t.Context(), mustParse(t, origin.URL+"/ok.m3u8"), headers)
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if got != body {
			t.Errorf("body = %q, want %q", got, body)
		}
		if status != http.StatusOK {
			t.Errorf("status = %d, want 200", status)
		}
	})

	t.Run("a refusal fails and names the status", func(t *testing.T) {
		_, status, err := client.Fetch(t.Context(), mustParse(t, origin.URL+"/spent.m3u8"), nil)
		if err == nil {
			t.Fatal("a 403 must fail the fetch")
		}
		if status != http.StatusForbidden {
			t.Errorf("status = %d, want 403 travelling out beside the error", status)
		}
	})

	t.Run("no answer at all reports no status", func(t *testing.T) {
		// The server is up but this port is not: nothing answers, so there is no
		// status to report and zero is the only honest one.
		_, status, err := client.Fetch(t.Context(), mustParse(t, "http://127.0.0.1:0/nothing.m3u8"), nil)
		if err == nil {
			t.Fatal("an unreachable origin must fail the fetch")
		}
		if status != 0 {
			t.Errorf("status = %d, want 0 (nothing answered)", status)
		}
	})
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

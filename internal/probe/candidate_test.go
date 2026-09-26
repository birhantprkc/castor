package probe

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stupside/castor/internal/media"
	"github.com/stupside/castor/internal/source"
)

func requireFFprobe(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not on PATH")
	}
	return path
}

func streamAt(t *testing.T, raw string) *source.Candidate {
	t.Helper()
	return &source.Candidate{URL: mustURL(t, raw)}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// Refusals ffprobe names only at warning level must still classify as refused.
func TestMeasureCarriesTheOriginsRefusal(t *testing.T) {
	probes := Candidate(requireFFprobe(t), 30*time.Second)
	const mediaPlaylist = "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\nseg0.ts\n#EXTINF:2.0,\nseg1.ts\n#EXT-X-ENDLIST\n"
	const masterPlaylist = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\nvariant.m3u8\n"

	// refusing serves the playlist at its own path and refuses everything else.
	refusing := func(playlist string, status int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if playlist != "" && r.URL.Path == "/signed.m3u8" {
				w.Header().Set("Content-Type", media.HLS)
				_, _ = w.Write([]byte(playlist))
				return
			}
			http.Error(w, "nope", status)
		}
	}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		status  string
	}{
		{"direct 403", refusing("", http.StatusForbidden), "403"},
		{"direct 410", refusing("", http.StatusGone), "410"},
		{"segments 410", refusing(mediaPlaylist, http.StatusGone), "410"},
		{"variant 403", refusing(masterPlaylist, http.StatusForbidden), "403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin := httptest.NewServer(tc.handler)
			t.Cleanup(origin.Close)
			_, reach, err := probes(streamAt(t, origin.URL+"/signed.m3u8")).Probe(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.status) {
				t.Errorf("error = %v, want the origin's %s in it", err, tc.status)
			}
			if reach != media.ReachRefused {
				t.Errorf("reach = %s, want refused", reach)
			}
		})
	}
}

// A probe castor killed at its own budget proves nothing about the origin.
func TestMeasureReportsATarpitAsUnproven(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-mpegURL")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(origin.Close)

	_, reach, err := Candidate(requireFFprobe(t), 300*time.Millisecond)(streamAt(t, origin.URL+"/slow.m3u8")).Probe(t.Context())
	if err == nil || reach != media.ReachUnproven {
		t.Errorf("Probe = (%s, %v), want an unproven failure", reach, err)
	}
}

func TestClassifyReach(t *testing.T) {
	for _, tc := range []struct {
		stderr string
		want   media.Reach
	}{
		{"signed.m3u8: Server returned 404 Not Found\n", media.ReachRefused},
		{"[http @ 0x14] HTTP error 429 Too Many Requests\n", media.ReachUnproven},
		{"signed.m3u8: Server returned 503 Service Unavailable\n", media.ReachUnproven},
	} {
		if got := classifyReach(tc.stderr); got != tc.want {
			t.Errorf("classifyReach(%q) = %s, want %s", tc.stderr, got, tc.want)
		}
	}
}

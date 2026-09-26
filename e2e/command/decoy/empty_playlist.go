package decoy

import (
	"io"
	"net/http"

	"github.com/stupside/castor/e2e/command"
	"github.com/stupside/castor/e2e/origin"
)

// EmptyPlaylist is a manifest that lists nothing, as anti-bot pages serve in place of the real one (#14).
type EmptyPlaylist struct{}

func (EmptyPlaylist) Name() string { return "empty-playlist" }

func (EmptyPlaylist) Mount(mux *http.ServeMux, _ *origin.Origin) string {
	mux.HandleFunc("/cdn/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-mpegURL")
		_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:1\n#EXT-X-ENDLIST\n")
	})
	return command.Fetch("/cdn/master.m3u8")
}

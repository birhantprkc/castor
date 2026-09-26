package decoy

import (
	"io"
	"net/http"

	"github.com/stupside/castor/e2e/command"
	"github.com/stupside/castor/e2e/origin"
)

// Image is the player's poster, which is not media however a page requests it (#60).
type Image struct{}

func (Image) Name() string { return "image" }

func (Image) Mount(mux *http.ServeMux, _ *origin.Origin) string {
	mux.HandleFunc("/assets/poster.svg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="9"/>`)
	})
	return command.Fetch("/assets/poster.svg")
}

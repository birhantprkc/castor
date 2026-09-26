package decoy

import (
	"net/http"

	"github.com/stupside/castor/e2e/origin"
)

// Blob is a media source the page built in memory, a blob: URL only that page can read (#49).
type Blob struct{}

func (Blob) Name() string { return "blob" }

func (Blob) Mount(*http.ServeMux, *origin.Origin) string {
	return `fetch(URL.createObjectURL(new Blob([new Uint8Array(4096)], {type: "video/mp4"}))).catch(function () {});`
}

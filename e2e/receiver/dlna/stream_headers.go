package dlna

import (
	"fmt"
	"net/http"
)

// streamHeaders holds the served stream to what DLNA devices require of the response.
func streamHeaders(declared string, h http.Header) []string {
	var problems []string
	if got := h.Get("transferMode.dlna.org"); got != "Streaming" {
		problems = append(problems, fmt.Sprintf("transferMode.dlna.org = %q, want Streaming", got))
	}
	if h.Get("contentFeatures.dlna.org") == "" {
		problems = append(problems, "no contentFeatures.dlna.org on the served stream")
	}
	if got := h.Get("Content-Type"); got != declared {
		problems = append(problems, fmt.Sprintf("served Content-Type %q, but the DIDL declared %q", got, declared))
	}
	return problems
}

package dlna

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
)

// streamHeaders holds the served stream to what DLNA renderers require of the response.
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

// didlContentType reads the MIME the DIDL-Lite res declares (http-get:*:<mime>:<features>).
func didlContentType(meta string) string {
	var didl struct {
		Res struct {
			ProtocolInfo string `xml:"protocolInfo,attr"`
		} `xml:"item>res"`
	}
	if err := xml.Unmarshal([]byte(meta), &didl); err != nil {
		return ""
	}
	fields := strings.SplitN(didl.Res.ProtocolInfo, ":", 4)
	if len(fields) < 3 {
		return ""
	}
	return fields[2]
}

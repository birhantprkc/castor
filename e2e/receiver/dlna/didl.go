package dlna

import (
	"encoding/xml"
	"strings"
)

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

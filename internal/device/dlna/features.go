package dlna

import (
	"fmt"

	"github.com/stupside/castor/internal/media"
)

// SupportedContentTypes lists the content types that DLNA renderers support.
var SupportedContentTypes = []string{"video/mp2t", media.MP4}

// DLNA.ORG_FLAGS values (see DLNA Guidelines, Vol. 1, Table 4-129).
// flagsLive: SENDER_PACED+S0_INCREASE+SN_INCREASE+STREAMING+HTTP_STALLING+DLNA_V15.
// flagsFile: STREAMING+HTTP_STALLING+DLNA_V15.
const (
	flagsLive = "8D300000000000000000000000000000"
	flagsFile = "01300000000000000000000000000000"
)

// profileFor returns the DLNA PN and FLAGS for a content type.
// MPEG_TS_HD_NA_ISO is for ffmpeg's 188-byte TS; the bare MPEG_TS_HD_NA
// profile is for 192-byte timestamped packets and Samsung rejects the mismatch.
func profileFor(contentType string) (name, flags string) {
	switch contentType {
	case "video/mp2t":
		return "MPEG_TS_HD_NA_ISO", flagsLive
	case "video/mp4":
		return "AVC_MP4_HP_HD_AAC", flagsFile
	}
	return "", flagsLive
}

// ContentFeatures returns a DLNA content features string for use in HTTP
// headers and DIDL metadata.
func ContentFeatures(contentType string) string {
	name, flags := profileFor(contentType)
	return fmt.Sprintf("DLNA.ORG_PN=%s;DLNA.ORG_OP=00;DLNA.ORG_CI=1;DLNA.ORG_FLAGS=%s", name, flags)
}

// StreamHeaders returns HTTP headers a DLNA renderer expects on a stream response.
// No Content-Length is set: the stream length is unknown and Samsung firmwares
// have been observed to mis-parse very large 64-bit values.
func StreamHeaders(contentType string) map[string]string {
	return map[string]string{
		"Connection":               "close",
		"Accept-Ranges":            "none",
		"transferMode.dlna.org":    "Streaming",
		"contentFeatures.dlna.org": ContentFeatures(contentType),
	}
}

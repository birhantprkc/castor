package dlna

import (
	"encoding/xml"
	"fmt"
	"net/url"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"

	"github.com/stupside/castor/services/apiserver/internal/device"
)

// servedHeaders are the headers a DLNA renderer expects on a response of container.
func servedHeaders(container mediav1.Container) map[string]string {
	return map[string]string{
		"Connection":               "close",
		"Accept-Ranges":            "none",
		"transferMode.dlna.org":    "Streaming",
		"contentFeatures.dlna.org": contentFeatures(container),
	}
}

// DLNA.ORG_FLAGS advertised for a live stream and for a whole file.
const (
	dlnaFlagsLive = "8D300000000000000000000000000000"
	dlnaFlagsFile = "01300000000000000000000000000000"
)

// contentFeatures is the DLNA PN and FLAGS a container is announced with.
func contentFeatures(container mediav1.Container) string {
	name, flags := "", dlnaFlagsLive
	switch container {
	case mediav1.Container_CONTAINER_MPEGTS:
		name = "MPEG_TS_HD_NA_ISO"
	case mediav1.Container_CONTAINER_MP4:
		name, flags = "AVC_MP4_HP_HD_AAC", dlnaFlagsFile
	}
	return fmt.Sprintf("DLNA.ORG_PN=%s;DLNA.ORG_OP=00;DLNA.ORG_CI=1;DLNA.ORG_FLAGS=%s", name, flags)
}

type didlLite struct {
	XMLName xml.Name `xml:"DIDL-Lite"`
	XMLNS   string   `xml:"xmlns,attr"`
	DC      string   `xml:"xmlns:dc,attr"`
	UPnP    string   `xml:"xmlns:upnp,attr"`
	Item    didlItem `xml:"item"`
}

type didlItem struct {
	ID         string  `xml:"id,attr"`
	ParentID   string  `xml:"parentID,attr"`
	Restricted string  `xml:"restricted,attr"`
	Title      string  `xml:"dc:title"`
	Class      string  `xml:"upnp:class"`
	Res        didlRes `xml:"res"`
}

type didlRes struct {
	ProtocolInfo string `xml:"protocolInfo,attr"`
	Value        string `xml:",chardata"`
}

// buildDIDLMetadata returns the DIDL-Lite XML the renderer needs to play streamURL.
func buildDIDLMetadata(streamURL *url.URL, container mediav1.Container) (string, error) {
	item := didlLite{
		XMLNS: "urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/",
		DC:    "http://purl.org/dc/elements/1.1/",
		UPnP:  "urn:schemas-upnp-org:metadata-1-0/upnp/",
		Item: didlItem{
			ID:         "0",
			ParentID:   "-1",
			Restricted: "1",
			Title:      "Castor Stream",
			Class:      "object.item.videoItem",
			Res: didlRes{
				ProtocolInfo: fmt.Sprintf("http-get:*:%s:%s", device.MIME(container), contentFeatures(container)),
				Value:        streamURL.String(),
			},
		},
	}

	data, err := xml.Marshal(item)
	if err != nil {
		return "", fmt.Errorf("marshaling DIDL-Lite: %w", err)
	}
	return string(data), nil
}

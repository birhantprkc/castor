package dlna

import (
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/stupside/castor/e2e/receiver"
)

// upnp is the device's UPnP side: its description, ConnectionManager and AVTransport.
type upnp struct {
	session *receiver.Session
	sink    string
	fetch   func(s *receiver.Session, uri, declared string)

	mu sync.Mutex
	// locked is how many more SetAVTransportURI the transport refuses.
	locked int
	// uri and meta are what SetAVTransportURI staged for Play.
	uri, meta string
	// reportedPlaying holds STOPPED back until castor has seen PLAYING, as a real transport does.
	reportedPlaying bool
}

// transportStates is AVTransport's word for each playback state.
var transportStates = map[receiver.Playback]string{
	receiver.Idle:    "NO_MEDIA_PRESENT",
	receiver.Playing: "PLAYING",
	receiver.Ended:   "STOPPED",
	receiver.Stopped: "STOPPED",
}

func (u *upnp) description(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	fmt.Fprintf(w, `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <device>
    <deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType>
    <friendlyName>castor e2e</friendlyName>
    <UDN>uuid:castor-e2e</UDN>
    <serviceList>
      <service><serviceType>%s</serviceType><serviceId>urn:upnp-org:serviceId:ConnectionManager</serviceId><SCPDURL>/cm.xml</SCPDURL><controlURL>/control/cm</controlURL><eventSubURL>/event/cm</eventSubURL></service>
      <service><serviceType>%s</serviceType><serviceId>urn:upnp-org:serviceId:AVTransport</serviceId><SCPDURL>/avt.xml</SCPDURL><controlURL>/control/avt</controlURL><eventSubURL>/event/avt</eventSubURL></service>
    </serviceList>
  </device>
</root>`, connectionManager, avTransport)
}

// envelope is a SOAP request body, read by local name as a device's stack does.
type envelope struct {
	CurrentURI         string `xml:"Body>SetAVTransportURI>CurrentURI"`
	CurrentURIMetaData string `xml:"Body>SetAVTransportURI>CurrentURIMetaData"`
}

// transportLocked is UPnP AVTransport's fault for a transport that cannot take a new URI yet.
const transportLocked = 705

// actions answer each SOAP action this device implements with its response body, or a UPnP fault code.
func (u *upnp) actions() map[string]func(envelope) (string, int) {
	return map[string]func(envelope) (string, int){
		"GetProtocolInfo": func(envelope) (string, int) {
			return "<Source></Source><Sink>" + html.EscapeString(u.sink) + "</Sink>", 0
		},
		"SetAVTransportURI": func(in envelope) (string, int) {
			u.mu.Lock()
			defer u.mu.Unlock()
			if u.locked > 0 {
				u.locked--
				return "", transportLocked
			}
			u.uri, u.meta = in.CurrentURI, in.CurrentURIMetaData
			return "", 0
		},
		"Play": func(envelope) (string, int) {
			u.mu.Lock()
			uri, meta := u.uri, u.meta
			u.mu.Unlock()
			if uri == "" {
				u.session.Problem("Play before SetAVTransportURI")
				return "", 0
			}
			u.fetch(u.session, uri, didlContentType(meta))
			return "", 0
		},
		"GetTransportInfo": func(envelope) (string, int) {
			return "<CurrentTransportState>" + u.transportState() + "</CurrentTransportState><CurrentTransportStatus>OK</CurrentTransportStatus><CurrentSpeed>1</CurrentSpeed>", 0
		},
	}
}

func (u *upnp) control(w http.ResponseWriter, req *http.Request) {
	service, name, ok := strings.Cut(strings.Trim(req.Header.Get("SOAPACTION"), `"`), "#")
	if !ok {
		http.Error(w, "no SOAPACTION", http.StatusBadRequest)
		return
	}
	act, ok := u.actions()[name]
	if !ok {
		u.session.Problem("unexpected SOAP action %s#%s", service, name)
		http.Error(w, "unimplemented action", http.StatusInternalServerError)
		return
	}
	raw, _ := io.ReadAll(req.Body)
	var in envelope
	if err := xml.Unmarshal(raw, &in); err != nil {
		u.session.Problem("%s: undecodable SOAP body: %v", name, err)
	}
	body, fault := act(in)
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	if fault != 0 {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><s:Fault><faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>%d</errorCode><errorDescription>Transport is locked</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`, fault)
		return
	}
	fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:%sResponse xmlns:u="%s">%s</u:%sResponse></s:Body></s:Envelope>`,
		name, service, body, name)
}

func (u *upnp) transportState() string {
	state := u.session.State()
	u.mu.Lock()
	defer u.mu.Unlock()
	if state.Over() && !u.reportedPlaying {
		state = receiver.Playing
	}
	u.reportedPlaying = u.reportedPlaying || state == receiver.Playing
	return transportStates[state]
}

package roku

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
	"github.com/stupside/castor/services/apiserver/internal/device"
	"github.com/stupside/castor/services/apiserver/internal/device/devicetest"
)

func ecpDevice(t *testing.T, ts *httptest.Server) *rokuDevice {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &rokuDevice{ecp: u, appID: "dev", name: "Bedroom Roku", hc: ts.Client()}
}

func TestTheChannelIsLaunchedWithTheFormatOfWhatItPlays(t *testing.T) {
	launched := make(chan url.Values, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/launch/dev" {
			launched <- r.URL.Query()
		}
	}))
	defer ts.Close()
	dev := ecpDevice(t, ts)
	stream := &url.URL{Scheme: "http", Host: "192.0.2.1:8080", Path: "/stream"}
	for container, want := range map[mediav1.Container]string{
		mediav1.Container_CONTAINER_MP4: "mp4",
		mediav1.Container_CONTAINER_MKV: "mkv",
		mediav1.Container_CONTAINER_HLS: "hls",
	} {
		if err := dev.Play(t.Context(), stream, container); err != nil {
			t.Fatal(err)
		}
		if got := <-launched; got.Get("format") != want || got.Get("url") != stream.String() {
			t.Errorf("playing %v launched the channel with %v, want format %q of the stream", container, got, want)
		}
	}
}

func TestInstallOutcome(t *testing.T) {
	for _, tt := range []struct {
		body    string
		wantErr bool
	}{
		{`<font color="green">Install Success.</font>`, false},
		{`Identical to previous version -- not replacing.`, false},
		{`<font color="red">Install Failure: Compilation Failed.</font>`, true},
		// No success marker, so a foreign dev page cannot pass a broken install off.
		{`<html>ok</html>`, true},
	} {
		if err := installOutcome([]byte(tt.body)); (err != nil) != tt.wantErr {
			t.Errorf("installOutcome(%q) err = %v, wantErr %v", tt.body, err, tt.wantErr)
		}
	}
}

func TestInstallChannelDigestUpload(t *testing.T) {
	var archiveLen int
	var challenged bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			challenged = true
			w.Header().Set("WWW-Authenticate", `Digest realm="rt", nonce="abc123", qop="auth"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("parsing content type: %v", err)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for part, err := mr.NextPart(); err == nil; part, err = mr.NextPart() {
			if part.FormName() == "archive" {
				b, _ := io.ReadAll(part)
				archiveLen = len(b)
			}
		}
		_, _ = io.WriteString(w, "Install Success.")
	}))
	defer ts.Close()

	if err := installChannel(t.Context(), ts.URL+"/plugin_install", "secret", []byte("PK\x03\x04fake-zip-bytes")); err != nil {
		t.Fatalf("installChannel() error = %v", err)
	}
	if !challenged || archiveLen == 0 {
		t.Errorf("challenged=%v archive=%d bytes, want a digest handshake carrying the archive", challenged, archiveLen)
	}
}

func TestConnectVerifiesTheChannelItWillLaunch(t *testing.T) {
	for _, tt := range []struct {
		name, apps, appID string
		wantErr           bool
	}{
		{name: "castor's dev channel is installed", apps: `<apps><app id="dev">Castor</app></apps>`},
		{name: "a foreign dev channel and no password", apps: `<apps><app id="dev">SomeoneElse</app></apps>`, wantErr: true},
		{name: "a published app that is not installed", apps: `<apps><app id="12345">MyChannel</app></apps>`, appID: "99999", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/query/apps" {
					http.NotFound(w, r)
					return
				}
				_, _ = io.WriteString(w, tt.apps)
			}))
			defer ts.Close()
			_, err := Family{Config: Config{AppID: tt.appID}}.Connect(t.Context(), device.Info{Name: "Bedroom Roku", Type: familyType, Address: ts.URL})
			if (err != nil) != tt.wantErr {
				t.Errorf("Connect() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// refusingAfter answers the media-player query as playing its first polls, then refuses every one, as a Roku unplugged would.
type refusingAfter struct{ answers, polls atomic.Int32 }

func (e *refusingAfter) RoundTrip(*http.Request) (*http.Response, error) {
	if e.polls.Add(1) > e.answers.Load() {
		return nil, errors.New("connect: connection refused")
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`<player error="false" state="play"/>`))}, nil
}

func TestARokuThatStopsAnsweringIsNamedGone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ecp := &refusingAfter{}
		ecp.answers.Store(2)
		dev := &rokuDevice{ecp: &url.URL{Scheme: "http", Host: "192.0.2.10:8060"}, name: "Bedroom Roku", hc: &http.Client{Transport: ecp}}
		away, gone := errors.AsType[*device.Gone](dev.AwaitEnd(t.Context()))
		if !gone {
			t.Fatal("AwaitEnd did not name the Roku gone once it stopped answering")
		}
		if away.Device != "Bedroom Roku" || away.Err == nil {
			t.Errorf("gone = %+v, want the device named and the last failure carried", away)
		}
	})
}

// playingECP answers every media-player query as playing, counting the polls.
type playingECP struct{ polls atomic.Int32 }

func (e *playingECP) RoundTrip(*http.Request) (*http.Response, error) {
	e.polls.Add(1)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`<player error="false" state="play"/>`))}, nil
}

func TestRokuAnswersWhenTheCastEnds(t *testing.T) {
	ecp := &playingECP{}
	devicetest.AwaitsTheCastsEnd(t, func() device.Device {
		return &rokuDevice{ecp: &url.URL{Scheme: "http", Host: "192.0.2.10:8060"}, name: "Bedroom Roku", hc: &http.Client{Transport: ecp}}
	})
	if ecp.polls.Load() == 0 {
		t.Error("the suite never polled the Roku")
	}
}

func TestLocate(t *testing.T) {
	for address, want := range map[string]string{
		"192.168.0.3":              "http://192.168.0.3:8060",
		"http://192.168.0.3:8888/": "http://192.168.0.3:8888",
	} {
		if got, err := (Family{}).Locate(t.Context(), address); err != nil || got != want {
			t.Errorf("Locate(%q) = %q, %v, want %q", address, got, err, want)
		}
	}
}

func TestRokuChannelZipRendersChannel(t *testing.T) {
	b, err := rokuChannelZip()
	if err != nil {
		t.Fatalf("rokuChannelZip() error = %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("reading zip: %v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(data)
	}
	if !strings.Contains(files["manifest"], "title="+rokuChannelTitle) {
		t.Errorf("manifest missing from the archive root or not rendered: %q", files["manifest"])
	}
	// The .tmpl suffix is dropped and the launch params wired.
	scene := files["components/MainScene.brs"]
	if !strings.Contains(scene, `a["`+rokuChannelParamURL+`"]`) || !strings.Contains(scene, `a["`+rokuChannelParamFormat+`"]`) {
		t.Errorf("scene did not wire launch params:\n%s", scene)
	}
}

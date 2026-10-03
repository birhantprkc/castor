package roku

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

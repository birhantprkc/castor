package roku

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestTheChannelZipRendersTheChannel(t *testing.T) {
	b, err := channelZip()
	if err != nil {
		t.Fatalf("channelZip() error = %v", err)
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
	if !strings.Contains(files["manifest"], "title="+channelTitle) {
		t.Errorf("manifest missing from the archive root or not rendered: %q", files["manifest"])
	}
	// The .tmpl suffix is dropped and the launch params wired.
	scene := files["components/MainScene.brs"]
	if !strings.Contains(scene, `a["`+paramURL+`"]`) || !strings.Contains(scene, `a["`+paramFormat+`"]`) {
		t.Errorf("scene did not wire launch params:\n%s", scene)
	}
}

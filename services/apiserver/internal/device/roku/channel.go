package roku

import (
	"archive/zip"
	"bytes"
	"embed"
	"io/fs"
	"strings"
	"text/template"
)

// Castor's Roku channel is a SceneGraph app whose only job is to play a stream URL passed over ECP.

// Launch parameter names the channel reads and the launcher must send.
const (
	rokuChannelParamURL    = "url"
	rokuChannelParamFormat = "format"
)

const rokuChannelTitle = "Castor"

//go:embed rokuassets
var rokuChannelAssets embed.FS

var rokuChannelData = struct {
	Title       string
	ParamURL    string
	ParamFormat string
}{rokuChannelTitle, rokuChannelParamURL, rokuChannelParamFormat}

// rokuChannelZip packs the rendered channel into a sideload archive with the manifest at the root.
func rokuChannelZip() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	err := fs.WalkDir(rokuChannelAssets, "rokuassets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := rokuChannelAssets.ReadFile(p)
		if err != nil {
			return err
		}
		name, content, err := renderRokuAsset(strings.TrimPrefix(p, "rokuassets/"), raw)
		if err != nil {
			return err
		}
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(content)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// renderRokuAsset expands a .tmpl source and drops the suffix; anything else passes through unchanged.
func renderRokuAsset(name string, raw []byte) (string, []byte, error) {
	if !strings.HasSuffix(name, ".tmpl") {
		return name, raw, nil
	}
	t, err := template.New(name).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return "", nil, err
	}
	var out bytes.Buffer
	if err := t.Execute(&out, rokuChannelData); err != nil {
		return "", nil, err
	}
	return strings.TrimSuffix(name, ".tmpl"), out.Bytes(), nil
}

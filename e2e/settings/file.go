package settings

import (
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
)

// File writes the document to a config.yaml castor is pointed at with -c.
type File struct{}

func (File) Name() string { return "file" }

func (File) Carry(t *testing.T, doc map[string]any) (Launch, error) {
	out, err := yaml.Marshal(doc)
	if err != nil {
		return Launch{}, err
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return Launch{}, err
	}
	return Launch{Flags: []string{"-c", path}, Dir: t.TempDir()}, nil
}

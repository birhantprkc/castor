package player

import (
	"context"
	"io"
	"os"

	"github.com/stupside/castor/e2e/receiver"
)

// File reads a single response to its end; it takes anything, so it belongs last.
type File struct{}

func (File) Name() string { return "file" }

func (File) Plays([]byte) bool { return true }

func (File) Record(_ context.Context, rec receiver.Recording) (string, error) {
	f, err := os.Create(rec.Tape)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = io.Copy(f, rec.Viewer.Pace(rec.Body))
	return rec.Tape, err
}

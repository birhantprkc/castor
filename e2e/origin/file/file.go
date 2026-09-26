// Package file publishes a stream as one progressive file, the way a plain video link serves it.
package file

import (
	"errors"
	"path/filepath"
	"slices"

	"github.com/stupside/castor/e2e/origin"
)

// Container is one file format; the file is its own only "segment", so serving behaviours apply to it.
type Container struct {
	Called, Ext, Muxer, MIME string
	Args                     []string
}

func (c Container) Name() string { return c.Called }

func (c Container) Supports(l origin.Layout) error {
	return errors.Join(
		refuse(l.Rungs > 1, "a single file carries one video rendition: use a height, not a ladder"),
		refuse(l.Audio && l.Carriage == origin.Separate, "a single file carries its audio muxed"),
		refuse(l.SegmentExt != "", "a single file has no segments to rename"),
	)
}

func (c Container) Package(dir string, _ origin.Layout) origin.Output {
	entry := "movie" + c.Ext
	return origin.Output{
		Entry:      entry,
		SegmentExt: c.Ext,
		Types:      map[string]string{c.Ext: c.MIME},
		Args:       slices.Concat([]string{"-f", c.Muxer}, c.Args, []string{filepath.Join(dir, entry)}),
	}
}

func refuse(broken bool, message string) error {
	if broken {
		return errors.New(message)
	}
	return nil
}

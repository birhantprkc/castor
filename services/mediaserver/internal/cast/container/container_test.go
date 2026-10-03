package container

import (
	"testing"

	"github.com/stupside/castor/services/mediaserver/internal/media"
)

// An encode trusts every format it is handed to say how it frames its streams and where it writes.
func TestEveryFormatCastorProducesCanBeWritten(t *testing.T) {
	for ct, f := range formats {
		if f.ContentType != ct || f.Muxer == "" || f.Tuning.Output == "" || f.Framing == media.FramingUnknown {
			t.Errorf("%s: %+v lacks a content type, muxer, output or framing", ct, f)
		}
	}
}

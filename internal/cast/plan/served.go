package plan

import (
	"fmt"

	"github.com/stupside/castor/internal/cast/container"
	"github.com/stupside/castor/internal/media"
)

// ServedFormat returns container renderer asks for; validated by registry.
func ServedFormat(caps media.Capabilities) (container.FormatInfo, error) {
	format, ok := container.FormatForContentType(caps.ServedContainer)
	if !ok {
		return container.FormatInfo{}, fmt.Errorf("the renderer asks to be served %q, which castor cannot produce", caps.ServedContainer)
	}
	return format, nil
}

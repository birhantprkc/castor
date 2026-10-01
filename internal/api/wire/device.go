package wire

import (
	"errors"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/internal/media"
)

// DeviceError keeps a renderer's failure typed where the server's recovery reads its kind.
func DeviceError(err error) *castorv1.DeviceError {
	if gone, ok := errors.AsType[*media.Gone](err); ok {
		g := &castorv1.DeviceError_Gone{Renderer: gone.Renderer, Observed: gone.Observed}
		if gone.Err != nil {
			g.Cause = gone.Err.Error()
		}
		return &castorv1.DeviceError{Error: &castorv1.DeviceError_Gone_{Gone: g}}
	}
	return &castorv1.DeviceError{Error: &castorv1.DeviceError_Message{Message: err.Error()}}
}

func FromDeviceError(e *castorv1.DeviceError) error {
	if g := e.GetGone(); g != nil {
		gone := &media.Gone{Renderer: g.GetRenderer(), Observed: g.GetObserved()}
		if g.GetCause() != "" {
			gone.Err = errors.New(g.GetCause())
		}
		return gone
	}
	return errors.New(e.GetMessage())
}

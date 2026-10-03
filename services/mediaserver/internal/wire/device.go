package wire

import (
	"errors"

	mediav1 "github.com/stupside/castor/gen/castor/media/v1"
	"github.com/stupside/castor/services/mediaserver/internal/media"
)

func FromDeviceError(e *mediav1.DeviceError) error {
	if g := e.GetGone(); g != nil {
		gone := &media.Gone{Device: g.GetDevice(), Observed: g.GetObserved()}
		if g.GetCause() != "" {
			gone.Err = errors.New(g.GetCause())
		}
		return gone
	}
	return errors.New(e.GetMessage())
}

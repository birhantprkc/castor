package device

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// Target is the device t names: one seen, discovered again when not yet seen, or one pinned at its address.
func (d *Directory) Target(ctx context.Context, t *castorv1.Target) (Info, error) {
	if pinned := t.GetPinned(); pinned != nil {
		kind := Type(pinned.GetType())
		if _, err := d.reach.family(kind); err != nil {
			return Info{}, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return Info{Type: kind, Address: pinned.GetAddress()}, nil
	}
	if info, ok := d.lookup(t.GetDeviceId()); ok {
		return info, nil
	}
	d.discover(ctx)
	if info, ok := d.lookup(t.GetDeviceId()); ok {
		return info, nil
	}
	return Info{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("no device %q on the network", t.GetDeviceId()))
}

// id is how the contract names a discovered device: its family, then its own identity; a pinned one has none.
func (i Info) id() string {
	if i.ID == "" {
		return ""
	}
	return string(i.Type) + ":" + i.ID
}

// Public is the device as the contract shows it.
func (i Info) Public() *castorv1.Device {
	return &castorv1.Device{Id: i.id(), Name: i.Name, Type: string(i.Type), Address: i.Address}
}

package device

import (
	"context"
	"sync"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// Directory is the devices this server reached, by the id a cast's target names them with.
type Directory struct {
	reach Registry

	mu   sync.Mutex
	seen map[string]Info
}

// NewDirectory lists and lends the devices reach finds.
func NewDirectory(reach Registry) *Directory {
	return &Directory{reach: reach, seen: map[string]Info{}}
}

func (d *Directory) ListDevices(ctx context.Context, _ *castorv1.ListDevicesRequest) (*castorv1.ListDevicesResponse, error) {
	found := d.discover(ctx)
	listed := make([]*castorv1.Device, len(found))
	for i, info := range found {
		listed[i] = info.Public()
	}
	return &castorv1.ListDevicesResponse{Devices: listed}, nil
}

func (d *Directory) discover(ctx context.Context) []Info {
	found := d.reach.Discover(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, info := range found {
		d.seen[info.id()] = info
	}
	return found
}

func (d *Directory) lookup(id string) (Info, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	info, ok := d.seen[id]
	return info, ok
}

package browse

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"strings"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/gen/castor/v1/castorv1connect"
	"github.com/stupside/castor/tui/internal/browse/picker"
	"github.com/stupside/castor/tui/internal/cast"
)

// Run asks which device to cast to, unless d pins one, then which title; chosen is false when the operator leaves without both.
func Run(ctx context.Context, devices castorv1connect.DeviceServiceClient, catalog Catalog, d cast.Device) (to *castorv1.Target, sel Selection, chosen bool, err error) {
	err = held(func() error {
		var name, typ string
		var err error
		if to, name, typ, err = device(ctx, devices, d); err != nil || to == nil {
			return err
		}
		if sel, chosen, err = title(ctx, catalog, strings.ToUpper(typ)+"  "+name); err != nil {
			return fmt.Errorf("browse: %w", err)
		}
		return nil
	})
	return to, sel, chosen, err
}

// device is the device d pins, or the one the operator picks; nil when they leave without one.
func device(ctx context.Context, devices castorv1connect.DeviceServiceClient, d cast.Device) (to *castorv1.Target, name, typ string, err error) {
	if d.Host != "" {
		to, err = cast.Target(ctx, devices, d)
		return to, cmp.Or(d.Name, d.Host), d.Type, err
	}
	picked, ok, err := picker.Device(ctx, discover(devices), d.Name)
	if err != nil {
		return nil, "", "", fmt.Errorf("picking device: %w", err)
	}
	if !ok {
		return nil, "", "", nil
	}
	return &castorv1.Target{Target: &castorv1.Target_DeviceId{DeviceId: picked.GetId()}}, picked.GetName(), picked.GetType(), nil
}

func discover(devices castorv1connect.DeviceServiceClient) picker.Discover {
	return func(ctx context.Context) []*castorv1.Device {
		listed, err := devices.ListDevices(ctx, &castorv1.ListDevicesRequest{})
		if err != nil {
			slog.WarnContext(ctx, "listing devices", "error", err)
		}
		return listed.GetDevices()
	}
}

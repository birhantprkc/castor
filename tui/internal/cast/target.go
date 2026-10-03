package cast

import (
	"context"
	"errors"
	"fmt"
	"strings"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
	"github.com/stupside/castor/gen/castor/v1/castorv1connect"
)

// Device is the device the commands cast to: found by name, or pinned at its host.
type Device struct {
	Name string `yaml:"name" validate:"required_without=Host"`
	Type string `yaml:"type" validate:"required"`
	Host string `yaml:"host"`
}

// Target is the device d names: pinned at its host, or found by name among those the API lists.
func Target(ctx context.Context, devices castorv1connect.DeviceServiceClient, d Device) (*castorv1.Target, error) {
	if d.Type == "" {
		return nil, errors.New("no device to cast to: set device.name and device.type (castor scan lists them)")
	}
	if d.Host != "" {
		return &castorv1.Target{Target: &castorv1.Target_Pinned_{Pinned: &castorv1.Target_Pinned{Type: d.Type, Address: d.Host}}}, nil
	}
	listed, err := devices.ListDevices(ctx, &castorv1.ListDevicesRequest{})
	if err != nil {
		return nil, err
	}
	for _, found := range listed.GetDevices() {
		if found.GetType() == d.Type && strings.EqualFold(found.GetName(), d.Name) {
			return &castorv1.Target{Target: &castorv1.Target_DeviceId{DeviceId: found.GetId()}}, nil
		}
	}
	return nil, fmt.Errorf("device %q (type %s) not found", d.Name, d.Type)
}

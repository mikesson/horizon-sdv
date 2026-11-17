// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package devicefeature

import (
	"context"
	"fmt"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// liveDevice loads a device for a route that goes on to its host. A deleting
// or deleted row answers not-found before any host is asked, because its CVD
// is being torn down or is gone. Reads of the row itself keep returning the
// tombstone and do not come through here.
func liveDevice(ctx context.Context, devices device.Getter, id string) (*device.Device, error) {
	d, err := devices.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.Status.IsRetired() {
		return nil, fmt.Errorf("device %s is %s: %w", id, d.Status, device.ErrNotFound)
	}
	return d, nil
}

// findInstance resolves one of a device's observed instances by name.
//
// This is the single place a caller-supplied instance name becomes an instance,
// so the 404 is worded once and every instance-addressed route refuses the same
// way. Instance names are caller-chosen in advanced mode, which makes the
// persisted array the only record of what this device's instances are called.
// Answering here rather than forwarding matters, because a host names its
// whole fleet in one namespace and would happily serve another device's
// instance under the same name.
func findInstance(d *device.Device, name string) (*device.Instance, error) {
	for i := range d.Instances {
		if d.Instances[i].Name == name {
			return &d.Instances[i], nil
		}
	}
	return nil, web.NewNotFoundError("device has no instance named " + name)
}

// instanceTarget is one named instance of a device's CVD group, together with
// the Host Orchestrator client for the host it runs on.
type instanceTarget struct {
	client   hoclient.Client
	group    string
	instance *device.Instance
}

// resolveInstance loads the device, the client for its host, and the named
// instance, the prerequisite every instance-addressed route shares.
//
// It does not require the device to be running. A dead instance is exactly the
// one an operator wants a screenshot or a display list from, and the host
// answers those for a stopped CVD.
func (s *DeviceService) resolveInstance(ctx context.Context, id, name string) (*instanceTarget, error) {
	client, d, err := s.hoClientForDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	inst, err := findInstance(d, name)
	if err != nil {
		return nil, err
	}
	group, _ := device.ParseCVDID(d.CVDID)
	if group == "" {
		return nil, fmt.Errorf("device %s has malformed CVD ID %q", d.ID, d.CVDID)
	}
	return &instanceTarget{client: client, group: group, instance: inst}, nil
}

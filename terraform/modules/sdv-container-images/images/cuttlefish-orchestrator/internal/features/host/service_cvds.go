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

package hostfeature

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// HostCVD is one CVD as the host's Host Orchestrator reports it, joined to the
// CCO device that owns its group.
type HostCVD struct {
	Group          string `json:"group" doc:"Host-side CVD group"`
	Name           string `json:"name" doc:"Instance name within the group"`
	Status         string `json:"status" doc:"CVD state as the Host Orchestrator reports it"`
	ADBSerial      string `json:"adb_serial,omitempty" doc:"ADB serial, when the Host Orchestrator reports one"`
	WebRTCDeviceID string `json:"webrtc_device_id,omitempty" doc:"WebRTC device ID the host's signaling server knows the instance by"`
	DeviceID       string `json:"device_id,omitempty" doc:"CCO device that owns the group; empty for CVDs CCO did not create, such as ones made by cvdr through /v1"`
}

// hostCVDLister is the one Host Orchestrator call the CVD view makes.
type hostCVDLister interface {
	ListCVDs(ctx context.Context) ([]*hoclient.CVD, error)
}

// hostGetter reads one host row.
type hostGetter interface {
	Get(ctx context.Context, id string) (*host.Host, error)
}

// DeviceLister returns the device rows recorded on a host.
type DeviceLister func(ctx context.Context, hostID string) ([]*device.Device, error)

// errHostOrchestrator marks a failed Host Orchestrator call, which the handler
// reports as 502.
var errHostOrchestrator = errors.New("host orchestrator did not answer")

// CVDViewer lists the CVDs a host actually runs, including ones CCO does not
// track as devices.
type CVDViewer struct {
	hosts     hostGetter
	devices   DeviceLister
	clientFor func(hostURL string) hostCVDLister
}

// NewCVDViewer builds the viewer. A nil factory or device lister leaves the
// view unavailable.
func NewCVDViewer(hosts hostGetter, devices DeviceLister, factory *hoclient.Factory) *CVDViewer {
	if factory == nil || devices == nil {
		return nil
	}
	return &CVDViewer{
		hosts:     hosts,
		devices:   devices,
		clientFor: func(hostURL string) hostCVDLister { return factory.ClientForHost(hostURL) },
	}
}

// List queries the host's Host Orchestrator live. It runs `cvd fleet` on the
// host, so it serves explicit requests only and no loop may poll it.
func (v *CVDViewer) List(ctx context.Context, hostID string) ([]HostCVD, error) {
	h, err := v.hosts.Get(ctx, hostID)
	if err != nil {
		return nil, fmt.Errorf("listing CVDs: %w", err)
	}
	if h.Status != host.StatusRunning {
		return nil, &host.InvalidStateError{Message: fmt.Sprintf(
			"host %s is %s; its CVDs can only be listed while it is running", hostID, h.Status)}
	}
	hoURL := h.HostOrchestratorURL()
	if hoURL == "" {
		return nil, &host.InvalidStateError{Message: fmt.Sprintf(
			"host %s has no IP address yet; its CVDs can only be listed once it has one", hostID)}
	}

	devices, err := v.devices(ctx, hostID)
	if err != nil {
		return nil, fmt.Errorf("listing devices on host %s: %w", hostID, err)
	}
	owner := make(map[string]string, len(devices))
	for _, d := range devices {
		if d.Status == device.StatusDeleted {
			continue
		}
		owner[device.CVDGroupName(d.ID)] = d.ID
	}

	cvds, err := v.clientFor(hoURL).ListCVDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: host %s: %w", errHostOrchestrator, hostID, err)
	}
	out := make([]HostCVD, 0, len(cvds))
	for _, c := range cvds {
		if c == nil {
			continue
		}
		out = append(out, HostCVD{
			Group:          c.Group,
			Name:           c.Name,
			Status:         c.Status,
			ADBSerial:      c.ADBSerial,
			WebRTCDeviceID: c.WebRTCDeviceID,
			DeviceID:       owner[c.Group],
		})
	}
	slices.SortFunc(out, func(a, b HostCVD) int {
		return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}

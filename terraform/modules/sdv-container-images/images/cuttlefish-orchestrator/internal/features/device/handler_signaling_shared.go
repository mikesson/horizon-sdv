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
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// signalingTarget is one signaling entry point's resolved device and the Host
// Orchestrator client for the host it sits on.
type signalingTarget struct {
	client hoclient.Client
	device *device.Device
}

// resolveHostClient loads the pair every signaling entry point starts with.
// The device and host errors pass through unwrapped so the transport keeps
// mapping them per family.
func resolveHostClient(ctx context.Context, devices device.Getter, hosts host.Getter, factory *hoclient.Factory, deviceID string) (*signalingTarget, error) {
	d, err := liveDevice(ctx, devices, deviceID)
	if err != nil {
		return nil, err
	}
	h, err := hosts.Get(ctx, d.HostID)
	if err != nil {
		return nil, err
	}
	return &signalingTarget{
		client: factory.ClientForHost(h.HostOrchestratorURL()),
		device: d,
	}, nil
}

// resolveOperatorDeviceID returns the WebRTC device ID the operator registered
// for one instance of a device's CVD group. Every Android VM registers its own,
// so this is what a stream or an ADB bridge is addressed by. The device id
// alone reaches nothing.
//
// The create records what the host reported, so the answer is normally on the
// row, which keeps a stream coming up while the host is briefly unreachable and
// takes a host round trip off every signaling request. A row that carries no id
// still asks the host. A device created before the instance array existed
// synthesizes its one instance from cvd_id and leaves the id empty.
//
// The id is never reconstructed. cvd generates "<group>-<name>-<instanceID>"
// from a fleet-unique instance id CCO never sees, appends "_<n>" when that
// collides with a caller-supplied id, and honors a caller's
// streaming.device_id verbatim when one is set (start.cpp
// ReplaceEmptyWebRTCDeviceIds, cf_streaming_configs.cpp DeviceId).
//
// So a failed lookup is an error rather than a cue to guess. Guessing sent
// both the signaling and the ADB bridge to an id the operator had never
// registered, which surfaces as a stream that connects and stays silent
// instead of as the host problem it is.
func resolveOperatorDeviceID(ctx context.Context, client hoclient.Client, d *device.Device, instance string) (string, error) {
	inst, err := findInstance(d, instance)
	if err != nil {
		return "", err
	}
	if inst.WebRTCDeviceID != "" {
		return inst.WebRTCDeviceID, nil
	}
	cvds, err := client.ListCVDs(ctx)
	if err != nil {
		return "", fmt.Errorf("listing CVDs to resolve the operator device id: %w", err)
	}
	group, _ := device.ParseCVDID(d.CVDID)
	for _, cvd := range cvds {
		if cvd.Name != inst.Name || (group != "" && cvd.Group != group) {
			continue
		}
		if cvd.WebRTCDeviceID == "" {
			return "", web.NewNotFoundError("instance " + inst.Name + " has no WebRTC device id on its host")
		}
		return cvd.WebRTCDeviceID, nil
	}
	return "", web.NewNotFoundError("instance " + inst.Name + " not found on host orchestrator")
}

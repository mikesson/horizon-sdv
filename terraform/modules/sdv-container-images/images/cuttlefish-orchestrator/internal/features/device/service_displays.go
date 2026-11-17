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
)

// hoClientForDevice resolves a device and the Host Orchestrator client for
// its host, the shared prerequisite of the display operations.
func (s *DeviceService) hoClientForDevice(ctx context.Context, id string) (hoclient.Client, *device.Device, error) {
	d, err := liveDevice(ctx, s.deviceRepo, id)
	if err != nil {
		return nil, nil, fmt.Errorf("device not found: %w", err)
	}
	h, err := s.hostRepo.Get(ctx, d.HostID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get host: %w", err)
	}
	hoURL := h.HostOrchestratorURL()
	if hoURL == "" {
		return nil, nil, fmt.Errorf("host %s has no IP address", h.ID)
	}
	return s.hoClientFactory.ClientForHost(hoURL), d, nil
}

// ListDisplays returns the active displays on one instance of a device's CVD
// group.
func (s *DeviceService) ListDisplays(ctx context.Context, id, instance string) ([]hoclient.Display, error) {
	t, err := s.resolveInstance(ctx, id, instance)
	if err != nil {
		return nil, err
	}
	resp, err := t.client.GetDisplays(ctx, t.group+"/"+t.instance.Name)
	if err != nil {
		return nil, fmt.Errorf("listing displays: %w", err)
	}
	return resp.Displays, nil
}

// AddDisplay adds a virtual display to one instance, returning the number the
// Host Orchestrator assigned it.
func (s *DeviceService) AddDisplay(ctx context.Context, id, instance string, req *hoclient.DisplayAddRequest) (int, error) {
	t, err := s.resolveInstance(ctx, id, instance)
	if err != nil {
		return 0, err
	}
	resp, err := t.client.AddDisplay(ctx, t.group, t.instance.Name, req)
	if err != nil {
		return 0, fmt.Errorf("adding display: %w", err)
	}
	return resp.DisplayNumber, nil
}

// RemoveDisplay removes a virtual display from one instance.
func (s *DeviceService) RemoveDisplay(ctx context.Context, id, instance string, displayNumber int) error {
	t, err := s.resolveInstance(ctx, id, instance)
	if err != nil {
		return err
	}
	if err := t.client.RemoveDisplay(ctx, t.group, t.instance.Name, displayNumber); err != nil {
		return fmt.Errorf("removing display: %w", err)
	}
	return nil
}

// Screenshot captures the current content of one display of one instance.
func (s *DeviceService) Screenshot(ctx context.Context, id, instance string, displayNumber int) (*hoclient.DisplayScreenshotResponse, error) {
	t, err := s.resolveInstance(ctx, id, instance)
	if err != nil {
		return nil, err
	}
	shot, err := t.client.GetDisplayScreenshot(ctx, t.group, t.instance.Name, displayNumber)
	if err != nil {
		return nil, fmt.Errorf("capturing screenshot: %w", err)
	}
	return shot, nil
}

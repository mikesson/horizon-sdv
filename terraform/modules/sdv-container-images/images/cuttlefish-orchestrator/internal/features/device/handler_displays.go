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
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// predefinedPresets contains common display configurations.
var predefinedPresets = []DisplayPreset{
	{Name: "Phone", Width: 720, Height: 1280, DPI: 320, RefreshRateHz: 60},
	{Name: "Tablet", Width: 1600, Height: 2560, DPI: 320, RefreshRateHz: 60},
	{Name: "Monitor", Width: 1920, Height: 1080, DPI: 160, RefreshRateHz: 60},
	{Name: "Foldable (Outer)", Width: 840, Height: 2208, DPI: 420, RefreshRateHz: 60},
	{Name: "Foldable (Inner)", Width: 1768, Height: 2208, DPI: 420, RefreshRateHz: 60},
	{Name: "Automotive", Width: 1024, Height: 768, DPI: 160, RefreshRateHz: 60},
}

// getPresetByName finds a preset by name (case-insensitive).
func getPresetByName(name string) *DisplayPreset {
	for _, p := range predefinedPresets {
		if strings.EqualFold(p.Name, name) {
			return &p
		}
	}
	return nil
}

// ListDisplays returns the active displays and available presets for one
// instance of a device's CVD group.
func (h *DeviceHandler) ListDisplays(ctx context.Context, input *ListDisplaysInput) (*ListDisplaysOutput, error) {
	displays, err := h.service.ListDisplays(ctx, input.ID, input.Instance)
	if err != nil {
		return nil, web.MapError(err)
	}

	out := &ListDisplaysOutput{}
	out.Body = ListDisplaysResponse{
		Displays: displays,
		Presets:  predefinedPresets,
	}
	return out, nil
}

// AddDisplay adds a display to one instance via its Host Orchestrator.
func (h *DeviceHandler) AddDisplay(ctx context.Context, input *AddDisplayInput) (*AddDisplayOutput, error) {
	req := input.Body

	// A preset overrides the dimension fields wholesale.
	if req.Preset != "" {
		preset := getPresetByName(req.Preset)
		if preset == nil {
			return nil, huma.Error400BadRequest("unknown preset: " + req.Preset)
		}
		req.Width = preset.Width
		req.Height = preset.Height
		req.DPI = preset.DPI
		req.RefreshRateHz = preset.RefreshRateHz
	}

	if req.Width <= 0 || req.Height <= 0 {
		return nil, huma.Error400BadRequest("width and height are required")
	}

	displayNumber, err := h.service.AddDisplay(ctx, input.ID, input.Instance, &hoclient.DisplayAddRequest{
		Width:         req.Width,
		Height:        req.Height,
		DPI:           req.DPI,
		RefreshRateHz: req.RefreshRateHz,
	})
	if err != nil {
		return nil, web.MapError(err)
	}

	out := &AddDisplayOutput{}
	out.Body = AddDisplayResponse{DisplayNumber: displayNumber}
	return out, nil
}

// RemoveDisplay removes a display from one instance via its Host Orchestrator.
func (h *DeviceHandler) RemoveDisplay(ctx context.Context, input *RemoveDisplayInput) (*RemoveDisplayOutput, error) {
	if err := h.service.RemoveDisplay(ctx, input.ID, input.Instance, input.DisplayNumber); err != nil {
		return nil, web.MapError(err)
	}
	return &RemoveDisplayOutput{}, nil
}

// GetScreenshot captures one display of one instance via its Host Orchestrator.
func (h *DeviceHandler) GetScreenshot(ctx context.Context, input *GetScreenshotInput) (*GetScreenshotOutput, error) {
	shot, err := h.service.Screenshot(ctx, input.ID, input.Instance, input.DisplayNumber)
	if err != nil {
		return nil, web.MapError(err)
	}
	out := &GetScreenshotOutput{}
	out.Body.ScreenshotBytes = shot.ScreenshotBytesBase64
	out.Body.MimeType = shot.ScreenshotMimeType
	return out, nil
}

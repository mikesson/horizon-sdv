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
	"fmt"
	"log/slog"

	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// machineTypeConfig holds resolved machine type configuration.
type machineTypeConfig struct {
	machineType string
	gpuType     string
	gpuCount    int64
	// bootDiskType is empty when the zonal default will do; C4 and N4 reject
	// pd-* and boot only from hyperdisk-balanced.
	bootDiskType string
	// architecture is the catalog's word for the shape's CPU, which the boot
	// image must match. Empty without a catalog, which leaves it unchecked.
	architecture machinetype.Architecture
}

// resolveMachineType resolves the machine shape (type, GPU type, GPU count)
// from the catalog. A resolution failure fails the provision. The task ladder
// absorbs transient catalog errors, and a disabled type must not provision.
// Falling back to compiled defaults here would silently build a shape the
// operator never chose. Without a configured repository (tests and harnesses
// only, since main always wires it) the compiled defaults apply.
func (s *HostService) resolveMachineType(ctx context.Context, requestedType, requestedGPU string, requestedGPUCount int64) (machineTypeConfig, error) {
	if s.machineTypes == nil {
		return s.resolveMachineTypeFromConfig(requestedType, requestedGPU, requestedGPUCount), nil
	}
	cfg, err := s.resolveMachineTypeFromDB(ctx, requestedType, requestedGPU, requestedGPUCount)
	if err != nil {
		return machineTypeConfig{}, fmt.Errorf("resolving machine type from the catalog: %w", err)
	}
	return cfg, nil
}

// resolveMachineTypeFromDB resolves machine type using the database.
func (s *HostService) resolveMachineTypeFromDB(ctx context.Context, reqType, reqGPU string, reqGPUCount int64) (machineTypeConfig, error) {
	if reqType == "" {
		return s.resolveDefaultFromDB(ctx, reqGPU, reqGPUCount)
	}
	return s.validateRequestedType(ctx, reqType, reqGPU, reqGPUCount)
}

// resolveDefaultFromDB gets the default machine type from database.
func (s *HostService) resolveDefaultFromDB(ctx context.Context, reqGPU string, reqGPUCount int64) (machineTypeConfig, error) {
	mt, err := s.machineTypes.GetDefault(ctx)
	if err != nil {
		return machineTypeConfig{}, fmt.Errorf("looking up the default machine type: %w", err)
	}

	gpuType, gpuCount := resolveGPUAttachment(mt.GPUMode, mt.DefaultGPUType, reqGPU, reqGPUCount)
	cfg := machineTypeConfig{
		machineType:  mt.ID,
		gpuType:      gpuType,
		gpuCount:     gpuCount,
		bootDiskType: mt.BootDiskType,
		architecture: mt.Architecture,
	}

	s.logger.Info("using default machine type from database",
		slog.String("machine_type", cfg.machineType),
		slog.String("gpu_type", cfg.gpuType),
		slog.Int64("gpu_count", cfg.gpuCount),
	)

	return cfg, nil
}

// validateRequestedType validates a specific machine type exists and is enabled.
func (s *HostService) validateRequestedType(ctx context.Context, reqType, reqGPU string, reqGPUCount int64) (machineTypeConfig, error) {
	// The catalog names the machine and the reason in its own error, so this
	// says only what it was doing.
	mt, err := s.machineTypes.Get(ctx, reqType)
	if err != nil {
		return machineTypeConfig{}, fmt.Errorf("resolving the requested machine type: %w", err)
	}

	if !mt.IsEnabled {
		return machineTypeConfig{}, fmt.Errorf("machine type '%s' is not enabled", reqType)
	}

	gpuType, gpuCount := resolveGPUAttachment(mt.GPUMode, mt.DefaultGPUType, reqGPU, reqGPUCount)
	cfg := machineTypeConfig{
		machineType:  reqType,
		gpuType:      gpuType,
		gpuCount:     gpuCount,
		bootDiskType: mt.BootDiskType,
		architecture: mt.Architecture,
	}

	s.logger.Info("validated machine type",
		slog.String("machine_type", cfg.machineType),
		slog.Bool("gpu_support", mt.GPUSupport),
		slog.String("gpu_type", cfg.gpuType),
		slog.Int64("gpu_count", cfg.gpuCount),
	)

	return cfg, nil
}

// resolveMachineTypeFromConfig is the machine-type source when no catalog
// repository is wired at all (tests and harnesses; production always wires
// one). It is not an error fallback. A catalog that exists but fails or
// rejects must fail the provision, and must never silently substitute compiled
// defaults. Without a catalog row there is no GPU mode, so GPUs are treated as
// attachable (N1-style), attached only when the caller explicitly requests a
// positive count.
func (s *HostService) resolveMachineTypeFromConfig(reqType, reqGPU string, reqGPUCount int64) machineTypeConfig {
	gpuType, gpuCount := resolveGPUAttachment(machinetype.GPUModeAttachable, fallbackGPUType, reqGPU, reqGPUCount)
	return machineTypeConfig{
		machineType: cmp.Or(reqType, fallbackMachineType),
		gpuType:     gpuType,
		gpuCount:    gpuCount,
	}
}

// resolveGPUAttachment computes the guestAccelerators to attach to the VM,
// honoring the machine type's GPU mode (mirrors gcecatalog's validation):
//   - integrated / none: attach nothing. Integrated GPUs (G2/G4/A-series) come
//     with the machine type and GCE rejects guestAccelerators; none has no GPU.
//   - attachable (N1): attach exactly the requested count. Zero means CPU-only,
//     so a GPU is never forced. An empty type with a positive count falls back
//     to the machine's recommended default type.
func resolveGPUAttachment(mode, defaultGPUType, reqGPUType string, reqGPUCount int64) (string, int64) {
	if mode != machinetype.GPUModeAttachable || reqGPUCount <= 0 {
		return "", 0
	}
	return cmp.Or(reqGPUType, defaultGPUType), reqGPUCount
}

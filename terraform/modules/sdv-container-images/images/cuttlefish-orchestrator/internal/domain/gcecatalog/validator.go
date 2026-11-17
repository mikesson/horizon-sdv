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

package gcecatalog

import (
	"context"
)

// Validator validates host creation requests against the GCE catalog.
type Validator interface {
	// Validate checks if a host configuration is valid.
	Validate(ctx context.Context, req ValidationRequest) (*ValidationResult, error)

	// RankZones returns the viable zones for the shape, best first:
	// hard-filtered (catalog availability, zone-health cooldowns, regional
	// quota headroom) and scored (headroom, health, proximity). Deterministic
	// for fixed inputs.
	RankZones(ctx context.Context, req ValidationRequest, opts RankOptions) ([]ZoneCandidate, error)
}

// ValidationRequest represents a request to validate a host configuration.
type ValidationRequest struct {
	// Zone is the requested zone (e.g., "us-central1-a").
	Zone string `json:"zone"`

	// MachineType is the requested machine type (e.g., "n1-standard-8").
	MachineType string `json:"machine_type"`

	// GPUType is the requested GPU type (e.g., "nvidia-tesla-t4").
	GPUType string `json:"gpu_type,omitempty"`

	// GPUCount is the requested number of GPUs.
	GPUCount int64 `json:"gpu_count,omitempty"`

	// ProvisioningModel is the VM provisioning model (STANDARD or SPOT).
	ProvisioningModel string `json:"provisioning_model,omitempty"`
}

// ValidationResult contains the result of validating a host configuration.
type ValidationResult struct {
	// Valid is true if the configuration passed all validation checks.
	Valid bool `json:"valid"`

	// Errors contains validation errors if Valid is false.
	Errors []ValidationError `json:"errors,omitempty"`

	// Warnings contains non-blocking warnings.
	Warnings []ValidationWarning `json:"warnings,omitempty"`

	// AlternativeZones lists zones where the configuration would work.
	AlternativeZones []string `json:"alternative_zones,omitempty"`
}

// ValidationError represents a validation failure.
type ValidationError struct {
	// Field is the field that failed validation (e.g., "zone", "machine_type").
	Field string `json:"field"`

	// Code is a machine-readable error code.
	Code string `json:"code"`

	// Message is a human-readable error message.
	Message string `json:"message"`

	// Suggestion provides guidance on how to fix the error.
	Suggestion string `json:"suggestion,omitempty"`
}

// ValidationWarning represents a non-blocking warning.
type ValidationWarning struct {
	// Code is a machine-readable warning code.
	Code string `json:"code"`

	// Message is a human-readable warning message.
	Message string `json:"message"`
}

// Validation error codes.
const (
	// Zone errors.
	ErrorCodeZoneNotFound = "zone_not_found"
	ErrorCodeZoneDisabled = "zone_disabled"
	ErrorCodeZoneDown     = "zone_down"

	// Machine type errors.
	ErrorCodeMachineTypeInvalid   = "machine_type_invalid"
	ErrorCodeMachineTypeNotInZone = "machine_type_not_in_zone"

	// GPU errors.
	ErrorCodeGPUNotSupported  = "gpu_not_supported"
	ErrorCodeGPUNotInZone     = "gpu_not_in_zone"
	ErrorCodeGPUCountExceeded = "gpu_count_exceeded"
	ErrorCodeGPUTypeFixed     = "gpu_type_fixed"

	// Quota errors.
	ErrorCodeQuotaCPUExceeded = "quota_cpu_exceeded"
	ErrorCodeQuotaGPUExceeded = "quota_gpu_exceeded"
	// ErrorCodeQuotaIPExceeded: the region lacks IN_USE_ADDRESSES headroom for
	// the external device-media address the deployment attaches per host.
	ErrorCodeQuotaIPExceeded = "quota_ip_exceeded"
	// ErrorCodeQuotaDiskExceeded: the region lacks the boot-disk capacity one
	// host of the shape needs. One code for both disk pools. Which pool it was
	// is in the message, and a caller can change neither by editing the request.
	ErrorCodeQuotaDiskExceeded = "quota_disk_exceeded"

	// Spot VM quota errors.
	ErrorCodeSpotQuotaCPUExceeded     = "spot_quota_cpu_exceeded"
	ErrorCodeSpotQuotaGPUExceeded     = "spot_quota_gpu_exceeded"
	ErrorCodeInvalidProvisioningModel = "invalid_provisioning_model"
)

// Warning codes.
const (
	WarningCodeCatalogStale   = "catalog_stale"
	WarningCodeQuotaNearLimit = "quota_near_limit"
)

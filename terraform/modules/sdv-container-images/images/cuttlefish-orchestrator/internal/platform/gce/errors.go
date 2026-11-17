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

package gce

// ErrorType classifies GCE provisioning errors.
type ErrorType string

// Error types for GCE provisioning failures.
const (
	// Zone errors.
	ErrorTypeZoneNotFound ErrorType = "zone_not_found"
	ErrorTypeZoneDown     ErrorType = "zone_down"

	// Machine type errors.
	ErrorTypeMachineTypeNotInZone ErrorType = "machine_type_not_in_zone"

	// GPU errors.
	ErrorTypeGPUNotInZone     ErrorType = "gpu_not_in_zone"
	ErrorTypeGPUCountExceeded ErrorType = "gpu_count_exceeded"
	ErrorTypeGPUNotAttachable ErrorType = "gpu_not_attachable"

	// Quota errors. The two disk pools are distinct: pd-* boot disks fill the
	// SSD pool, hyperdisk-balanced ones the hyperdisk pool, and they are raised
	// by separate quota requests.
	ErrorTypeQuotaCPU       ErrorType = "quota_cpu_exceeded"
	ErrorTypeQuotaGPU       ErrorType = "quota_gpu_exceeded"
	ErrorTypeQuotaIP        ErrorType = "quota_ip_exceeded"
	ErrorTypeQuotaSSD       ErrorType = "quota_ssd_exceeded"
	ErrorTypeQuotaHyperdisk ErrorType = "quota_hyperdisk_exceeded"
	ErrorTypeQuotaInstance  ErrorType = "quota_instances_exceeded"

	// Permission errors.
	ErrorTypePermissionDenied ErrorType = "permission_denied"

	// Resource errors.
	ErrorTypeResourceExhausted ErrorType = "resource_exhausted"
	ErrorTypeResourceNotReady  ErrorType = "resource_not_ready"

	// Throttling: the API said "slow down", not "this zone or region is out
	// of capacity". Kept distinct from ErrorTypeResourceExhausted so capacity
	// reactions (zone cooldowns, failover, alternative-zone advice) can never
	// key on a rate limit.
	ErrorTypeRateLimit ErrorType = "rate_limited"

	// Network errors.
	ErrorTypeNetworkNotFound ErrorType = "network_not_found"
	ErrorTypeSubnetNotFound  ErrorType = "subnet_not_found"

	// Image errors.
	ErrorTypeImageNotFound ErrorType = "image_not_found"

	// Unknown/other errors.
	ErrorTypeUnknown ErrorType = "unknown"
)

// ErrorTypes returns every classification a host's error_type can carry, in the
// order the field's enum tag declares them.
//
// It exists because that vocabulary is written down three times (here, in the
// tag on host.Host.ErrorType, and in the console's union) and only tests hold
// the copies together. The console's copy was invented separately and shared
// exactly two values with this one, so every other real failure rendered as a
// generic "Error".
func ErrorTypes() []ErrorType {
	return []ErrorType{
		ErrorTypeZoneNotFound, ErrorTypeZoneDown,
		ErrorTypeMachineTypeNotInZone,
		ErrorTypeGPUNotInZone, ErrorTypeGPUCountExceeded, ErrorTypeGPUNotAttachable,
		ErrorTypeQuotaCPU, ErrorTypeQuotaGPU, ErrorTypeQuotaIP,
		ErrorTypeQuotaSSD, ErrorTypeQuotaHyperdisk, ErrorTypeQuotaInstance,
		ErrorTypePermissionDenied,
		ErrorTypeResourceExhausted, ErrorTypeResourceNotReady,
		ErrorTypeRateLimit,
		ErrorTypeNetworkNotFound, ErrorTypeSubnetNotFound,
		ErrorTypeImageNotFound,
		ErrorTypeUnknown,
	}
}

// ClassifiedError represents a parsed GCE error with metadata.
type ClassifiedError struct {
	// Type is the classified error type.
	Type ErrorType `json:"type"`

	// Code is the original GCE error code/reason.
	Code string `json:"code,omitempty"`

	// Message is a human-readable error message.
	Message string `json:"message"`

	// Suggestion provides actionable guidance to fix the error.
	Suggestion string `json:"suggestion,omitempty"`

	// AlternativeZones lists zones where the request might succeed.
	AlternativeZones []string `json:"alternative_zones,omitempty"`

	// QuotaAxis names the exhausted quota pool as GCE reported it structurally
	// (e.g. CPUS_PER_VM_FAMILY:C4). Empty when the failure was not a quota
	// refusal, or carried no structured detail and was classified off its text.
	QuotaAxis string `json:"quota_axis,omitempty"`

	// IsRetryable indicates if retrying might succeed.
	IsRetryable bool `json:"is_retryable"`
}

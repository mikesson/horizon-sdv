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

package host

import (
	"fmt"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
)

// NotFoundError is returned when a host cannot be found.
type NotFoundError struct {
	ID string
}

func (e *NotFoundError) Error() string {
	if e.ID != "" {
		return "host not found: " + e.ID
	}
	return "host not found"
}

// Is makes any *NotFoundError match the ErrNotFound sentinel via errors.Is.
func (e *NotFoundError) Is(target error) bool {
	_, ok := target.(*NotFoundError)
	return ok
}

// ReservedNameError rejects a display name that reaches into the binding
// namespace. It has no sentinel: web.MapError matches it structurally, the
// same way it handles CatalogValidationError.
type ReservedNameError struct {
	Name string
}

func (e *ReservedNameError) Error() string {
	return fmt.Sprintf(
		"host name %q must not start with %q: name is a display label, and the GCE instance name is derived from it and returned as gce_instance_name",
		e.Name, InstancePrefix)
}

// IDShapedNameError rejects a display name that parses as a UUID. Matched
// structurally by web.MapError, like ReservedNameError.
type IDShapedNameError struct {
	Name string
}

func (e *IDShapedNameError) Error() string {
	return fmt.Sprintf("host name %q has the shape of a UUID: clients accept a name or an ID in the same argument, so choose a name that is not an ID", e.Name)
}

// BootDiskSizeError rejects a boot-disk request outside [MinBootDiskGB,
// MaxBootDiskGB] (0 is valid: the provisioning default). It has no sentinel:
// web.MapError and the legacy facade match it structurally, the same way they
// handle ReservedNameError.
type BootDiskSizeError struct {
	RequestedGB int64
}

func (e *BootDiskSizeError) Error() string {
	return fmt.Sprintf("boot_disk_size_gb %d is out of range: must be %d-%d, or 0 for the default",
		e.RequestedGB, MinBootDiskGB, MaxBootDiskGB)
}

// CapacityExceededError is returned when a host, or placement across the
// fleet, has no capacity for another device. Message, when set, overrides
// the derived per-host text (placement uses it for zone-exhaustion detail).
type CapacityExceededError struct {
	HostID       string
	CurrentCount int
	MaxCapacity  int
	Message      string
}

func (e *CapacityExceededError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.HostID != "" && e.MaxCapacity > 0 {
		return fmt.Sprintf("host capacity exceeded: host %s has %d/%d devices", e.HostID, e.CurrentCount, e.MaxCapacity)
	}
	return "host capacity exceeded"
}

// Is makes any *CapacityExceededError match the ErrCapacityExceeded sentinel via errors.Is.
func (e *CapacityExceededError) Is(target error) bool {
	_, ok := target.(*CapacityExceededError)
	return ok
}

// InvalidStateError is returned when a host state transition is invalid.
type InvalidStateError struct {
	Current  string
	Expected string
	Message  string
}

func (e *InvalidStateError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Current != "" && e.Expected != "" {
		return fmt.Sprintf("invalid state transition: cannot transition from %s to %s", e.Current, e.Expected)
	}
	return "invalid host state transition"
}

// Is makes any *InvalidStateError match the ErrInvalidState sentinel via errors.Is.
func (e *InvalidStateError) Is(target error) bool {
	_, ok := target.(*InvalidStateError)
	return ok
}

// VMHorizonError refuses a lease that would outlast the host's VM: GCE stops
// the VM at TerminatesAt under its max_run_duration, and Expiry plus the
// teardown grace passes it. It matches ErrInvalidState, so the API answers 409.
type VMHorizonError struct {
	HostID       string
	Expiry       time.Time
	TerminatesAt time.Time
}

func (e *VMHorizonError) Error() string {
	return fmt.Sprintf(
		"host %s: Compute Engine stops this VM at %s, when it reaches its maximum run duration, "+
			"and a lease ending at %s plus the %d-minute teardown grace would pass that. "+
			"Restarting the host resets the VM's run duration",
		e.HostID, e.TerminatesAt.UTC().Format(time.RFC3339), e.Expiry.UTC().Format(time.RFC3339), GracePeriodSeconds/60)
}

// Is makes any *VMHorizonError match the ErrInvalidState sentinel via errors.Is.
func (e *VMHorizonError) Is(target error) bool {
	_, ok := target.(*InvalidStateError)
	return ok
}

// CatalogValidationError is returned when pre-creation validation against the
// GCE catalog fails. It has no sentinel: web.MapError matches it
// structurally and folds the details into the problem-details response.
type CatalogValidationError struct {
	// Errors contains the validation failures.
	Errors []CatalogValidationDetail `json:"errors"`

	// Warnings contains non-blocking warnings.
	Warnings []CatalogValidationWarning `json:"warnings,omitempty"`

	// AlternativeZones lists zones where the configuration would work.
	AlternativeZones []string `json:"alternative_zones,omitempty"`
}

// CatalogValidationDetail is the catalog's own validation-error vocabulary;
// aliased (not copied) so the two domains cannot drift and no translation
// layer exists between them.
type CatalogValidationDetail = gcecatalog.ValidationError

// CatalogValidationWarning is the catalog's warning vocabulary, aliased for
// the same reason.
type CatalogValidationWarning = gcecatalog.ValidationWarning

func (e *CatalogValidationError) Error() string {
	if len(e.Errors) == 0 {
		return "catalog validation failed"
	}
	return e.Errors[0].Message
}

// DegradedError is returned when a device create targets a host fenced by the
// poison condition (repeated create failures with a leaked-tap / stale-
// registration signature). It is transient: remediation clears the condition.
type DegradedError struct {
	HostID string
}

func (e *DegradedError) Error() string {
	if e.HostID != "" {
		return fmt.Sprintf("host %s is degraded (fenced from placement pending remediation)", e.HostID)
	}
	return "host is degraded (fenced from placement pending remediation)"
}

// Is makes any *DegradedError match the ErrDegraded sentinel via errors.Is.
func (e *DegradedError) Is(target error) bool {
	_, ok := target.(*DegradedError)
	return ok
}

// NotRetryableError is returned when a host cannot be retried.
type NotRetryableError struct {
	HostID string
	Status string
}

func (e *NotRetryableError) Error() string {
	if e.HostID != "" && e.Status != "" {
		return fmt.Sprintf("host %s cannot be retried: current status is %s", e.HostID, e.Status)
	}
	return "host cannot be retried"
}

// Is makes any *NotRetryableError match the ErrNotRetryable sentinel via errors.Is.
func (e *NotRetryableError) Is(target error) bool {
	_, ok := target.(*NotRetryableError)
	return ok
}

// Sentinels for errors.Is matching; any instance of the corresponding type
// matches via its Is method. HTTP status codes live outside this package:
// web.MapError decides them for /v2, and the legacy /v1 facade classifies the
// same families independently against its own frozen contract.
var (
	// ErrNotFound is returned when a host cannot be found.
	ErrNotFound = &NotFoundError{}

	// ErrCapacityExceeded is returned when the host does not have enough capacity.
	ErrCapacityExceeded = &CapacityExceededError{}

	// ErrInvalidState is returned when a state transition is invalid.
	ErrInvalidState = &InvalidStateError{}

	// ErrNotRetryable is returned when a host cannot be retried.
	ErrNotRetryable = &NotRetryableError{}

	// ErrDegraded is returned when a create targets a poison-fenced host.
	ErrDegraded = &DegradedError{}
)

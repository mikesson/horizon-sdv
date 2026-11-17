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

package device

import (
	"context"
	"time"
)

// ListFilter provides filtering and pagination options for listing devices.
//
// All filter fields are optional. When multiple filters are set, they are
// combined with AND logic. Use Limit and Offset for pagination.
type ListFilter struct {
	// HostID filters devices to those running on a specific host.
	HostID *string

	// Status filters devices to those in a specific lifecycle state.
	Status *Status

	// HealthStatus filters devices to those in a specific health state (e.g.
	// the zombie suspector lists running+zombie devices).
	HealthStatus *HealthStatus

	// CreatedBy filters devices to those created by a specific user email.
	CreatedBy *string

	// Name filters devices to those with exactly this name. It never matches
	// a deleted device, whatever Status and IncludeDeleted say.
	Name *string

	// BuildID filters devices to those using a specific Android build.
	BuildID *string

	// BatchID filters devices to the members of a specific batch.
	BatchID *string

	// BugreportUncaptured restricts results to devices whose
	// bugreport_captured_at is NULL, which is the failed-capture detector's
	// scan for error devices not yet handled by capture-on-error.
	BugreportUncaptured bool

	// Strong requests a strong (read-your-writes) read instead of the default
	// bounded-staleness list read. Set it when the caller must observe rows it
	// (or its client) just wrote, such as a batch admission reading back its
	// members. A stale read there can miss just-created rows entirely.
	Strong bool

	// Limit is the maximum number of devices to return (for pagination).
	Limit int

	// Offset is the number of devices to skip (for pagination).
	Offset int

	// AfterCreatedAt and AfterID are the sort key of the last row of the
	// previous keyset page. A non-empty AfterID returns only rows after it in
	// (created_at DESC, device_id DESC) order. Count ignores them.
	AfterCreatedAt time.Time
	AfterID        string

	// IncludeDeleted includes soft-deleted devices in the results.
	IncludeDeleted bool
}

// Getter is the read-one port for transport handlers that only resolve a
// device (displays, TURN, WebRTC signaling). The device repository satisfies
// it.
type Getter interface {
	// Get retrieves a device by ID
	Get(ctx context.Context, id string) (*Device, error)
}

// Repository defines the abstract data access contract for devices.
//
// This interface is implemented by the adapter in internal/features/device/repository.go,
// decoupling the domain from the underlying storage (Cloud Spanner).
type Repository interface {
	Getter

	// List retrieves devices matching the filter
	List(ctx context.Context, filter ListFilter) ([]*Device, error)

	// Count returns the number of devices matching the filter, ignoring
	// pagination (same predicates as List).
	Count(ctx context.Context, filter ListFilter) (int64, error)

	// ListByHost retrieves all devices on a host
	ListByHost(ctx context.Context, hostID string) ([]*Device, error)

	// CountLiveByHost returns how many devices on a host still back (or are
	// acquiring) a CVD, which is every state except error/deleting/deleted. A
	// host-wide `cvd reset` would destroy exactly these, so poison remediation
	// gates on it.
	CountLiveByHost(ctx context.Context, hostID string) (int64, error)

	// Create stores a new device
	Create(ctx context.Context, device *Device) error

	// UpdateStatus updates only the device status
	UpdateStatus(ctx context.Context, id string, status Status, errorMsg string) error

	// CountByStatus returns device count per status
	CountByStatus(ctx context.Context) (map[Status]int64, error)

	// BatchUpdateHealth applies one health-check cycle's results in a single
	// commit. Updates marked TimestampOnly advance only the check/healthy
	// timestamps (steady-state healthy devices), keeping write churn at one
	// commit per host instead of one per device.
	BatchUpdateHealth(ctx context.Context, updates []HealthUpdate) error

	// ListNeedingHealthCheck returns devices that need a health check
	ListNeedingHealthCheck(ctx context.Context, olderThan time.Duration) ([]*Device, error)
}

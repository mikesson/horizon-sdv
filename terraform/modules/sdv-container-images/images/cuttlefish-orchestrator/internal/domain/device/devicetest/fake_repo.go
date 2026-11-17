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

// Package devicetest provides an in-memory fake of the device.Repository
// interface, shared by unit tests across features and jobs.
package devicetest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// FakeRepo is an in-memory device.Repository. It stores devices by pointer, so
// mutations made through the repository are visible to test-held references.
// It is not synchronized: seed state before handing it to background loops.
type FakeRepo struct {
	// Devices is the backing store, keyed by device ID. Tests may seed and
	// inspect it directly.
	Devices map[string]*device.Device

	// NeedingCheck is returned verbatim by ListNeedingHealthCheck.
	NeedingCheck []*device.Device

	// LastListFilter records the most recent List/Count filter, so transport
	// tests can assert what filter actually reached the repository.
	LastListFilter *device.ListFilter

	// Per-method error injection: when non-nil, the method fails with it.
	// ListErr also fails ListNeedingHealthCheck.
	GetErr          error
	ListErr         error
	CreateErr       error
	UpdateErr       error
	UpdateStatusErr error
	UpdateHealthErr error

	// StatusUpdates and HealthUpdates record the last status passed to
	// UpdateStatus / BatchUpdateHealth per device, including calls for
	// devices that are not stored.
	StatusUpdates map[string]device.Status
	HealthUpdates map[string]device.HealthStatus

	// HealthBatches records every BatchUpdateHealth call verbatim.
	HealthBatches [][]device.HealthUpdate

	// Snapshots is the backing store for ListSnapshotsByDevice, keyed by
	// snapshot ID. Tests may seed and inspect it directly.
	Snapshots map[string]*device.Snapshot

	// BootObservationErr fails RecordBootObservation when non-nil.
	BootObservationErr error
}

var _ device.Repository = (*FakeRepo)(nil)

// NewFakeRepo returns a FakeRepo seeded with the given devices.
func NewFakeRepo(seed ...*device.Device) *FakeRepo {
	f := &FakeRepo{
		Devices:       make(map[string]*device.Device, len(seed)),
		StatusUpdates: make(map[string]device.Status),
		HealthUpdates: make(map[string]device.HealthStatus),
		Snapshots:     make(map[string]*device.Snapshot),
	}
	for _, d := range seed {
		f.Devices[d.ID] = d
	}
	return f
}

// RecordBootObservation stores the observation and CVD status on the device,
// mirroring the production one-sample write. A missing device is not an
// error, matching the repository's NotFound-swallowing contract.
func (f *FakeRepo) RecordBootObservation(_ context.Context, _, deviceID, cvdStatus string, obs *device.BootObservation) error {
	if f.BootObservationErr != nil {
		return f.BootObservationErr
	}
	d, ok := f.Devices[deviceID]
	if !ok {
		return nil
	}
	d.BootObservation = obs
	d.CVDStatus = cvdStatus
	return nil
}

// Get returns the stored device or device.ErrNotFound.
func (f *FakeRepo) Get(_ context.Context, id string) (*device.Device, error) {
	if f.GetErr != nil {
		return nil, f.GetErr
	}
	d, ok := f.Devices[id]
	if !ok {
		return nil, device.ErrNotFound
	}
	return d, nil
}

// List returns devices in ID order, honoring the HostID and Status filters.
func (f *FakeRepo) List(_ context.Context, filter device.ListFilter) ([]*device.Device, error) {
	f.LastListFilter = &filter
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	var result []*device.Device
	for _, id := range slices.Sorted(maps.Keys(f.Devices)) {
		d := f.Devices[id]
		if filter.HostID != nil && d.HostID != *filter.HostID {
			continue
		}
		// Mirrors the real repository: an explicit Status filter wins; otherwise
		// deleted rows are excluded unless IncludeDeleted.
		if filter.Status != nil {
			if d.Status != *filter.Status {
				continue
			}
		} else if !filter.IncludeDeleted && d.Status == device.StatusDeleted {
			continue
		}
		if filter.HealthStatus != nil && d.HealthStatus != *filter.HealthStatus {
			continue
		}
		if filter.BatchID != nil && d.BatchID != *filter.BatchID {
			continue
		}
		if filter.BugreportUncaptured && d.BugreportCapturedAt != nil {
			continue
		}
		result = append(result, d)
	}
	return result, nil
}

// Count returns the number of devices matching the filter.
func (f *FakeRepo) Count(ctx context.Context, filter device.ListFilter) (int64, error) {
	devices, err := f.List(ctx, filter)
	if err != nil {
		return 0, err
	}
	return int64(len(devices)), nil
}

// ListByHost returns the devices on the given host in ID order.
func (f *FakeRepo) ListByHost(ctx context.Context, hostID string) ([]*device.Device, error) {
	return f.List(ctx, device.ListFilter{HostID: &hostID})
}

// CountLiveByHost counts devices on the host in any state except
// error/deleting/deleted (the states that back no CVD).
func (f *FakeRepo) CountLiveByHost(_ context.Context, hostID string) (int64, error) {
	if f.ListErr != nil {
		return 0, f.ListErr
	}
	var count int64
	for _, d := range f.Devices {
		if d.HostID != hostID {
			continue
		}
		switch d.Status {
		case device.StatusError, device.StatusDeleting, device.StatusDeleted:
		default:
			count++
		}
	}
	return count, nil
}

// Create stores the device.
func (f *FakeRepo) Create(_ context.Context, d *device.Device) error {
	if f.CreateErr != nil {
		return f.CreateErr
	}
	f.Devices[d.ID] = d
	return nil
}

// UpdateStatus records the call and applies the status to a stored device.
func (f *FakeRepo) UpdateStatus(_ context.Context, id string, status device.Status, errorMsg string) error {
	if f.UpdateStatusErr != nil {
		return f.UpdateStatusErr
	}
	f.StatusUpdates[id] = status
	if d, ok := f.Devices[id]; ok {
		d.Status = status
		d.ErrorMessage = errorMsg
	}
	return nil
}

// Mutate mirrors the real repository contract: fn runs against a copy of the
// stored device and the result is persisted only when fn succeeds; a deleted
// or missing row reports device.ErrNotFound. UpdateErr injects a failure.
func (f *FakeRepo) Mutate(_ context.Context, id string, fn func(*device.Device) error) (*device.Device, error) {
	if f.UpdateErr != nil {
		return nil, f.UpdateErr
	}
	d, ok := f.Devices[id]
	if !ok {
		return nil, device.ErrNotFound
	}
	working := *d
	if err := fn(&working); err != nil {
		return nil, fmt.Errorf("mutating device %s: %w", id, err)
	}
	working.UpdatedAt = time.Now()
	f.Devices[id] = &working
	return &working, nil
}

// CountByStatus returns device counts per status.
func (f *FakeRepo) CountByStatus(_ context.Context) (map[device.Status]int64, error) {
	counts := make(map[device.Status]int64)
	for _, d := range f.Devices {
		counts[d.Status]++
	}
	return counts, nil
}

// BatchUpdateHealth records the call and applies each full update to its
// stored device (TimestampOnly updates change no health columns).
func (f *FakeRepo) BatchUpdateHealth(_ context.Context, updates []device.HealthUpdate) error {
	if f.UpdateHealthErr != nil {
		return f.UpdateHealthErr
	}
	f.HealthBatches = append(f.HealthBatches, updates)
	for _, u := range updates {
		if u.TimestampOnly {
			continue
		}
		f.HealthUpdates[u.DeviceID] = u.Status
		if d, ok := f.Devices[u.DeviceID]; ok {
			d.HealthStatus = u.Status
			d.ConsecutiveFails = int(u.ConsecutiveFails)
			d.LastFailureReason = u.FailReason
		}
	}
	return nil
}

// ListSnapshotsByDevice returns the seeded snapshots for one device, newest
// first like the real query. ListErr fails it.
func (f *FakeRepo) ListSnapshotsByDevice(_ context.Context, deviceID string) ([]*device.Snapshot, error) {
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	var out []*device.Snapshot
	for _, s := range f.Snapshots {
		if s.DeviceID == deviceID {
			cp := *s
			out = append(out, &cp)
		}
	}
	slices.SortFunc(out, func(a, b *device.Snapshot) int {
		return b.CreatedAt.Compare(a.CreatedAt)
	})
	return out, nil
}

// ListNeedingHealthCheck returns the NeedingCheck slice.
func (f *FakeRepo) ListNeedingHealthCheck(_ context.Context, _ time.Duration) ([]*device.Device, error) {
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	return f.NeedingCheck, nil
}

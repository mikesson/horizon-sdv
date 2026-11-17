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

// Package hosttest provides an in-memory fake of the host.Repository
// interface, shared by unit tests across features and jobs.
package hosttest

import (
	"context"
	"maps"
	"slices"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// FakeRepo is an in-memory host.Repository. It stores hosts by pointer, so
// mutations made through the repository are visible to test-held references.
// It is not synchronized: seed state before handing it to background loops.
type FakeRepo struct {
	// Hosts is the backing store, keyed by host ID. Tests may seed and
	// inspect it directly.
	Hosts map[string]*host.Host

	// LastListFilter records the most recent List/Count filter, so transport
	// tests can assert what filter actually reached the repository.
	LastListFilter *host.ListFilter

	// Per-method error injection: when non-nil, the method fails with it.
	GetErr    error
	ListErr   error
	CreateErr error
	DeleteErr error
}

var _ host.Repository = (*FakeRepo)(nil)

// NewFakeRepo returns a FakeRepo seeded with the given hosts.
func NewFakeRepo(seed ...*host.Host) *FakeRepo {
	f := &FakeRepo{
		Hosts: make(map[string]*host.Host, len(seed)),
	}
	for _, h := range seed {
		f.Hosts[h.ID] = h
	}
	return f
}

// Get returns the stored host or host.ErrNotFound.
func (f *FakeRepo) Get(_ context.Context, id string) (*host.Host, error) {
	if f.GetErr != nil {
		return nil, f.GetErr
	}
	h, ok := f.Hosts[id]
	if !ok {
		return nil, host.ErrNotFound
	}
	return h, nil
}

// RecordCreateFailure counts a poison-signature create failure against the host,
// reporting whether this call newly degraded it.
func (f *FakeRepo) RecordCreateFailure(_ context.Context, hostID, reason string) (bool, error) {
	h, ok := f.Hosts[hostID]
	if !ok {
		return false, host.ErrNotFound
	}
	return h.RecordCreateFailure(reason), nil
}

// ClearCreateFailures lifts the host's degraded condition and resets its
// failure counter.
func (f *FakeRepo) ClearCreateFailures(_ context.Context, hostID string) error {
	if h, ok := f.Hosts[hostID]; ok {
		h.ClearDegraded()
	}
	return nil
}

// GetByIDs returns the hosts that exist, omitting the ones that do not rather
// than reporting host.ErrNotFound. It fails through GetErr, the same injection
// point as Get.
func (f *FakeRepo) GetByIDs(_ context.Context, ids []string) (map[string]*host.Host, error) {
	if f.GetErr != nil {
		return nil, f.GetErr
	}
	result := make(map[string]*host.Host, len(ids))
	for _, id := range ids {
		if h, ok := f.Hosts[id]; ok {
			result[id] = h
		}
	}
	return result, nil
}

// List returns hosts in ID order, honoring the Zone and Status filters.
func (f *FakeRepo) List(_ context.Context, filter host.ListFilter) ([]*host.Host, error) {
	f.LastListFilter = &filter
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	var result []*host.Host
	for _, id := range slices.Sorted(maps.Keys(f.Hosts)) {
		h := f.Hosts[id]
		if filter.Zone != nil && h.Zone != *filter.Zone {
			continue
		}
		if filter.Status != nil && h.Status != *filter.Status {
			continue
		}
		if filter.Degraded != nil && *filter.Degraded != h.IsDegraded() {
			continue
		}
		result = append(result, h)
	}
	return result, nil
}

// Count returns the number of hosts matching the filter.
func (f *FakeRepo) Count(ctx context.Context, filter host.ListFilter) (int64, error) {
	hosts, err := f.List(ctx, filter)
	if err != nil {
		return 0, err
	}
	return int64(len(hosts)), nil
}

// Create stores the host.
func (f *FakeRepo) Create(_ context.Context, h *host.Host) error {
	if f.CreateErr != nil {
		return f.CreateErr
	}
	f.Hosts[h.ID] = h
	return nil
}

// Delete removes the host.
func (f *FakeRepo) Delete(_ context.Context, id string) error {
	if f.DeleteErr != nil {
		return f.DeleteErr
	}
	delete(f.Hosts, id)
	return nil
}

// CountByStatus returns host counts per status.
func (f *FakeRepo) CountByStatus(_ context.Context) (map[host.Status]int64, error) {
	counts := make(map[host.Status]int64)
	for _, h := range f.Hosts {
		counts[h.Status]++
	}
	return counts, nil
}

// FindAvailableHosts returns hosts in the zone with at least requiredSlots free.
func (f *FakeRepo) FindAvailableHosts(_ context.Context, zone string, requiredSlots int64) ([]*host.Host, error) {
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	var result []*host.Host
	for _, id := range slices.Sorted(maps.Keys(f.Hosts)) {
		h := f.Hosts[id]
		if h.Zone == zone && !h.IsDegraded() && h.HasCapacity() && h.AvailableSlots() >= requiredSlots {
			result = append(result, h)
		}
	}
	return result, nil
}

// MarkHOReady records first HO contact on a stored host and promotes it
// 'starting' -> 'running'. Idempotent (no-op once recorded).
func (f *FakeRepo) MarkHOReady(_ context.Context, id string) (bool, error) {
	h, ok := f.Hosts[id]
	if !ok {
		return false, host.ErrNotFound
	}
	return h.MarkHOReady(), nil
}

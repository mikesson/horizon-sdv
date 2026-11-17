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
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"testing"

	"cloud.google.com/go/spanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/gce"
)

// fakeZoneHealthStore records failure cells in memory and serves whatever
// stockout memory a test seeded.
type fakeZoneHealthStore struct {
	recorded    []recordedFailure
	cleared     []string
	consecutive map[string]int64

	// cells is the stockout memory a walk consults; capacityReads counts the
	// reads it took, so a walk that grew a read per rung fails rather than
	// merely getting slower.
	cells         []*gcecatalog.ZoneHealth
	capacityReads int
	families      []string
}

type recordedFailure struct{ zone, family, metric string }

// RecordFailure counts consecutive failures per cell the way the store does,
// because the returned count is what bounds the stockout re-probe: a fake that
// always answered 1 would make "one probe per cooldown episode" untestable.
func (f *fakeZoneHealthStore) RecordFailure(_ context.Context, zone, family, metric, _ string) (*gcecatalog.ZoneHealth, error) {
	f.recorded = append(f.recorded, recordedFailure{zone, family, metric})
	if f.consecutive == nil {
		f.consecutive = map[string]int64{}
	}
	f.consecutive[zone+"/"+family]++
	return &gcecatalog.ZoneHealth{
		Zone: zone, MachineFamily: family, BindingMetric: metric,
		ConsecutiveFailures: f.consecutive[zone+"/"+family],
	}, nil
}

func (f *fakeZoneHealthStore) ClearCell(_ context.Context, zone, family string) error {
	f.cleared = append(f.cleared, zone+"/"+family)
	return nil
}

func (f *fakeZoneHealthStore) ListCells(context.Context) ([]*gcecatalog.ZoneHealth, error) {
	return nil, nil
}

func (f *fakeZoneHealthStore) ListCapacityCells(_ context.Context, families []string) ([]*gcecatalog.ZoneHealth, error) {
	f.capacityReads++
	f.families = families
	matched := make([]*gcecatalog.ZoneHealth, 0, len(f.cells))
	for _, cell := range f.cells {
		if slices.Contains(families, cell.MachineFamily) {
			matched = append(matched, cell)
		}
	}
	return matched, nil
}

// stockoutErr mimics GCE's zoneResourcePoolExhausted insert rejection.
func stockoutErr() error {
	return &googleapi.Error{
		Code: http.StatusServiceUnavailable,
		Errors: []googleapi.ErrorItem{{
			Reason:  "zoneResourcePoolExhausted",
			Message: "The zone does not have enough resources",
		}},
	}
}

// quotaRefusal is a quota-exceeded operation failure carrying the structured
// detail GCE attaches to one. The axis is derived through the same mapping the
// platform adapter reads the operation with; that the adapter reads it off the
// wire at all is proven where it does the reading.
func quotaRefusal(message, metricName string, dimensions map[string]string) error {
	return &gce.OperationError{
		Err:       &googleapi.Error{Code: http.StatusForbidden, Message: message},
		Reason:    "quotaExceeded",
		QuotaAxis: gcecatalog.QuotaTypeForMetricName(metricName, dimensions),
	}
}

// failoverHost seeds a mid-provision host in europe-west4-a.
func failoverHost() *host.Host {
	return &host.Host{
		ID: testHostID, Name: "h", Zone: "europe-west4-a", MachineType: "n1-standard-8",
		GCEInstanceName: host.InstanceName("h", testHostID, 0),
		Status:          host.StatusProvisioning, DesiredState: host.DesiredRunning,
		MaxDevices: 4,
	}
}

func failoverService(t *testing.T, seed *host.Host, validator *fakePlacementValidator) (*HostService, *atomicHostRepo, *fakeZoneHealthStore) {
	t.Helper()
	repo := newAtomicHostRepo(seed)
	zh := &fakeZoneHealthStore{}
	svc, _ := newTestService(t, repo, nil,
		WithCatalogValidator(validator), WithZoneHealth(zh))
	return svc, repo, zh
}

func TestFailoverMetricClassificationTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		wantMetric string
		wantOK     bool
	}{
		{"stockout", stockoutErr(), gcecatalog.ZoneMetricCapacity, true},
		{"cpu quota", &googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{{
			Reason: "quotaExceeded", Message: "Quota CPUS exceeded in region",
		}}}, gcecatalog.ZoneMetricQuotaCPU, true},
		{"gpu quota", &googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{{
			Reason: "quotaExceeded", Message: "Quota NVIDIA_T4_GPUS exceeded",
		}}}, gcecatalog.ZoneMetricQuotaGPU, true},
		// A dimensioned CPU refusal cools the CPU axis, and the family it was
		// for rides the cell's own key rather than the metric name.
		{"per-family cpu quota", quotaRefusal(
			"Quota CPUS_PER_VM_FAMILY exceeded. Limit: 300.0 in region europe-west4.",
			"compute.googleapis.com/cpus_per_vm_family",
			map[string]string{"region": "europe-west4", "vm_family": "C4"},
		), gcecatalog.ZoneMetricQuotaCPU, true},
		// A hyperdisk refusal cools the hyperdisk axis, not the SSD one: the
		// two pools are raised by separate quota requests, and a cell naming
		// the wrong one sends an operator after the wrong increase.
		{"hyperdisk quota", quotaRefusal(
			"Quota HDB_TOTAL_GB exceeded. Limit: 40960.0 in region europe-west4.",
			"compute.googleapis.com/hyperdisk_balanced_total_storage",
			map[string]string{"region": "europe-west4"},
		), gcecatalog.ZoneMetricQuotaHyperdisk, true},
		{"machine type not in zone", errors.New("machine type not found in zone"), gcecatalog.ZoneMetricAvailability, true},
		{"rate limit is not capacity", &googleapi.Error{Code: http.StatusTooManyRequests}, "", false},
		// The quota-phrased per-minute rate limit (empty structured errors)
		// must not read as a regional quota cooldown.
		{"quota-phrased rate limit is not capacity", &googleapi.Error{
			Code:    http.StatusTooManyRequests,
			Message: "Quota exceeded for quota metric 'Queries' and limit 'Queries per minute' of service 'compute.googleapis.com'",
		}, "", false},
		{"permission denied is not capacity", &googleapi.Error{Code: http.StatusForbidden}, "", false},
		{"generic 5xx is not capacity", &googleapi.Error{Code: http.StatusServiceUnavailable, Message: "backend error"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			metric, ok := failoverMetric(gce.ClassifyError(tt.err))
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantMetric, metric)
		})
	}
}

func TestFailoverMovesHostAndBumpsIncarnation(t *testing.T) {
	t.Parallel()
	seed := failoverHost()
	validator := &fakePlacementValidator{candidates: []gcecatalog.ZoneCandidate{
		{Zone: "us-central1-a", Region: "us-central1", Score: 90},
	}}
	svc, repo, zh := failoverService(t, seed, validator)

	svc.FailoverZone(t.Context(), testHostID, stockoutErr(), true)

	h := repo.Hosts[testHostID]
	assert.Equal(t, "us-central1-a", h.Zone, "the host binds to the best other zone")
	assert.Equal(t, int64(1), h.Incarnation, "a moved host is a fresh incarnation")
	assert.Equal(t, host.InstanceName("h", testHostID, 1), h.GCEInstanceName)
	assert.NotNil(t, h.ProvisionBlockedAt,
		"the refusal is stamped on the row and survives the move: the new zone is still a bet")
	assert.Contains(t, h.ProvisionBlockedReason, "resources")
	require.Len(t, zh.recorded, 1)
	assert.Equal(t, "europe-west4-a", zh.recorded[0].zone)
	assert.Equal(t, "n1", zh.recorded[0].family)
	assert.Equal(t, gcecatalog.ZoneMetricCapacity, zh.recorded[0].metric)
	assert.Empty(t, validator.lastOpts.ExcludeZone,
		"no explicit exclusion: the recorded cooldown is what filters the failed zone")
}

func TestFailoverPinnedHostRecordsCooldownButStays(t *testing.T) {
	t.Parallel()
	seed := failoverHost()
	seed.RequestedZone, seed.ZonePinned = "europe-west4-a", true
	validator := &fakePlacementValidator{candidates: []gcecatalog.ZoneCandidate{
		{Zone: "us-central1-a", Region: "us-central1", Score: 90},
	}}
	svc, repo, zh := failoverService(t, seed, validator)

	svc.FailoverZone(t.Context(), testHostID, stockoutErr(), true)

	h := repo.Hosts[testHostID]
	assert.Equal(t, "europe-west4-a", h.Zone, "a pinned host retries in place")
	assert.Zero(t, h.Incarnation)
	assert.NotNil(t, h.ProvisionBlockedAt, "a pinned host that cannot move is the one placement most needs to skip")
	assert.Len(t, zh.recorded, 1, "the cooldown is still recorded so other placements learn")
	assert.Zero(t, validator.rankCalls, "no ranking for a host that cannot move")
}

func TestFailoverExhaustedRecordsWithoutMoving(t *testing.T) {
	t.Parallel()
	seed := failoverHost()
	validator := &fakePlacementValidator{candidates: []gcecatalog.ZoneCandidate{
		{Zone: "us-central1-a", Region: "us-central1", Score: 90},
	}}
	svc, repo, zh := failoverService(t, seed, validator)

	svc.FailoverZone(t.Context(), testHostID, stockoutErr(), false)

	assert.Equal(t, "europe-west4-a", repo.Hosts[testHostID].Zone,
		"no delivery remains, so there is nothing to move for")
	assert.Len(t, zh.recorded, 1)
}

func TestFailoverNonCapacityErrorDoesNothing(t *testing.T) {
	t.Parallel()
	seed := failoverHost()
	validator := &fakePlacementValidator{candidates: []gcecatalog.ZoneCandidate{
		{Zone: "us-central1-a", Region: "us-central1", Score: 90},
	}}
	svc, repo, zh := failoverService(t, seed, validator)

	svc.FailoverZone(t.Context(), testHostID,
		&googleapi.Error{Code: http.StatusTooManyRequests}, true)

	assert.Equal(t, "europe-west4-a", repo.Hosts[testHostID].Zone)
	assert.Empty(t, zh.recorded, "a rate limit must not poison the zone")
	assert.Nil(t, repo.Hosts[testHostID].ProvisionBlockedAt,
		"being throttled says nothing about capacity, so the host stays placeable")
}

// The marker fences placement from the FIRST refusal and dates from it: a
// second refusal in another zone updates the reason and keeps the timestamp,
// so an operator reads how long the host has been turned away, not how
// recently.
func TestFailoverBlockedMarkerDatesFromTheFirstRefusal(t *testing.T) {
	t.Parallel()
	seed := failoverHost()
	validator := &fakePlacementValidator{candidates: []gcecatalog.ZoneCandidate{
		{Zone: "us-central1-a", Region: "us-central1", Score: 90},
	}}
	svc, repo, _ := failoverService(t, seed, validator)

	svc.FailoverZone(t.Context(), testHostID, stockoutErr(), true)
	first := repo.Hosts[testHostID].ProvisionBlockedAt
	require.NotNil(t, first)
	firstReason := repo.Hosts[testHostID].ProvisionBlockedReason

	svc.FailoverZone(t.Context(), testHostID, quotaRefusal("Quota CPUS exceeded", "CPUS", nil), true)
	h := repo.Hosts[testHostID]
	assert.Equal(t, first, h.ProvisionBlockedAt, "re-marking keeps the first timestamp")
	assert.NotEqual(t, firstReason, h.ProvisionBlockedReason, "and carries the latest reason")
	assert.Contains(t, h.ProvisionBlockedReason, "quota")
}

func TestFailoverSupersededByConcurrentTransition(t *testing.T) {
	t.Parallel()
	seed := failoverHost()
	seed.Status = host.StatusDeleting // a user delete won the race
	seed.DesiredState = host.DesiredAbsent
	validator := &fakePlacementValidator{candidates: []gcecatalog.ZoneCandidate{
		{Zone: "us-central1-a", Region: "us-central1", Score: 90},
	}}
	svc, repo, zh := failoverService(t, seed, validator)

	svc.FailoverZone(t.Context(), testHostID, stockoutErr(), true)

	h := repo.Hosts[testHostID]
	assert.Equal(t, "europe-west4-a", h.Zone, "a deleting host is never moved")
	assert.Equal(t, host.StatusDeleting, h.Status)
	assert.Empty(t, zh.recorded, "the failure lost its race; nothing is recorded or moved")
	assert.Nil(t, h.ProvisionBlockedAt, "nor is the row marked")
}

// raceFlipRepo flips the stored host's status at the moment Mutate runs,
// modeling a concurrent transition that lands between FailoverZone's read and
// its zone write.
type raceFlipRepo struct {
	*atomicHostRepo
	flipTo host.Status
}

func (r *raceFlipRepo) Mutate(ctx context.Context, id string, fn func(*host.Host) error, extra ...*spanner.Mutation) (*host.Host, error) {
	r.Hosts[id].Status = r.flipTo // the concurrent transition wins here
	return r.atomicHostRepo.Mutate(ctx, id, fn, extra...)
}

// A delete landing between FailoverZone's read and its zone write must lose
// inside the Mutate closure (errFailoverSuperseded), and not only at the
// earlier read-time status check: the cooldown stays recorded, ranking has
// happened, and the host keeps the winner's state untouched.
func TestFailoverCASGuardLosesToMidFlightDelete(t *testing.T) {
	t.Parallel()
	seed := failoverHost()
	validator := &fakePlacementValidator{candidates: []gcecatalog.ZoneCandidate{
		{Zone: "us-central1-a", Region: "us-central1", Score: 90},
	}}
	repo := &raceFlipRepo{atomicHostRepo: newAtomicHostRepo(seed), flipTo: host.StatusDeleting}
	zh := &fakeZoneHealthStore{}
	svc := &HostService{
		hostRepo: repo, logger: slog.New(slog.DiscardHandler),
		zoneHealth: zh, catalogValidator: validator,
	}

	svc.FailoverZone(t.Context(), testHostID, stockoutErr(), true)

	h := repo.Hosts[testHostID]
	assert.Equal(t, host.StatusDeleting, h.Status, "the concurrent delete's state survives")
	assert.Equal(t, "europe-west4-a", h.Zone, "the losing CAS must not move the host")
	assert.Zero(t, h.Incarnation, "the losing CAS must not bump the incarnation")
	assert.Equal(t, host.InstanceName("h", testHostID, 0), h.GCEInstanceName)
	assert.Len(t, zh.recorded, 1, "the capacity failure was real; the cooldown stays")
	assert.Equal(t, 1, validator.rankCalls, "the race was lost at the write, after ranking")
}

func TestFailoverNoBetterZoneRetriesInPlace(t *testing.T) {
	t.Parallel()
	seed := failoverHost()
	validator := &fakePlacementValidator{candidates: []gcecatalog.ZoneCandidate{
		{Zone: "europe-west4-a", Region: "europe-west4", Score: 50},
	}}
	svc, repo, zh := failoverService(t, seed, validator)

	svc.FailoverZone(t.Context(), testHostID, stockoutErr(), true)

	h := repo.Hosts[testHostID]
	assert.Equal(t, "europe-west4-a", h.Zone)
	assert.Zero(t, h.Incarnation)
	assert.Len(t, zh.recorded, 1, "the cooldown is recorded even when nowhere better exists")
}

func TestFailoverHintCompilesToPreferredRegion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		requested string
		want      string
	}{
		{"", ""},
		{"us-central1", "us-central1"},   // a declarative region hint
		{"us-central1-b", "us-central1"}, // a /v1 zone token
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, preferredRegionOf(tt.requested), "requested=%q", tt.requested)
	}
}

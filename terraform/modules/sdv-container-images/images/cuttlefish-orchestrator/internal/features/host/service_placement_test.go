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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// fakePlacementValidator scripts RankZones and accepts every Validate call.
type fakePlacementValidator struct {
	candidates []gcecatalog.ZoneCandidate
	// byMachineType answers per shape, for the walk tests: a shape ladder asks
	// the ranker a different question per rung, and a fake that answered every
	// shape identically could not tell a rung step from a zone move.
	byMachineType map[string][]gcecatalog.ZoneCandidate
	rankErr       error
	rankCalls     int
	lastReq       gcecatalog.ValidationRequest
	lastOpts      gcecatalog.RankOptions
	lastValidate  gcecatalog.ValidationRequest
}

func (f *fakePlacementValidator) Validate(_ context.Context, req gcecatalog.ValidationRequest) (*gcecatalog.ValidationResult, error) {
	f.lastValidate = req
	return &gcecatalog.ValidationResult{Valid: true}, nil
}

func (f *fakePlacementValidator) RankZones(_ context.Context, req gcecatalog.ValidationRequest, opts gcecatalog.RankOptions) ([]gcecatalog.ZoneCandidate, error) {
	f.rankCalls++
	f.lastReq, f.lastOpts = req, opts
	if shaped, ok := f.byMachineType[req.MachineType]; ok {
		return shaped, f.rankErr
	}
	return f.candidates, f.rankErr
}

func TestCreateAutoRanksZoneAndPersistsIntent(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	validator := &fakePlacementValidator{candidates: []gcecatalog.ZoneCandidate{
		{Zone: "europe-west4-b", Region: "europe-west4", Score: 90},
		{Zone: "europe-west4-a", Region: "europe-west4", Score: 70},
	}}
	svc, _ := newTestService(t, repo, nil, WithCatalogValidator(validator))

	h, err := svc.Create(t.Context(), host.CreateRequest{
		Name: "auto-host", MachineType: "n1-standard-8", MaxDevices: 4,
		LocationHint: "europe-west4",
	}, "u@example.com", host.OriginUser)
	require.NoError(t, err)

	assert.Equal(t, "europe-west4-b", h.Zone, "the best-ranked zone binds")
	assert.Equal(t, "europe-west4", h.RequestedZone, "the hint is the persisted intent")
	assert.False(t, h.ZonePinned)
	assert.Equal(t, 1, validator.rankCalls)
	assert.Equal(t, "europe-west4", validator.lastOpts.PreferredRegion)
	assert.Equal(t, "n1-standard-8", validator.lastReq.MachineType)
}

func TestCreatePinnedZoneSkipsRanking(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	validator := &fakePlacementValidator{}
	svc, _ := newTestService(t, repo, nil, WithCatalogValidator(validator))

	h, err := svc.Create(t.Context(), host.CreateRequest{
		Name: "pinned-host", Zone: "us-central1-a", ZonePinned: true, MaxDevices: 4,
	}, "u@example.com", host.OriginUser)
	require.NoError(t, err)

	assert.Equal(t, "us-central1-a", h.Zone)
	assert.Equal(t, "us-central1-a", h.RequestedZone, "a pin is its own request token")
	assert.True(t, h.ZonePinned)
	assert.Zero(t, validator.rankCalls, "a pinned create never ranks")
}

// The catalog precheck looks a machine type up by name, so a create naming none
// was refused as "Machine type ” is not supported" although the catalog has a
// default for exactly that case.
func TestCreateWithoutMachineTypeTakesTheCatalogDefault(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	validator := &fakePlacementValidator{}
	svc, _ := newTestService(t, repo, nil,
		WithCatalogValidator(validator), WithMachineTypeCatalog(imageCatalog()))

	h, err := svc.Create(t.Context(), host.CreateRequest{
		Name: "plain", Zone: "us-central1-a", ZonePinned: true, MaxDevices: 1,
	}, "u@example.com", host.OriginUser)
	require.NoError(t, err)

	assert.Equal(t, "n1-standard-8", validator.lastValidate.MachineType, "the precheck judges the default")
	assert.Equal(t, "n1-standard-8", h.MachineType, "the row records the shape that is built")
}

func TestCreatePreRankedZoneBindsWithoutPinning(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	validator := &fakePlacementValidator{}
	svc, _ := newTestService(t, repo, nil, WithCatalogValidator(validator))

	// The batch path ranks once per spec and replicates the zone into every
	// host of the template: bound, hint recorded, never pinned.
	h, err := svc.Create(t.Context(), host.CreateRequest{
		Name: "batch", Zone: "europe-west4-b", LocationHint: "europe-west4", MaxDevices: 4,
	}, "u@example.com", host.OriginUser)
	require.NoError(t, err)

	assert.Equal(t, "europe-west4-b", h.Zone)
	assert.Equal(t, "europe-west4", h.RequestedZone)
	assert.False(t, h.ZonePinned, "a pre-ranked binding stays failover-eligible")
	assert.Zero(t, validator.rankCalls, "a pre-ranked create never re-ranks")
}

func TestCreateAutoWithNoViableZoneFails(t *testing.T) {
	t.Parallel()
	repo := newAtomicHostRepo()
	validator := &fakePlacementValidator{} // no candidates
	svc, _ := newTestService(t, repo, nil, WithCatalogValidator(validator))

	_, err := svc.Create(t.Context(), host.CreateRequest{
		Name: "auto-host", MachineType: "n1-standard-8", MaxDevices: 4,
	}, "u@example.com", host.OriginUser)
	var verr *host.CatalogValidationError
	require.ErrorAs(t, err, &verr)
	assert.Empty(t, repo.Hosts, "nothing is created when no zone is viable")
}

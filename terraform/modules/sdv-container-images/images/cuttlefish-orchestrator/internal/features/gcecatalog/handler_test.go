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

package gcecatalogfeature

import (
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype/machinetypetest"
)

// curationHandler wires the curation surface over a catalog snapshot holding
// three zones and a two-shape curated catalog, with one zone enabled.
func curationHandler(t *testing.T) (*Handler, *mockCatalogRepo) {
	t.Helper()
	catalog := newMockCatalogRepo()
	catalog.enableZone("europe-west4-a", gcecatalog.ZoneStatusUp)
	catalog.zoneStatuses["europe-west4-b"] = gcecatalog.ZoneStatusUp
	catalog.zoneStatuses["us-central1-a"] = gcecatalog.ZoneStatusDown
	catalog.machineTypes["europe-west4-a"] = map[string]bool{"n1-standard-8": true, "n2-standard-8": true}
	// Reported as offering neither, which is an answer in its own right.
	catalog.machineTypes["europe-west4-b"] = map[string]bool{}
	// us-central1-a is left unreported.

	types := machinetypetest.NewFakeRepo()
	for _, id := range []string{"n1-standard-8", "n2-standard-8"} {
		types.Types[id] = &machinetype.MachineType{ID: id, Family: "n1", VCPUs: 8, IsEnabled: true}
	}
	return NewHandler(catalog, catalog, types), catalog
}

// The curation page lists what the snapshot holds. Serve a snapshot
// missing a zone and the page must stop listing it. The mutation that catches
// a page still reading a mirror is exactly this: drop europe-west4-b from the
// snapshot and it must disappear from the response.
func TestListAdminZonesShowsTheSnapshotsWorld(t *testing.T) {
	t.Parallel()
	h, catalog := curationHandler(t)

	out, err := h.ListAdminZones(t.Context(), &ListAdminZonesInput{})
	require.NoError(t, err)
	assert.Equal(t, []string{"n1-standard-8", "n2-standard-8"}, out.Body.CuratedShapes)

	byZone := map[string]AdminZone{}
	for _, z := range out.Body.Zones {
		byZone[z.Zone] = z
	}
	require.Len(t, byZone, 3, "every zone Compute Engine offers is listed, enabled or not")

	a := byZone["europe-west4-a"]
	assert.True(t, a.IsEnabled)
	assert.Equal(t, "europe-west4", a.Region)
	assert.Equal(t, string(gcecatalog.ZoneStatusUp), a.Status)
	assert.Equal(t, []string{"n1-standard-8", "n2-standard-8"}, a.OfferedShapes)
	assert.True(t, a.ShapesReported)

	b := byZone["europe-west4-b"]
	assert.False(t, b.IsEnabled)
	assert.Empty(t, b.OfferedShapes)
	assert.True(t, b.ShapesReported, "a zone reported as offering none of them is a positive answer")

	c := byZone["us-central1-a"]
	assert.Equal(t, string(gcecatalog.ZoneStatusDown), c.Status)
	assert.False(t, c.ShapesReported,
		"a zone Compute Engine said nothing about must not read as offering nothing")

	// The snapshot moves; the page moves with it.
	delete(catalog.zoneStatuses, "europe-west4-b")
	delete(catalog.machineTypes, "europe-west4-b")
	out, err = h.ListAdminZones(t.Context(), &ListAdminZonesInput{})
	require.NoError(t, err)
	for _, z := range out.Body.Zones {
		assert.NotEqual(t, "europe-west4-b", z.Zone,
			"a zone the snapshot dropped must leave the curation page")
	}
}

// A zone an operator enabled that the snapshot does not hold stays listed:
// dropping it would hide a decision that is live in placement.
func TestListAdminZonesKeepsAnEnabledZoneTheSnapshotLacks(t *testing.T) {
	t.Parallel()
	h, catalog := curationHandler(t)
	catalog.enabledZones = append(catalog.enabledZones, "mars-east1-a")

	out, err := h.ListAdminZones(t.Context(), &ListAdminZonesInput{})
	require.NoError(t, err)
	var found bool
	for _, z := range out.Body.Zones {
		if z.Zone == "mars-east1-a" {
			found = true
			assert.True(t, z.IsEnabled)
			assert.False(t, z.ShapesReported)
		}
	}
	assert.True(t, found, "an enabled zone must be listed even when the snapshot has never seen it")
}

// Fail-closed: with a snapshot, a zone Compute Engine does not offer is
// refused with a 400 and nothing is stored.
func TestSetEnabledZonesRefusesAZoneTheSnapshotDoesNotOffer(t *testing.T) {
	t.Parallel()
	h, catalog := curationHandler(t)

	input := &SetEnabledZonesInput{}
	input.Body.Zones = []string{"europe-west4-b", "mars-east1-a"}
	_, err := h.SetEnabledZones(t.Context(), input)

	require.Error(t, err)
	// Stays errors.As: huma.StatusError does not embed error, so it cannot
	// satisfy errors.AsType's constraint.
	var status huma.StatusError
	require.ErrorAs(t, err, &status, "the refusal must carry an HTTP status, not be a bare error")
	assert.Equal(t, http.StatusBadRequest, status.GetStatus())
	assert.Equal(t, []string{"europe-west4-a"}, catalog.enabledZones, "a refused write stores nothing")
}

// Fail-open on absence: an empty deployment must be configurable before the
// first catalog refresh lands, so with no snapshot any well-formed zone name
// is accepted.
func TestSetEnabledZonesAcceptsAnyZoneWithoutASnapshot(t *testing.T) {
	t.Parallel()
	h, catalog := curationHandler(t)
	clear(catalog.zoneStatuses)

	input := &SetEnabledZonesInput{}
	input.Body.Zones = []string{"mars-east1-a"}
	out, err := h.SetEnabledZones(t.Context(), input)

	require.NoError(t, err)
	assert.Equal(t, []string{"mars-east1-a"}, out.Body.Zones)
	assert.Equal(t, []string{"mars-east1-a"}, catalog.enabledZones)
}

// The set replaces whole, so an empty array is a legal statement: nowhere is
// enabled.
func TestSetEnabledZonesReplacesTheSetWhole(t *testing.T) {
	t.Parallel()
	h, catalog := curationHandler(t)

	input := &SetEnabledZonesInput{}
	input.Body.Zones = []string{}
	out, err := h.SetEnabledZones(t.Context(), input)

	require.NoError(t, err)
	assert.Empty(t, out.Body.Zones)
	assert.Empty(t, catalog.enabledZones)
}

// Freshness reports the snapshot's age and its silences.
func TestCatalogFreshnessReportsSnapshotAgeAndSilence(t *testing.T) {
	t.Parallel()
	h, catalog := curationHandler(t)

	out, err := h.CatalogFreshness(t.Context(), &CatalogFreshnessInput{})
	require.NoError(t, err)
	assert.Nil(t, out.Body.BuiltAt, "no snapshot has been built yet")
	assert.Empty(t, out.Body.Partial)
	assert.NotNil(t, out.Body.Partial, "an empty silence list serializes as [], never as null")

	built := time.Now().Add(-2 * time.Hour)
	catalog.builtAt = built
	catalog.partial = []string{"machine types: us-central1-a"}
	out, err = h.CatalogFreshness(t.Context(), &CatalogFreshnessInput{})
	require.NoError(t, err)
	require.NotNil(t, out.Body.BuiltAt)
	assert.Equal(t, built, *out.Body.BuiltAt)
	assert.Equal(t, []string{"machine types: us-central1-a"}, out.Body.Partial)
}

// The availability view is the enabled set with the snapshot's answers
// stamped on it; an unknown cell is not a promise.
func TestListZoneAvailabilityReportsOnlyPositiveOffers(t *testing.T) {
	t.Parallel()
	h, catalog := curationHandler(t)
	catalog.enableZone("us-central1-a", gcecatalog.ZoneStatusDown)

	out, err := h.ListZoneAvailability(t.Context(),
		&ListZoneAvailabilityInput{MachineType: "n1-standard-8"})
	require.NoError(t, err)
	require.Len(t, out.Body.Zones, 2)

	assert.Equal(t, "europe-west4-a", out.Body.Zones[0].Zone)
	assert.True(t, out.Body.Zones[0].MachineTypeAvailable)
	assert.Equal(t, string(gcecatalog.ZoneStatusUp), out.Body.Zones[0].Status)

	assert.Equal(t, "us-central1-a", out.Body.Zones[1].Zone)
	assert.False(t, out.Body.Zones[1].MachineTypeAvailable,
		"an unreported zone is not a promise of availability")
	assert.Equal(t, string(gcecatalog.ZoneStatusDown), out.Body.Zones[1].Status)
}

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
	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// fakeReprober records the re-probe tasks the failure path files.
type fakeReprober struct{ filed []*task.Task }

func (f *fakeReprober) Enqueue(_ context.Context, t *task.Task) error {
	f.filed = append(f.filed, t)
	return nil
}

// reproberService seeds a mid-provision host whose failures file re-probes.
func reproberService(t *testing.T) (*HostService, *fakeReprober, *fakeZoneHealthStore) {
	t.Helper()
	repo := newAtomicHostRepo(failoverHost())
	zh := &fakeZoneHealthStore{}
	filer := &fakeReprober{}
	// No validator: the re-probe is filed off the recorded cell, not off a move,
	// so this keeps the test to the one behavior.
	svc, _ := newTestService(t, repo, nil, WithZoneHealth(zh), WithStockoutReprober(filer))
	return svc, filer, zh
}

// A stockout files exactly one re-probe, keyed by the cell that failed.
func TestStockoutFilesOneReprobeForTheFailedCell(t *testing.T) {
	t.Parallel()
	svc, filer, _ := reproberService(t)

	svc.FailoverZone(t.Context(), testHostID, stockoutErr(), false)

	require.Len(t, filer.filed, 1)
	filed := filer.filed[0]
	assert.Equal(t, task.TypeCatalogReprobeSpotAdvice, filed.Type)
	assert.Equal(t, "europe-west4-a/n1-standard-8", filed.ResourceID,
		"the task names the advice cell: the zone that refused and the shape it refused")
	assert.Equal(t, task.PriorityResync, filed.Priority,
		"an advisory refresh must not claim ahead of user intent")
}

// Bounded by the cooldown row the failure just wrote: a storm hitting one cell
// again and again re-probes once, because only the failure that OPENS the cell
// reads back a consecutive count of 1.
func TestStockoutStormFilesOneReprobePerCooldownEpisode(t *testing.T) {
	t.Parallel()
	svc, filer, zh := reproberService(t)

	for range 5 {
		svc.FailoverZone(t.Context(), testHostID, stockoutErr(), false)
	}

	assert.Len(t, zh.recorded, 5, "every failure still cools the cell")
	assert.Len(t, filer.filed, 1, "only the failure that opened the cooldown files a probe")

	// A successful provision clears the cell; the next stockout opens a new
	// episode and earns a fresh probe.
	zh.consecutive = nil
	svc.FailoverZone(t.Context(), testHostID, stockoutErr(), false)
	assert.Len(t, filer.filed, 2, "a cleared cell starts a new episode")
}

// The advice API answers one question, is Spot obtainable here, so only a
// stockout earns a probe. A quota refusal cools the same cell and tells the
// capacity advisor nothing.
func TestQuotaRefusalFilesNoReprobe(t *testing.T) {
	t.Parallel()
	svc, filer, zh := reproberService(t)

	svc.FailoverZone(t.Context(), testHostID, quotaRefusal(
		"Quota CPUS exceeded. Limit: 300.0 in region europe-west4.",
		"compute.googleapis.com/cpus", map[string]string{"region": "europe-west4"},
	), false)

	require.Len(t, zh.recorded, 1)
	assert.Equal(t, gcecatalog.ZoneMetricQuotaCPU, zh.recorded[0].metric)
	assert.Empty(t, filer.filed, "a quota wall says nothing about Spot obtainability")
}

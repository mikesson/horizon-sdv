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

package catalogsync

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// fakeProber scripts per-cell advice; missing keys read as no recommendation,
// and cells in failCells error.
type fakeProber struct {
	cells     map[string]*gcecatalog.SpotAdvice // "zone/machineType"
	failCells map[string]bool
	probes    []string
}

func (f *fakeProber) ProbeCell(_ context.Context, _, zone, machineType string) (*gcecatalog.SpotAdvice, error) {
	key := zone + "/" + machineType
	f.probes = append(f.probes, key)
	if f.failCells[key] {
		return nil, errors.New("probe boom")
	}
	return f.cells[key], nil
}

func adviceSyncService(t *testing.T, prober SpotAdviceProber) *Service {
	t.Helper()
	s, err := New(Config{
		ProjectID:              "test-project",
		SpotAdviceSyncInterval: time.Minute,
	}, slog.Default(), WithSpotAdvice(prober, &fakeAdviceStore{}, &fakeMachineTypes{}, &fakeEnabledZones{}))
	require.NoError(t, err)
	return s
}

// The three dependencies beside the prober, inert: probeSpotCells is handed
// its cells directly, so these exist to satisfy the constructor.
type fakeAdviceStore struct{ gcecatalog.SpotAdviceStore }

type fakeMachineTypes struct{}

func (*fakeMachineTypes) List(context.Context, machinetype.ListFilter) ([]*machinetype.MachineType, error) {
	return nil, nil
}

type fakeEnabledZones struct{}

func (*fakeEnabledZones) ListEnabledZones(context.Context) ([]string, error) { return nil, nil }

func TestProbeSpotCellsCoversEveryCellAndSkipsFailures(t *testing.T) {
	t.Parallel()
	prober := &fakeProber{
		cells: map[string]*gcecatalog.SpotAdvice{
			"europe-west4-a/n1-standard-8": {Zone: "europe-west4-a", MachineType: "n1-standard-8", Obtainability: 0.9, UptimeTierSeconds: 3600},
			"europe-west4-b/n1-standard-8": {Zone: "europe-west4-b", MachineType: "n1-standard-8", Obtainability: 0.4, UptimeTierSeconds: 600},
			// europe-west4-a/g2-standard-8 is left out, so it has no recommendation.
		},
		failCells: map[string]bool{"europe-west4-b/g2-standard-8": true},
	}
	s := adviceSyncService(t, prober)

	zones := []string{"europe-west4-a", "europe-west4-b"}
	// The bare-metal shape must never be probed: GCE has no bare-metal Spot
	// VMs, so its probe can only ever fail.
	types := []*machinetype.MachineType{
		{ID: "n1-standard-8"}, {ID: "g2-standard-8"}, {ID: "c4a-highmem-96-metal"},
	}

	cells := s.probeSpotCells(t.Context(), zones, types)
	assert.Len(t, prober.probes, 4, "every zone × VM machine-type cell is probed; bare metal is skipped")
	assert.NotContains(t, prober.probes, "europe-west4-a/c4a-highmem-96-metal")
	require.Len(t, cells, 2, "failed and empty probes drop their cell, not the batch")
	got := map[string]float64{}
	for _, c := range cells {
		got[c.Zone] = c.Obtainability
	}
	assert.InDelta(t, 0.9, got["europe-west4-a"], 1e-9)
	assert.InDelta(t, 0.4, got["europe-west4-b"], 1e-9)
}

// The constructor refuses a service whose probe cannot run, so a half-wired
// deployment fails at startup instead of running a loop that does nothing.
func TestNewRefusesWithoutEveryDep(t *testing.T) {
	t.Parallel()
	_, err := New(Config{ProjectID: "test-project", SpotAdviceSyncInterval: time.Minute},
		slog.Default(), WithSpotAdvice(&fakeProber{}, nil, nil, nil))
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "SpotAdvice", cfgErr.Field, "the prober alone must not enable the loop")
}

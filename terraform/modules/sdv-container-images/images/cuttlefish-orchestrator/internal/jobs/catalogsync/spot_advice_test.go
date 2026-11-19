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

type recordingAdviceStore struct {
	upserted []*gcecatalog.SpotAdvice
	err      error
}

func (s *recordingAdviceStore) UpsertCells(_ context.Context, cells []*gcecatalog.SpotAdvice) error {
	if s.err != nil {
		return s.err
	}
	s.upserted = append(s.upserted, cells...)
	return nil
}

func (s *recordingAdviceStore) ListCells(context.Context) ([]*gcecatalog.SpotAdvice, error) {
	return s.upserted, nil
}

type stubMachineTypes struct {
	types      []*machinetype.MachineType
	err        error
	lastFilter machinetype.ListFilter
}

func (s *stubMachineTypes) List(_ context.Context, filter machinetype.ListFilter) ([]*machinetype.MachineType, error) {
	s.lastFilter = filter
	if s.err != nil {
		return nil, s.err
	}
	return s.types, nil
}

type stubEnabledZones struct {
	zones []string
	err   error
}

func (s *stubEnabledZones) ListEnabledZones(context.Context) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.zones, nil
}

func TestSyncSpotAdvice(t *testing.T) {
	t.Parallel()

	t.Run("happy path probes enabled cells and upserts to store", func(t *testing.T) {
		prober := &fakeProber{
			cells: map[string]*gcecatalog.SpotAdvice{
				"europe-west4-a/n1-standard-8": {
					Zone:              "europe-west4-a",
					MachineType:       "n1-standard-8",
					Obtainability:     0.85,
					UptimeTierSeconds: 7200,
				},
			},
		}
		store := &recordingAdviceStore{}
		mt := &stubMachineTypes{
			types: []*machinetype.MachineType{
				{ID: "n1-standard-8"},
				{ID: "c4a-highmem-96-metal"},
			},
		}
		zones := &stubEnabledZones{zones: []string{"europe-west4-a"}}

		s, err := New(Config{
			ProjectID:              "test-project",
			SpotAdviceSyncInterval: time.Minute,
		}, slog.Default(), WithSpotAdvice(prober, store, mt, zones))
		require.NoError(t, err)
		assert.True(t, s.spotAdviceConfigured())

		require.NoError(t, s.syncSpotAdvice(t.Context()))
		require.NotNil(t, mt.lastFilter.IsEnabled)
		assert.True(t, *mt.lastFilter.IsEnabled)
		assert.Equal(t, MaxProbedShapes, mt.lastFilter.Limit)
		require.Len(t, store.upserted, 1)
		assert.Equal(t, "europe-west4-a", store.upserted[0].Zone)
		assert.Equal(t, "n1-standard-8", store.upserted[0].MachineType)
		assert.InDelta(t, 0.85, store.upserted[0].Obtainability, 1e-9)
	})

	t.Run("zones error aborts before listing machine types or storing", func(t *testing.T) {
		store := &recordingAdviceStore{}
		mt := &stubMachineTypes{types: []*machinetype.MachineType{{ID: "n1-standard-8"}}}
		zones := &stubEnabledZones{err: errors.New("zones query failed")}

		s, err := New(Config{
			ProjectID:              "test-project",
			SpotAdviceSyncInterval: time.Minute,
		}, slog.Default(), WithSpotAdvice(&fakeProber{}, store, mt, zones))
		require.NoError(t, err)

		err = s.syncSpotAdvice(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "listing enabled zones")
		assert.Empty(t, store.upserted)
		assert.Zero(t, mt.lastFilter.Limit)
	})

	t.Run("machineTypes error aborts before storing", func(t *testing.T) {
		store := &recordingAdviceStore{}
		mt := &stubMachineTypes{err: errors.New("catalog unavailable")}
		zones := &stubEnabledZones{zones: []string{"europe-west4-a"}}

		s, err := New(Config{
			ProjectID:              "test-project",
			SpotAdviceSyncInterval: time.Minute,
		}, slog.Default(), WithSpotAdvice(&fakeProber{}, store, mt, zones))
		require.NoError(t, err)

		err = s.syncSpotAdvice(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "listing machine types")
		assert.Empty(t, store.upserted)
	})

	t.Run("store error returns contextual error", func(t *testing.T) {
		prober := &fakeProber{
			cells: map[string]*gcecatalog.SpotAdvice{
				"europe-west4-a/n1-standard-8": {Zone: "europe-west4-a", MachineType: "n1-standard-8", Obtainability: 0.7},
			},
		}
		store := &recordingAdviceStore{err: errors.New("spanner write aborted")}
		mt := &stubMachineTypes{types: []*machinetype.MachineType{{ID: "n1-standard-8"}}}
		zones := &stubEnabledZones{zones: []string{"europe-west4-a"}}

		s, err := New(Config{
			ProjectID:              "test-project",
			SpotAdviceSyncInterval: time.Minute,
		}, slog.Default(), WithSpotAdvice(prober, store, mt, zones))
		require.NoError(t, err)

		err = s.syncSpotAdvice(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "storing spot advice")
	})

	t.Run("spotAdviceConfigured is false when unwired and New rejects unwired dependencies", func(t *testing.T) {
		assert.False(t, (&Service{}).spotAdviceConfigured())

		_, err := New(Config{ProjectID: "test-project", SpotAdviceSyncInterval: time.Minute}, slog.Default())
		var cfgErr *ConfigError
		require.ErrorAs(t, err, &cfgErr)
		assert.Equal(t, "SpotAdvice", cfgErr.Field)
	})
}

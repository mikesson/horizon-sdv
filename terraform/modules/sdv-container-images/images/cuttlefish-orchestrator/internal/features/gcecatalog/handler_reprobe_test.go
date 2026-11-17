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
	"context"
	"errors"
	"log/slog"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

type recordingProber struct {
	calls  []string
	advice *gcecatalog.SpotAdvice
	err    error
}

func (p *recordingProber) ProbeCell(_ context.Context, region, zone, machineType string) (*gcecatalog.SpotAdvice, error) {
	p.calls = append(p.calls, region+"|"+zone+"|"+machineType)
	return p.advice, p.err
}

type recordingAdviceStore struct {
	cells []*gcecatalog.SpotAdvice
	err   error
}

func (s *recordingAdviceStore) UpsertCells(_ context.Context, cells []*gcecatalog.SpotAdvice) error {
	if s.err != nil {
		return s.err
	}
	s.cells = append(s.cells, cells...)
	return nil
}

func (s *recordingAdviceStore) ListCells(context.Context) ([]*gcecatalog.SpotAdvice, error) {
	return s.cells, nil
}

func quietLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// The handler probes exactly the cell it was given, with the region derived
// from the zone so the task carries no third field to disagree with, and stores
// the one row that comes back.
func TestReprobeAdviceRefreshesTheNamedCell(t *testing.T) {
	t.Parallel()
	prober := &recordingProber{advice: &gcecatalog.SpotAdvice{
		Zone: "europe-west4-b", MachineType: "n1-standard-8", Obtainability: 0.15,
		UptimeTierSeconds: gcecatalog.SpotUptimeTierMinute,
	}}
	store := &recordingAdviceStore{}
	h := NewReprobeAdviceHandler(prober, store, quietLogger())

	err := h.Handle(t.Context(), task.New(h.TaskType(), "europe-west4-b/n1-standard-8"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(prober.calls) != 1 || prober.calls[0] != "europe-west4|europe-west4-b|n1-standard-8" {
		t.Errorf("probe calls = %v, want one call for the named cell", prober.calls)
	}
	if len(store.cells) != 1 || store.cells[0].Obtainability != 0.15 {
		t.Errorf("stored cells = %+v, want the freshly probed one", store.cells)
	}
}

// A probe with no recommendation leaves the stored row alone: "the API had
// nothing to say" and "this cell was never measured" rank differently, and
// deleting the row would turn the first into the second.
func TestReprobeAdviceKeepsTheRowWhenThereIsNoRecommendation(t *testing.T) {
	t.Parallel()
	store := &recordingAdviceStore{}
	h := NewReprobeAdviceHandler(&recordingProber{}, store, quietLogger())

	if err := h.Handle(t.Context(), task.New(h.TaskType(), "europe-west4-b/n1-standard-8")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(store.cells) != 0 {
		t.Errorf("stored cells = %+v, want none written", store.cells)
	}
}

// Bare metal is never Spot capacity, and the advice API refuses the request
// rather than answering it, so the handler must not spend one.
func TestReprobeAdviceSkipsBareMetal(t *testing.T) {
	t.Parallel()
	prober := &recordingProber{}
	h := NewReprobeAdviceHandler(prober, &recordingAdviceStore{}, quietLogger())

	if err := h.Handle(t.Context(), task.New(h.TaskType(), "europe-west4-b/c4a-standard-4-metal")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(prober.calls) != 0 {
		t.Errorf("probe calls = %v, want none for a bare-metal shape", prober.calls)
	}
}

// A malformed resource id fails rather than probing something invented from
// half of it.
func TestReprobeAdviceRejectsAMalformedCellKey(t *testing.T) {
	t.Parallel()
	prober := &recordingProber{}
	h := NewReprobeAdviceHandler(prober, &recordingAdviceStore{}, quietLogger())

	if err := h.Handle(t.Context(), task.New(h.TaskType(), "europe-west4-b")); err == nil {
		t.Fatal("Handle accepted a resource id with no machine type")
	}
	if len(prober.calls) != 0 {
		t.Errorf("probe calls = %v, want none", prober.calls)
	}
}

// A failed probe is a failed task: the queue's retry is the whole recovery
// here, and swallowing the error would leave ranking on the stale score the
// re-probe exists to replace.
func TestReprobeAdviceSurfacesProbeAndStoreFailures(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	h := NewReprobeAdviceHandler(&recordingProber{err: boom}, &recordingAdviceStore{}, quietLogger())
	if err := h.Handle(t.Context(), task.New(h.TaskType(), "europe-west4-b/n1-standard-8")); !errors.Is(err, boom) {
		t.Errorf("probe failure = %v, want the cause preserved", err)
	}

	stored := NewReprobeAdviceHandler(
		&recordingProber{advice: &gcecatalog.SpotAdvice{Zone: "europe-west4-b", MachineType: "n1-standard-8"}},
		&recordingAdviceStore{err: boom}, quietLogger())
	if err := stored.Handle(t.Context(), task.New(h.TaskType(), "europe-west4-b/n1-standard-8")); !errors.Is(err, boom) {
		t.Errorf("store failure = %v, want the cause preserved", err)
	}
}

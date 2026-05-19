// Copyright (c) 2024-2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	"reflect"
	"testing"
)

// TestComputeDependentsFromCatalog verifies the in-memory reverse-dependency index:
//   - only enabled modules (ID assigned and present in state.EnabledModules) contribute edges
//   - hard/soft edges are recorded separately
//   - self-edges are skipped
//   - output slices are sorted and deterministic
func TestComputeDependentsFromCatalog(t *testing.T) {
	entries := []CatalogEntry{
		{Name: "sample-hard"},
		{Name: "sample-soft"},
		{
			Name:             "sample",
			HardDependencies: []string{"sample-hard"},
			SoftDependencies: []string{"sample-soft"},
		},
		{
			Name:             "other",
			SoftDependencies: []string{"sample-soft"},
		},
		{
			Name:             "orphan-sample",
			HardDependencies: []string{"sample-hard"},
		},
	}

	state := &State{
		EnabledModules: []string{"id-sample", "id-other", "id-sample-hard", "id-sample-soft"},
		ModuleIDs: map[string]string{
			"sample":        "id-sample",
			"other":         "id-other",
			"sample-hard":   "id-sample-hard",
			"sample-soft":   "id-sample-soft",
			"orphan-sample": "id-orphan",
		},
	}

	index := ComputeDependentsFromCatalog(entries, state)

	if rd := index["sample-hard"]; rd == nil || !reflect.DeepEqual(rd.Hard, []string{"sample"}) {
		t.Fatalf("sample-hard hard dependents: got %+v want [sample]", rd)
	}
	if rd := index["sample-hard"]; rd == nil || len(rd.Soft) != 0 {
		t.Fatalf("sample-hard soft dependents: got %+v want []", rd)
	}

	if rd := index["sample-soft"]; rd == nil || !reflect.DeepEqual(rd.Soft, []string{"other", "sample"}) {
		t.Fatalf("sample-soft soft dependents: got %+v want [other sample]", rd)
	}

	if rd, ok := index["sample"]; !ok || rd == nil || len(rd.Hard) != 0 || len(rd.Soft) != 0 {
		t.Fatalf("sample should exist with empty dependents, got %+v ok=%v", rd, ok)
	}
}

func TestComputeDependentsFromCatalog_SkipsDisabled(t *testing.T) {
	entries := []CatalogEntry{
		{Name: "sample-hard"},
		{Name: "sample", HardDependencies: []string{"sample-hard"}},
	}

	state := &State{
		EnabledModules: []string{"id-sample-hard"},
		ModuleIDs: map[string]string{
			"sample":      "id-sample",
			"sample-hard": "id-sample-hard",
		},
	}

	index := ComputeDependentsFromCatalog(entries, state)
	if rd := index["sample-hard"]; rd == nil || len(rd.Hard) != 0 {
		t.Fatalf("disabled parent must not register as dependent, got %+v", rd)
	}
}

func TestComputeDependentsFromCatalog_SkipsSelfEdges(t *testing.T) {
	entries := []CatalogEntry{
		{Name: "a", HardDependencies: []string{"a"}, SoftDependencies: []string{"a"}},
	}
	state := &State{
		EnabledModules: []string{"id-a"},
		ModuleIDs:      map[string]string{"a": "id-a"},
	}
	index := ComputeDependentsFromCatalog(entries, state)
	if rd := index["a"]; rd == nil || len(rd.Hard) != 0 || len(rd.Soft) != 0 {
		t.Fatalf("self-edges must be skipped, got %+v", rd)
	}
}

func TestComputeDependentsFromCatalog_NilState(t *testing.T) {
	entries := []CatalogEntry{{Name: "a"}, {Name: "b", HardDependencies: []string{"a"}}}
	index := ComputeDependentsFromCatalog(entries, nil)
	if rd, ok := index["a"]; !ok || rd == nil || len(rd.Hard) != 0 {
		t.Fatalf("nil state: expected empty dependents slot for a, got %+v ok=%v", rd, ok)
	}
}

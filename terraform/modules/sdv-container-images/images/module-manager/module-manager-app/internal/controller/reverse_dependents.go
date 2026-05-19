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
	"context"
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReverseDependents contains enabled reverse edges for a target module.
type ReverseDependents struct {
	Hard []string
	Soft []string
}

// DependentsIndex maps module name to its enabled reverse dependents.
type DependentsIndex map[string]*ReverseDependents

// ComputeDependentsFromCatalog builds a reverse-edge index from catalog entries and enabled state.
// A module entry E contributes to Dependents[D].Hard iff E is enabled and D in E.HardDependencies,
// and analogously for Soft. Returned slices are sorted and never contain E==D self-edges.
func ComputeDependentsFromCatalog(entries []CatalogEntry, state *State) DependentsIndex {
	out := make(DependentsIndex, len(entries))
	for _, e := range entries {
		out[e.Name] = &ReverseDependents{}
	}
	if state == nil {
		return out
	}
	enabledSet := make(map[string]bool, len(state.EnabledModules))
	for _, id := range state.EnabledModules {
		enabledSet[id] = true
	}
	for _, e := range entries {
		id := state.ModuleIDs[e.Name]
		if id == "" || !enabledSet[id] {
			continue
		}
		for _, dep := range e.HardDependencies {
			if dep == e.Name {
				continue
			}
			if rd, ok := out[dep]; ok {
				rd.Hard = append(rd.Hard, e.Name)
			} else {
				out[dep] = &ReverseDependents{Hard: []string{e.Name}}
			}
		}
		for _, dep := range e.SoftDependencies {
			if dep == e.Name {
				continue
			}
			if rd, ok := out[dep]; ok {
				rd.Soft = append(rd.Soft, e.Name)
			} else {
				out[dep] = &ReverseDependents{Soft: []string{e.Name}}
			}
		}
	}
	for _, rd := range out {
		sort.Strings(rd.Hard)
		sort.Strings(rd.Soft)
	}
	return out
}

// ListEnabledReverseDependents returns enabled module names that reference targetModule
// as either a hard dependency or a soft dependency. Data is sourced from ModuleCatalog
// (desired) and ModuleManagerState (runtime); c and mmNamespace are retained for call-site
// compatibility but no longer used.
func ListEnabledReverseDependents(ctx context.Context, c client.Client, catalogStore CatalogStoreInterface, mmNamespace string, state *State, targetModule string) (*ReverseDependents, error) {
	_ = c
	_ = mmNamespace
	out := &ReverseDependents{}
	if state == nil {
		return out, nil
	}
	entries, err := catalogStore.List(ctx)
	if err != nil {
		return nil, err
	}
	index := ComputeDependentsFromCatalog(entries, state)
	if rd, ok := index[targetModule]; ok && rd != nil {
		out.Hard = append([]string(nil), rd.Hard...)
		out.Soft = append([]string(nil), rd.Soft...)
	}
	return out, nil
}

// ListHardDependents returns enabled module names that declare targetModule as a hard dependency.
func ListHardDependents(ctx context.Context, c client.Client, catalogStore CatalogStoreInterface, mmNamespace string, state *State, targetModule string) ([]string, error) {
	edges, err := ListEnabledReverseDependents(ctx, c, catalogStore, mmNamespace, state, targetModule)
	if err != nil {
		return nil, err
	}
	return edges.Hard, nil
}

// ListSoftDependents returns enabled module names that declare targetModule as a soft dependency.
func ListSoftDependents(ctx context.Context, c client.Client, catalogStore CatalogStoreInterface, mmNamespace string, state *State, targetModule string) ([]string, error) {
	edges, err := ListEnabledReverseDependents(ctx, c, catalogStore, mmNamespace, state, targetModule)
	if err != nil {
		return nil, err
	}
	return edges.Soft, nil
}

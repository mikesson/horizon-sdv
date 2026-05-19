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
	"errors"
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// autoDisableMaxWaves caps cascade sweeps to avoid pathological loops; practical cascades are <=2.
const autoDisableMaxWaves = 8

// RunAutoDisableSweep performs a catalog-driven auto-disable sweep: any enabled catalog
// entry with autoDisableWhenUnused=true and zero enabled hard+soft dependents is disabled.
// The sweep cascades until steady state (no more candidates disable in a wave).
func RunAutoDisableSweep(ctx context.Context, apiReader client.Reader, c client.Client, mmNamespace, argocdNamespace string, stateStore StateStoreInterface, catalogStore CatalogStoreInterface) error {
	_, err := runAutoDisableSweep(ctx, apiReader, c, mmNamespace, argocdNamespace, stateStore, catalogStore)
	return err
}

// runAutoDisableSweep iteratively disables unused opted-in modules until none qualify.
// Returns the list of modules disabled during the sweep (across all waves).
func runAutoDisableSweep(ctx context.Context, apiReader client.Reader, c client.Client, mmNamespace, argocdNamespace string, stateStore StateStoreInterface, catalogStore CatalogStoreInterface) ([]string, error) {
	logger := log.FromContext(ctx)
	var allDisabled []string
	var waveErrs []error
	for wave := 0; wave < autoDisableMaxWaves; wave++ {
		disabled, err := autoDisableOneWave(ctx, apiReader, c, mmNamespace, argocdNamespace, stateStore, catalogStore)
		if err != nil {
			waveErrs = append(waveErrs, err)
		}
		if len(disabled) == 0 {
			break
		}
		allDisabled = append(allDisabled, disabled...)
	}
	if len(allDisabled) > 0 {
		logger.Info("auto-disable sweep complete", "disabledModules", allDisabled)
	}
	if len(waveErrs) > 0 {
		return allDisabled, errors.Join(waveErrs...)
	}
	return allDisabled, nil
}

// autoDisableOneWave runs a single sweep over the catalog, disabling each eligible module.
// Eligibility: entry.AutoDisableWhenUnused=true, module currently enabled, zero enabled
// hard and soft dependents. Returns names disabled this wave, sorted.
func autoDisableOneWave(ctx context.Context, apiReader client.Reader, c client.Client, mmNamespace, argocdNamespace string, stateStore StateStoreInterface, catalogStore CatalogStoreInterface) ([]string, error) {
	logger := log.FromContext(ctx)
	state, err := stateStore.Get(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := catalogStore.List(ctx)
	if err != nil {
		return nil, err
	}
	enabledSet := make(map[string]bool, len(state.EnabledModules))
	for _, id := range state.EnabledModules {
		enabledSet[id] = true
	}
	index := ComputeDependentsFromCatalog(entries, state)

	type candidate struct {
		name     string
		moduleID string
	}
	var candidates []candidate
	for i := range entries {
		e := &entries[i]
		if !e.AutoDisableWhenUnused {
			continue
		}
		id := state.ModuleIDs[e.Name]
		if id == "" || !enabledSet[id] {
			continue
		}
		rd := index[e.Name]
		if rd != nil && (len(rd.Hard) > 0 || len(rd.Soft) > 0) {
			continue
		}
		candidates = append(candidates, candidate{name: e.Name, moduleID: id})
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].name < candidates[j].name })

	var disabled []string
	var candErrs []error
	for _, cand := range candidates {
		state, err := stateStore.Get(ctx)
		if err != nil {
			return disabled, err
		}
		edges, err := ListEnabledReverseDependents(ctx, c, catalogStore, mmNamespace, state, cand.name)
		if err != nil {
			return disabled, err
		}
		if len(edges.Hard) > 0 || len(edges.Soft) > 0 {
			logger.Info("skipping auto-disable because dependents reappeared", "module", cand.name, "hardDependents", edges.Hard, "softDependents", edges.Soft)
			continue
		}
		logger.Info("attempting auto-disable", "module", cand.name, "moduleID", cand.moduleID)
		if err := PerformModuleDisable(ctx, c, stateStore, catalogStore, argocdNamespace, mmNamespace, cand.name, cand.moduleID, false); err != nil {
			logger.Error(err, "auto-disable failed", "module", cand.name, "moduleID", cand.moduleID)
			candErrs = append(candErrs, err)
			continue
		}
		disabled = append(disabled, cand.name)
		if err := ResyncSoftFeaturesForParentsOfSoftDep(ctx, apiReader, c, argocdNamespace, mmNamespace, stateStore, catalogStore, cand.name); err != nil {
			logger.Error(err, "resync soft features after auto-disable", "module", cand.name)
			candErrs = append(candErrs, err)
		}
	}
	if len(candErrs) > 0 {
		return disabled, errors.Join(candErrs...)
	}
	return disabled, nil
}

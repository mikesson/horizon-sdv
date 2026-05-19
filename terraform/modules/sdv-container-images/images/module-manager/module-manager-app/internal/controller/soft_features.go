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
	"fmt"
	"sort"
	"strings"

	horizonv1alpha1 "github.com/acn-horizon-sdv/module-manager/api/v1alpha1"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// SoftFeaturesHelmKey is the Helm values key for per-soft-dependency flags (map moduleName -> bool).
	SoftFeaturesHelmKey      = "softFeaturesEnabled"
	legacySoftFeatureHelmKey = "softFeatureEnabled"
)

// ComputeSoftFeaturesEnabledMap returns, for parent module P, a map from each soft dependency name to whether that module is enabled.
func ComputeSoftFeaturesEnabledMap(ctx context.Context, c client.Client, mmNamespace string, state *State, catalogStore CatalogStoreInterface, parentModuleName string) (map[string]bool, error) {
	softDeps, err := softDepsForParent(ctx, c, catalogStore, mmNamespace, parentModuleName)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(softDeps))
	enabledSet := make(map[string]bool)
	for _, id := range state.EnabledModules {
		enabledSet[id] = true
	}
	for _, s := range softDeps {
		out[s] = moduleNameEnabledInState(ctx, c, mmNamespace, state, enabledSet, s)
	}
	return out, nil
}

func softDepsForParent(ctx context.Context, c client.Client, catalogStore CatalogStoreInterface, mmNamespace, parentModuleName string) ([]string, error) {
	_ = c
	_ = mmNamespace
	entries, err := catalogStore.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].Name == parentModuleName {
			return append([]string(nil), entries[i].SoftDependencies...), nil
		}
	}
	return nil, nil
}

func moduleNameEnabledInState(ctx context.Context, c client.Client, mmNamespace string, state *State, enabledSet map[string]bool, moduleName string) bool {
	_ = ctx
	_ = c
	_ = mmNamespace
	if state == nil {
		return false
	}
	id := state.ModuleIDs[moduleName]
	return id != "" && enabledSet[id]
}

// CollectEnabledParentsWithSoftDependency returns enabled module names that list softDepName as a soft dependency (registration and catalog).
func CollectEnabledParentsWithSoftDependency(ctx context.Context, c client.Client, catalogStore CatalogStoreInterface, mmNamespace string, state *State, softDepName string) ([]string, error) {
	return ListSoftDependents(ctx, c, catalogStore, mmNamespace, state, softDepName)
}

// PatchApplicationHelmSoftFeaturesMap merges softFeaturesEnabled into spec.source.helm.values YAML and removes legacy softFeatureEnabled.
func PatchApplicationHelmSoftFeaturesMap(ctx context.Context, apiReader client.Reader, writer client.Client, argoNamespace, appName string, features map[string]bool) error {
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	key := client.ObjectKey{Namespace: argoNamespace, Name: appName}
	if err := apiReader.Get(ctx, key, app); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	spec, ok, _ := unstructured.NestedMap(app.Object, "spec")
	if !ok || spec == nil {
		return fmt.Errorf("application %s/%s: no spec", argoNamespace, appName)
	}
	source, _ := spec["source"].(map[string]interface{})
	if source == nil {
		source = make(map[string]interface{})
		spec["source"] = source
	}
	helm, _ := source["helm"].(map[string]interface{})
	if helm == nil {
		helm = make(map[string]interface{})
		source["helm"] = helm
	}
	valuesStr, _ := helm["values"].(string)

	var root map[string]interface{}
	if strings.TrimSpace(valuesStr) != "" {
		if err := yaml.Unmarshal([]byte(valuesStr), &root); err != nil {
			return fmt.Errorf("parse helm values for %s: %w", appName, err)
		}
	}
	if root == nil {
		root = make(map[string]interface{})
	}
	delete(root, legacySoftFeatureHelmKey)
	nested := make(map[string]interface{}, len(features))
	keys := make([]string, 0, len(features))
	for k := range features {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		nested[k] = features[k]
	}
	root[SoftFeaturesHelmKey] = nested

	out, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshal helm values for %s: %w", appName, err)
	}
	helm["values"] = string(out)
	source["helm"] = helm
	spec["source"] = source
	if err := unstructured.SetNestedMap(app.Object, spec, "spec"); err != nil {
		return err
	}
	return writer.Update(ctx, app)
}

// RemoveSoftFeaturesFromApplicationHelm deletes softFeaturesEnabled (and legacy softFeatureEnabled) from spec.source.helm.values.
func RemoveSoftFeaturesFromApplicationHelm(ctx context.Context, apiReader client.Reader, writer client.Client, argoNamespace, appName string) error {
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	key := client.ObjectKey{Namespace: argoNamespace, Name: appName}
	if err := apiReader.Get(ctx, key, app); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	spec, ok, _ := unstructured.NestedMap(app.Object, "spec")
	if !ok || spec == nil {
		return fmt.Errorf("application %s/%s: no spec", argoNamespace, appName)
	}
	source, _ := spec["source"].(map[string]interface{})
	if source == nil {
		return nil
	}
	helm, _ := source["helm"].(map[string]interface{})
	if helm == nil {
		return nil
	}
	valuesStr, _ := helm["values"].(string)
	var root map[string]interface{}
	if strings.TrimSpace(valuesStr) != "" {
		if err := yaml.Unmarshal([]byte(valuesStr), &root); err != nil {
			return fmt.Errorf("parse helm values for %s: %w", appName, err)
		}
	}
	if root == nil {
		return nil
	}
	delete(root, legacySoftFeatureHelmKey)
	delete(root, SoftFeaturesHelmKey)
	out, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshal helm values for %s: %w", appName, err)
	}
	helm["values"] = string(out)
	source["helm"] = helm
	spec["source"] = source
	if err := unstructured.SetNestedMap(app.Object, spec, "spec"); err != nil {
		return err
	}
	return writer.Update(ctx, app)
}

// SyncSoftFeaturesForModule updates soft-feature state for a parent module per ModuleCatalog (Helm values, ConfigMap, or both).
func SyncSoftFeaturesForModule(ctx context.Context, apiReader client.Reader, writer client.Client, argoNS, mmNS string, stateStore StateStoreInterface, catalogStore CatalogStoreInterface, parentModuleName string) error {
	state, err := stateStore.Get(ctx)
	if err != nil {
		return err
	}
	m, err := ComputeSoftFeaturesEnabledMap(ctx, writer, mmNS, state, catalogStore, parentModuleName)
	if err != nil {
		return err
	}
	if m == nil {
		m = map[string]bool{}
	}
	entry, err := catalogEntryForModule(ctx, catalogStore, parentModuleName)
	if err != nil {
		return err
	}
	mode := effectiveSoftFeaturesPropagation(entry)
	appName := ApplicationName(parentModuleName)

	switch mode {
	case horizonv1alpha1.SoftFeaturesPropagationConfigMap:
		if len(entry.SoftFeaturesConfigMapNamespaces) == 0 {
			return fmt.Errorf("catalog module %q: softFeaturesConfigMapNamespaces is required when softFeaturesPropagation is ConfigMap", parentModuleName)
		}
		if err := RemoveSoftFeaturesFromApplicationHelm(ctx, apiReader, writer, argoNS, appName); err != nil {
			return err
		}
		return EnsureSoftFeaturesConfigMaps(ctx, apiReader, writer, entry.SoftFeaturesConfigMapNamespaces, parentModuleName, m)
	case horizonv1alpha1.SoftFeaturesPropagationHelmValuesAndConfigMap:
		if len(entry.SoftFeaturesConfigMapNamespaces) == 0 {
			return fmt.Errorf("catalog module %q: softFeaturesConfigMapNamespaces is required when softFeaturesPropagation is HelmValuesAndConfigMap", parentModuleName)
		}
		if err := PatchApplicationHelmSoftFeaturesMap(ctx, apiReader, writer, argoNS, appName, m); err != nil {
			return err
		}
		return EnsureSoftFeaturesConfigMaps(ctx, apiReader, writer, entry.SoftFeaturesConfigMapNamespaces, parentModuleName, m)
	default:
		return PatchApplicationHelmSoftFeaturesMap(ctx, apiReader, writer, argoNS, appName, m)
	}
}

// ResyncSoftFeaturesForParentsOfSoftDep recomputes softFeaturesEnabled for every enabled parent that lists toggledModule as a soft dependency.
func ResyncSoftFeaturesForParentsOfSoftDep(ctx context.Context, apiReader client.Reader, writer client.Client, argoNS, mmNS string, stateStore StateStoreInterface, catalogStore CatalogStoreInterface, toggledModule string) error {
	state, err := stateStore.Get(ctx)
	if err != nil {
		return err
	}
	parents, err := CollectEnabledParentsWithSoftDependency(ctx, writer, catalogStore, mmNS, state, toggledModule)
	if err != nil {
		return err
	}
	var firstErr error
	for _, p := range parents {
		if err := SyncSoftFeaturesForModule(ctx, apiReader, writer, argoNS, mmNS, stateStore, catalogStore, p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SyncSoftFeaturesForAllEnabledModulesWithSoftDeps runs SyncSoftFeaturesForModule for every enabled catalog
// module that declares softDependencies (parents). Call after ModuleCatalog changes so ConfigMap / Helm
// propagation matches the catalog without requiring a soft-dependency toggle.
func SyncSoftFeaturesForAllEnabledModulesWithSoftDeps(ctx context.Context, apiReader client.Reader, writer client.Client, argoNS, mmNS string, stateStore StateStoreInterface, catalogStore CatalogStoreInterface) error {
	state, err := stateStore.Get(ctx)
	if err != nil {
		return err
	}
	entries, err := catalogStore.List(ctx)
	if err != nil {
		return err
	}
	enabledID := make(map[string]bool, len(state.EnabledModules))
	for _, id := range state.EnabledModules {
		enabledID[id] = true
	}
	var firstErr error
	for _, e := range entries {
		if len(e.SoftDependencies) == 0 {
			continue
		}
		id := state.ModuleIDs[e.Name]
		if id == "" || !enabledID[id] {
			continue
		}
		if err := SyncSoftFeaturesForModule(ctx, apiReader, writer, argoNS, mmNS, stateStore, catalogStore, e.Name); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

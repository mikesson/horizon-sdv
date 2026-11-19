// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"errors"
	"slices"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestPerformModuleDisable(t *testing.T) {
	t.Run("rejects_empty_moduleName_or_moduleID", func(t *testing.T) {
		c := newControllerTestClient(t, interceptor.Funcs{})
		state := &sfMemStateStore{}
		cat := &sfMemCatalogStore{}

		if err := PerformModuleDisable(t.Context(), c, state, cat, "argocd", "mm", "", "id-1", true, ""); !errContains(err, "required") {
			t.Fatalf("expected error for empty moduleName, got %v", err)
		}
		if err := PerformModuleDisable(t.Context(), c, state, cat, "argocd", "mm", "sample", "", true, ""); !errContains(err, "required") {
			t.Fatalf("expected error for empty moduleID, got %v", err)
		}
	})

	t.Run("blocks_when_enforceNoHardDependents_is_true_and_dependent_enabled", func(t *testing.T) {
		c := newControllerTestClient(t, interceptor.Funcs{})
		state := &sfMemStateStore{
			state: &State{
				EnabledModules: []string{"id-base", "id-consumer"},
				ModuleIDs: map[string]string{
					"base":     "id-base",
					"consumer": "id-consumer",
				},
			},
		}
		cat := &sfMemCatalogStore{
			entries: []CatalogEntry{
				{Name: "base"},
				{Name: "consumer", HardDependencies: []string{"base"}},
			},
		}

		err := PerformModuleDisable(t.Context(), c, state, cat, "argocd", "mm", "base", "id-base", true, "")
		if err == nil || !strings.Contains(err.Error(), "consumer") {
			t.Fatalf("expected hard-dependent refusal mentioning consumer, got %v", err)
		}
		if state.updateCalls != 0 {
			t.Errorf("state.Update should not run when hard dependents block disable, got %d calls", state.updateCalls)
		}
	})

	t.Run("rolls_back_skip_reconcile_when_state_update_fails", func(t *testing.T) {
		parent := appWithHelmValues("argocd", "mod-sample", "a: 1\n")
		c := newControllerTestClient(t, interceptor.Funcs{}, parent)
		state := &sfMemStateStore{
			state: &State{
				EnabledModules:        []string{"id-sample"},
				ModuleIDs:             map[string]string{"sample": "id-sample"},
				ModuleTargetRevisions: map[string]string{"sample": "main"},
			},
			updateErr: errors.New("state store unavailable"),
		}
		cat := &sfMemCatalogStore{
			entries: []CatalogEntry{{Name: "sample"}},
		}

		err := PerformModuleDisable(t.Context(), c, state, cat, "argocd", "mm", "sample", "id-sample", true, "")
		if !errContains(err, "state store unavailable") {
			t.Fatalf("expected state update error, got %v", err)
		}

		// Parent app should still exist and have skip-reconcile annotation cleared.
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(argoApplicationGVK)
		if getErr := c.Get(t.Context(), client.ObjectKey{Namespace: "argocd", Name: "mod-sample"}, got); getErr != nil {
			t.Fatalf("parent app should still exist after rollback, got %v", getErr)
		}
		if _, ok := got.GetAnnotations()["argocd.argoproj.io/skip-reconcile"]; ok {
			t.Errorf("skip-reconcile annotation should be cleared after state failure, got %v", got.GetAnnotations())
		}
	})

	t.Run("happy_path_updates_state_and_deletes_parent_Application", func(t *testing.T) {
		parent := appWithHelmValues("argocd", "mod-sample", "a: 1\n")
		c := newControllerTestClient(t, interceptor.Funcs{}, parent)
		state := &sfMemStateStore{
			state: &State{
				EnabledModules:        []string{"id-sample", "id-other"},
				ModuleIDs:             map[string]string{"sample": "id-sample", "other": "id-other"},
				ModuleTargetRevisions: map[string]string{"sample": "v1.0.0", "other": "main"},
			},
		}
		cat := &sfMemCatalogStore{
			entries: []CatalogEntry{
				{Name: "sample"},
				{Name: "other"},
			},
		}

		if err := PerformModuleDisable(t.Context(), c, state, cat, "argocd", "mm", "sample", "id-sample", true, ""); err != nil {
			t.Fatalf("PerformModuleDisable failed: %v", err)
		}
		if slices.Contains(state.state.EnabledModules, "id-sample") {
			t.Errorf("id-sample should be removed from EnabledModules: %v", state.state.EnabledModules)
		}
		if _, hasRev := state.state.ModuleTargetRevisions["sample"]; hasRev {
			t.Errorf("sample target revision pin should be cleared: %v", state.state.ModuleTargetRevisions)
		}
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(argoApplicationGVK)
		if err := c.Get(t.Context(), client.ObjectKey{Namespace: "argocd", Name: "mod-sample"}, got); !apierrors.IsNotFound(err) {
			t.Errorf("expected parent mod-sample Application deleted, got err=%v", err)
		}
	})
}

func TestDisableModuleAndRefresh(t *testing.T) {
	t.Run("propagates_PerformModuleDisable_error", func(t *testing.T) {
		c := newControllerTestClient(t, interceptor.Funcs{})
		state := &sfMemStateStore{
			state: &State{
				EnabledModules: []string{"id-base", "id-consumer"},
				ModuleIDs:      map[string]string{"base": "id-base", "consumer": "id-consumer"},
			},
		}
		cat := &sfMemCatalogStore{
			entries: []CatalogEntry{
				{Name: "base"},
				{Name: "consumer", HardDependencies: []string{"base"}},
			},
		}
		err := DisableModuleAndRefresh(t.Context(), c, c, state, cat, "argocd", "mm", "base", "id-base", true, "")
		if err == nil {
			t.Fatal("expected error to propagate from PerformModuleDisable")
		}
	})

	t.Run("cascades_auto_disable_and_resyncs_soft_feature_parents", func(t *testing.T) {
		parentSoft := appWithHelmValues("argocd", "mod-parent", "softFeaturesEnabled:\n  feature: true\n")
		featureApp := appWithHelmValues("argocd", "mod-feature", "")
		unusedApp := appWithHelmValues("argocd", "mod-unused", "")

		c := newControllerTestClient(t, interceptor.Funcs{}, parentSoft, featureApp, unusedApp)
		state := &sfMemStateStore{
			state: &State{
				EnabledModules: []string{"id-parent", "id-feature", "id-unused"},
				ModuleIDs: map[string]string{
					"parent":  "id-parent",
					"feature": "id-feature",
					"unused":  "id-unused",
				},
			},
		}
		cat := &sfMemCatalogStore{
			entries: []CatalogEntry{
				{Name: "parent", SoftDependencies: []string{"feature"}},
				{Name: "feature"},
				{Name: "unused", AutoDisableWhenUnused: true},
			},
		}

		if err := DisableModuleAndRefresh(t.Context(), c, c, state, cat, "argocd", "mm", "feature", "id-feature", true, ""); err != nil {
			t.Fatalf("DisableModuleAndRefresh failed: %v", err)
		}
		if slices.Contains(state.state.EnabledModules, "id-feature") {
			t.Errorf("id-feature should be disabled: %v", state.state.EnabledModules)
		}
		if slices.Contains(state.state.EnabledModules, "id-unused") {
			t.Errorf("id-unused (autoDisableWhenUnused with zero dependents) should be auto-disabled: %v", state.state.EnabledModules)
		}

		// Parent app should have softFeaturesEnabled["feature"] updated to false.
		gotParent := &unstructured.Unstructured{}
		gotParent.SetGroupVersionKind(argoApplicationGVK)
		if err := c.Get(t.Context(), client.ObjectKey{Namespace: "argocd", Name: "mod-parent"}, gotParent); err != nil {
			t.Fatalf("get mod-parent: %v", err)
		}
		vals, _, _ := unstructured.NestedString(gotParent.Object, "spec", "source", "helm", "values")
		if !strings.Contains(vals, "feature: false") {
			t.Errorf("expected parent Helm values to update feature: false, got:\n%s", vals)
		}
	})
}

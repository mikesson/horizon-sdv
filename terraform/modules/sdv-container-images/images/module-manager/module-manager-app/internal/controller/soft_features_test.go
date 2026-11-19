// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type sfMemStateStore struct {
	state       *State
	getErr      error
	updateErr   error
	updateCalls int
	invalidated int
	onUpdate    func(*State) error
}

func (m *sfMemStateStore) Get(context.Context) (*State, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	if m.state == nil {
		return &State{ModuleIDs: map[string]string{}}, nil
	}
	cp := *m.state
	cp.EnabledModules = slices.Clone(m.state.EnabledModules)
	cp.ModuleIDs = maps.Clone(m.state.ModuleIDs)
	cp.ModuleTargetRevisions = maps.Clone(m.state.ModuleTargetRevisions)
	return &cp, nil
}

func (m *sfMemStateStore) Update(_ context.Context, st *State) error {
	m.updateCalls++
	if m.onUpdate != nil {
		if err := m.onUpdate(st); err != nil {
			return err
		}
	}
	if m.updateErr != nil {
		return m.updateErr
	}
	cp := *st
	cp.EnabledModules = slices.Clone(st.EnabledModules)
	cp.ModuleIDs = maps.Clone(st.ModuleIDs)
	cp.ModuleTargetRevisions = maps.Clone(st.ModuleTargetRevisions)
	m.state = &cp
	return nil
}

func (m *sfMemStateStore) InvalidateCache() {
	m.invalidated++
}

type sfMemCatalogStore struct {
	entries []CatalogEntry
	listErr error
}

func (m *sfMemCatalogStore) List(context.Context) ([]CatalogEntry, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return slices.Clone(m.entries), nil
}

func (m *sfMemCatalogStore) GetPath(_ context.Context, name string) (string, error) {
	for _, e := range m.entries {
		if e.Name == name {
			return e.Path, nil
		}
	}
	return "", errors.New("not found: " + name)
}

func errContains(err error, substr string) bool {
	return err != nil && strings.Contains(err.Error(), substr)
}

func newControllerTestClient(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(corev1): %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithInterceptorFuncs(funcs).
		Build()
}

func appWithHelmValues(ns, name, helmValues string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(argoApplicationGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	u.Object["spec"] = map[string]interface{}{
		"source": map[string]interface{}{
			"repoURL": "https://example.invalid/repo.git",
			"helm": map[string]interface{}{
				"values": helmValues,
			},
		},
	}
	return u
}

func withFastSoftFeatureBackoff(t *testing.T, steps int) {
	t.Helper()
	orig := softFeaturesConfigMapRetryBackoff
	softFeaturesConfigMapRetryBackoff = wait.Backoff{
		Duration: time.Millisecond,
		Factor:   1.0,
		Steps:    steps,
	}
	t.Cleanup(func() {
		softFeaturesConfigMapRetryBackoff = orig
	})
}

func TestComputeSoftFeaturesEnabledMapAndHelpers(t *testing.T) {
	c := newControllerTestClient(t, interceptor.Funcs{})
	catalog := &sfMemCatalogStore{
		entries: []CatalogEntry{
			{
				Name:             "parent",
				Path:             "gitops/modules/parent",
				SoftDependencies: []string{"dep-on", "dep-off"},
			},
			{
				Name:             "other-parent",
				Path:             "gitops/modules/other",
				SoftDependencies: []string{"dep-on"},
			},
			{Name: "dep-on", Path: "gitops/modules/dep-on"},
			{Name: "dep-off", Path: "gitops/modules/dep-off"},
		},
	}
	state := &State{
		EnabledModules: []string{"id-parent", "id-dep-on"},
		ModuleIDs: map[string]string{
			"parent":       "id-parent",
			"other-parent": "id-other",
			"dep-on":       "id-dep-on",
			"dep-off":      "id-dep-off",
		},
	}

	gotMap, err := ComputeSoftFeaturesEnabledMap(t.Context(), c, "mm", state, catalog, "parent")
	if err != nil {
		t.Fatalf("ComputeSoftFeaturesEnabledMap failed: %v", err)
	}
	wantMap := map[string]bool{"dep-on": true, "dep-off": false}
	if !maps.Equal(gotMap, wantMap) {
		t.Fatalf("ComputeSoftFeaturesEnabledMap = %v, want %v", gotMap, wantMap)
	}

	if deps, err := softDepsForParent(t.Context(), c, catalog, "mm", "nonexistent"); err != nil || deps != nil {
		t.Fatalf("softDepsForParent(missing) = (%v, %v), want (nil, nil)", deps, err)
	}

	catErr := &sfMemCatalogStore{listErr: errors.New("catalog down")}
	if _, err := softDepsForParent(t.Context(), c, catErr, "mm", "parent"); !errContains(err, "catalog down") {
		t.Fatalf("expected catalog error, got %v", err)
	}

	if moduleNameEnabledInState(t.Context(), c, "mm", nil, map[string]bool{"id-dep-on": true}, "dep-on") {
		t.Error("moduleNameEnabledInState(nil state) should be false")
	}
	if moduleNameEnabledInState(t.Context(), c, "mm", state, map[string]bool{"id-dep-on": true}, "unknown-mod") {
		t.Error("moduleNameEnabledInState(unknown) should be false")
	}
	if moduleNameEnabledInState(t.Context(), c, "mm", state, map[string]bool{"id-dep-on": true}, "dep-off") {
		t.Error("moduleNameEnabledInState(disabled dep-off) should be false")
	}

	parents, err := CollectEnabledParentsWithSoftDependency(t.Context(), c, catalog, "mm", state, "dep-on")
	if err != nil {
		t.Fatalf("CollectEnabledParentsWithSoftDependency failed: %v", err)
	}
	if !slices.Equal(parents, []string{"parent"}) {
		t.Fatalf("CollectEnabledParentsWithSoftDependency = %v, want [parent]", parents)
	}
}

func TestPatchAndRemoveApplicationHelmSoftFeatures(t *testing.T) {
	c := newControllerTestClient(t, interceptor.Funcs{})

	// Missing Application is a no-op for both Patch and Remove.
	if err := PatchApplicationHelmSoftFeaturesMap(t.Context(), c, c, "argocd", "missing", map[string]bool{"a": true}); err != nil {
		t.Fatalf("Patch missing app failed: %v", err)
	}
	if err := RemoveSoftFeaturesFromApplicationHelm(t.Context(), c, c, "argocd", "missing"); err != nil {
		t.Fatalf("Remove missing app failed: %v", err)
	}

	// Missing spec errors on Patch.
	noSpec := &unstructured.Unstructured{}
	noSpec.SetGroupVersionKind(argoApplicationGVK)
	noSpec.SetNamespace("argocd")
	noSpec.SetName("nospec")
	cNoSpec := newControllerTestClient(t, interceptor.Funcs{}, noSpec)
	if err := PatchApplicationHelmSoftFeaturesMap(t.Context(), cNoSpec, cNoSpec, "argocd", "nospec", map[string]bool{"a": true}); !errContains(err, "no spec") {
		t.Fatalf("expected missing spec error, got %v", err)
	}

	// Invalid YAML errors on Patch.
	badYAMLApp := appWithHelmValues("argocd", "bad-yaml", ":\n  - [\nbad")
	cBad := newControllerTestClient(t, interceptor.Funcs{}, badYAMLApp)
	if err := PatchApplicationHelmSoftFeaturesMap(t.Context(), cBad, cBad, "argocd", "bad-yaml", map[string]bool{"a": true}); err == nil {
		t.Fatal("expected invalid YAML error on Patch")
	}

	// Happy path: preserves existing keys, drops legacy key, sets new map; then Remove cleans both keys.
	validApp := appWithHelmValues("argocd", "mod-parent", "replicaCount: 2\nsoftFeatureEnabled: true\n")
	cValid := newControllerTestClient(t, interceptor.Funcs{}, validApp)
	if err := PatchApplicationHelmSoftFeaturesMap(t.Context(), cValid, cValid, "argocd", "mod-parent", map[string]bool{"dep-on": true, "dep-off": false}); err != nil {
		t.Fatalf("Patch failed: %v", err)
	}

	gotApp := &unstructured.Unstructured{}
	gotApp.SetGroupVersionKind(argoApplicationGVK)
	if err := cValid.Get(t.Context(), client.ObjectKey{Namespace: "argocd", Name: "mod-parent"}, gotApp); err != nil {
		t.Fatalf("Get patched app: %v", err)
	}
	vals, _, _ := unstructured.NestedString(gotApp.Object, "spec", "source", "helm", "values")
	parsed := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(vals), &parsed); err != nil {
		t.Fatalf("unmarshal patched values: %v", err)
	}
	if _, legacyPresent := parsed[legacySoftFeatureHelmKey]; legacyPresent {
		t.Errorf("legacy %q key should have been removed: %v", legacySoftFeatureHelmKey, parsed)
	}
	if parsed["replicaCount"] != 2 {
		t.Errorf("replicaCount should be preserved, got %v", parsed["replicaCount"])
	}
	sfRaw, ok := parsed[SoftFeaturesHelmKey].(map[string]interface{})
	if !ok || sfRaw["dep-on"] != true || sfRaw["dep-off"] != false {
		t.Errorf("unexpected %q map: %#v", SoftFeaturesHelmKey, parsed[SoftFeaturesHelmKey])
	}

	if err := RemoveSoftFeaturesFromApplicationHelm(t.Context(), cValid, cValid, "argocd", "mod-parent"); err != nil {
		t.Fatalf("RemoveSoftFeaturesFromApplicationHelm failed: %v", err)
	}
	if err := cValid.Get(t.Context(), client.ObjectKey{Namespace: "argocd", Name: "mod-parent"}, gotApp); err != nil {
		t.Fatalf("Get after remove: %v", err)
	}
	valsAfter, _, _ := unstructured.NestedString(gotApp.Object, "spec", "source", "helm", "values")
	parsedAfter := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(valsAfter), &parsedAfter); err != nil {
		t.Fatalf("unmarshal after remove: %v", err)
	}
	if _, present := parsedAfter[SoftFeaturesHelmKey]; present {
		t.Errorf("%q should be removed after RemoveSoftFeaturesFromApplicationHelm", SoftFeaturesHelmKey)
	}
	if parsedAfter["replicaCount"] != 2 {
		t.Errorf("replicaCount must still remain after remove, got %v", parsedAfter["replicaCount"])
	}
}

func TestSyncSoftFeaturesModesAndBulkSync(t *testing.T) {
	nsTarget := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app-ns"}}

	t.Run("ConfigMap_without_namespaces_returns_error", func(t *testing.T) {
		c := newControllerTestClient(t, interceptor.Funcs{})
		state := &sfMemStateStore{state: &State{
			EnabledModules: []string{"id-p"},
			ModuleIDs:      map[string]string{"p": "id-p"},
		}}
		cat := &sfMemCatalogStore{entries: []CatalogEntry{{
			Name:                    "p",
			SoftDependencies:        []string{"d"},
			SoftFeaturesPropagation: "ConfigMap",
		}}}
		if err := SyncSoftFeaturesForModule(t.Context(), c, c, "argocd", "mm", state, cat, "p"); !errContains(err, "softFeaturesConfigMapNamespaces") {
			t.Fatalf("expected missing softFeaturesConfigMapNamespaces error, got %v", err)
		}
	})

	t.Run("HelmValuesAndConfigMap_and_Resync", func(t *testing.T) {
		app := appWithHelmValues("argocd", "mod-p", "existing: keep\n")
		c := newControllerTestClient(t, interceptor.Funcs{}, nsTarget, app)
		state := &sfMemStateStore{state: &State{
			EnabledModules: []string{"id-p", "id-d"},
			ModuleIDs:      map[string]string{"p": "id-p", "d": "id-d"},
		}}
		cat := &sfMemCatalogStore{entries: []CatalogEntry{
			{
				Name:                            "p",
				SoftDependencies:                []string{"d"},
				SoftFeaturesPropagation:         "HelmValuesAndConfigMap",
				SoftFeaturesConfigMapNamespaces: []string{"app-ns"},
			},
			{Name: "d"},
		}}

		if err := SyncSoftFeaturesForAllEnabledModulesWithSoftDeps(t.Context(), c, c, "argocd", "mm", state, cat); err != nil {
			t.Fatalf("SyncSoftFeaturesForAllEnabledModulesWithSoftDeps failed: %v", err)
		}

		var cm corev1.ConfigMap
		if err := c.Get(t.Context(), client.ObjectKey{Namespace: "app-ns", Name: SoftFeaturesConfigMapName("p")}, &cm); err != nil {
			t.Fatalf("expected ConfigMap created: %v", err)
		}
		var jsonMap map[string]bool
		if err := json.Unmarshal([]byte(cm.Data[SoftFeaturesConfigMapKeyJSON]), &jsonMap); err != nil || !jsonMap["d"] {
			t.Fatalf("unexpected ConfigMap JSON %q: err=%v", cm.Data[SoftFeaturesConfigMapKeyJSON], err)
		}
		if !strings.Contains(cm.Data[SoftFeaturesConfigMapKeyEnv], "SOFT_FEATURE_ENABLED_D=true") {
			t.Errorf("expected SOFT_FEATURE_ENABLED_D=true in env data, got %q", cm.Data[SoftFeaturesConfigMapKeyEnv])
		}

		// Now toggle "d" off and resync parents of "d".
		state.state.EnabledModules = []string{"id-p"}
		if err := ResyncSoftFeaturesForParentsOfSoftDep(t.Context(), c, c, "argocd", "mm", state, cat, "d"); err != nil {
			t.Fatalf("ResyncSoftFeaturesForParentsOfSoftDep failed: %v", err)
		}
		if err := c.Get(t.Context(), client.ObjectKey{Namespace: "app-ns", Name: SoftFeaturesConfigMapName("p")}, &cm); err != nil {
			t.Fatalf("re-fetch ConfigMap: %v", err)
		}
		if err := json.Unmarshal([]byte(cm.Data[SoftFeaturesConfigMapKeyJSON]), &jsonMap); err != nil || jsonMap["d"] {
			t.Fatalf("expected d=false after resync, got %v (err=%v)", jsonMap, err)
		}
	})

	t.Run("Default_HelmValues_cleans_soft_features_when_no_deps", func(t *testing.T) {
		app := appWithHelmValues("argocd", "mod-p", "keep: 1\nsoftFeaturesEnabled:\n  old: true\n")
		c := newControllerTestClient(t, interceptor.Funcs{}, app)
		state := &sfMemStateStore{state: &State{
			EnabledModules: []string{"id-p"},
			ModuleIDs:      map[string]string{"p": "id-p"},
		}}
		cat := &sfMemCatalogStore{entries: []CatalogEntry{{Name: "p"}}}
		if err := SyncSoftFeaturesForModule(t.Context(), c, c, "argocd", "mm", state, cat, "p"); err != nil {
			t.Fatalf("SyncSoftFeaturesForModule failed: %v", err)
		}
		gotApp := &unstructured.Unstructured{}
		gotApp.SetGroupVersionKind(argoApplicationGVK)
		_ = c.Get(t.Context(), client.ObjectKey{Namespace: "argocd", Name: "mod-p"}, gotApp)
		vals, _, _ := unstructured.NestedString(gotApp.Object, "spec", "source", "helm", "values")
		if strings.Contains(vals, "old:") || !strings.Contains(vals, SoftFeaturesHelmKey) {
			t.Errorf("expected old soft dependency cleared and %q synced, got:\n%s", SoftFeaturesHelmKey, vals)
		}
	})
}

func TestEnsureSoftFeaturesConfigMaps_EdgeCases(t *testing.T) {
	c := newControllerTestClient(t, interceptor.Funcs{})
	if err := EnsureSoftFeaturesConfigMaps(t.Context(), c, c, nil, "p", map[string]bool{"d": true}); !errContains(err, "no non-empty namespaces") {
		t.Fatalf("expected no non-empty namespaces error, got %v", err)
	}

	t.Run("missing_namespace_retries_until_backoff_exhausted", func(t *testing.T) {
		withFastSoftFeatureBackoff(t, 2)
		var nsGetAttempts int
		cRetry := newControllerTestClient(t, interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, isNS := obj.(*corev1.Namespace); isNS {
					nsGetAttempts++
					return apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "namespaces"}, key.Name)
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		})
		err := EnsureSoftFeaturesConfigMaps(t.Context(), cRetry, cRetry, []string{"missing-ns"}, "p", map[string]bool{"d": true})
		if !apierrors.IsNotFound(err) {
			t.Fatalf("expected NotFound error after retries, got %v", err)
		}
		if nsGetAttempts != 2 {
			t.Errorf("expected 2 retry attempts, got %d", nsGetAttempts)
		}
	})

	t.Run("terminating_namespace_returns_namespaceTerminatingError", func(t *testing.T) {
		withFastSoftFeatureBackoff(t, 2)
		now := metav1.Now()
		termNS := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "term-ns",
				DeletionTimestamp: &now,
				Finalizers:        []string{"kubernetes"},
			},
		}
		cTerm := newControllerTestClient(t, interceptor.Funcs{}, termNS)
		err := EnsureSoftFeaturesConfigMaps(t.Context(), cTerm, cTerm, []string{"term-ns"}, "p", map[string]bool{"d": true})
		var termErr namespaceTerminatingError
		if !errors.As(err, &termErr) {
			t.Fatalf("expected namespaceTerminatingError, got %T (%v)", err, err)
		}
	})

	t.Run("updates_existing_ConfigMap_and_retries_conflict", func(t *testing.T) {
		withFastSoftFeatureBackoff(t, 3)
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app-ns"}}
		existing := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "app-ns",
				Name:      SoftFeaturesConfigMapName("p"),
			},
			Data: map[string]string{
				SoftFeaturesConfigMapKeyJSON: `{"d":false}`,
			},
		}
		var updateCalls int
		cUpd := newControllerTestClient(t, interceptor.Funcs{
			Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				updateCalls++
				if updateCalls == 1 {
					return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, obj.GetName(), errors.New("stale version"))
				}
				return cl.Update(ctx, obj, opts...)
			},
		}, ns, existing)

		if err := EnsureSoftFeaturesConfigMaps(t.Context(), cUpd, cUpd, []string{"app-ns"}, "p", map[string]bool{"d": true}); err != nil {
			t.Fatalf("EnsureSoftFeaturesConfigMaps conflict retry failed: %v", err)
		}
		if updateCalls != 2 {
			t.Errorf("expected 2 Update attempts (1 conflict + 1 success), got %d", updateCalls)
		}
		var updated corev1.ConfigMap
		if err := cUpd.Get(t.Context(), client.ObjectKey{Namespace: "app-ns", Name: SoftFeaturesConfigMapName("p")}, &updated); err != nil {
			t.Fatalf("Get updated ConfigMap: %v", err)
		}
		var got map[string]bool
		if err := json.Unmarshal([]byte(updated.Data[SoftFeaturesConfigMapKeyJSON]), &got); err != nil || !got["d"] {
			t.Fatalf("expected updated d=true, got %q", updated.Data[SoftFeaturesConfigMapKeyJSON])
		}
		_ = reflect.DeepEqual(got, map[string]bool{"d": true})
	})
}

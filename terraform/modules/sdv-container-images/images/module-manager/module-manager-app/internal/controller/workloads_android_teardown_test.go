// Copyright (c) 2026 Accenture, All Rights Reserved.
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
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPartitionArgoCDFinalizers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		in          []string
		wantKept    []string
		wantRemoved []string
	}{
		{
			name:        "empty",
			in:          nil,
			wantKept:    nil,
			wantRemoved: nil,
		},
		{
			name:        "resources_finalizer_only",
			in:          []string{"resources-finalizer.argocd.argoproj.io"},
			wantKept:    nil,
			wantRemoved: []string{"resources-finalizer.argocd.argoproj.io"},
		},
		{
			name: "mixed",
			in: []string{
				"resources-finalizer.argocd.argoproj.io",
				"custom.example.com/cleanup",
			},
			wantKept:    []string{"custom.example.com/cleanup"},
			wantRemoved: []string{"resources-finalizer.argocd.argoproj.io"},
		},
		{
			name:        "no_argo",
			in:          []string{"custom.example.com/cleanup"},
			wantKept:    []string{"custom.example.com/cleanup"},
			wantRemoved: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotKept, gotRemoved := partitionArgoCDFinalizers(tc.in)
			if !reflect.DeepEqual(gotKept, tc.wantKept) {
				t.Fatalf("kept: got %#v want %#v", gotKept, tc.wantKept)
			}
			if !reflect.DeepEqual(gotRemoved, tc.wantRemoved) {
				t.Fatalf("removed: got %#v want %#v", gotRemoved, tc.wantRemoved)
			}
		})
	}
}

func TestCuttlefishComputeInstanceTemplatesRemaining(t *testing.T) {
	t.Parallel()
	ul := &unstructured.UnstructuredList{}
	ul.Items = []unstructured.Unstructured{
		*mustCit(t, "workflows", "cf-it-a", nil),
		*mustCit(t, "workflows", "other", map[string]string{"app": "x"}),
		*mustCit(t, "workflows", "x", map[string]string{"horizon-sdv.io/cuttlefish-kcc-template": "true"}),
	}
	if got := cuttlefishComputeInstanceTemplatesRemaining(ul); got != 3 {
		t.Fatalf("remaining: got %d want 3 (all ComputeInstanceTemplate in list)", got)
	}
	if got := cuttlefishComputeInstanceTemplatesRemaining(nil); got != 0 {
		t.Fatalf("nil list: got %d want 0", got)
	}
}

func TestConfigConnectorStatusErrorsLikelyStaleCitBlock(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		u    *unstructured.Unstructured
		want bool
	}{
		{name: "nil", u: nil, want: false},
		{name: "no_status", u: &unstructured.Unstructured{Object: map[string]interface{}{}}, want: false},
		{
			name: "addon_wedge_message",
			u: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{Object: map[string]interface{}{}}
				_ = unstructured.SetNestedSlice(u.Object, []interface{}{
					"error during reconciliation: error building deployment objects: cannot finalize deletion until all Config Connector resources in namespace have been removed: there are 2 Config Connector resource(s) in namespace (2 ComputeInstanceTemplate(s))",
				}, "status", "errors")
				return u
			}(),
			want: true,
		},
		{
			name: "unrelated_error",
			u: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{Object: map[string]interface{}{}}
				_ = unstructured.SetNestedSlice(u.Object, []interface{}{"some other failure"}, "status", "errors")
				return u
			}(),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := configConnectorStatusErrorsLikelyStaleCitBlock(tc.u); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func mustCit(t *testing.T, ns, name string, labels map[string]string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "compute.cnrm.cloud.google.com", Version: "v1beta1", Kind: "ComputeInstanceTemplate"})
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetLabels(labels)
	return u
}

func TestTerminateArgoCDApplicationOperationIfAny_clearsSpecOperation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	app.SetNamespace("argocd")
	app.SetName("workloads-android")
	if err := unstructured.SetNestedMap(app.Object, map[string]interface{}{
		"sync": map[string]interface{}{"revision": "abc"},
	}, "spec", "operation"); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithObjects(app.DeepCopy()).Build()
	if err := terminateArgoCDApplicationOperationIfAny(ctx, c, "argocd", "workloads-android"); err != nil {
		t.Fatal(err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(app.GroupVersionKind())
	if err := c.Get(ctx, client.ObjectKey{Namespace: "argocd", Name: "workloads-android"}, got); err != nil {
		t.Fatal(err)
	}
	op, found, err := unstructured.NestedMap(got.Object, "spec", "operation")
	if err != nil {
		t.Fatal(err)
	}
	if found && op != nil && len(op) > 0 {
		t.Fatalf("expected spec.operation cleared, found=%v op=%#v", found, op)
	}
}

func TestTerminateArgoCDApplicationOperationIfAny_noOpWhenNoOperation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	app.SetNamespace("argocd")
	app.SetName("workloads-android")
	c := fake.NewClientBuilder().WithObjects(app.DeepCopy()).Build()
	if err := terminateArgoCDApplicationOperationIfAny(ctx, c, "argocd", "workloads-android"); err != nil {
		t.Fatal(err)
	}
}

func TestTerminateArgoCDApplicationOperationIfAny_ignoresNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := fake.NewClientBuilder().Build()
	if err := terminateArgoCDApplicationOperationIfAny(ctx, c, "argocd", "missing-app"); err != nil {
		t.Fatal(err)
	}
}

func TestDisableArgoCDApplicationAutomatedSyncIfAny_clearsAutomated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	app.SetNamespace("argocd")
	app.SetName("workloads-android")
	if err := unstructured.SetNestedMap(app.Object, map[string]interface{}{
		"syncOptions": []interface{}{"CreateNamespace=true"},
		"automated":   map[string]interface{}{},
	}, "spec", "syncPolicy"); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithObjects(app.DeepCopy()).Build()
	if err := disableArgoCDApplicationAutomatedSyncIfAny(ctx, c, "argocd", "workloads-android"); err != nil {
		t.Fatal(err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(app.GroupVersionKind())
	if err := c.Get(ctx, client.ObjectKey{Namespace: "argocd", Name: "workloads-android"}, got); err != nil {
		t.Fatal(err)
	}
	auto, found, err := unstructured.NestedMap(got.Object, "spec", "syncPolicy", "automated")
	if err != nil {
		t.Fatal(err)
	}
	if found && auto != nil && len(auto) > 0 {
		t.Fatalf("expected spec.syncPolicy.automated cleared, found=%v auto=%#v", found, auto)
	}
	opts, _, err := unstructured.NestedSlice(got.Object, "spec", "syncPolicy", "syncOptions")
	if err != nil || len(opts) == 0 {
		t.Fatalf("expected syncOptions preserved: err=%v opts=%#v", err, opts)
	}
}

func TestDisableArgoCDApplicationAutomatedSyncIfAny_ignoresNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := fake.NewClientBuilder().Build()
	if err := disableArgoCDApplicationAutomatedSyncIfAny(ctx, c, "argocd", "missing"); err != nil {
		t.Fatal(err)
	}
}

func TestPreparePrefixedModuleParentAndChildForDelete_orderAndPatches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	parent := &unstructured.Unstructured{}
	parent.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	parent.SetNamespace("argocd")
	parent.SetName("mod-workloads-android")
	if err := unstructured.SetNestedMap(parent.Object, map[string]interface{}{
		"sync": map[string]interface{}{"revision": "p1"},
	}, "spec", "operation"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedMap(parent.Object, map[string]interface{}{
		"automated": map[string]interface{}{},
	}, "spec", "syncPolicy"); err != nil {
		t.Fatal(err)
	}

	child := &unstructured.Unstructured{}
	child.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	child.SetNamespace("argocd")
	child.SetName("sbx-workloads-android")
	if err := unstructured.SetNestedMap(child.Object, map[string]interface{}{
		"sync": map[string]interface{}{"revision": "c1"},
	}, "spec", "operation"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedMap(child.Object, map[string]interface{}{
		"automated": map[string]interface{}{},
	}, "spec", "syncPolicy"); err != nil {
		t.Fatal(err)
	}

	c := fake.NewClientBuilder().WithObjects(parent.DeepCopy(), child.DeepCopy()).Build()
	if err := preparePrefixedModuleParentAndChildForDelete(ctx, c, "argocd", "workloads-android", "sbx-workloads-android"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mod-workloads-android", "sbx-workloads-android"} {
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(parent.GroupVersionKind())
		if err := c.Get(ctx, client.ObjectKey{Namespace: "argocd", Name: name}, got); err != nil {
			t.Fatal(err)
		}
		op, found, err := unstructured.NestedMap(got.Object, "spec", "operation")
		if err != nil {
			t.Fatal(err)
		}
		if found && op != nil && len(op) > 0 {
			t.Fatalf("%s: expected operation cleared", name)
		}
		auto, foundA, err := unstructured.NestedMap(got.Object, "spec", "syncPolicy", "automated")
		if err != nil {
			t.Fatal(err)
		}
		if foundA && auto != nil && len(auto) > 0 {
			t.Fatalf("%s: expected automated cleared", name)
		}
	}
}

func TestDeletePrefixedChildApplicationIfPresent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	child := &unstructured.Unstructured{}
	child.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	child.SetNamespace("argocd")
	child.SetName("workloads-android")
	c := fake.NewClientBuilder().WithObjects(child).Build()
	if err := deletePrefixedChildApplicationIfPresent(ctx, c, "argocd", "", "workloads-android"); err != nil {
		t.Fatal(err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(child.GroupVersionKind())
	if err := c.Get(ctx, client.ObjectKeyFromObject(child), got); !errors.IsNotFound(err) {
		t.Fatalf("expected child Application deleted, get err=%v", err)
	}
	if err := deletePrefixedChildApplicationIfPresent(ctx, c, "argocd", "", "workloads-android"); err != nil {
		t.Fatal(err)
	}
	if err := deletePrefixedChildApplicationIfPresent(ctx, c, "argocd", "", "sample"); err != nil {
		t.Fatal(err)
	}
}

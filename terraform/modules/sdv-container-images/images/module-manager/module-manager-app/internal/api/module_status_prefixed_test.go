// Copyright (c) 2026 Accenture, All Rights Reserved.

package api

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestManagedChildApplicationPresent(t *testing.T) {
	parent := unstructured.Unstructured{}
	parent.SetLabels(map[string]string{labelAppRole: labelAppRoleParent})
	child := unstructured.Unstructured{}
	child.SetLabels(map[string]string{labelAppRole: labelAppRoleChild})

	if managedChildApplicationPresent([]unstructured.Unstructured{parent}) {
		t.Fatal("expected false with parent only")
	}
	if !managedChildApplicationPresent([]unstructured.Unstructured{parent, child}) {
		t.Fatal("expected true with parent and child")
	}
}

func TestParentSkipReconcile(t *testing.T) {
	app := &unstructured.Unstructured{}
	app.Object = map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				argoCDSkipReconcileAnnotationKey: "true",
			},
		},
	}
	if !parentSkipReconcile(app) {
		t.Fatal("expected skip-reconcile true")
	}
	app.Object = map[string]interface{}{"metadata": map[string]interface{}{}}
	if parentSkipReconcile(app) {
		t.Fatal("expected skip-reconcile false")
	}
}

func TestFillPrefixedModuleStackStatus(t *testing.T) {
	status := &StatusResponse{}
	fillPrefixedModuleStackStatus("workloads-android", true, nil, nil, nil, nil, status)
	if status.ExpectedManagedApplicationCount == nil || *status.ExpectedManagedApplicationCount != 2 {
		t.Fatalf("expectedManagedApplicationCount: got %v", status.ExpectedManagedApplicationCount)
	}
	if status.ManagedChildApplicationPresent == nil || *status.ManagedChildApplicationPresent {
		t.Fatalf("managedChildApplicationPresent: got %v", status.ManagedChildApplicationPresent)
	}

	status2 := &StatusResponse{}
	fillPrefixedModuleStackStatus("sample", true, nil, nil, nil, nil, status2)
	if status2.ExpectedManagedApplicationCount != nil {
		t.Fatal("sample module should not set prefixed stack fields")
	}
}

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

package api

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestAnyManagedApplicationDeletionTimestamp_childTerminating(t *testing.T) {
	t.Parallel()
	parent := &unstructured.Unstructured{}
	parent.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	parent.SetName("mod-workloads-android")

	child := parent.DeepCopy()
	child.SetName("workloads-android")
	if err := unstructured.SetNestedField(child.Object, "2026-05-13T01:00:00Z", "metadata", "deletionTimestamp"); err != nil {
		t.Fatal(err)
	}

	got := anyManagedApplicationDeletionTimestamp(nil, parent, []unstructured.Unstructured{*child})
	if got == "" {
		t.Fatal("expected non-empty RFC3339 deletion timestamp when child Application is terminating")
	}
}

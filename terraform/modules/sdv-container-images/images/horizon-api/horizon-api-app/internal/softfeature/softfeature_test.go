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
//
package softfeature

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSoftModuleEnabledForParent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const mmNS = "module-manager"

	state := &unstructured.Unstructured{}
	state.SetAPIVersion("horizon-sdv.io/v1alpha1")
	state.SetKind("ModuleManagerState")
	state.SetNamespace(mmNS)
	state.SetName("cluster")
	if err := unstructured.SetNestedStringSlice(state.Object, []string{"id-soft"}, "status", "enabledModules"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedStringMap(state.Object, map[string]string{"sample-soft": "id-soft"}, "status", "moduleIds"); err != nil {
		t.Fatal(err)
	}

	cat := &unstructured.Unstructured{}
	cat.SetAPIVersion("horizon-sdv.io/v1alpha1")
	cat.SetKind("ModuleCatalog")
	cat.SetNamespace(mmNS)
	cat.SetName("cluster")
	if err := unstructured.SetNestedSlice(cat.Object, []interface{}{
		map[string]interface{}{
			"name":             "sample",
			"softDependencies": []interface{}{"sample-soft"},
		},
	}, "spec", "modules"); err != nil {
		t.Fatal(err)
	}

	cl := fake.NewClientBuilder().WithObjects(state, cat).Build()

	got, err := SoftModuleEnabledForParent(ctx, cl, mmNS, "cluster", "cluster", "sample", "sample-soft")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatalf("got enabled=false want true")
	}

	got, err = SoftModuleEnabledForParent(ctx, cl, mmNS, "cluster", "cluster", "sample", "other-soft")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatalf("got enabled=true want false for unrelated soft dep")
	}
}

func TestSoftModuleEnabledForParent_catalogNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cl := fake.NewClientBuilder().Build()
	_, err := SoftModuleEnabledForParent(ctx, cl, "module-manager", "cluster", "cluster", "sample", "sample-soft")
	if err != nil {
		t.Fatal(err)
	}
}

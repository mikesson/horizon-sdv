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
	"fmt"
	"testing"

	horizonv1alpha1 "github.com/acn-horizon-sdv/module-manager/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSoftFeaturesConfigMapName(t *testing.T) {
	if got := SoftFeaturesConfigMapName("sample-module"); got != "horizon-sdv-soft-features-sample-module" {
		t.Fatalf("got %q", got)
	}
	if got := SoftFeaturesConfigMapName("foo_bar"); got != "horizon-sdv-soft-features-foo-bar" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildSoftFeaturesConfigMapData(t *testing.T) {
	data := buildSoftFeaturesConfigMapData(map[string]bool{"sample-soft-module": true, "a": false})
	wantEnv := "SOFT_FEATURE_ENABLED_A=false\nSOFT_FEATURE_ENABLED_SAMPLE_SOFT_MODULE=true"
	if data[SoftFeaturesConfigMapKeyEnv] != wantEnv {
		t.Fatalf("env payload:\n%s\nwant:\n%s", data[SoftFeaturesConfigMapKeyEnv], wantEnv)
	}
	wantJSON := `{"a":false,"sample-soft-module":true}`
	if data[SoftFeaturesConfigMapKeyJSON] != wantJSON {
		t.Fatalf("json: %s want %s", data[SoftFeaturesConfigMapKeyJSON], wantJSON)
	}
}

func TestEffectiveSoftFeaturesPropagation(t *testing.T) {
	if g := effectiveSoftFeaturesPropagation(CatalogEntry{}); g != horizonv1alpha1.SoftFeaturesPropagationHelmValues {
		t.Fatalf("default: %s", g)
	}
	if g := effectiveSoftFeaturesPropagation(CatalogEntry{SoftFeaturesPropagation: horizonv1alpha1.SoftFeaturesPropagationConfigMap}); g != horizonv1alpha1.SoftFeaturesPropagationConfigMap {
		t.Fatalf("got %s", g)
	}
}

func TestEnsureSoftFeaturesConfigMaps_RetriesOnTerminatingNamespaceForbidden(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sample-module-hello"}}
	baseClient := fake.NewClientBuilder().WithObjects(ns).Build()
	writer := &forbiddenOnceOnCreateClient{
		Client: baseClient,
		err: apierrors.NewForbidden(
			schema.GroupResource{Resource: "configmaps"},
			"horizon-sdv-soft-features-sample",
			fmt.Errorf("unable to create new content in namespace %s because it is being terminated", ns.Name),
		),
	}

	err := EnsureSoftFeaturesConfigMaps(ctx, baseClient, writer, []string{ns.Name}, "sample", map[string]bool{"sample-soft": true})
	if err != nil {
		t.Fatalf("EnsureSoftFeaturesConfigMaps() error = %v", err)
	}
	if writer.createCalls != 2 {
		t.Fatalf("expected 2 create attempts, got %d", writer.createCalls)
	}

	cm := &corev1.ConfigMap{}
	if err := baseClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: "horizon-sdv-soft-features-sample"}, cm); err != nil {
		t.Fatalf("get configmap: %v", err)
	}
	if got := cm.Data[SoftFeaturesConfigMapKeyJSON]; got != `{"sample-soft":true}` {
		t.Fatalf("unexpected soft-features.json payload: %s", got)
	}
}

type forbiddenOnceOnCreateClient struct {
	client.Client
	err         error
	createCalls int
}

func (c *forbiddenOnceOnCreateClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.createCalls++
	if c.createCalls == 1 {
		return c.err
	}
	return c.Client.Create(ctx, obj, opts...)
}

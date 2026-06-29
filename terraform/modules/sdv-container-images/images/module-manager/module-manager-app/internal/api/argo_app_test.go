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
package api

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBuildHelmValuesYAML_escapesQuotes(t *testing.T) {
	got := BuildHelmValuesYAML(`mod"name`, `https://example.com/a"b`, `feat/foo"bar`, "", "")
	for _, want := range []string{`mod\"name`, `https://example.com/a\"b`, `feat/foo\"bar`} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected substring %q in:\n%s", want, got)
		}
	}
}

func TestBuildHelmValuesYAML_includesModuleConfig(t *testing.T) {
	got := BuildHelmValuesYAML("m", "https://r", "main", "foo: bar\nbaz: 1", "")
	for _, want := range []string{"config:", "  foo: bar", "  baz: 1", "repo:", "  revision: \"main\""} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected substring %q in:\n%s", want, got)
		}
	}
}

func TestBuildHelmValuesYAML_parentOverviewNamespace(t *testing.T) {
	cfg := "namespacePrefix: pfx-\nprojectID: x\n"
	got := BuildHelmValuesYAML("any-module", "https://r", "main", cfg, "pfx-workflows")
	want := "overviewNamespace: \"pfx-workflows\""
	if !strings.Contains(got, want) {
		t.Fatalf("expected substring %q in:\n%s", want, got)
	}
	gotNo := BuildHelmValuesYAML("any-module", "https://r", "main", cfg, "")
	if strings.Contains(gotNo, "overviewNamespace:") {
		t.Fatalf("did not expect overviewNamespace when parent namespace empty:\n%s", gotNo)
	}
}

func TestCreateApplicationIdempotent_FreshCreate(t *testing.T) {
	t.Parallel()

	c := fake.NewClientBuilder().Build()
	app := testApplication("mod-sample")
	if err := createApplicationIdempotentWithBackoff(context.Background(), c, app, wait.Backoff{Duration: time.Millisecond, Factor: 1, Steps: 2}); err != nil {
		t.Fatalf("createApplicationIdempotentWithBackoff() error = %v", err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(app.GroupVersionKind())
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(app), got); err != nil {
		t.Fatalf("get created app: %v", err)
	}
}

func TestCreateApplicationIdempotent_AlreadyExistsManaged(t *testing.T) {
	t.Parallel()

	existing := testApplication("mod-sample")
	c := fake.NewClientBuilder().WithObjects(existing.DeepCopy()).Build()
	app := testApplication("mod-sample")
	if err := createApplicationIdempotentWithBackoff(context.Background(), c, app, wait.Backoff{Duration: time.Millisecond, Factor: 1, Steps: 2}); err != nil {
		t.Fatalf("createApplicationIdempotentWithBackoff() error = %v", err)
	}
}

func TestCreateApplicationIdempotent_WaitsForDeletingThenCreates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	app := testApplication("mod-sample")
	// Seed as terminating: fake client rejects mutating deletionTimestamp via Update (immutable).
	deleting := app.DeepCopy()
	ts := metav1.Now()
	deleting.SetDeletionTimestamp(&ts)
	c := fake.NewClientBuilder().WithObjects(deleting).Build()

	go func() {
		time.Sleep(20 * time.Millisecond)
		// Simulate finalizer completion: fake client removes the object when finalizers are
		// cleared while deletionTimestamp is set (see controller-runtime fake client update).
		cur := &unstructured.Unstructured{}
		cur.SetGroupVersionKind(deleting.GroupVersionKind())
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(deleting), cur); err != nil {
			return
		}
		cur.SetFinalizers(nil)
		_ = c.Update(context.Background(), cur)
	}()

	if err := createApplicationIdempotentWithBackoff(ctx, c, testApplication("mod-sample"), wait.Backoff{Duration: 5 * time.Millisecond, Factor: 1, Steps: 20}); err != nil {
		t.Fatalf("createApplicationIdempotentWithBackoff() error = %v", err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(app.GroupVersionKind())
	if err := c.Get(ctx, client.ObjectKeyFromObject(app), got); err != nil {
		t.Fatalf("get recreated app: %v", err)
	}
}

func TestCreateApplicationIdempotent_TimesOutForDeletingObject(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	app := testApplication("mod-sample")
	deleting := app.DeepCopy()
	ts := metav1.Now()
	deleting.SetDeletionTimestamp(&ts)
	c := fake.NewClientBuilder().WithObjects(deleting).Build()

	err := createApplicationIdempotentWithBackoff(ctx, c, testApplication("mod-sample"), wait.Backoff{Duration: time.Millisecond, Factor: 1, Steps: 3})
	if err == nil {
		t.Fatal("expected timeout-like error, got nil")
	}
	if !strings.Contains(err.Error(), "wait for deleting application") {
		t.Fatalf("expected wait error, got %v", err)
	}
}

func testApplication(name string) *unstructured.Unstructured {
	app := BuildArgoCDApplication(name, "sample", "argocd", "default", "module-manager", "https://example.com/repo.git", "main", "gitops/modules/sample-module", "", "")
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	return app
}

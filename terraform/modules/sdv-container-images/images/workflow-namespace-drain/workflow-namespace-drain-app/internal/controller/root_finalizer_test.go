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
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeWorkflowDrainer struct {
	done  bool
	err   error
	calls int
}

func (f *fakeWorkflowDrainer) Drain(context.Context) (bool, error) {
	f.calls++
	return f.done, f.err
}

func TestRootFinalizerReconcilerNoDeletionTimestamp(t *testing.T) {
	t.Parallel()

	app := rootApp("argocd", "horizon-sdv", []string{WorkflowDrainFinalizer}, nil)
	drainer := &fakeWorkflowDrainer{done: true}
	reconciler := rootReconciler(t, app, drainer)

	result, err := reconciler.Reconcile(context.Background(), rootRequest())
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Fatalf("expected empty result, got %#v", result)
	}
	if drainer.calls != 0 {
		t.Fatalf("expected drainer not to be called, got %d calls", drainer.calls)
	}
}

func TestRootFinalizerReconcilerFinalizerAbsent(t *testing.T) {
	t.Parallel()

	now := metav1.Now()
	app := rootApp("argocd", "horizon-sdv", []string{"resources-finalizer.argocd.argoproj.io"}, &now)
	drainer := &fakeWorkflowDrainer{done: true}
	reconciler := rootReconciler(t, app, drainer)

	result, err := reconciler.Reconcile(context.Background(), rootRequest())
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Fatalf("expected empty result, got %#v", result)
	}
	if drainer.calls != 0 {
		t.Fatalf("expected drainer not to be called, got %d calls", drainer.calls)
	}
}

func TestRootFinalizerReconcilerDrainNotDoneKeepsFinalizer(t *testing.T) {
	t.Parallel()

	now := metav1.Now()
	app := rootApp("argocd", "horizon-sdv", []string{"resources-finalizer.argocd.argoproj.io", WorkflowDrainFinalizer}, &now)
	drainer := &fakeWorkflowDrainer{done: false}
	reconciler := rootReconciler(t, app, drainer)

	result, err := reconciler.Reconcile(context.Background(), rootRequest())
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter != 5*time.Second {
		t.Fatalf("expected 5s requeue, got %#v", result)
	}
	if drainer.calls != 1 {
		t.Fatalf("expected drainer to be called once, got %d calls", drainer.calls)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(argoApplicationGVK)
	if err := reconciler.Client.Get(context.Background(), client.ObjectKey{Namespace: "argocd", Name: "horizon-sdv"}, got); err != nil {
		t.Fatalf("get root app: %v", err)
	}
	if !hasFinalizerUnstructured(got, WorkflowDrainFinalizer) {
		t.Fatal("expected workflow drain finalizer to remain")
	}
}

func TestRootFinalizerReconcilerDrainErrorRequeuesAndKeepsFinalizer(t *testing.T) {
	t.Parallel()

	now := metav1.Now()
	app := rootApp("argocd", "horizon-sdv", []string{WorkflowDrainFinalizer}, &now)
	drainer := &fakeWorkflowDrainer{err: errors.New("temporary API error")}
	reconciler := rootReconciler(t, app, drainer)

	result, err := reconciler.Reconcile(context.Background(), rootRequest())
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter != 10*time.Second {
		t.Fatalf("expected 10s requeue, got %#v", result)
	}
	if drainer.calls != 1 {
		t.Fatalf("expected drainer to be called once, got %d calls", drainer.calls)
	}
}

func TestRootFinalizerReconcilerDrainDoneRemovesOnlyOwnFinalizer(t *testing.T) {
	t.Parallel()

	now := metav1.Now()
	otherFinalizers := []string{
		"resources-finalizer.argocd.argoproj.io",
		"horizon-sdv.io/module-manager-platform-drain",
		WorkflowDrainFinalizer,
	}
	app := rootApp("argocd", "horizon-sdv", otherFinalizers, &now)
	drainer := &fakeWorkflowDrainer{done: true}
	reconciler := rootReconciler(t, app, drainer)

	result, err := reconciler.Reconcile(context.Background(), rootRequest())
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Fatalf("expected empty result, got %#v", result)
	}
	if drainer.calls != 1 {
		t.Fatalf("expected drainer to be called once, got %d calls", drainer.calls)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(argoApplicationGVK)
	if err := reconciler.Client.Get(context.Background(), client.ObjectKey{Namespace: "argocd", Name: "horizon-sdv"}, got); err != nil {
		t.Fatalf("get root app: %v", err)
	}
	if hasFinalizerUnstructured(got, WorkflowDrainFinalizer) {
		t.Fatal("expected workflow drain finalizer to be removed")
	}
	if !hasFinalizerUnstructured(got, "resources-finalizer.argocd.argoproj.io") {
		t.Fatal("expected ArgoCD resources finalizer to be preserved")
	}
	if !hasFinalizerUnstructured(got, "horizon-sdv.io/module-manager-platform-drain") {
		t.Fatal("expected module-manager finalizer to be preserved")
	}
}

func rootRequest() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "argocd", Name: "horizon-sdv"}}
}

func rootReconciler(t *testing.T, app *unstructured.Unstructured, drainer workflowDrainer) *RootFinalizerReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(argoApplicationGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationList"}, &unstructured.UnstructuredList{})
	return &RootFinalizerReconciler{
		Client:             fake.NewClientBuilder().WithScheme(scheme).WithObjects(app).Build(),
		Drainer:            drainer,
		ArgoCDNamespace:    "argocd",
		GitOpsRootAppNames: []string{"horizon-sdv"},
		Finalizer:          WorkflowDrainFinalizer,
	}
}

func rootApp(namespace, name string, finalizers []string, deletionTimestamp *metav1.Time) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(argoApplicationGVK)
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetFinalizers(finalizers)
	u.SetDeletionTimestamp(deletionTimestamp)
	return u
}

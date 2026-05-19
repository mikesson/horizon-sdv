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
	"sort"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testArgoNS = "argocd"

func newApp(name string, labels map[string]string, finalizers []string, deletionTime *time.Time) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(argoApplicationGVK)
	u.SetName(name)
	u.SetNamespace(testArgoNS)
	if labels != nil {
		u.SetLabels(labels)
	}
	if finalizers != nil {
		u.SetFinalizers(finalizers)
	}
	if deletionTime != nil {
		t := metav1.NewTime(*deletionTime)
		u.SetDeletionTimestamp(&t)
	}
	return u
}

// newFakeClient builds a fake client that knows about the Argo Application GVK so
// list-by-label queries on unstructured objects work correctly.
func newFakeClient(objs ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(argoApplicationGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationList"},
		&unstructured.UnstructuredList{})
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()
}

func getFinalizers(t *testing.T, c client.Client, name string) []string {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(argoApplicationGVK)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: testArgoNS, Name: name}, u); err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	out := append([]string(nil), u.GetFinalizers()...)
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// PlatformDrainer unit tests
// ---------------------------------------------------------------------------

func TestRemovePlatformDrainFinalizer_RemovesOnlyPlatformFinalizer(t *testing.T) {
	app := newApp("horizon-sdv", nil,
		[]string{"resources-finalizer.argocd.argoproj.io", PlatformDrainFinalizer}, nil)
	c := newFakeClient(app)
	d := &PlatformDrainer{Client: c, APIReader: c, ArgoCDNamespace: testArgoNS}

	if err := d.RemovePlatformDrainFinalizer(context.Background(), testArgoNS, "horizon-sdv"); err != nil {
		t.Fatalf("RemovePlatformDrainFinalizer: %v", err)
	}
	got := getFinalizers(t, c, "horizon-sdv")
	if len(got) != 1 || got[0] != "resources-finalizer.argocd.argoproj.io" {
		t.Fatalf("finalizers = %v, want [resources-finalizer.argocd.argoproj.io]", got)
	}
}

func TestRemovePlatformDrainFinalizer_NoopWhenAbsent(t *testing.T) {
	app := newApp("horizon-sdv", nil, []string{"resources-finalizer.argocd.argoproj.io"}, nil)
	c := newFakeClient(app)
	d := &PlatformDrainer{Client: c, APIReader: c, ArgoCDNamespace: testArgoNS}

	if err := d.RemovePlatformDrainFinalizer(context.Background(), testArgoNS, "horizon-sdv"); err != nil {
		t.Fatalf("RemovePlatformDrainFinalizer: %v", err)
	}
	got := getFinalizers(t, c, "horizon-sdv")
	if len(got) != 1 || got[0] != "resources-finalizer.argocd.argoproj.io" {
		t.Fatalf("finalizers should be unchanged, got %v", got)
	}
}

func TestManagedModuleApplicationsRemaining_TrueWhenLabeled(t *testing.T) {
	now := time.Now()
	managed := newApp("mod-sample",
		map[string]string{ModuleManagerManagedLabelKey: "true"},
		[]string{"resources-finalizer.argocd.argoproj.io"}, &now)
	unmanaged := newApp("mod-other", nil, nil, nil)

	c := newFakeClient(managed, unmanaged)
	d := &PlatformDrainer{Client: c, APIReader: c, ArgoCDNamespace: testArgoNS}

	remaining, err := d.ManagedModuleApplicationsRemaining(context.Background(), testArgoNS)
	if err != nil {
		t.Fatalf("ManagedModuleApplicationsRemaining: %v", err)
	}
	if !remaining {
		t.Fatal("expected remaining=true when a managed app exists")
	}
}

func TestManagedModuleApplicationsRemaining_FalseWhenNoneLabeled(t *testing.T) {
	unmanaged := newApp("mod-other", nil, nil, nil)
	c := newFakeClient(unmanaged)
	d := &PlatformDrainer{Client: c, APIReader: c, ArgoCDNamespace: testArgoNS}

	remaining, err := d.ManagedModuleApplicationsRemaining(context.Background(), testArgoNS)
	if err != nil {
		t.Fatalf("ManagedModuleApplicationsRemaining: %v", err)
	}
	if remaining {
		t.Fatal("expected remaining=false when no managed apps exist")
	}
}

// ---------------------------------------------------------------------------
// RootGitOpsApplicationReconciler unit tests
// ---------------------------------------------------------------------------

// stubDrainer satisfies the platformDrainer interface for reconciler tests.
type stubDrainer struct {
	drainErr            error
	managedRemaining    bool
	managedRemainingErr error
	stripKCCCalled      int
	platformFinRemoved  bool
	destNamespaces      []string
}

func (s *stubDrainer) DrainAllEnabledModules(ctx context.Context) error {
	return s.drainErr
}
func (s *stubDrainer) ManagedModuleApplicationsRemaining(ctx context.Context, ns string) (bool, error) {
	return s.managedRemaining, s.managedRemainingErr
}
func (s *stubDrainer) ManagedDestinationNamespaces(ctx context.Context, ns string) []string {
	return s.destNamespaces
}
func (s *stubDrainer) StripKCCFinalizersInNamespaces(ctx context.Context, namespaces []string) error {
	s.stripKCCCalled++
	return nil
}
func (s *stubDrainer) RemovePlatformDrainFinalizer(ctx context.Context, ns, name string) error {
	s.platformFinRemoved = true
	return nil
}

func TestReconcileRoot_RemovesPlatformFinalizerWhenNoAppsRemain(t *testing.T) {
	delTime := time.Now()
	root := newApp("horizon-sdv", nil, []string{PlatformDrainFinalizer}, &delTime)
	c := newFakeClient(root)
	stub := &stubDrainer{managedRemaining: false}

	r := &RootGitOpsApplicationReconciler{
		Client:             c,
		Drainer:            stub,
		ArgoCDNamespace:    testArgoNS,
		GitOpsRootAppNames: []string{"horizon-sdv"},
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{Namespace: testArgoNS, Name: "horizon-sdv"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !stub.platformFinRemoved {
		t.Fatal("platform-drain finalizer should be removed when no managed apps remain")
	}
	if stub.stripKCCCalled != 0 {
		t.Fatal("KCC stripper should not be called when no apps remain")
	}
}

func TestReconcileRoot_StripsKCCAndRequeuesWhileAppsRemain(t *testing.T) {
	delTime := time.Now()
	root := newApp("horizon-sdv", nil, []string{PlatformDrainFinalizer}, &delTime)
	c := newFakeClient(root)
	stub := &stubDrainer{managedRemaining: true, destNamespaces: []string{"sample-module-hello"}}

	r := &RootGitOpsApplicationReconciler{
		Client:             c,
		Drainer:            stub,
		ArgoCDNamespace:    testArgoNS,
		GitOpsRootAppNames: []string{"horizon-sdv"},
	}
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{Namespace: testArgoNS, Name: "horizon-sdv"},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("expected non-zero RequeueAfter while apps remain")
	}
	if stub.stripKCCCalled != 1 {
		t.Fatalf("KCC stripper should be called once while apps remain, got %d", stub.stripKCCCalled)
	}
	if stub.platformFinRemoved {
		t.Fatal("platform-drain finalizer must not be removed while apps remain")
	}
}

func TestReconcileRoot_NoopWhenNotDeleting(t *testing.T) {
	root := newApp("horizon-sdv", nil, []string{PlatformDrainFinalizer}, nil) // no deletion timestamp
	c := newFakeClient(root)
	stub := &stubDrainer{}

	r := &RootGitOpsApplicationReconciler{
		Client:             c,
		Drainer:            stub,
		ArgoCDNamespace:    testArgoNS,
		GitOpsRootAppNames: []string{"horizon-sdv"},
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{Namespace: testArgoNS, Name: "horizon-sdv"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if stub.platformFinRemoved {
		t.Fatal("should not remove finalizer when app is not being deleted")
	}
}

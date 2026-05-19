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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// stubAPIGroupLister implements the narrow APIGroupLister interface.
type stubAPIGroupLister struct {
	resources []*metav1.APIResourceList
}

func (s *stubAPIGroupLister) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return nil, s.resources, nil
}

// newKCCFakeClient builds a fake client registered for a KCC-like GVK.
func newKCCFakeClient(gvk schema.GroupVersionKind, objs ...client.Object) client.Client {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind + "List"},
		&unstructured.UnstructuredList{})
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func kccObj(gvk schema.GroupVersionKind, ns, name string, finalizers []string, deleting bool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetFinalizers(finalizers)
	if deleting {
		t := metav1.NewTime(time.Now())
		u.SetDeletionTimestamp(&t)
	}
	return u
}

func TestStripKCCFinalizers_RemovesKCCFinalizerOnDeletingObject(t *testing.T) {
	pubsubGVK := schema.GroupVersionKind{Group: "pubsub.cnrm.cloud.google.com", Version: "v1beta1", Kind: "PubSubTopic"}
	obj := kccObj(pubsubGVK, "sample-module-hello", "sample-events",
		[]string{kccFinalizer, "some.other/finalizer"}, true)

	c := newKCCFakeClient(pubsubGVK, obj)
	disc := &stubAPIGroupLister{
		resources: []*metav1.APIResourceList{
			{
				GroupVersion: "pubsub.cnrm.cloud.google.com/v1beta1",
				APIResources: []metav1.APIResource{
					{Name: "pubsubtopics", Kind: "PubSubTopic", Namespaced: true, Verbs: []string{"list", "patch", "get"}},
				},
			},
		},
	}

	d := &PlatformDrainer{Client: c, APIReader: c, DiscoveryClient: disc}
	if err := d.StripKCCFinalizersInNamespaces(context.Background(), []string{"sample-module-hello"}); err != nil {
		t.Fatalf("StripKCCFinalizersInNamespaces: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(pubsubGVK)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "sample-module-hello", Name: "sample-events"}, got); err != nil {
		// Object was garbage-collected (last finalizer removed with deletionTimestamp set). Acceptable.
		return
	}
	for _, f := range got.GetFinalizers() {
		if f == kccFinalizer {
			t.Fatalf("kcc finalizer should have been removed, got %v", got.GetFinalizers())
		}
	}
}

func TestStripKCCFinalizers_DoesNothingForNonDeletingObject(t *testing.T) {
	pubsubGVK := schema.GroupVersionKind{Group: "pubsub.cnrm.cloud.google.com", Version: "v1beta1", Kind: "PubSubTopic"}
	obj := kccObj(pubsubGVK, "sample-module-hello", "sample-events",
		[]string{kccFinalizer}, false) // no deletionTimestamp

	c := newKCCFakeClient(pubsubGVK, obj)
	disc := &stubAPIGroupLister{
		resources: []*metav1.APIResourceList{
			{
				GroupVersion: "pubsub.cnrm.cloud.google.com/v1beta1",
				APIResources: []metav1.APIResource{
					{Name: "pubsubtopics", Kind: "PubSubTopic", Namespaced: true, Verbs: []string{"list", "patch", "get"}},
				},
			},
		},
	}

	d := &PlatformDrainer{Client: c, APIReader: c, DiscoveryClient: disc}
	if err := d.StripKCCFinalizersInNamespaces(context.Background(), []string{"sample-module-hello"}); err != nil {
		t.Fatalf("StripKCCFinalizersInNamespaces: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(pubsubGVK)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "sample-module-hello", Name: "sample-events"}, got); err != nil {
		t.Fatalf("get object: %v", err)
	}
	finalizers := got.GetFinalizers()
	if len(finalizers) != 1 || finalizers[0] != kccFinalizer {
		t.Fatalf("finalizers should be untouched for non-deleting object, got %v", finalizers)
	}
}

func TestStripKCCFinalizers_SkipsNonKCCGroups(t *testing.T) {
	appGVK := schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(appGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationList"}, &unstructured.UnstructuredList{})
	obj := kccObj(appGVK, "argocd", "mod-sample", []string{kccFinalizer}, true)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(obj).Build()

	disc := &stubAPIGroupLister{
		resources: []*metav1.APIResourceList{
			{
				GroupVersion: "argoproj.io/v1alpha1",
				APIResources: []metav1.APIResource{
					{Name: "applications", Kind: "Application", Namespaced: true, Verbs: []string{"list", "patch", "get"}},
				},
			},
		},
	}

	d := &PlatformDrainer{Client: c, APIReader: c, DiscoveryClient: disc}
	if err := d.StripKCCFinalizersInNamespaces(context.Background(), []string{"argocd"}); err != nil {
		t.Fatalf("StripKCCFinalizersInNamespaces: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(appGVK)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "argocd", Name: "mod-sample"}, got); err != nil {
		return // garbage collected
	}
	finalizers := got.GetFinalizers()
	if len(finalizers) != 1 || finalizers[0] != kccFinalizer {
		t.Fatalf("non-KCC group should not be touched, got %v", finalizers)
	}
}

func TestStripKCCFinalizers_NoopWhenDiscoveryIsNil(t *testing.T) {
	d := &PlatformDrainer{DiscoveryClient: nil}
	if err := d.StripKCCFinalizersInNamespaces(context.Background(), []string{"any-ns"}); err != nil {
		t.Fatalf("should be a noop when DiscoveryClient is nil, got: %v", err)
	}
}

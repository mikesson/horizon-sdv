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

	"github.com/acn-horizon-sdv/horizon-api/internal/catalog"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// CatalogReconciler rebuilds the in-memory catalog on Module Manager / WorkflowTemplate / ClusterWorkflowTemplate changes.
type CatalogReconciler struct {
	Catalog *catalog.Catalog

	ModuleManagerNS string
	WorkflowsNS     string
	StateName       string
	CatalogName     string
}

func enqueueCatalog(_ context.Context, _ client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "catalog"}}}
}

// SetupWithManager registers watches for ModuleManagerState, ModuleCatalog, WorkflowTemplates in the workflows namespace, and cluster-scoped ClusterWorkflowTemplates.
// WorkflowTemplate watches must not filter on horizon-sdv.io/expose: when expose flips true→false, the object would no longer match
// the predicate and no reconcile would run, leaving stale entries in the catalog/OpenAPI until another unrelated event occurred.
func (r *CatalogReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mm := &unstructured.Unstructured{}
	mm.SetGroupVersionKind(schema.GroupVersionKind{Group: "horizon-sdv.io", Version: "v1alpha1", Kind: "ModuleManagerState"})

	mc := &unstructured.Unstructured{}
	mc.SetGroupVersionKind(schema.GroupVersionKind{Group: "horizon-sdv.io", Version: "v1alpha1", Kind: "ModuleCatalog"})

	wt := &unstructured.Unstructured{}
	wt.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "WorkflowTemplate"})

	cwt := &unstructured.Unstructured{}
	cwt.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ClusterWorkflowTemplate"})

	return builder.ControllerManagedBy(mgr).
		Named("horizon-api-catalog").
		For(mm, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetNamespace() == r.ModuleManagerNS && o.GetName() == r.StateName
		}))).
		Watches(
			mc,
			handler.EnqueueRequestsFromMapFunc(enqueueCatalog),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
				return o.GetNamespace() == r.ModuleManagerNS && o.GetName() == r.CatalogName
			}))).
		Watches(
			wt,
			handler.EnqueueRequestsFromMapFunc(enqueueCatalog),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
				return o.GetNamespace() == r.WorkflowsNS
			}))).
		Watches(
			cwt,
			handler.EnqueueRequestsFromMapFunc(enqueueCatalog)).
		Complete(r)
}

// Reconcile implements reconcile.Reconciler.
func (r *CatalogReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	_ = req
	if err := r.Catalog.Rebuild(ctx); err != nil {
		return reconcile.Result{}, fmt.Errorf("catalog rebuild: %w", err)
	}
	return reconcile.Result{}, nil
}

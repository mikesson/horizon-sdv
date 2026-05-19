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
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const WorkflowDrainFinalizer = "horizon-sdv.io/workflow-namespace-drain"

var argoApplicationGVK = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}

type workflowDrainer interface {
	Drain(ctx context.Context) (bool, error)
}

// RootFinalizerReconciler drains Argo Workflows when the root horizon-sdv Application is deleted.
type RootFinalizerReconciler struct {
	Client             client.Client
	Drainer            workflowDrainer
	ArgoCDNamespace    string
	GitOpsRootAppNames []string
	Finalizer          string
}

func (r *RootFinalizerReconciler) finalizer() string {
	if r.Finalizer != "" {
		return r.Finalizer
	}
	return WorkflowDrainFinalizer
}

func (r *RootFinalizerReconciler) rootNameSet() map[string]struct{} {
	m := make(map[string]struct{}, len(r.GitOpsRootAppNames))
	for _, n := range r.GitOpsRootAppNames {
		if n != "" {
			m[n] = struct{}{}
		}
	}
	return m
}

// Reconcile implements reconcile.Reconciler.
func (r *RootFinalizerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	names := r.rootNameSet()
	if len(names) == 0 || r.ArgoCDNamespace == "" {
		return ctrl.Result{}, nil
	}
	if req.Namespace != r.ArgoCDNamespace {
		return ctrl.Result{}, nil
	}
	if _, ok := names[req.Name]; !ok {
		return ctrl.Result{}, nil
	}

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(argoApplicationGVK)
	if err := r.Client.Get(ctx, req.NamespacedName, u); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	finalizer := r.finalizer()
	if ts := u.GetDeletionTimestamp(); ts == nil || ts.IsZero() {
		return ctrl.Result{}, nil
	}
	if !hasFinalizerUnstructured(u, finalizer) {
		return ctrl.Result{}, nil
	}

	done, err := r.Drainer.Drain(ctx)
	if err != nil {
		logger.Error(err, "drain Argo Workflows for platform teardown")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	if !done {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if err := r.RemoveRootFinalizer(ctx, req.Namespace, req.Name); err != nil {
		logger.Error(err, "remove workflow drain finalizer from root Application")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}
	return ctrl.Result{}, nil
}

func hasFinalizerUnstructured(u *unstructured.Unstructured, want string) bool {
	for _, f := range u.GetFinalizers() {
		if f == want {
			return true
		}
	}
	return false
}

// RemoveRootFinalizer removes only this controller's finalizer from the root Application.
func (r *RootFinalizerReconciler) RemoveRootFinalizer(ctx context.Context, ns, name string) error {
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(argoApplicationGVK)
	key := client.ObjectKey{Namespace: ns, Name: name}
	if err := r.Client.Get(ctx, key, app); err != nil {
		return client.IgnoreNotFound(err)
	}
	finalizers := app.GetFinalizers()
	var kept []string
	for _, f := range finalizers {
		if f != r.finalizer() {
			kept = append(kept, f)
		}
	}
	if len(kept) == len(finalizers) {
		return nil
	}
	app.SetFinalizers(kept)
	if err := r.Client.Update(ctx, app); err != nil {
		return err
	}
	return nil
}

// SetupWithManager registers the reconciler for Argo CD Applications.
func (r *RootFinalizerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	names := r.rootNameSet()
	if len(names) == 0 {
		return nil
	}
	ns := r.ArgoCDNamespace
	finalizer := r.finalizer()
	argo := &unstructured.Unstructured{}
	argo.SetGroupVersionKind(argoApplicationGVK)

	matchRoot := func(u *unstructured.Unstructured) bool {
		if u.GetNamespace() != ns {
			return false
		}
		_, ok := names[u.GetName()]
		return ok
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(argo).
		WithEventFilter(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				u, ok := e.Object.(*unstructured.Unstructured)
				if !ok || !matchRoot(u) {
					return false
				}
				ts := u.GetDeletionTimestamp()
				if ts == nil || ts.IsZero() {
					return false
				}
				return hasFinalizerUnstructured(u, finalizer)
			},
			DeleteFunc: func(e event.DeleteEvent) bool { return false },
			UpdateFunc: func(e event.UpdateEvent) bool {
				u, ok := e.ObjectNew.(*unstructured.Unstructured)
				if !ok || !matchRoot(u) {
					return false
				}
				ts := u.GetDeletionTimestamp()
				return ts != nil && !ts.IsZero() && hasFinalizerUnstructured(u, finalizer)
			},
			GenericFunc: func(e event.GenericEvent) bool { return false },
		}).
		Complete(r)
}

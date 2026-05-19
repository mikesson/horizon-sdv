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

var argoApplicationGVK = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}

// platformDrainer is the subset of *PlatformDrainer the reconciler uses. Defined as an
// interface to keep the reconciler unit-testable without a real state/catalog store.
type platformDrainer interface {
	DrainAllEnabledModules(ctx context.Context) error
	ManagedModuleApplicationsRemaining(ctx context.Context, argoCDNamespace string) (bool, error)
	ManagedDestinationNamespaces(ctx context.Context, argoCDNamespace string) []string
	StripKCCFinalizersInNamespaces(ctx context.Context, namespaces []string) error
	RemovePlatformDrainFinalizer(ctx context.Context, ns, name string) error
}

// RootGitOpsApplicationReconciler drains module Applications when the root horizon-sdv
// Application is deleted, then removes PlatformDrainFinalizer to unblock Terraform destroy.
type RootGitOpsApplicationReconciler struct {
	Client             client.Client
	Drainer            platformDrainer
	ArgoCDNamespace    string
	GitOpsRootAppNames []string
}

func (r *RootGitOpsApplicationReconciler) rootNameSet() map[string]struct{} {
	m := make(map[string]struct{}, len(r.GitOpsRootAppNames))
	for _, n := range r.GitOpsRootAppNames {
		if n != "" {
			m[n] = struct{}{}
		}
	}
	return m
}

// Reconcile implements reconcile.Reconciler.
func (r *RootGitOpsApplicationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.ArgoCDNamespace == "" || req.Namespace != r.ArgoCDNamespace {
		return ctrl.Result{}, nil
	}
	if _, ok := r.rootNameSet()[req.Name]; ok {
		return r.reconcileRoot(ctx, req)
	}
	return ctrl.Result{}, nil
}

// reconcileRoot drains modules and removes PlatformDrainFinalizer from the root Application.
func (r *RootGitOpsApplicationReconciler) reconcileRoot(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(argoApplicationGVK)
	if err := r.Client.Get(ctx, req.NamespacedName, u); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if ts := u.GetDeletionTimestamp(); ts == nil || ts.IsZero() {
		return ctrl.Result{}, nil
	}
	if !hasFinalizerUnstructured(u, PlatformDrainFinalizer) {
		return ctrl.Result{}, nil
	}

	if err := r.Drainer.DrainAllEnabledModules(ctx); err != nil {
		logger.Error(err, "drain all modules for platform teardown")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	remaining, err := r.Drainer.ManagedModuleApplicationsRemaining(ctx, r.ArgoCDNamespace)
	if err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}
	if remaining {
		// Child Applications are still present (waiting on KCC or other finalizers).
		// Re-run the KCC stripper on every reconcile iteration so stuck KCC objects
		// unblock as soon as their token expires or auth is revoked.
		nss := r.Drainer.ManagedDestinationNamespaces(ctx, r.ArgoCDNamespace)
		if err := r.Drainer.StripKCCFinalizersInNamespaces(ctx, nss); err != nil {
			logger.Error(err, "strip KCC finalizers during platform drain wait")
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if err := r.Drainer.RemovePlatformDrainFinalizer(ctx, req.Namespace, req.Name); err != nil {
		logger.Error(err, "remove platform drain finalizer from root Application")
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

// SetupWithManager registers the reconciler for Argo CD Applications.
// It watches only the configured root Applications during deletion.
func (r *RootGitOpsApplicationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	rootNames := r.rootNameSet()
	if len(rootNames) == 0 {
		return nil
	}
	ns := r.ArgoCDNamespace
	argo := &unstructured.Unstructured{}
	argo.SetGroupVersionKind(argoApplicationGVK)

	matchWatched := func(u *unstructured.Unstructured) bool {
		if u.GetNamespace() != ns {
			return false
		}
		_, ok := rootNames[u.GetName()]
		return ok
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(argo).
		WithEventFilter(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				u, ok := e.Object.(*unstructured.Unstructured)
				if !ok || !matchWatched(u) {
					return false
				}
				ts := u.GetDeletionTimestamp()
				if ts == nil || ts.IsZero() {
					return false
				}
				return hasFinalizerUnstructured(u, PlatformDrainFinalizer)
			},
			DeleteFunc: func(e event.DeleteEvent) bool { return false },
			UpdateFunc: func(e event.UpdateEvent) bool {
				u, ok := e.ObjectNew.(*unstructured.Unstructured)
				if !ok || !matchWatched(u) {
					return false
				}
				ts := u.GetDeletionTimestamp()
				return ts != nil && !ts.IsZero()
			},
			GenericFunc: func(e event.GenericEvent) bool { return false },
		}).
		Complete(r)
}

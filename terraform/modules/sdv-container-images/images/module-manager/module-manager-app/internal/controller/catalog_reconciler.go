// Copyright (c) 2024-2026 Accenture, All Rights Reserved.
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

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	horizonv1alpha1 "github.com/acn-horizon-sdv/module-manager/api/v1alpha1"
)

// ModuleCatalogReconciler runs the auto-disable sweep and soft-feature sync whenever the
// ModuleCatalog spec changes (dependency wiring or autoDisableWhenUnused edits).
type ModuleCatalogReconciler struct {
	client.Client
	APIReader       client.Reader
	Namespace       string
	ArgoCDNamespace string
	StateStore      StateStoreInterface
	CatalogStore    CatalogStoreInterface
}

// Reconcile runs after ModuleCatalog spec changes.
func (r *ModuleCatalogReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ModuleOpsMutex.Lock()
	defer ModuleOpsMutex.Unlock()
	if err := RunAutoDisableSweep(ctx, r.APIReader, r.Client, r.Namespace, r.ArgoCDNamespace, r.StateStore, r.CatalogStore); err != nil {
		return ctrl.Result{}, err
	}
	if err := SyncSoftFeaturesForAllEnabledModulesWithSoftDeps(ctx, r.APIReader, r.Client, r.ArgoCDNamespace, r.Namespace, r.StateStore, r.CatalogStore); err != nil {
		logger.Error(err, "sync soft features after ModuleCatalog change")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler.
func (r *ModuleCatalogReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&horizonv1alpha1.ModuleCatalog{}).
		Complete(r)
}

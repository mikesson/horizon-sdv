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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	horizonv1alpha1 "github.com/acn-horizon-sdv/module-manager/api/v1alpha1"
)

// DefaultModuleCatalogName is the name of the singleton ModuleCatalog CR.
const DefaultModuleCatalogName = "cluster"

// CatalogStoreCR reads the module catalog from a ModuleCatalog custom resource.
type CatalogStoreCR struct {
	client client.Client
	ns     string
	name   string
}

// NewCatalogStoreCR returns a CatalogStore that reads from the given ModuleCatalog CR (singleton per namespace).
func NewCatalogStoreCR(c client.Client, namespace, catalogCRName string) *CatalogStoreCR {
	if catalogCRName == "" {
		catalogCRName = DefaultModuleCatalogName
	}
	return &CatalogStoreCR{client: c, ns: namespace, name: catalogCRName}
}

// List returns all catalog entries from the ModuleCatalog CR.
func (c *CatalogStoreCR) List(ctx context.Context) ([]CatalogEntry, error) {
	obj := &horizonv1alpha1.ModuleCatalog{}
	err := c.client.Get(ctx, client.ObjectKey{Namespace: c.ns, Name: c.name}, obj)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	entries := make([]CatalogEntry, 0, len(obj.Spec.Modules))
	for _, m := range obj.Spec.Modules {
		e := CatalogEntry{
			Name:                     m.Name,
			Path:                     m.Path,
			OverviewPath:             m.OverviewPath,
			OverviewService:          m.OverviewService,
			OverviewServiceNamespace: m.OverviewServiceNamespace,
		}
		if len(m.HardDependencies) > 0 {
			e.HardDependencies = make([]string, len(m.HardDependencies))
			copy(e.HardDependencies, m.HardDependencies)
		}
		if len(m.SoftDependencies) > 0 {
			e.SoftDependencies = make([]string, len(m.SoftDependencies))
			copy(e.SoftDependencies, m.SoftDependencies)
		}
		if len(m.Applications) > 0 {
			e.Applications = make([]CatalogApplication, 0, len(m.Applications))
			for _, a := range m.Applications {
				if a.ID == "" || a.URL == "" {
					continue
				}
				e.Applications = append(e.Applications, CatalogApplication{ID: a.ID, Title: a.Title, URL: a.URL})
			}
			if len(e.Applications) == 0 {
				e.Applications = nil
			}
		}
		e.SoftFeaturesPropagation = m.SoftFeaturesPropagation
		if len(m.SoftFeaturesConfigMapNamespaces) > 0 {
			e.SoftFeaturesConfigMapNamespaces = append([]string(nil), m.SoftFeaturesConfigMapNamespaces...)
		}
		e.AutoDisableWhenUnused = m.AutoDisableWhenUnused
		entries = append(entries, e)
	}
	return entries, nil
}

// GetPath returns the repo path for a module name, or empty string if not in catalog.
func (c *CatalogStoreCR) GetPath(ctx context.Context, moduleName string) (string, error) {
	entries, err := c.List(ctx)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Name == moduleName {
			return e.Path, nil
		}
	}
	return "", nil
}

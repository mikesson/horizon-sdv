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
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	catalogKey = "modules"
)

// CatalogApplication is metadata for a module child application (from ModuleCatalog).
type CatalogApplication struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
}

// CatalogEntry describes a module in the catalog (name, path, optional dependencies).
type CatalogEntry struct {
	Name                            string               `json:"name"`
	Path                            string               `json:"path"`
	OverviewPath                    string               `json:"overviewPath,omitempty"`
	OverviewService                 string               `json:"overviewService,omitempty"`
	OverviewServiceNamespace        string               `json:"overviewServiceNamespace,omitempty"`
	HardDependencies                []string             `json:"hardDependencies,omitempty"`
	SoftDependencies                []string             `json:"softDependencies,omitempty"`
	Applications                    []CatalogApplication `json:"applications,omitempty"`
	SoftFeaturesPropagation         string               `json:"softFeaturesPropagation,omitempty"`
	SoftFeaturesConfigMapNamespaces []string             `json:"softFeaturesConfigMapNamespaces,omitempty"`
	// AutoDisableWhenUnused allows Module Manager to automatically disable this module
	// when both hard and soft dependents drop to zero.
	AutoDisableWhenUnused bool `json:"autoDisableWhenUnused,omitempty"`
}

// CatalogStoreInterface is implemented by CatalogStore (ConfigMap) and CatalogStoreCR (CRD).
type CatalogStoreInterface interface {
	List(ctx context.Context) ([]CatalogEntry, error)
	GetPath(ctx context.Context, moduleName string) (string, error)
}

// CatalogStore reads the module catalog from a ConfigMap (deprecated: use CatalogStoreCR).
type CatalogStore struct {
	client client.Client
	ns     string
	name   string
}

// NewCatalogStore returns a CatalogStore that reads from the given ConfigMap.
func NewCatalogStore(c client.Client, namespace, configMapName string) *CatalogStore {
	return &CatalogStore{client: c, ns: namespace, name: configMapName}
}

// List returns all catalog entries (name -> path).
func (c *CatalogStore) List(ctx context.Context) ([]CatalogEntry, error) {
	cm := &corev1.ConfigMap{}
	err := c.client.Get(ctx, client.ObjectKey{Namespace: c.ns, Name: c.name}, cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	raw, ok := cm.Data[catalogKey]
	if !ok || raw == "" {
		return nil, nil
	}
	var entries []CatalogEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// GetPath returns the repo path for a module name, or empty string if not in catalog.
func (c *CatalogStore) GetPath(ctx context.Context, moduleName string) (string, error) {
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

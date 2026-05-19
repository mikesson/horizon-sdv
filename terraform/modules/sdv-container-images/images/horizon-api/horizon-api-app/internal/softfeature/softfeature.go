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
// Package softfeature resolves whether a soft-dependency module is enabled for a parent module,
// using the same sources as Module Manager (ModuleCatalog for desired state and
// ModuleManagerState for runtime state).
package softfeature

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SoftModuleEnabledForParent reports whether softModuleName (e.g. sample-soft) is enabled for the
// cluster, when parentModuleName (e.g. sample) lists it as a soft dependency in ModuleCatalog.
// If the soft module is not among the parent's soft dependencies, returns (false, nil).
func SoftModuleEnabledForParent(ctx context.Context, c client.Client, mmNamespace, stateCRName, catalogCRName, parentModuleName, softModuleName string) (bool, error) {
	deps, err := softDepsForParent(ctx, c, mmNamespace, catalogCRName, parentModuleName)
	if err != nil {
		return false, err
	}
	found := false
	for _, d := range deps {
		if d == softModuleName {
			found = true
			break
		}
	}
	if !found {
		return false, nil
	}

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "horizon-sdv.io", Version: "v1alpha1", Kind: "ModuleManagerState"})
	if err := c.Get(ctx, client.ObjectKey{Namespace: mmNamespace, Name: stateCRName}, u); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get ModuleManagerState: %w", err)
	}

	enabledIDs := map[string]bool{}
	if ids, found, _ := unstructured.NestedStringSlice(u.Object, "status", "enabledModules"); found {
		for _, id := range ids {
			enabledIDs[id] = true
		}
	}
	moduleIDByName := map[string]string{}
	if m, found, _ := unstructured.NestedStringMap(u.Object, "status", "moduleIds"); found {
		for name, id := range m {
			moduleIDByName[name] = id
		}
	}

	id := moduleIDByName[softModuleName]
	return id != "" && enabledIDs[id], nil
}

func softDepsForParent(ctx context.Context, c client.Client, mmNamespace, catalogCRName, parentModuleName string) ([]string, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "horizon-sdv.io", Version: "v1alpha1", Kind: "ModuleCatalog"})
	if err := c.Get(ctx, client.ObjectKey{Namespace: mmNamespace, Name: catalogCRName}, u); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get ModuleCatalog: %w", err)
	}
	modules, found, err := unstructured.NestedSlice(u.Object, "spec", "modules")
	if err != nil || !found {
		return nil, nil
	}
	for _, mod := range modules {
		m, ok := mod.(map[string]interface{})
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(m, "name")
		if name != parentModuleName {
			continue
		}
		deps, _, _ := unstructured.NestedStringSlice(m, "softDependencies")
		return append([]string(nil), deps...), nil
	}
	return nil, nil
}

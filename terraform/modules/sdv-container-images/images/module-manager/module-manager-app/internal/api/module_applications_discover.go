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

package api

import (
	"context"
	"log"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/acn-horizon-sdv/module-manager/internal/controller"
)

// Labels / annotations for Developer Portal "Applications" discovered from Argo CD child Applications.
// Aligns with WorkflowTemplate exposure (horizon-sdv.io/expose) but uses annotations for URL/title (label length limits).
const (
	labelModule        = "horizon-sdv.io/module"
	labelAppRole       = "horizon-sdv.io/app-role"
	labelAppRoleParent = "parent"
	labelAppRoleChild  = "child"
	labelExpose        = "horizon-sdv.io/expose"
	labelExposeVal    = "true"

	annPortalURL   = "horizon-sdv.io/portal-url"
	annPortalTitle = "horizon-sdv.io/portal-title"
	annPortalID    = "horizon-sdv.io/portal-id"
)

// discoverApplicationsFromArgoChildApps lists Argo CD Applications labeled as child apps of the module
// with horizon-sdv.io/expose=true and portal-url set.
func (h *Handler) discoverApplicationsFromArgoChildApps(ctx context.Context, moduleName string) ([]ModuleApplication, error) {
	if strings.TrimSpace(moduleName) == "" || strings.TrimSpace(h.argocdNamespace) == "" {
		return nil, nil
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationList"})
	err := h.apiReader.List(ctx, list, client.InNamespace(h.argocdNamespace), client.MatchingLabels{
		labelModule:  moduleName,
		labelAppRole: labelAppRoleChild,
		labelExpose:  labelExposeVal,
	})
	if err != nil {
		return nil, err
	}
	var out []ModuleApplication
	for i := range list.Items {
		item := &list.Items[i]
		labels := item.GetLabels()
		if labels == nil || labels[labelExpose] != labelExposeVal {
			continue
		}
		ann := item.GetAnnotations()
		if ann == nil {
			continue
		}
		url := strings.TrimSpace(ann[annPortalURL])
		if url == "" {
			continue
		}
		id := strings.TrimSpace(ann[annPortalID])
		if id == "" {
			id = item.GetName()
		}
		title := strings.TrimSpace(ann[annPortalTitle])
		if title == "" {
			title = displayTitleFromArgoAppName(item.GetName(), moduleName)
		}
		out = append(out, ModuleApplication{ID: id, Title: title, URL: url})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func displayTitleFromArgoAppName(appName, moduleName string) string {
	prefix := "mod-" + moduleName + "-"
	if strings.HasPrefix(appName, prefix) {
		return appName[len(prefix):]
	}
	return appName
}

// mergeModuleApplications merges catalog-derived apps first, then Argo-discovered rows, deduping by URL and id.
func mergeModuleApplications(catalog []ModuleApplication, fromArgo []ModuleApplication) []ModuleApplication {
	seenURL := make(map[string]struct{})
	seenID := make(map[string]struct{})
	var out []ModuleApplication
	add := func(a ModuleApplication) {
		u := strings.TrimSpace(a.URL)
		id := strings.TrimSpace(a.ID)
		if u == "" || id == "" {
			return
		}
		if _, ok := seenID[id]; ok {
			return
		}
		if _, ok := seenURL[u]; ok {
			return
		}
		seenID[id] = struct{}{}
		seenURL[u] = struct{}{}
		out = append(out, ModuleApplication{ID: id, Title: a.Title, URL: u})
	}
	for _, a := range catalog {
		add(a)
	}
	for _, a := range fromArgo {
		add(a)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// listModuleManagedArgoApplications returns Argo CD Applications labeled as parent or child for the module catalog name.
func (h *Handler) listModuleManagedArgoApplications(ctx context.Context, moduleName string) ([]unstructured.Unstructured, error) {
	if strings.TrimSpace(moduleName) == "" || strings.TrimSpace(h.argocdNamespace) == "" {
		return nil, nil
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationList"})
	if err := h.apiReader.List(ctx, list, client.InNamespace(h.argocdNamespace), client.MatchingLabels{
		labelModule: moduleName,
	}); err != nil {
		return nil, err
	}
	out := make([]unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		labels := item.GetLabels()
		if labels == nil {
			continue
		}
		switch strings.TrimSpace(labels[labelAppRole]) {
		case labelAppRoleParent, labelAppRoleChild:
			out = append(out, *item)
		default:
			continue
		}
	}
	return out, nil
}

func (h *Handler) mergedApplicationsForModule(ctx context.Context, moduleName string, catalogApps []controller.CatalogApplication) []ModuleApplication {
	cat := catalogApplicationsToResponse(catalogApps)
	argo, err := h.discoverApplicationsFromArgoChildApps(ctx, moduleName)
	if err != nil {
		log.Printf("module applications: list Argo child Applications for %q: %v", moduleName, err)
		return cat
	}
	return mergeModuleApplications(cat, argo)
}

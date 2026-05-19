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
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Ensure the real discovery.DiscoveryClient satisfies our narrow APIGroupLister interface.
var _ APIGroupLister = (*discovery.DiscoveryClient)(nil)

const kccFinalizer = "cnrm.cloud.google.com/finalizer"

// StripKCCFinalizersInNamespaces discovers every API group whose name ends in
// ".cnrm.cloud.google.com", lists all objects of each resource in the given namespaces,
// and removes cnrm.cloud.google.com/finalizer from any object that has a non-zero
// deletionTimestamp. All other finalizers are preserved.
//
// This is used as a defensive fallback during platform drain: once KCC has lost its
// GCP workload-identity token (because Terraform is tearing down IAM bindings in
// parallel), KCC's delete reconcile loop can never succeed. The underlying GCP resource
// (PubSub topic, etc.) is owned by the GCP project and is cleaned up when the project
// itself is destroyed. Removing the Kubernetes finalizer unblocks namespace termination
// and allows ArgoCD's cascade to complete.
//
// Errors on individual GVKs are logged and skipped so that one unlistable CRD does not
// abort the whole sweep.
func (p *PlatformDrainer) StripKCCFinalizersInNamespaces(ctx context.Context, namespaces []string) error {
	if len(namespaces) == 0 || p.DiscoveryClient == nil {
		return nil
	}
	logger := log.FromContext(ctx).WithName("kcc-drain")

	_, apiResourceLists, err := p.DiscoveryClient.ServerGroupsAndResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return err
	}

	for _, rl := range apiResourceLists {
		gv, parseErr := schema.ParseGroupVersion(rl.GroupVersion)
		if parseErr != nil {
			continue
		}
		if !strings.HasSuffix(gv.Group, ".cnrm.cloud.google.com") {
			continue
		}

		for i := range rl.APIResources {
			res := &rl.APIResources[i]
			// Skip sub-resources (e.g. "pubsubtopics/status") and non-namespaced resources.
			if strings.Contains(res.Name, "/") || !res.Namespaced {
				continue
			}
			// Only process resources that support list and patch.
			if !resourceSupportsVerb(res.Verbs, "list") || !resourceSupportsVerb(res.Verbs, "patch") {
				continue
			}

			gvk := schema.GroupVersionKind{Group: gv.Group, Version: gv.Version, Kind: res.Kind}

			for _, ns := range namespaces {
				ul := &unstructured.UnstructuredList{}
				ul.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   gv.Group,
					Version: gv.Version,
					Kind:    res.Kind + "List",
				})
				if listErr := p.APIReader.List(ctx, ul, client.InNamespace(ns)); listErr != nil {
					if !errors.IsNotFound(listErr) {
						logger.V(1).Info("list KCC resource (skipping)", "gvk", gvk, "namespace", ns, "err", listErr)
					}
					continue
				}
				for j := range ul.Items {
					item := &ul.Items[j]
					if item.GetDeletionTimestamp().IsZero() {
						continue
					}
					finalizers := item.GetFinalizers()
					var kept []string
					for _, f := range finalizers {
						if f != kccFinalizer {
							kept = append(kept, f)
						}
					}
					if len(kept) == len(finalizers) {
						continue
					}
					item.SetFinalizers(kept)
					if patchErr := p.Client.Update(ctx, item); patchErr != nil && !errors.IsNotFound(patchErr) {
						logger.Error(patchErr, "strip KCC finalizer", "gvk", gvk, "namespace", ns, "name", item.GetName())
					} else {
						logger.Info("stripped KCC finalizer", "gvk", gvk, "namespace", ns, "name", item.GetName())
					}
				}
			}
		}
	}
	return nil
}

func resourceSupportsVerb(verbs []string, verb string) bool {
	for _, v := range verbs {
		if v == verb {
			return true
		}
	}
	return false
}

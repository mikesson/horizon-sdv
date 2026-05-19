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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	workflowGVK     = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Workflow"}
	workflowListGVK = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "WorkflowList"}
)

// WorkflowDrainer deletes Argo Workflow CRs and force-clears their finalizers after a grace window.
type WorkflowDrainer struct {
	Client          client.Client
	APIReader       client.Reader
	WorkflowsNS     string
	GracefulTimeout time.Duration
	PageSize        int64
}

// Drain returns true when the workflows namespace has no Workflow CRs left.
func (d *WorkflowDrainer) Drain(ctx context.Context) (bool, error) {
	logger := log.FromContext(ctx)
	workflows, err := d.listWorkflows(ctx)
	if err != nil {
		if meta.IsNoMatchError(err) {
			logger.Info("Workflow CRD is not available, considering workflow drain complete")
			return true, nil
		}
		return false, err
	}
	if len(workflows) == 0 {
		return true, nil
	}

	now := time.Now()
	for i := range workflows {
		wf := workflows[i].DeepCopy()
		wf.SetGroupVersionKind(workflowGVK)
		if wf.GetDeletionTimestamp() == nil || wf.GetDeletionTimestamp().IsZero() {
			if err := d.Client.Delete(ctx, wf, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
				return false, err
			}
			logger.Info("deleted Workflow", "namespace", wf.GetNamespace(), "name", wf.GetName())
			continue
		}
		if finalizers := wf.GetFinalizers(); len(finalizers) > 0 && now.Sub(wf.GetDeletionTimestamp().Time) >= d.GracefulTimeout {
			patch := []byte(`{"metadata":{"finalizers":[]}}`)
			if err := d.Client.Patch(ctx, wf, client.RawPatch(types.MergePatchType, patch)); err != nil && !apierrors.IsNotFound(err) {
				return false, err
			}
			logger.Info("force-cleared Workflow finalizers", "namespace", wf.GetNamespace(), "name", wf.GetName())
		}
	}

	remaining, err := d.listWorkflows(ctx)
	if err != nil {
		if meta.IsNoMatchError(err) {
			return true, nil
		}
		return false, err
	}
	return len(remaining) == 0, nil
}

func (d *WorkflowDrainer) listWorkflows(ctx context.Context) ([]unstructured.Unstructured, error) {
	var items []unstructured.Unstructured
	continueToken := ""
	for {
		ul := &unstructured.UnstructuredList{}
		ul.SetGroupVersionKind(workflowListGVK)
		opts := []client.ListOption{client.InNamespace(d.WorkflowsNS)}
		if d.PageSize > 0 {
			opts = append(opts, client.Limit(d.PageSize))
		}
		if continueToken != "" {
			opts = append(opts, client.Continue(continueToken))
		}
		if err := d.APIReader.List(ctx, ul, opts...); err != nil {
			return nil, err
		}
		items = append(items, ul.Items...)
		continueToken = ul.GetContinue()
		if continueToken == "" {
			break
		}
	}
	return items, nil
}

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
package workflow

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

// GVR is the Kubernetes API resource for Argo Workflows.
var GVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}
var workflowTemplateGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflowtemplates"}
var clusterWorkflowTemplateGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "clusterworkflowtemplates"}

// Store performs Workflow CR operations in a single namespace.
type Store struct {
	ri    dynamic.ResourceInterface
	wtRi  dynamic.ResourceInterface
	cwtRi dynamic.NamespaceableResourceInterface
	pods  corev1client.PodInterface
}

// NewStore builds a dynamic client scoped to namespace ns.
func NewStore(cfg *rest.Config, ns string) (*Store, error) {
	d, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Store{
		ri:    d.Resource(GVR).Namespace(ns),
		wtRi:  d.Resource(workflowTemplateGVR).Namespace(ns),
		cwtRi: d.Resource(clusterWorkflowTemplateGVR),
		pods:  cs.CoreV1().Pods(ns),
	}, nil
}

// ResolveWorkflowTemplateModules resolves horizon-sdv.io/module for each template name.
// It tries namespaced WorkflowTemplate first, then ClusterWorkflowTemplate.
func (s *Store) ResolveWorkflowTemplateModules(ctx context.Context, templateNames []string) map[string]string {
	out := make(map[string]string, len(templateNames))
	for _, name := range templateNames {
		if name == "" {
			continue
		}
		if _, seen := out[name]; seen {
			continue
		}
		if s.wtRi != nil {
			if u, err := s.wtRi.Get(ctx, name, metav1.GetOptions{}); err == nil {
				out[name] = ModuleLabelValue(u)
				continue
			} else if err != nil && !apierrors.IsNotFound(err) {
				out[name] = ""
				continue
			}
		}
		if s.cwtRi != nil {
			if u, err := s.cwtRi.Get(ctx, name, metav1.GetOptions{}); err == nil {
				out[name] = ModuleLabelValue(u)
				continue
			}
		}
		out[name] = ""
	}
	return out
}

// ListPodNamesForWorkflow returns Pod object names for workflow pods (Argo sets this label on step pods).
func (s *Store) ListPodNamesForWorkflow(ctx context.Context, workflowName string) ([]string, error) {
	if s.pods == nil {
		return nil, nil
	}
	list, err := s.pods.List(ctx, metav1.ListOptions{
		LabelSelector: "workflows.argoproj.io/workflow=" + workflowName,
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, list.Items[i].Name)
	}
	return out, nil
}

// Get returns a Workflow by name.
func (s *Store) Get(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	return s.ri.Get(ctx, name, metav1.GetOptions{})
}

// List lists workflows with optional limit and continue token.
func (s *Store) List(ctx context.Context, limit int64, continueToken string) (*unstructured.UnstructuredList, error) {
	opts := metav1.ListOptions{
		LabelSelector: HorizonWorkflowListLabelSelector(),
	}
	if limit > 0 {
		opts.Limit = limit
	}
	if continueToken != "" {
		opts.Continue = continueToken
	}
	return s.ri.List(ctx, opts)
}

// PatchShutdown sets spec.shutdown to Stop (graceful stop for running workflows).
func (s *Store) PatchShutdown(ctx context.Context, name string) error {
	patch := []byte(`{"spec":{"shutdown":"Stop"}}`)
	_, err := s.ri.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch workflow shutdown: %w", err)
	}
	return nil
}

// Delete removes the Workflow CR by name.
func (s *Store) Delete(ctx context.Context, name string) error {
	err := s.ri.Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("delete workflow: %w", err)
	}
	return nil
}

// WaitUntilWorkflowDeleted polls Get until the object returns NotFound (finalizers cleared and CR removed).
func (s *Store) WaitUntilWorkflowDeleted(ctx context.Context, name string, pollInterval time.Duration) error {
	if pollInterval < 100*time.Millisecond {
		pollInterval = 500 * time.Millisecond
	}
	for {
		_, err := s.ri.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("wait workflow deleted: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

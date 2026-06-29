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
	"fmt"
	"log"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ModuleConfigHelmStartupSync runs once after cache warm-up and patches every parent module Argo CD Application so
// spec.source.helm.values.config matches the current MODULE_CONFIG env. Rolling the module-manager Deployment thus
// updates enabled modules without re-enable or target-revision API calls.
// Lists only Applications labeled app-role=parent; child apps (multi-source spec.sources) are excluded.
type ModuleConfigHelmStartupSync struct {
	Client       client.Client
	APIReader    client.Reader
	ArgoNS       string
	ModuleConfig string
}

func (s *ModuleConfigHelmStartupSync) Start(ctx context.Context) error {
	cfg := strings.TrimSpace(s.ModuleConfig)
	if cfg == "" {
		return nil
	}
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(5 * time.Second):
	}

	ul := &unstructured.UnstructuredList{}
	ul.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationList"})
	if err := s.APIReader.List(ctx, ul,
		client.InNamespace(s.ArgoNS),
		client.MatchingLabels{
			ModuleManagerManagedLabelKey: "true",
			ModuleManagerAppRoleLabelKey: ModuleManagerAppRoleParent,
		},
	); err != nil {
		return fmt.Errorf("list module-manager-managed Applications: %w", err)
	}
	for i := range ul.Items {
		name := ul.Items[i].GetName()
		if err := SyncApplicationHelmValuesConfig(ctx, s.Client, s.APIReader, s.ArgoNS, name, cfg); err != nil {
			log.Printf("module-config helm startup sync: sync MODULE_CONFIG into Application %q: %v", name, err)
		}
	}
	return nil
}

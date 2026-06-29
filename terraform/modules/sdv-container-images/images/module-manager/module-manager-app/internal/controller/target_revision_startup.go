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

// TargetRevisionStartupSync runs once after cache warm-up and patches every enabled, unpinned parent module
// Argo CD Application so spec.source.targetRevision and Helm repo.revision follow the current --target-revision.
// Rolling the module-manager Deployment after a platform branch change thus updates following modules without
// manual PUT or disable/enable.
type TargetRevisionStartupSync struct {
	Client          client.Client
	APIReader       client.Reader
	ArgoNS          string
	DefaultRevision string
	StateStore      StateStoreInterface
}

func (s *TargetRevisionStartupSync) Start(ctx context.Context) error {
	defaultRev := strings.TrimSpace(s.DefaultRevision)
	if defaultRev == "" {
		return nil
	}
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(5 * time.Second):
	}

	state, err := s.StateStore.Get(ctx)
	if err != nil {
		return fmt.Errorf("load module state for target-revision startup sync: %w", err)
	}

	ul := &unstructured.UnstructuredList{}
	ul.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationList"})
	if err := s.APIReader.List(ctx, ul,
		client.InNamespace(s.ArgoNS),
		client.MatchingLabels{
			ModuleManagerManagedLabelKey: "true",
			ModuleManagerAppRoleLabelKey:   ModuleManagerAppRoleParent,
		},
	); err != nil {
		return fmt.Errorf("list module-manager-managed parent Applications: %w", err)
	}

	enabledSet := make(map[string]bool, len(state.EnabledModules))
	for _, id := range state.EnabledModules {
		enabledSet[id] = true
	}

	for i := range ul.Items {
		app := &ul.Items[i]
		moduleName := strings.TrimSpace(app.GetLabels()[moduleLabelKey])
		if moduleName == "" {
			continue
		}
		moduleID := state.ModuleIDs[moduleName]
		if moduleID == "" || !enabledSet[moduleID] {
			continue
		}
		if IsModulePinned(state, moduleName) {
			continue
		}
		if err := SyncApplicationTargetRevision(ctx, s.Client, s.APIReader, s.ArgoNS, app.GetName(), defaultRev); err != nil {
			log.Printf("target-revision startup sync: sync Application %q (module %q): %v", app.GetName(), moduleName, err)
		}
	}
	return nil
}

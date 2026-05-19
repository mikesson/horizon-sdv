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
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	horizonv1alpha1 "github.com/acn-horizon-sdv/module-manager/api/v1alpha1"
)

// DefaultModuleManagerStateName is the name of the singleton ModuleManagerState CR.
const DefaultModuleManagerStateName = "cluster"

// StateStoreCR reads and writes module state from a ModuleManagerState custom resource.
type StateStoreCR struct {
	client client.Client
	ns     string
	name   string
	mu     sync.RWMutex
	cache  *State
}

// NewStateStoreCR returns a StateStore that uses the given ModuleManagerState CR (singleton per namespace).
func NewStateStoreCR(c client.Client, namespace, stateCRName string) *StateStoreCR {
	if stateCRName == "" {
		stateCRName = DefaultModuleManagerStateName
	}
	return &StateStoreCR{client: c, ns: namespace, name: stateCRName}
}

// Get returns the current state from the ModuleManagerState CR.
func (s *StateStoreCR) Get(ctx context.Context) (*State, error) {
	s.mu.RLock()
	if s.cache != nil {
		st := *s.cache
		s.mu.RUnlock()
		return &st, nil
	}
	s.mu.RUnlock()

	obj := &horizonv1alpha1.ModuleManagerState{}
	err := s.client.Get(ctx, client.ObjectKey{Namespace: s.ns, Name: s.name}, obj)
	if err != nil {
		if apierrors.IsNotFound(err) {
			st := &State{ModuleIDs: make(map[string]string)}
			s.mu.Lock()
			s.cache = st
			s.mu.Unlock()
			return st, nil
		}
		return nil, err
	}

	st := &State{
		EnabledModules: append([]string(nil), obj.Status.EnabledModules...),
		ModuleIDs:      make(map[string]string),
	}
	if obj.Status.ModuleIDs != nil {
		for k, v := range obj.Status.ModuleIDs {
			st.ModuleIDs[k] = v
		}
	}
	if obj.Status.ModuleTargetRevisions != nil {
		st.ModuleTargetRevisions = make(map[string]string, len(obj.Status.ModuleTargetRevisions))
		for k, v := range obj.Status.ModuleTargetRevisions {
			st.ModuleTargetRevisions[k] = v
		}
	}
	if wv := obj.Status.WorkflowsVisibility; wv != nil {
		st.WorkflowsVisibility = &WorkflowsVisibilitySettings{
			AllowedSubmittedFrom: append([]string(nil), wv.AllowedSubmittedFrom...),
		}
	}
	if st.ModuleIDs == nil {
		st.ModuleIDs = make(map[string]string)
	}

	s.mu.Lock()
	s.cache = st
	s.mu.Unlock()
	return st, nil
}

// Update persists state to the ModuleManagerState CR.
func (s *StateStoreCR) Update(ctx context.Context, st *State) error {
	obj := &horizonv1alpha1.ModuleManagerState{}
	err := s.client.Get(ctx, client.ObjectKey{Namespace: s.ns, Name: s.name}, obj)
	if err != nil {
		if apierrors.IsNotFound(err) {
			obj = &horizonv1alpha1.ModuleManagerState{
				ObjectMeta: metav1.ObjectMeta{Namespace: s.ns, Name: s.name},
			}
			if err := s.client.Create(ctx, obj); err != nil {
				return err
			}
			// Status is often ignored on Create when using subresources; set it explicitly.
			obj.Status.EnabledModules = append([]string(nil), st.EnabledModules...)
			obj.Status.ModuleIDs = make(map[string]string)
			if st.ModuleIDs != nil {
				for k, v := range st.ModuleIDs {
					obj.Status.ModuleIDs[k] = v
				}
			}
			if st.WorkflowsVisibility != nil {
				obj.Status.WorkflowsVisibility = &horizonv1alpha1.WorkflowsVisibilitySettings{
					AllowedSubmittedFrom: append([]string(nil), st.WorkflowsVisibility.AllowedSubmittedFrom...),
				}
			}
			if st.ModuleTargetRevisions != nil {
				obj.Status.ModuleTargetRevisions = make(map[string]string, len(st.ModuleTargetRevisions))
				for k, v := range st.ModuleTargetRevisions {
					obj.Status.ModuleTargetRevisions[k] = v
				}
			} else {
				obj.Status.ModuleTargetRevisions = nil
			}
			if err := s.client.Status().Update(ctx, obj); err != nil {
				return err
			}
			s.mu.Lock()
			s.cache = st
			s.mu.Unlock()
			return nil
		}
		return err
	}

	obj.Status.EnabledModules = append([]string(nil), st.EnabledModules...)
	obj.Status.ModuleIDs = make(map[string]string)
	if st.ModuleIDs != nil {
		for k, v := range st.ModuleIDs {
			obj.Status.ModuleIDs[k] = v
		}
	}
	if st.ModuleTargetRevisions != nil {
		obj.Status.ModuleTargetRevisions = make(map[string]string, len(st.ModuleTargetRevisions))
		for k, v := range st.ModuleTargetRevisions {
			obj.Status.ModuleTargetRevisions[k] = v
		}
	} else {
		obj.Status.ModuleTargetRevisions = nil
	}
	if st.WorkflowsVisibility != nil {
		obj.Status.WorkflowsVisibility = &horizonv1alpha1.WorkflowsVisibilitySettings{
			AllowedSubmittedFrom: append([]string(nil), st.WorkflowsVisibility.AllowedSubmittedFrom...),
		}
	} else {
		obj.Status.WorkflowsVisibility = nil
	}
	if err := s.client.Status().Update(ctx, obj); err != nil {
		return err
	}
	s.mu.Lock()
	s.cache = st
	s.mu.Unlock()
	return nil
}

// InvalidateCache clears the in-memory cache so the next Get reads from the cluster.
func (s *StateStoreCR) InvalidateCache() {
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
}

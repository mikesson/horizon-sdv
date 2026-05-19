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
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	stateKey = "state"
)

// WorkflowsVisibilitySettings is persisted Developer Portal preference (stored in module manager state).
type WorkflowsVisibilitySettings struct {
	// AllowedSubmittedFrom lists horizon-sdv.io/submitted-from label values that may appear in the portal.
	// Empty or nil means no restriction (show all sources the API returns).
	AllowedSubmittedFrom []string `json:"allowedSubmittedFrom,omitempty"`
}

// State holds enabled modules and name-to-ID mapping.
type State struct {
	EnabledModules []string          `json:"enabledModules"`
	ModuleIDs      map[string]string `json:"moduleIds"` // name -> assigned ID
	// ModuleTargetRevisions maps module name to Argo CD spec.source.targetRevision (Git ref).
	// Missing key means use the Module Manager process default (same as --target-revision).
	ModuleTargetRevisions map[string]string `json:"moduleTargetRevisions,omitempty"`
	// WorkflowsVisibility is optional portal configuration (ignored by module enable/disable logic).
	WorkflowsVisibility *WorkflowsVisibilitySettings `json:"workflowsVisibility,omitempty"`
}

// EffectiveTargetRevision returns the Git ref for moduleName from persisted state, or defaultRev when unset.
func EffectiveTargetRevision(state *State, moduleName, defaultRev string) string {
	defaultRev = strings.TrimSpace(defaultRev)
	if state != nil && state.ModuleTargetRevisions != nil {
		if r, ok := state.ModuleTargetRevisions[moduleName]; ok && strings.TrimSpace(r) != "" {
			return strings.TrimSpace(r)
		}
	}
	return defaultRev
}

// StateStoreInterface is implemented by both StateStore (ConfigMap) and StateStoreCR (CRD).
type StateStoreInterface interface {
	Get(ctx context.Context) (*State, error)
	Update(ctx context.Context, st *State) error
	InvalidateCache()
}

// StateStore reads and writes module state from a ConfigMap (deprecated: use StateStoreCR).
type StateStore struct {
	client client.Client
	ns     string
	name   string
	mu     sync.RWMutex
	cache  *State
}

// NewStateStore returns a StateStore that uses the given ConfigMap.
func NewStateStore(c client.Client, namespace, configMapName string) *StateStore {
	return &StateStore{client: c, ns: namespace, name: configMapName}
}

// Get returns the current state, reading from the cluster if cache is nil.
func (s *StateStore) Get(ctx context.Context) (*State, error) {
	s.mu.RLock()
	if s.cache != nil {
		st := *s.cache
		s.mu.RUnlock()
		return &st, nil
	}
	s.mu.RUnlock()

	cm := &corev1.ConfigMap{}
	err := s.client.Get(ctx, client.ObjectKey{Namespace: s.ns, Name: s.name}, cm)
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

	var st State
	if raw, ok := cm.Data[stateKey]; ok && raw != "" {
		if err := json.Unmarshal([]byte(raw), &st); err != nil {
			return nil, err
		}
	}
	if st.ModuleIDs == nil {
		st.ModuleIDs = make(map[string]string)
	}

	s.mu.Lock()
	s.cache = &st
	s.mu.Unlock()
	return &st, nil
}

// Update persists state to the ConfigMap.
func (s *StateStore) Update(ctx context.Context, st *State) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}

	cm := &corev1.ConfigMap{}
	err = s.client.Get(ctx, client.ObjectKey{Namespace: s.ns, Name: s.name}, cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			cm = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: s.ns, Name: s.name},
				Data:       map[string]string{stateKey: string(data)},
			}
			return s.client.Create(ctx, cm)
		}
		return err
	}
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[stateKey] = string(data)
	if err := s.client.Update(ctx, cm); err != nil {
		return err
	}
	s.mu.Lock()
	s.cache = st
	s.mu.Unlock()
	return nil
}

// InvalidateCache clears the in-memory cache so the next Get reads from the cluster.
func (s *StateStore) InvalidateCache() {
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
}

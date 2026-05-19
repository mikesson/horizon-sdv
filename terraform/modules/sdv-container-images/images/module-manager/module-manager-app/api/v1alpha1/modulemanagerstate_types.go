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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ModuleManagerStateSpec is reserved for future use (e.g. desired enabled list).
type ModuleManagerStateSpec struct{}

// ModuleManagerStateStatus holds the current module state.
type ModuleManagerStateStatus struct {
	// EnabledModules is the list of module IDs that are currently enabled.
	EnabledModules []string `json:"enabledModules,omitempty"`
	// ModuleIDs maps module name to the assigned stable ID.
	ModuleIDs map[string]string `json:"moduleIds,omitempty"`
	// ModuleTargetRevisions maps module name to Git ref (branch, tag, commit) for the module's Argo CD Application.
	ModuleTargetRevisions map[string]string `json:"moduleTargetRevisions,omitempty"`
	// WorkflowsVisibility stores Developer Portal workflow list filters (optional).
	WorkflowsVisibility *WorkflowsVisibilitySettings `json:"workflowsVisibility,omitempty"`
}

// WorkflowsVisibilitySettings is persisted alongside module enablement state.
type WorkflowsVisibilitySettings struct {
	AllowedSubmittedFrom []string `json:"allowedSubmittedFrom,omitempty"`
}

// DeepCopyInto copies WorkflowsVisibilitySettings.
func (in *WorkflowsVisibilitySettings) DeepCopyInto(out *WorkflowsVisibilitySettings) {
	*out = *in
	if in.AllowedSubmittedFrom != nil {
		out.AllowedSubmittedFrom = make([]string, len(in.AllowedSubmittedFrom))
		copy(out.AllowedSubmittedFrom, in.AllowedSubmittedFrom)
	}
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced

// ModuleManagerState is the schema for the module manager state (singleton per namespace).
type ModuleManagerState struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModuleManagerStateSpec   `json:"spec,omitempty"`
	Status ModuleManagerStateStatus `json:"status,omitempty"`
}

// DeepCopyObject implements runtime.Object.
func (in *ModuleManagerState) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := &ModuleManagerState{}
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the receiver into out.
func (in *ModuleManagerState) DeepCopyInto(out *ModuleManagerState) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopyInto copies Spec.
func (in *ModuleManagerStateSpec) DeepCopyInto(out *ModuleManagerStateSpec) {
	*out = *in
}

// DeepCopyInto copies Status.
func (in *ModuleManagerStateStatus) DeepCopyInto(out *ModuleManagerStateStatus) {
	*out = *in
	if in.EnabledModules != nil {
		out.EnabledModules = make([]string, len(in.EnabledModules))
		copy(out.EnabledModules, in.EnabledModules)
	}
	if in.ModuleIDs != nil {
		out.ModuleIDs = make(map[string]string, len(in.ModuleIDs))
		for k, v := range in.ModuleIDs {
			out.ModuleIDs[k] = v
		}
	}
	if in.ModuleTargetRevisions != nil {
		out.ModuleTargetRevisions = make(map[string]string, len(in.ModuleTargetRevisions))
		for k, v := range in.ModuleTargetRevisions {
			out.ModuleTargetRevisions[k] = v
		}
	} else {
		out.ModuleTargetRevisions = nil
	}
	if in.WorkflowsVisibility != nil {
		out.WorkflowsVisibility = new(WorkflowsVisibilitySettings)
		in.WorkflowsVisibility.DeepCopyInto(out.WorkflowsVisibility)
	} else {
		out.WorkflowsVisibility = nil
	}
}

// +kubebuilder:object:root=true

// ModuleManagerStateList contains a list of ModuleManagerState.
type ModuleManagerStateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModuleManagerState `json:"items"`
}

// DeepCopyObject implements runtime.Object.
func (in *ModuleManagerStateList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := &ModuleManagerStateList{}
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the list.
func (in *ModuleManagerStateList) DeepCopyInto(out *ModuleManagerStateList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]ModuleManagerState, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

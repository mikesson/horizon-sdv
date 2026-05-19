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

// Soft-features propagation modes (ModuleCatalogEntry.softFeaturesPropagation).
const (
	SoftFeaturesPropagationHelmValues             = "HelmValues"
	SoftFeaturesPropagationConfigMap              = "ConfigMap"
	SoftFeaturesPropagationHelmValuesAndConfigMap = "HelmValuesAndConfigMap"
)

// ModuleCatalogApplication is a link to a module-deployed application (e.g. public HTTPRoute path or absolute URL).
type ModuleCatalogApplication struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
}

// ModuleCatalogEntry is one module in the catalog.
type ModuleCatalogEntry struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	OverviewPath string `json:"overviewPath,omitempty"`
	// OverviewService is the in-cluster Service name that serves overview HTML (with OverviewServiceNamespace).
	OverviewService string `json:"overviewService,omitempty"`
	// OverviewServiceNamespace is the namespace containing OverviewService.
	OverviewServiceNamespace string                     `json:"overviewServiceNamespace,omitempty"`
	HardDependencies         []string                   `json:"hardDependencies,omitempty"`
	SoftDependencies         []string                   `json:"softDependencies,omitempty"`
	Applications             []ModuleCatalogApplication `json:"applications,omitempty"`
	// SoftFeaturesPropagation controls how Module Manager exposes soft-dependency enablement to workloads.
	// Omitted or empty means HelmValues (merge softFeaturesEnabled into the parent Argo CD Application helm values).
	// +kubebuilder:validation:Enum=HelmValues;ConfigMap;HelmValuesAndConfigMap
	SoftFeaturesPropagation string `json:"softFeaturesPropagation,omitempty"`
	// SoftFeaturesConfigMapNamespaces lists namespaces where Module Manager upserts the soft-features ConfigMap
	// when SoftFeaturesPropagation is ConfigMap or HelmValuesAndConfigMap (required for those modes).
	SoftFeaturesConfigMapNamespaces []string `json:"softFeaturesConfigMapNamespaces,omitempty"`
	// AutoDisableWhenUnused, when true, allows Module Manager to automatically disable this module
	// once both hard and soft dependent counts transition from greater than zero to zero. Default false.
	AutoDisableWhenUnused bool `json:"autoDisableWhenUnused,omitempty"`
}

// ModuleCatalogSpec defines the catalog of known modules.
type ModuleCatalogSpec struct {
	Modules []ModuleCatalogEntry `json:"modules,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced

// ModuleCatalog is the schema for the module catalog (singleton per namespace).
type ModuleCatalog struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ModuleCatalogSpec `json:"spec,omitempty"`
}

// DeepCopyObject implements runtime.Object.
func (in *ModuleCatalog) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := &ModuleCatalog{}
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the receiver into out.
func (in *ModuleCatalog) DeepCopyInto(out *ModuleCatalog) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
}

// DeepCopyInto copies Spec.
func (in *ModuleCatalogSpec) DeepCopyInto(out *ModuleCatalogSpec) {
	*out = *in
	if in.Modules != nil {
		out.Modules = make([]ModuleCatalogEntry, len(in.Modules))
		for i := range in.Modules {
			in.Modules[i].DeepCopyInto(&out.Modules[i])
		}
	}
}

// DeepCopyInto copies ModuleCatalogEntry.
func (in *ModuleCatalogEntry) DeepCopyInto(out *ModuleCatalogEntry) {
	*out = *in
	if in.HardDependencies != nil {
		out.HardDependencies = make([]string, len(in.HardDependencies))
		copy(out.HardDependencies, in.HardDependencies)
	}
	if in.SoftDependencies != nil {
		out.SoftDependencies = make([]string, len(in.SoftDependencies))
		copy(out.SoftDependencies, in.SoftDependencies)
	}
	if in.Applications != nil {
		out.Applications = make([]ModuleCatalogApplication, len(in.Applications))
		for i := range in.Applications {
			in.Applications[i].DeepCopyInto(&out.Applications[i])
		}
	}
	if in.SoftFeaturesConfigMapNamespaces != nil {
		out.SoftFeaturesConfigMapNamespaces = make([]string, len(in.SoftFeaturesConfigMapNamespaces))
		copy(out.SoftFeaturesConfigMapNamespaces, in.SoftFeaturesConfigMapNamespaces)
	}
}

// DeepCopyInto copies ModuleCatalogApplication.
func (in *ModuleCatalogApplication) DeepCopyInto(out *ModuleCatalogApplication) {
	*out = *in
}

// +kubebuilder:object:root=true

// ModuleCatalogList contains a list of ModuleCatalog.
type ModuleCatalogList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModuleCatalog `json:"items"`
}

// DeepCopyObject implements runtime.Object.
func (in *ModuleCatalogList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := &ModuleCatalogList{}
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the list.
func (in *ModuleCatalogList) DeepCopyInto(out *ModuleCatalogList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]ModuleCatalog, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

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
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func mergeTargetRevisionIntoHelmValuesYAML(valuesStr, revision string) (newValues string, changed bool, err error) {
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return "", false, fmt.Errorf("target revision cannot be empty")
	}
	var root map[string]interface{}
	if strings.TrimSpace(valuesStr) != "" {
		if err := yaml.Unmarshal([]byte(valuesStr), &root); err != nil {
			return "", false, fmt.Errorf("parse helm values: %w", err)
		}
	}
	if root == nil {
		root = make(map[string]interface{})
	}
	repo, _ := root["repo"].(map[string]interface{})
	if repo == nil {
		repo = make(map[string]interface{})
	}
	currentRev, _ := repo["revision"].(string)
	if strings.TrimSpace(currentRev) == revision {
		return valuesStr, false, nil
	}
	repo["revision"] = revision
	root["repo"] = repo
	out, err := yaml.Marshal(root)
	if err != nil {
		return "", false, fmt.Errorf("marshal helm values: %w", err)
	}
	return string(out), true, nil
}

func mergeModuleConfigIntoHelmValuesYAML(valuesStr, moduleConfig string) (newValues string, changed bool, err error) {
	moduleConfig = strings.TrimSpace(moduleConfig)
	if moduleConfig == "" {
		return "", false, nil
	}
	var cfg map[string]interface{}
	if err := yaml.Unmarshal([]byte(moduleConfig), &cfg); err != nil {
		return "", false, fmt.Errorf("parse MODULE_CONFIG: %w", err)
	}
	var root map[string]interface{}
	if strings.TrimSpace(valuesStr) != "" {
		if err := yaml.Unmarshal([]byte(valuesStr), &root); err != nil {
			return "", false, fmt.Errorf("parse helm values: %w", err)
		}
	}
	if root == nil {
		root = make(map[string]interface{})
	}
	if reflect.DeepEqual(root["config"], cfg) {
		return valuesStr, false, nil
	}
	root["config"] = cfg
	out, err := yaml.Marshal(root)
	if err != nil {
		return "", false, fmt.Errorf("marshal helm values: %w", err)
	}
	return string(out), true, nil
}

// argoSourcePathTakesModuleConfig returns true for Application source entries where MODULE_CONFIG is merged
// into helm values (umbrella charts under gitops/ in the platform repo). Multi-source child apps (e.g.
// workloads-android) use spec.sources; the parent mod-* app uses spec.source only.
func argoSourcePathTakesModuleConfig(path string) bool {
	return strings.Contains(path, "gitops/")
}

// applyModuleConfigToArgoSource merges MODULE_CONFIG into source.helm.values (top-level config key) when helm
// is present. Returns whether values changed.
func applyModuleConfigToArgoSource(src map[string]interface{}, moduleConfig string) (bool, error) {
	helm, _ := src["helm"].(map[string]interface{})
	if helm == nil {
		return false, nil
	}
	valuesStr, _ := helm["values"].(string)
	newVals, changed, err := mergeModuleConfigIntoHelmValuesYAML(valuesStr, moduleConfig)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	helm["values"] = newVals
	src["helm"] = helm
	return true, nil
}

// SyncApplicationTargetRevision updates spec.source.targetRevision and the repo.revision key in inline Helm values
// for parent module Applications (spec.source). No-op when both already match revision.
func SyncApplicationTargetRevision(ctx context.Context, writer client.Client, reader client.Reader, argoNS, appName, revision string) error {
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return nil
	}

	key := client.ObjectKey{Namespace: argoNS, Name: appName}
	// Re-read and re-apply on every attempt so a transient optimistic-concurrency
	// conflict (Argo CD or a concurrent startup sync mutating the same Application)
	// is retried instead of silently dropping the target-revision update.
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		app := &unstructured.Unstructured{}
		app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
		if err := reader.Get(ctx, key, app); err != nil {
			if errors.IsNotFound(err) {
				return nil
			}
			return err
		}

		currentRev, _, _ := unstructured.NestedString(app.Object, "spec", "source", "targetRevision")
		specChanged := strings.TrimSpace(currentRev) != revision

		src, srcOK, err := unstructured.NestedMap(app.Object, "spec", "source")
		if err != nil {
			return err
		}
		if !srcOK || src == nil {
			return nil
		}

		helmChanged := false
		helm, _ := src["helm"].(map[string]interface{})
		if helm != nil {
			valuesStr, _ := helm["values"].(string)
			newVals, changed, err := mergeTargetRevisionIntoHelmValuesYAML(valuesStr, revision)
			if err != nil {
				return fmt.Errorf("merge target revision into Application %s/%s: %w", argoNS, appName, err)
			}
			if changed {
				helmChanged = true
				helm["values"] = newVals
				src["helm"] = helm
			}
		}

		if !specChanged && !helmChanged {
			return nil
		}
		if specChanged {
			src["targetRevision"] = revision
		}
		if err := unstructured.SetNestedMap(app.Object, src, "spec", "source"); err != nil {
			return err
		}
		return writer.Update(ctx, app)
	})
}

// SyncApplicationHelmValuesConfig replaces spec.source.helm.values.config (or the same under spec.sources for
// gitops/ paths) with MODULE_CONFIG (YAML), preserving moduleName, repo, overviewNamespace, and
// softFeaturesEnabled. Module Manager injects MODULE_CONFIG from the Deployment env; parent Applications
// snapshot Helm values at enable time, so this keeps config (including scm) aligned after GitOps upgrades
// without chart-side defaults or re-enabling modules.
func SyncApplicationHelmValuesConfig(ctx context.Context, writer client.Client, reader client.Reader, argoNS, appName, moduleConfig string) error {
	moduleConfig = strings.TrimSpace(moduleConfig)
	if moduleConfig == "" {
		return nil
	}

	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	key := client.ObjectKey{Namespace: argoNS, Name: appName}
	if err := reader.Get(ctx, key, app); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// Single-source (typical mod-* parent Application).
	src, srcOK, err := unstructured.NestedMap(app.Object, "spec", "source")
	if err != nil {
		return err
	}
	if srcOK && src != nil {
		changed, err := applyModuleConfigToArgoSource(src, moduleConfig)
		if err != nil {
			return fmt.Errorf("merge MODULE_CONFIG into Application %s/%s: %w", argoNS, appName, err)
		}
		if changed {
			if err := unstructured.SetNestedMap(app.Object, src, "spec", "source"); err != nil {
				return err
			}
			return writer.Update(ctx, app)
		}
		return nil
	}

	// Multi-source (e.g. workloads-android, workloads-common child Application).
	sources, sourcesOK, err := unstructured.NestedSlice(app.Object, "spec", "sources")
	if err != nil {
		return err
	}
	if !sourcesOK || len(sources) == 0 {
		return nil
	}
	anyChanged := false
	for i := range sources {
		item, ok := sources[i].(map[string]interface{})
		if !ok {
			continue
		}
		path, _, _ := unstructured.NestedString(item, "path")
		if !argoSourcePathTakesModuleConfig(path) {
			continue
		}
		changed, err := applyModuleConfigToArgoSource(item, moduleConfig)
		if err != nil {
			return fmt.Errorf("merge MODULE_CONFIG into Application %s/%s source path %q: %w", argoNS, appName, path, err)
		}
		if changed {
			anyChanged = true
			sources[i] = item
		}
	}
	if !anyChanged {
		return nil
	}
	if err := unstructured.SetNestedSlice(app.Object, sources, "spec", "sources"); err != nil {
		return err
	}
	return writer.Update(ctx, app)
}

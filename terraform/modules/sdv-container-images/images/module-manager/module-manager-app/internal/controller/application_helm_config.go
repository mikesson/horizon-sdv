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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

// SyncApplicationHelmValuesConfig replaces spec.source.helm.values.config with MODULE_CONFIG (YAML), preserving
// moduleName, repo, overviewNamespace, and softFeaturesEnabled. Module Manager injects MODULE_CONFIG from the
// Deployment env; parent Applications snapshot Helm values at enable time, so this keeps config (including scm)
// aligned after GitOps upgrades without chart-side defaults or re-enabling modules.
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
	src, ok, err := unstructured.NestedMap(app.Object, "spec", "source")
	if err != nil || !ok || src == nil {
		return fmt.Errorf("application %s/%s: no spec.source", argoNS, appName)
	}
	helm, _ := src["helm"].(map[string]interface{})
	if helm == nil {
		return nil
	}
	valuesStr, _ := helm["values"].(string)
	newVals, changed, err := mergeModuleConfigIntoHelmValuesYAML(valuesStr, moduleConfig)
	if err != nil {
		return fmt.Errorf("merge MODULE_CONFIG into Application %s/%s: %w", argoNS, appName, err)
	}
	if !changed {
		return nil
	}
	helm["values"] = newVals
	src["helm"] = helm
	if err := unstructured.SetNestedMap(app.Object, src, "spec", "source"); err != nil {
		return err
	}
	return writer.Update(ctx, app)
}

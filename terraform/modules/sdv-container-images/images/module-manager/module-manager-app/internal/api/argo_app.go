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

package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var createApplicationBackoff = wait.Backoff{
	Duration: 500 * time.Millisecond,
	Factor:   2.0,
	Steps:    7,
	Jitter:   0.1,
}

const moduleManagerManagedLabelKey = "horizon-sdv.io/module-manager-managed"

func escapeYAMLString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	return strings.ReplaceAll(s, `"`, `\"`)
}

// BuildHelmValuesYAML returns the inline Helm values string used for module parent Applications.
// When parentOverviewNamespace is non-empty (same as catalog overviewServiceNamespace when overview is configured),
// appends top-level overviewNamespace for charts that render the in-cluster overview workload.
func BuildHelmValuesYAML(moduleName, repoURL, revision, moduleConfig, parentOverviewNamespace string) string {
	helmValues := "moduleName: \"" + escapeYAMLString(moduleName) + "\"\n"
	if moduleConfig != "" {
		helmValues += "config:\n"
		for _, line := range strings.Split(moduleConfig, "\n") {
			if line != "" {
				helmValues += "  " + line + "\n"
			}
		}
	}
	if strings.TrimSpace(parentOverviewNamespace) != "" {
		helmValues += "overviewNamespace: \"" + escapeYAMLString(strings.TrimSpace(parentOverviewNamespace)) + "\"\n"
	}
	helmValues += "repo:\n  url: \"" + escapeYAMLString(repoURL) + "\"\n  revision: \"" + escapeYAMLString(revision) + "\"\n"
	return helmValues
}

// BuildArgoCDApplication builds an Argo CD Application for a module chart at the given Git revision.
func BuildArgoCDApplication(name, moduleName, argocdNamespace, project, destinationNamespace, repoURL, revision, path, moduleConfig, parentOverviewNamespace string) *unstructured.Unstructured {
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	app.SetNamespace(argocdNamespace)
	app.SetName(name)
	app.SetLabels(map[string]string{
		"horizon-sdv.io/module":      moduleName,
		"horizon-sdv.io/app-role":    "parent",
		moduleManagerManagedLabelKey: "true",
	})
	app.SetFinalizers([]string{"resources-finalizer.argocd.argoproj.io"})
	helmValues := BuildHelmValuesYAML(moduleName, repoURL, revision, moduleConfig, parentOverviewNamespace)
	source := map[string]interface{}{
		"repoURL":        repoURL,
		"targetRevision": revision,
		"path":           path,
		"helm": map[string]interface{}{
			"values": helmValues,
		},
	}
	app.Object["spec"] = map[string]interface{}{
		"project": project,
		"source":  source,
		"destination": map[string]interface{}{
			"server":    "https://kubernetes.default.svc",
			"namespace": destinationNamespace,
		},
		"syncPolicy": map[string]interface{}{
			"syncOptions": []interface{}{"CreateNamespace=true"},
			"automated":   map[string]interface{}{},
		},
	}
	return app
}

func createApplicationIdempotent(ctx context.Context, c client.Client, app *unstructured.Unstructured) error {
	return createApplicationIdempotentWithBackoff(ctx, c, app, createApplicationBackoff)
}

func createApplicationIdempotentWithBackoff(ctx context.Context, c client.Client, app *unstructured.Unstructured, backoff wait.Backoff) error {
	if err := c.Create(ctx, app); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return err
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(app.GroupVersionKind())
	key := client.ObjectKeyFromObject(app)
	if err := c.Get(ctx, key, existing); err != nil {
		if apierrors.IsNotFound(err) {
			return c.Create(ctx, app)
		}
		return err
	}
	if existing.GetDeletionTimestamp().IsZero() {
		if !applicationManagedByModuleManager(existing) {
			return fmt.Errorf("application %s/%s already exists and is not managed by module-manager", key.Namespace, key.Name)
		}
		return nil
	}

	var lastErr error
	waitErr := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		current := &unstructured.Unstructured{}
		current.SetGroupVersionKind(app.GroupVersionKind())
		err := c.Get(ctx, key, current)
		if err == nil {
			if current.GetDeletionTimestamp().IsZero() {
				lastErr = fmt.Errorf("application %s/%s still exists while waiting for terminating object to be removed", key.Namespace, key.Name)
				return false, nil
			}
			lastErr = fmt.Errorf("application %s/%s is still terminating", key.Namespace, key.Name)
			return false, nil
		}
		if !apierrors.IsNotFound(err) {
			return false, err
		}
		createErr := c.Create(ctx, app)
		if createErr == nil {
			return true, nil
		}
		if apierrors.IsAlreadyExists(createErr) {
			lastErr = createErr
			return false, nil
		}
		return false, createErr
	})
	if waitErr == nil {
		return nil
	}
	if lastErr == nil {
		lastErr = waitErr
	}
	return fmt.Errorf("wait for deleting application %s/%s before re-create: %w", key.Namespace, key.Name, lastErr)
}

func applicationManagedByModuleManager(app *unstructured.Unstructured) bool {
	return app.GetLabels()[moduleManagerManagedLabelKey] == "true"
}

// PatchApplicationTargetRevision updates spec.source.targetRevision and embedded Helm repo.revision values.
func PatchApplicationTargetRevision(ctx context.Context, c client.Client, argoNS, appName, repoURL, revision, moduleName, moduleConfig, parentOverviewNamespace string) error {
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	if err := c.Get(ctx, client.ObjectKey{Namespace: argoNS, Name: appName}, app); err != nil {
		return err
	}
	if err := unstructured.SetNestedField(app.Object, revision, "spec", "source", "targetRevision"); err != nil {
		return fmt.Errorf("set targetRevision: %w", err)
	}
	src, found, err := unstructured.NestedMap(app.Object, "spec", "source")
	if err != nil || !found || src == nil {
		return fmt.Errorf("application %s/%s has no spec.source", argoNS, appName)
	}
	helm, _ := src["helm"].(map[string]interface{})
	if helm == nil {
		helm = map[string]interface{}{}
	}
	helm["values"] = BuildHelmValuesYAML(moduleName, repoURL, revision, moduleConfig, parentOverviewNamespace)
	src["helm"] = helm
	if err := unstructured.SetNestedMap(app.Object, src, "spec", "source"); err != nil {
		return fmt.Errorf("set helm values: %w", err)
	}
	return c.Update(ctx, app)
}

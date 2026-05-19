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
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	horizonv1alpha1 "github.com/acn-horizon-sdv/module-manager/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// SoftFeaturesConfigMapNamePrefix is the prefix for ConfigMaps that hold soft-feature flags (suffix: sanitized module name).
	SoftFeaturesConfigMapNamePrefix = "horizon-sdv-soft-features-"
	// SoftFeaturesConfigMapKeyEnv is a newline-separated KEY=value file (SOFT_FEATURE_ENABLED_*), for shell-friendly consumers.
	SoftFeaturesConfigMapKeyEnv = "features.env"
	// SoftFeaturesConfigMapKeyJSON is JSON object moduleName -> bool.
	SoftFeaturesConfigMapKeyJSON = "soft-features.json"

	managedByLabelKey   = "app.kubernetes.io/managed-by"
	managedByLabelValue = "module-manager"
	moduleLabelKey      = "horizon-sdv.io/module"
)

var softFeaturesConfigMapRetryBackoff = wait.Backoff{
	Duration: 500 * time.Millisecond,
	Factor:   2.0,
	Steps:    7,
	Jitter:   0.1,
}

type namespaceTerminatingError struct {
	namespace string
}

func (e namespaceTerminatingError) Error() string {
	return fmt.Sprintf("namespace %s is being terminated", e.namespace)
}

// SoftFeaturesConfigMapName returns the ConfigMap name for a parent module (e.g. horizon-sdv-soft-features-sample-module).
func SoftFeaturesConfigMapName(moduleName string) string {
	return SoftFeaturesConfigMapNamePrefix + strings.ReplaceAll(moduleName, "_", "-")
}

func catalogEntryForModule(ctx context.Context, catalogStore CatalogStoreInterface, parentModuleName string) (CatalogEntry, error) {
	entries, err := catalogStore.List(ctx)
	if err != nil {
		return CatalogEntry{}, err
	}
	for i := range entries {
		if entries[i].Name == parentModuleName {
			return entries[i], nil
		}
	}
	return CatalogEntry{}, nil
}

func effectiveSoftFeaturesPropagation(e CatalogEntry) string {
	if e.SoftFeaturesPropagation == "" {
		return horizonv1alpha1.SoftFeaturesPropagationHelmValues
	}
	return e.SoftFeaturesPropagation
}

// softDepEnvLine returns one line like SOFT_FEATURE_ENABLED_SAMPLE_SOFT_MODULE=true matching Helm template naming.
func softDepEnvLine(moduleName string, enabled bool) string {
	s := strings.ReplaceAll(moduleName, "-", "_")
	s = strings.ReplaceAll(s, ".", "_")
	key := "SOFT_FEATURE_ENABLED_" + strings.ToUpper(s)
	val := "false"
	if enabled {
		val = "true"
	}
	return key + "=" + val
}

func buildSoftFeaturesConfigMapData(features map[string]bool) map[string]string {
	keys := make([]string, 0, len(features))
	for k := range features {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var envLines strings.Builder
	for _, k := range keys {
		envLines.WriteString(softDepEnvLine(k, features[k]))
		envLines.WriteByte('\n')
	}
	j, err := json.Marshal(features)
	if err != nil {
		j = []byte("{}")
	}
	return map[string]string{
		SoftFeaturesConfigMapKeyEnv:  strings.TrimSuffix(envLines.String(), "\n"),
		SoftFeaturesConfigMapKeyJSON: string(j),
	}
}

// EnsureSoftFeaturesConfigMaps creates or updates the soft-features ConfigMap in each namespace.
// apiReader must be uncached (e.g. mgr.GetAPIReader()): workload namespaces are not in the manager's
// DefaultNamespaces cache, so a cached client Get fails with "unknown namespace for the cache".
func EnsureSoftFeaturesConfigMaps(ctx context.Context, apiReader client.Reader, writer client.Client, namespaces []string, parentModuleName string, features map[string]bool) error {
	logger := log.FromContext(ctx)
	var nsList []string
	for _, ns := range namespaces {
		if strings.TrimSpace(ns) != "" {
			nsList = append(nsList, strings.TrimSpace(ns))
		}
	}
	if len(nsList) == 0 {
		return fmt.Errorf("soft-features ConfigMap: no non-empty namespaces for module %s", parentModuleName)
	}
	name := SoftFeaturesConfigMapName(parentModuleName)
	data := buildSoftFeaturesConfigMapData(features)
	for _, ns := range nsList {
		key := client.ObjectKey{Namespace: ns, Name: name}
		attempt := 0
		var lastRetryErr error
		err := wait.ExponentialBackoffWithContext(ctx, softFeaturesConfigMapRetryBackoff, func(ctx context.Context) (bool, error) {
			attempt++
			if err := ensureNamespaceReady(ctx, apiReader, ns); err != nil {
				if isSoftFeaturesRetryableError(err) {
					lastRetryErr = err
					logger.Info("namespace not ready for soft-features ConfigMap write; retrying", "module", parentModuleName, "namespace", ns, "attempt", attempt, "error", err.Error())
					return false, nil
				}
				return false, fmt.Errorf("get namespace %s: %w", ns, err)
			}
			cm := &corev1.ConfigMap{}
			getErr := apiReader.Get(ctx, key, cm)
			if apierrors.IsNotFound(getErr) {
				cm = &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: ns,
						Name:      name,
						Labels: map[string]string{
							managedByLabelKey: managedByLabelValue,
							moduleLabelKey:    parentModuleName,
						},
					},
					Data: data,
				}
				createErr := writer.Create(ctx, cm)
				if createErr == nil {
					return true, nil
				}
				if apierrors.IsAlreadyExists(createErr) || isSoftFeaturesRetryableError(createErr) {
					lastRetryErr = createErr
					logger.Info("create soft-features ConfigMap retried", "module", parentModuleName, "namespace", ns, "attempt", attempt, "error", createErr.Error())
					return false, nil
				}
				return false, fmt.Errorf("create soft-features ConfigMap %s/%s: %w", ns, name, createErr)
			}
			if getErr != nil {
				if isSoftFeaturesRetryableError(getErr) {
					lastRetryErr = getErr
					logger.Info("get soft-features ConfigMap retried", "module", parentModuleName, "namespace", ns, "attempt", attempt, "error", getErr.Error())
					return false, nil
				}
				return false, fmt.Errorf("get soft-features ConfigMap %s/%s: %w", ns, name, getErr)
			}
			if cm.Data == nil {
				cm.Data = map[string]string{}
			}
			for k, v := range data {
				cm.Data[k] = v
			}
			if cm.Labels == nil {
				cm.Labels = map[string]string{}
			}
			cm.Labels[managedByLabelKey] = managedByLabelValue
			cm.Labels[moduleLabelKey] = parentModuleName
			updateErr := writer.Update(ctx, cm)
			if updateErr == nil {
				return true, nil
			}
			if apierrors.IsConflict(updateErr) || isSoftFeaturesRetryableError(updateErr) {
				lastRetryErr = updateErr
				logger.Info("update soft-features ConfigMap retried", "module", parentModuleName, "namespace", ns, "attempt", attempt, "error", updateErr.Error())
				return false, nil
			}
			return false, fmt.Errorf("update soft-features ConfigMap %s/%s: %w", ns, name, updateErr)
		})
		if err == nil {
			continue
		}
		if lastRetryErr == nil {
			lastRetryErr = err
		}
		return fmt.Errorf("sync soft-features ConfigMap %s/%s: %w", ns, name, lastRetryErr)
	}
	return nil
}

func ensureNamespaceReady(ctx context.Context, apiReader client.Reader, namespace string) error {
	ns := &corev1.Namespace{}
	if err := apiReader.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
		return err
	}
	if !ns.GetDeletionTimestamp().IsZero() {
		return namespaceTerminatingError{namespace: namespace}
	}
	return nil
}

func isSoftFeaturesRetryableError(err error) bool {
	var terminating namespaceTerminatingError
	if errors.As(err, &terminating) {
		return true
	}
	return isNamespaceNotFoundError(err) || isNamespaceTerminatingStatusError(err)
}

func isNamespaceNotFoundError(err error) bool {
	if !apierrors.IsNotFound(err) {
		return false
	}
	if statusErr, ok := err.(*apierrors.StatusError); ok {
		details := statusErr.Status().Details
		if details != nil && strings.EqualFold(details.Kind, "namespaces") {
			return true
		}
		msg := strings.ToLower(statusErr.Status().Message)
		if strings.Contains(msg, "namespaces") && strings.Contains(msg, "not found") {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "namespaces") && strings.Contains(msg, "not found")
}

func isNamespaceTerminatingStatusError(err error) bool {
	if !apierrors.IsForbidden(err) {
		return false
	}
	if statusErr, ok := err.(*apierrors.StatusError); ok {
		msg := strings.ToLower(statusErr.Status().Message)
		if strings.Contains(msg, "is being terminated") {
			return true
		}
		details := statusErr.Status().Details
		if details != nil {
			for _, cause := range details.Causes {
				if cause.Type == metav1.CauseTypeForbidden && strings.Contains(strings.ToLower(cause.Message), "is being terminated") {
					return true
				}
			}
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "is being terminated")
}

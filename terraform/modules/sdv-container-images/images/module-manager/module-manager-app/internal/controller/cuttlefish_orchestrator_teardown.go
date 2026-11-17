// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
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
	"strconv"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	cuttlefishOrchestratorModuleName         = "cuttlefish-orchestrator"
	cuttlefishOrchestratorNamespaceSuffix    = "cuttlefish-orchestrator"
	cuttlefishOrchestratorAPIDeploymentName  = "cuttlefish-orchestrator-api"
	cuttlefishOrchestratorAPILabelKey        = "app.kubernetes.io/name"
	cuttlefishOrchestratorAPILabelValue      = "cuttlefish-orchestrator-api"
	cuttlefishOrchestratorCleanupCronJobName = "cco-cleanup"

	resumeCuttlefishOrchestratorCleanupTimeout = 30 * time.Second
)

// Vars (not consts) so unit tests can shorten polling and timeouts.
var (
	cuttlefishOrchestratorDrainPollInterval = 5 * time.Second
	cuttlefishOrchestratorAPIDrainTimeout   = 2 * time.Minute
	cuttlefishOrchestratorCleanupJobTimeout = 22 * time.Minute

	ccoCleanupJobSeq uint64
)

var (
	ccoAppsDeploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	ccoCorePodListGVK    = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "PodList"}
	ccoBatchCronJobGVK   = schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"}
	ccoBatchJobGVK       = schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
)

func cuttlefishOrchestratorNamespace(moduleConfig string) string {
	return NamespacePrefixFromModuleConfig(moduleConfig) + cuttlefishOrchestratorNamespaceSuffix
}

func cuttlefishOrchestratorCoreChildAppName(namespacePrefix, moduleName string) string {
	return namespacePrefix + ApplicationName(moduleName) + "-app"
}

func nextCleanupJobName() string {
	seq := atomic.AddUint64(&ccoCleanupJobSeq, 1)
	ts := strconv.FormatInt(time.Now().UnixNano(), 36)
	return fmt.Sprintf("cco-cleanup-%s-%d", ts, seq)
}

// DrainCuttlefishOrchestratorModule performs authoritative GCE and API teardown when disabling
// cuttlefish-orchestrator. Argo CD has no PreDelete hook type (and PostDelete causes OutOfSync
// wedges), so module-manager must pause reconciliation, scale cuttlefish-orchestrator-api to zero,
// wait for API pods to exit, instantiate a batch/v1 Job from suspended CronJob/cco-cleanup, and
// wait for cleanup completion before deleting the parent or child Argo CD Applications.
func DrainCuttlefishOrchestratorModule(ctx context.Context, c client.Client, argocdNamespace, moduleConfig, moduleName string) (err error) {
	if moduleName != cuttlefishOrchestratorModuleName {
		return nil
	}

	prefix := NamespacePrefixFromModuleConfig(moduleConfig)
	targetNS := prefix + cuttlefishOrchestratorNamespaceSuffix
	parentApp := ApplicationName(moduleName)
	coreChildApp := cuttlefishOrchestratorCoreChildAppName(prefix, moduleName)
	logger := log.FromContext(ctx).WithValues("module", moduleName, "namespace", targetNS)

	pausedParent := false
	pausedChild := false
	defer func() {
		if err == nil {
			return
		}
		resumeCuttlefishOrchestratorAfterFailedDrain(c, argocdNamespace, parentApp, coreChildApp, pausedParent, pausedChild)
	}()

	if err = terminateArgoCDApplicationOperationIfAny(ctx, c, argocdNamespace, parentApp); err != nil {
		return fmt.Errorf("terminate parent Application operation %q: %w", parentApp, err)
	}
	if err = setArgoCDApplicationSkipReconcile(ctx, c, argocdNamespace, parentApp, true); err != nil {
		return fmt.Errorf("pause parent Application %q: %w", parentApp, err)
	}
	pausedParent = true

	if err = terminateArgoCDApplicationOperationIfAny(ctx, c, argocdNamespace, coreChildApp); err != nil {
		return fmt.Errorf("terminate core child Application operation %q: %w", coreChildApp, err)
	}
	if err = setArgoCDApplicationSkipReconcile(ctx, c, argocdNamespace, coreChildApp, true); err != nil {
		return fmt.Errorf("pause core child Application %q: %w", coreChildApp, err)
	}
	pausedChild = true

	if err = scaleDownCuttlefishOrchestratorAPI(ctx, c, targetNS); err != nil {
		return fmt.Errorf("scale down %s in %q: %w", cuttlefishOrchestratorAPIDeploymentName, targetNS, err)
	}

	if err = runCuttlefishOrchestratorCleanupJob(ctx, c, targetNS, moduleName); err != nil {
		return fmt.Errorf("run %s in %q: %w", cuttlefishOrchestratorCleanupCronJobName, targetNS, err)
	}

	// Disable automated sync and resume reconciliation on the core child Application while parent still
	// carries skip-reconcile so Argo prune can tear down child resources once parent is deleted.
	if err = disableArgoCDApplicationAutomatedSyncIfAny(ctx, c, argocdNamespace, coreChildApp); err != nil {
		return fmt.Errorf("disable core child automated sync %q: %w", coreChildApp, err)
	}
	if err = setArgoCDApplicationSkipReconcile(ctx, c, argocdNamespace, coreChildApp, false); err != nil {
		return fmt.Errorf("resume core child Application %q after drain: %w", coreChildApp, err)
	}
	pausedChild = false

	logger.Info("cuttlefish_orchestrator_teardown: API scaled down and GCE host VM cleanup succeeded",
		"parentApplication", parentApp, "coreChildApplication", coreChildApp)
	return nil
}

// clearParentSkipReconcileIfCuttlefishOrchestrator clears skip-reconcile on mod-cuttlefish-orchestrator
// right before deleting the parent Application (and on error paths before returning), so Argo CD can
// prune the parent's overview Deployment and cascade-delete child Applications.
func clearParentSkipReconcileIfCuttlefishOrchestrator(ctx context.Context, c client.Client, argocdNamespace, moduleName string) {
	if moduleName != cuttlefishOrchestratorModuleName {
		return
	}
	parentName := ApplicationName(moduleName)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), resumeCuttlefishOrchestratorCleanupTimeout)
	defer cancel()
	if err := setArgoCDApplicationSkipReconcile(cleanupCtx, c, argocdNamespace, parentName, false); err != nil {
		log.FromContext(ctx).Error(err, "clear skip-reconcile on parent Application (cuttlefish-orchestrator)",
			"application", parentName, "namespace", argocdNamespace)
	}
}

func resumeCuttlefishOrchestratorAfterFailedDrain(c client.Client, argocdNamespace, parentApp, coreChildApp string, pausedParent, pausedChild bool) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), resumeCuttlefishOrchestratorCleanupTimeout)
	defer cancel()
	logger := log.FromContext(cleanupCtx)
	if pausedChild {
		if err := setArgoCDApplicationSkipReconcile(cleanupCtx, c, argocdNamespace, coreChildApp, false); err != nil {
			logger.Error(err, "resume core child Application after failed drain", "application", coreChildApp, "namespace", argocdNamespace)
		}
	}
	if pausedParent {
		if err := setArgoCDApplicationSkipReconcile(cleanupCtx, c, argocdNamespace, parentApp, false); err != nil {
			logger.Error(err, "resume parent Application after failed drain", "application", parentApp, "namespace", argocdNamespace)
		}
	}
}

func scaleDownCuttlefishOrchestratorAPI(ctx context.Context, c client.Client, namespace string) error {
	logger := log.FromContext(ctx).WithValues("deployment", cuttlefishOrchestratorAPIDeploymentName, "namespace", namespace)
	dep := &unstructured.Unstructured{}
	dep.SetGroupVersionKind(ccoAppsDeploymentGVK)
	key := types.NamespacedName{Namespace: namespace, Name: cuttlefishOrchestratorAPIDeploymentName}
	if err := c.Get(ctx, key, dep); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	replicas, hasReplicas := readNumericField(dep.Object, "spec", "replicas")
	if !hasReplicas || replicas != 0 {
		patch := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"replicas":0}}`))
		if err := c.Patch(ctx, dep, patch); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("patch replicas=0: %w", err)
		}
		logger.Info("scaled cuttlefish-orchestrator-api Deployment to 0 replicas")
	}

	deadline := time.Now().Add(cuttlefishOrchestratorAPIDrainTimeout)
	for {
		pods := &unstructured.UnstructuredList{}
		pods.SetGroupVersionKind(ccoCorePodListGVK)
		if err := c.List(ctx, pods,
			client.InNamespace(namespace),
			client.MatchingLabels{cuttlefishOrchestratorAPILabelKey: cuttlefishOrchestratorAPILabelValue},
		); err != nil {
			return fmt.Errorf("list API pods: %w", err)
		}
		activePods := countActiveCCOPods(pods.Items)
		if activePods == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out after %s waiting for %d API pod(s) to terminate",
				cuttlefishOrchestratorAPIDrainTimeout, activePods)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cuttlefishOrchestratorDrainPollInterval):
		}
	}
}

func countActiveCCOPods(pods []unstructured.Unstructured) int {
	active := 0
	for i := range pods {
		phase, _, _ := unstructured.NestedString(pods[i].Object, "status", "phase")
		if phase == "Succeeded" || phase == "Failed" {
			continue
		}
		active++
	}
	return active
}

func runCuttlefishOrchestratorCleanupJob(ctx context.Context, c client.Client, namespace, moduleName string) error {
	logger := log.FromContext(ctx).WithValues("cronjob", cuttlefishOrchestratorCleanupCronJobName, "namespace", namespace)
	cj := &unstructured.Unstructured{}
	cj.SetGroupVersionKind(ccoBatchCronJobGVK)
	key := types.NamespacedName{Namespace: namespace, Name: cuttlefishOrchestratorCleanupCronJobName}
	if err := c.Get(ctx, key, cj); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	jobTemplate, found, err := unstructured.NestedMap(cj.Object, "spec", "jobTemplate")
	if err != nil || !found || jobTemplate == nil {
		return fmt.Errorf("CronJob %s/%s is missing spec.jobTemplate", namespace, cuttlefishOrchestratorCleanupCronJobName)
	}
	specMap, found, err := unstructured.NestedMap(jobTemplate, "spec")
	if err != nil || !found || specMap == nil {
		return fmt.Errorf("CronJob %s/%s is missing spec.jobTemplate.spec", namespace, cuttlefishOrchestratorCleanupCronJobName)
	}

	jobName := nextCleanupJobName()
	jobSpecCopy := runtime.DeepCopyJSON(specMap)
	if _, ok := jobSpecCopy["ttlSecondsAfterFinished"]; !ok {
		jobSpecCopy["ttlSecondsAfterFinished"] = int64(300)
	}

	labels := map[string]string{
		"cronjob-name":              cuttlefishOrchestratorCleanupCronJobName,
		"app.kubernetes.io/name":    cuttlefishOrchestratorCleanupCronJobName,
		"horizon-sdv.io/module":     moduleName,
		"app.kubernetes.io/part-of": cuttlefishOrchestratorModuleName,
	}
	if tplLabels, ok, _ := unstructured.NestedStringMap(jobTemplate, "metadata", "labels"); ok {
		for k, v := range tplLabels {
			labels[k] = v
		}
	}

	job := &unstructured.Unstructured{}
	job.SetGroupVersionKind(ccoBatchJobGVK)
	job.SetNamespace(namespace)
	job.SetName(jobName)
	job.SetLabels(labels)
	job.SetAnnotations(map[string]string{
		"cronjob.kubernetes.io/instantiate": "manual",
	})
	if uid := cj.GetUID(); uid != "" {
		if err := unstructured.SetNestedSlice(job.Object, []interface{}{
			map[string]interface{}{
				"apiVersion": "batch/v1",
				"kind":       "CronJob",
				"name":       cj.GetName(),
				"uid":        string(uid),
			},
		}, "metadata", "ownerReferences"); err != nil {
			return fmt.Errorf("set cleanup Job ownerReference: %w", err)
		}
	}
	if err := unstructured.SetNestedMap(job.Object, jobSpecCopy, "spec"); err != nil {
		return fmt.Errorf("set cleanup Job spec: %w", err)
	}

	if err := c.Create(ctx, job); err != nil {
		return fmt.Errorf("create cleanup Job %s/%s: %w", namespace, jobName, err)
	}
	logger.Info("created cco-cleanup Job from CronJob template", "job", jobName)

	return waitForCleanupJobCompletion(ctx, c, namespace, jobName)
}

func waitForCleanupJobCompletion(ctx context.Context, c client.Client, namespace, jobName string) error {
	jobKey := types.NamespacedName{Namespace: namespace, Name: jobName}
	deadline := time.Now().Add(cuttlefishOrchestratorCleanupJobTimeout)
	for {
		cur := &unstructured.Unstructured{}
		cur.SetGroupVersionKind(ccoBatchJobGVK)
		if err := c.Get(ctx, jobKey, cur); err != nil {
			return fmt.Errorf("get cleanup Job %s/%s: %w", namespace, jobName, err)
		}
		done, failedMsg, err := evaluateCleanupJobStatus(cur)
		if err != nil {
			return err
		}
		if failedMsg != "" {
			return fmt.Errorf("cleanup Job %s/%s failed: %s", namespace, jobName, failedMsg)
		}
		if done {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out after %s waiting for cleanup Job %s/%s to complete",
				cuttlefishOrchestratorCleanupJobTimeout, namespace, jobName)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cuttlefishOrchestratorDrainPollInterval):
		}
	}
}

func evaluateCleanupJobStatus(job *unstructured.Unstructured) (done bool, failedMsg string, err error) {
	if job == nil {
		return false, "", fmt.Errorf("nil cleanup Job")
	}
	conditions, found, err := unstructured.NestedSlice(job.Object, "status", "conditions")
	if err != nil {
		return false, "", fmt.Errorf("read Job status.conditions: %w", err)
	}
	if found {
		for _, item := range conditions {
			cond, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			condType, _ := cond["type"].(string)
			condStatus, _ := cond["status"].(string)
			if condStatus != "True" {
				continue
			}
			if condType == "Complete" {
				return true, "", nil
			}
			if condType == "Failed" {
				reason, _ := cond["reason"].(string)
				message, _ := cond["message"].(string)
				if reason != "" || message != "" {
					return false, fmt.Sprintf("%s: %s", reason, message), nil
				}
				return false, "Failed condition is True", nil
			}
		}
	}
	if succeeded, ok := readNumericField(job.Object, "status", "succeeded"); ok && succeeded >= 1 {
		return true, "", nil
	}
	return false, "", nil
}

func readNumericField(obj map[string]interface{}, fields ...string) (int64, bool) {
	val, found, err := unstructured.NestedFieldNoCopy(obj, fields...)
	if err != nil || !found || val == nil {
		return 0, false
	}
	switch v := val.(type) {
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	default:
		return 0, false
	}
}

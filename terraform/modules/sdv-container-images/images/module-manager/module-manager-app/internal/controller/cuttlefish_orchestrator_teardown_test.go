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
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func withFastCCODrain(t *testing.T, poll, apiTimeout, jobTimeout time.Duration) {
	t.Helper()
	oldPoll, oldAPI, oldJob := cuttlefishOrchestratorDrainPollInterval, cuttlefishOrchestratorAPIDrainTimeout, cuttlefishOrchestratorCleanupJobTimeout
	cuttlefishOrchestratorDrainPollInterval = poll
	cuttlefishOrchestratorAPIDrainTimeout = apiTimeout
	cuttlefishOrchestratorCleanupJobTimeout = jobTimeout
	t.Cleanup(func() {
		cuttlefishOrchestratorDrainPollInterval = oldPoll
		cuttlefishOrchestratorAPIDrainTimeout = oldAPI
		cuttlefishOrchestratorCleanupJobTimeout = oldJob
	})
}

func ccoTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := appsv1.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := batchv1.AddToScheme(s); err != nil {
		panic(err)
	}
	s.AddKnownTypeWithName(argoCDApplicationGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: argoCDApplicationGVK.Group, Version: argoCDApplicationGVK.Version, Kind: "ApplicationList",
	}, &unstructured.UnstructuredList{})
	return s
}

func ccoTestClient(funcs interceptor.Funcs, objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(ccoTestScheme()).WithObjects(objs...).WithInterceptorFuncs(funcs).Build()
}

func ccoArgoApp(ns, name string, automated map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(argoCDApplicationGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetFinalizers([]string{"resources-finalizer.argocd.argoproj.io"})
	u.Object["spec"] = map[string]interface{}{
		"syncPolicy": map[string]interface{}{
			"automated": automated,
		},
	}
	return u
}

func ccoAPIDeployment(ns string, replicas int64) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ccoAppsDeploymentGVK)
	u.SetNamespace(ns)
	u.SetName(cuttlefishOrchestratorAPIDeploymentName)
	u.Object["spec"] = map[string]interface{}{
		"replicas": replicas,
	}
	return u
}

func ccoAPIPod(ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"})
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetLabels(map[string]string{
		cuttlefishOrchestratorAPILabelKey: cuttlefishOrchestratorAPILabelValue,
	})
	return u
}

func ccoCleanupCronJob(ns string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ccoBatchCronJobGVK)
	u.SetNamespace(ns)
	u.SetName(cuttlefishOrchestratorCleanupCronJobName)
	u.SetUID("cronjob-uid-1")
	u.Object["spec"] = map[string]interface{}{
		"schedule": "0 0 1 1 *",
		"suspend":  true,
		"jobTemplate": map[string]interface{}{
			"metadata": map[string]interface{}{
				"labels": map[string]interface{}{
					"app.kubernetes.io/component": "cleanup",
				},
			},
			"spec": map[string]interface{}{
				"backoffLimit":          int64(1),
				"activeDeadlineSeconds": int64(1200),
			},
		},
	}
	return u
}

type ccoFakeCatalog struct{}

func (c *ccoFakeCatalog) List(context.Context) ([]CatalogEntry, error) { return nil, nil }
func (c *ccoFakeCatalog) GetPath(_ context.Context, _ string) (string, error) {
	return "gitops/modules/cuttlefish-orchestrator", nil
}

func TestDrainCuttlefishOrchestratorModule_NoOpForOtherModules(t *testing.T) {
	c := ccoTestClient(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			t.Fatal("must not call Get for other modules")
			return nil
		},
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			t.Fatal("must not call List for other modules")
			return nil
		},
	})
	if err := DrainCuttlefishOrchestratorModule(context.Background(), c, "argocd", "", "sample"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPerformModuleDisable_CuttlefishOrchestratorDrainsAndDeletesParent(t *testing.T) {
	for _, tc := range []struct {
		name         string
		moduleConfig string
		argoNS       string
		targetNS     string
		childApp     string
	}{
		{
			name:         "main-environment",
			moduleConfig: "",
			argoNS:       "argocd",
			targetNS:     "cuttlefish-orchestrator",
			childApp:     "mod-cuttlefish-orchestrator-app",
		},
		{
			name:         "sub-environment",
			moduleConfig: "namespacePrefix: dev1-\n",
			argoNS:       "dev1-argocd",
			targetNS:     "dev1-cuttlefish-orchestrator",
			childApp:     "dev1-mod-cuttlefish-orchestrator-app",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MODULE_CONFIG", "")
			withFastCCODrain(t, 5*time.Millisecond, 500*time.Millisecond, 500*time.Millisecond)

			parent := ccoArgoApp(tc.argoNS, "mod-cuttlefish-orchestrator", map[string]interface{}{"prune": true})
			child := ccoArgoApp(tc.argoNS, tc.childApp, map[string]interface{}{"prune": true})
			dep := ccoAPIDeployment(tc.targetNS, 1)
			pod := ccoAPIPod(tc.targetNS, "cuttlefish-orchestrator-api-abc")
			cj := ccoCleanupCronJob(tc.targetNS)

			var mu sync.Mutex
			var events []string
			var createdJobName string

			var c client.Client
			funcs := interceptor.Funcs{
				Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if err := cl.Patch(ctx, obj, patch, opts...); err != nil {
						return err
					}
					if obj.GetObjectKind().GroupVersionKind().Kind == "Deployment" && obj.GetName() == cuttlefishOrchestratorAPIDeploymentName {
						mu.Lock()
						events = append(events, "scaled-api-0")
						mu.Unlock()
						// Simulate kubelet terminating the API pod once scaled down, while asserting
						// parent + child were already paused before scale-down.
						pCheck := &unstructured.Unstructured{}
						pCheck.SetGroupVersionKind(argoCDApplicationGVK)
						if err := cl.Get(ctx, client.ObjectKey{Namespace: tc.argoNS, Name: "mod-cuttlefish-orchestrator"}, pCheck); err != nil {
							return err
						}
						if pCheck.GetAnnotations()[argoCDSkipReconcileAnnotationKey] != "true" {
							t.Errorf("parent Application skip-reconcile must be true before API scale-down")
						}
						cCheck := &unstructured.Unstructured{}
						cCheck.SetGroupVersionKind(argoCDApplicationGVK)
						if err := cl.Get(ctx, client.ObjectKey{Namespace: tc.argoNS, Name: tc.childApp}, cCheck); err != nil {
							return err
						}
						if cCheck.GetAnnotations()[argoCDSkipReconcileAnnotationKey] != "true" {
							t.Errorf("child Application skip-reconcile must be true before API scale-down")
						}
						return cl.Delete(ctx, ccoAPIPod(tc.targetNS, "cuttlefish-orchestrator-api-abc"))
					}
					return nil
				},
				Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if err := cl.Create(ctx, obj, opts...); err != nil {
						return err
					}
					if obj.GetObjectKind().GroupVersionKind().Kind == "Job" {
						mu.Lock()
						createdJobName = obj.GetName()
						events = append(events, "created-cleanup-job")
						mu.Unlock()

						// Ensure API pod is gone before cleanup Job starts.
						pods := &unstructured.UnstructuredList{}
						pods.SetGroupVersionKind(ccoCorePodListGVK)
						if err := cl.List(ctx, pods, client.InNamespace(tc.targetNS)); err != nil {
							return err
						}
						if len(pods.Items) != 0 {
							t.Errorf("cleanup Job must not be created while %d API pods still exist", len(pods.Items))
						}
					}
					return nil
				},
				Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					err := cl.Get(ctx, key, obj, opts...)
					if err != nil {
						return err
					}
					mu.Lock()
					jName := createdJobName
					mu.Unlock()
					if obj.GetObjectKind().GroupVersionKind().Kind == "Job" && jName != "" && key.Name == jName {
						u := obj.(*unstructured.Unstructured)
						_ = unstructured.SetNestedSlice(u.Object, []interface{}{
							map[string]interface{}{
								"type":   "Complete",
								"status": "True",
							},
						}, "status", "conditions")
					}
					return nil
				},
			}
			c = ccoTestClient(funcs, parent, child, dep, pod, cj)
			store := &cwsFakeStateStore{st: &State{
				EnabledModules: []string{"mod-cco"},
				ModuleIDs:      map[string]string{cuttlefishOrchestratorModuleName: "mod-cco"},
			}}

			if err := PerformModuleDisable(context.Background(), c, store, &ccoFakeCatalog{}, tc.argoNS, "module-manager", cuttlefishOrchestratorModuleName, "mod-cco", false, tc.moduleConfig); err != nil {
				t.Fatalf("PerformModuleDisable failed: %v", err)
			}

			if len(store.st.EnabledModules) != 0 || store.updates != 1 {
				t.Errorf("expected module disabled with 1 state update, got enabled=%v updates=%d", store.st.EnabledModules, store.updates)
			}
			if p, ok := cwsExists2(t, c, argoCDApplicationGVK, tc.argoNS, "mod-cuttlefish-orchestrator"); ok && p.GetDeletionTimestamp().IsZero() {
				t.Error("expected parent Application mod-cuttlefish-orchestrator to be deleted or terminating")
			}

			gotChild, ok := cwsExists2(t, c, argoCDApplicationGVK, tc.argoNS, tc.childApp)
			if !ok {
				t.Fatal("expected child Application to still exist (for Argo cascade delete)")
			}
			if _, hasSkip := gotChild.GetAnnotations()[argoCDSkipReconcileAnnotationKey]; hasSkip {
				t.Errorf("expected child skip-reconcile cleared after drain, got %v", gotChild.GetAnnotations())
			}
			if auto, found, _ := unstructured.NestedMap(gotChild.Object, "spec", "syncPolicy", "automated"); found && auto != nil {
				t.Errorf("expected child automated sync disabled after drain, got %v", auto)
			}

			mu.Lock()
			gotEvents := append([]string(nil), events...)
			jName := createdJobName
			mu.Unlock()
			if len(gotEvents) != 2 || gotEvents[0] != "scaled-api-0" || gotEvents[1] != "created-cleanup-job" {
				t.Errorf("unexpected event sequence: %v", gotEvents)
			}
			if jName == "" {
				t.Fatal("expected cleanup Job to be created")
			}
			jobObj, ok := cwsExists2(t, c, ccoBatchJobGVK, tc.targetNS, jName)
			if !ok {
				t.Fatalf("expected cleanup Job %s/%s to exist", tc.targetNS, jName)
			}
			if ttl, ok := readNumericField(jobObj.Object, "spec", "ttlSecondsAfterFinished"); !ok || ttl != 300 {
				t.Errorf("expected ttlSecondsAfterFinished=300, got %d (ok=%v)", ttl, ok)
			}
		})
	}
}

func TestPerformModuleDisable_CuttlefishOrchestratorCleanupJobFailureKeepsEnabledAndResumesApps(t *testing.T) {
	t.Setenv("MODULE_CONFIG", "")
	withFastCCODrain(t, 5*time.Millisecond, 200*time.Millisecond, 200*time.Millisecond)

	parent := ccoArgoApp("argocd", "mod-cuttlefish-orchestrator", map[string]interface{}{"prune": true})
	child := ccoArgoApp("argocd", "mod-cuttlefish-orchestrator-app", map[string]interface{}{"prune": true})
	dep := ccoAPIDeployment("cuttlefish-orchestrator", 0)
	cj := ccoCleanupCronJob("cuttlefish-orchestrator")

	var jobName string
	c := ccoTestClient(interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if obj.GetObjectKind().GroupVersionKind().Kind == "Job" {
				jobName = obj.GetName()
			}
			return cl.Create(ctx, obj, opts...)
		},
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			err := cl.Get(ctx, key, obj, opts...)
			if err != nil {
				return err
			}
			if obj.GetObjectKind().GroupVersionKind().Kind == "Job" && jobName != "" && key.Name == jobName {
				u := obj.(*unstructured.Unstructured)
				_ = unstructured.SetNestedSlice(u.Object, []interface{}{
					map[string]interface{}{
						"type":    "Failed",
						"status":  "True",
						"reason":  "BackoffLimitExceeded",
						"message": "Job has reached the specified backoff limit",
					},
				}, "status", "conditions")
			}
			return nil
		},
	}, parent, child, dep, cj)

	store := &cwsFakeStateStore{st: &State{
		EnabledModules: []string{"mod-cco"},
		ModuleIDs:      map[string]string{cuttlefishOrchestratorModuleName: "mod-cco"},
	}}

	err := PerformModuleDisable(context.Background(), c, store, &ccoFakeCatalog{}, "argocd", "module-manager", cuttlefishOrchestratorModuleName, "mod-cco", false, "")
	if err == nil || !strings.Contains(err.Error(), "BackoffLimitExceeded") {
		t.Fatalf("expected failed cleanup Job error, got %v", err)
	}
	if store.updates != 0 || len(store.st.EnabledModules) != 1 {
		t.Errorf("module state must stay enabled on cleanup failure (updates=%d enabled=%v)", store.updates, store.st.EnabledModules)
	}

	gotParent, ok := cwsExists2(t, c, argoCDApplicationGVK, "argocd", "mod-cuttlefish-orchestrator")
	if !ok {
		t.Fatal("parent Application must not be deleted when cleanup fails")
	}
	if _, hasSkip := gotParent.GetAnnotations()[argoCDSkipReconcileAnnotationKey]; hasSkip {
		t.Errorf("parent skip-reconcile must be cleared after failure, got %v", gotParent.GetAnnotations())
	}

	gotChild, ok := cwsExists2(t, c, argoCDApplicationGVK, "argocd", "mod-cuttlefish-orchestrator-app")
	if !ok {
		t.Fatal("child Application must remain after cleanup failure")
	}
	if _, hasSkip := gotChild.GetAnnotations()[argoCDSkipReconcileAnnotationKey]; hasSkip {
		t.Errorf("child skip-reconcile must be cleared after failure, got %v", gotChild.GetAnnotations())
	}
}

func TestPerformModuleDisable_CuttlefishOrchestratorAPIPodTimeoutAbortsBeforeJob(t *testing.T) {
	t.Setenv("MODULE_CONFIG", "")
	withFastCCODrain(t, 5*time.Millisecond, 25*time.Millisecond, 200*time.Millisecond)

	parent := ccoArgoApp("argocd", "mod-cuttlefish-orchestrator", map[string]interface{}{"prune": true})
	child := ccoArgoApp("argocd", "mod-cuttlefish-orchestrator-app", map[string]interface{}{"prune": true})
	dep := ccoAPIDeployment("cuttlefish-orchestrator", 1)
	stuckPod := ccoAPIPod("cuttlefish-orchestrator", "cuttlefish-orchestrator-api-stuck")
	cj := ccoCleanupCronJob("cuttlefish-orchestrator")

	c := ccoTestClient(interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if obj.GetObjectKind().GroupVersionKind().Kind == "Job" {
				t.Fatal("must not start cleanup Job while API pod is still running")
			}
			return cl.Create(ctx, obj, opts...)
		},
	}, parent, child, dep, stuckPod, cj)

	store := &cwsFakeStateStore{st: &State{
		EnabledModules: []string{"mod-cco"},
		ModuleIDs:      map[string]string{cuttlefishOrchestratorModuleName: "mod-cco"},
	}}

	err := PerformModuleDisable(context.Background(), c, store, &ccoFakeCatalog{}, "argocd", "module-manager", cuttlefishOrchestratorModuleName, "mod-cco", false, "")
	if err == nil || !strings.Contains(err.Error(), "waiting for 1 API pod(s) to terminate") {
		t.Fatalf("expected API pod drain timeout error, got %v", err)
	}
	if store.updates != 0 || len(store.st.EnabledModules) != 1 {
		t.Errorf("module state must stay enabled on API drain timeout")
	}
	gotParent, _ := cwsExists2(t, c, argoCDApplicationGVK, "argocd", "mod-cuttlefish-orchestrator")
	if _, hasSkip := gotParent.GetAnnotations()[argoCDSkipReconcileAnnotationKey]; hasSkip {
		t.Errorf("parent skip-reconcile must be cleared after timeout")
	}
}

func TestPerformModuleDisable_CuttlefishOrchestratorPartialInstallSucceeds(t *testing.T) {
	t.Setenv("MODULE_CONFIG", "")
	withFastCCODrain(t, 5*time.Millisecond, 100*time.Millisecond, 100*time.Millisecond)

	parent := ccoArgoApp("argocd", "mod-cuttlefish-orchestrator", map[string]interface{}{"prune": true})
	c := ccoTestClient(interceptor.Funcs{}, parent)
	store := &cwsFakeStateStore{st: &State{
		EnabledModules: []string{"mod-cco"},
		ModuleIDs:      map[string]string{cuttlefishOrchestratorModuleName: "mod-cco"},
	}}

	if err := PerformModuleDisable(context.Background(), c, store, &ccoFakeCatalog{}, "argocd", "module-manager", cuttlefishOrchestratorModuleName, "mod-cco", false, ""); err != nil {
		t.Fatalf("expected partial install disable to succeed, got %v", err)
	}
	if len(store.st.EnabledModules) != 0 {
		t.Errorf("expected module disabled, got %v", store.st.EnabledModules)
	}
	if p, ok := cwsExists2(t, c, argoCDApplicationGVK, "argocd", "mod-cuttlefish-orchestrator"); ok && p.GetDeletionTimestamp().IsZero() {
		t.Error("expected parent Application deleted or terminating")
	}
}

func TestPlatformDrainer_CuttlefishOrchestratorDrainsDuringPlatformTeardown(t *testing.T) {
	t.Setenv("MODULE_CONFIG", "namespacePrefix: qa-\n")
	withFastCCODrain(t, 5*time.Millisecond, 200*time.Millisecond, 200*time.Millisecond)

	parent := ccoArgoApp("qa-argocd", "mod-cuttlefish-orchestrator", map[string]interface{}{"prune": true})
	child := ccoArgoApp("qa-argocd", "qa-mod-cuttlefish-orchestrator-app", map[string]interface{}{"prune": true})
	dep := ccoAPIDeployment("qa-cuttlefish-orchestrator", 1)
	cj := ccoCleanupCronJob("qa-cuttlefish-orchestrator")

	var createdJob string
	c := ccoTestClient(interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if obj.GetObjectKind().GroupVersionKind().Kind == "Job" {
				createdJob = obj.GetName()
			}
			return cl.Create(ctx, obj, opts...)
		},
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			err := cl.Get(ctx, key, obj, opts...)
			if err != nil {
				return err
			}
			if obj.GetObjectKind().GroupVersionKind().Kind == "Job" && createdJob != "" && key.Name == createdJob {
				u := obj.(*unstructured.Unstructured)
				_ = unstructured.SetNestedField(u.Object, int64(1), "status", "succeeded")
			}
			return nil
		},
	}, parent, child, dep, cj)

	store := &cwsFakeStateStore{st: &State{
		EnabledModules: []string{"mod-cco"},
		ModuleIDs:      map[string]string{cuttlefishOrchestratorModuleName: "mod-cco"},
	}}
	drainer := &PlatformDrainer{
		Client:          c,
		APIReader:       c,
		StateStore:      store,
		CatalogStore:    &ccoFakeCatalog{},
		Namespace:       "module-manager",
		ArgoCDNamespace: "qa-argocd",
	}

	if err := drainer.disableOne(context.Background(), cuttlefishOrchestratorModuleName, false); err != nil {
		t.Fatalf("PlatformDrainer.disableOne failed: %v", err)
	}
	if createdJob == "" {
		t.Fatal("expected PlatformDrainer.disableOne to execute cco-cleanup Job")
	}
	gotDep, ok := cwsExists2(t, c, ccoAppsDeploymentGVK, "qa-cuttlefish-orchestrator", cuttlefishOrchestratorAPIDeploymentName)
	if !ok {
		t.Fatal("expected Deployment fixture to remain for inspection")
	}
	if reps, ok := readNumericField(gotDep.Object, "spec", "replicas"); !ok || reps != 0 {
		t.Errorf("expected Deployment scaled to 0, got replicas=%d (ok=%v)", reps, ok)
	}
	if len(store.st.EnabledModules) != 0 {
		t.Errorf("expected module removed from EnabledModules, got %v", store.st.EnabledModules)
	}
	if p, ok := cwsExists2(t, c, argoCDApplicationGVK, "qa-argocd", "mod-cuttlefish-orchestrator"); ok && p.GetDeletionTimestamp().IsZero() {
		t.Error("expected parent Application deleted or terminating")
	}
	nss := drainer.platformDrainNamespaces(context.Background())
	foundCCONS := false
	for _, ns := range nss {
		if ns == "qa-cuttlefish-orchestrator" {
			foundCCONS = true
			break
		}
	}
	if !foundCCONS {
		t.Errorf("expected platformDrainNamespaces to include qa-cuttlefish-orchestrator, got %v", nss)
	}
}

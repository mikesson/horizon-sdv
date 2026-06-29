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
	"testing"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMergeModuleConfigIntoHelmValuesYAML_preservesSoftFeaturesAndRepo(t *testing.T) {
	values := `moduleName: "workloads-android"
config:
  namespacePrefix: "pfx-"
  scm:
    authMethod: userpass
repo:
  url: "https://repo"
  revision: "HEAD"
softFeaturesEnabled:
  sample-soft: true
`
	moduleCfg := `namespacePrefix: "pfx-"
scm:
  authMethod: app
domain: example.com
`
	got, changed, err := mergeModuleConfigIntoHelmValuesYAML(values, moduleCfg)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected merge to change values")
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal([]byte(got), &root); err != nil {
		t.Fatal(err)
	}
	cfg, ok := root["config"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing config: %s", got)
	}
	if cfg["domain"] != "example.com" {
		t.Fatalf("expected domain in config, got %+v", cfg)
	}
	m, ok := cfg["scm"].(map[string]interface{})
	if !ok || m["authMethod"] != "app" {
		t.Fatalf("expected scm.authMethod app, got %+v", cfg)
	}
	if _, ok := root["softFeaturesEnabled"]; !ok {
		t.Fatal("lost softFeaturesEnabled")
	}
	if root["repo"] == nil {
		t.Fatal("lost repo")
	}
}

func TestMergeModuleConfigIntoHelmValuesYAML_noOpWhenEqual(t *testing.T) {
	y := `config:
  scm:
    authMethod: pat
moduleName: m
`
	got, changed, err := mergeModuleConfigIntoHelmValuesYAML(y, "scm:\n  authMethod: pat\n")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("unexpected change, got %q", got)
	}
	if got != y {
		t.Fatalf("expected unchanged body, got %q", got)
	}
}

func TestArgoSourcePathTakesModuleConfig(t *testing.T) {
	if !argoSourcePathTakesModuleConfig("gitops/workloads/android") {
		t.Fatal("expected gitops/workloads path")
	}
	if !argoSourcePathTakesModuleConfig("gitops/modules/workloads-common/prepare-github-app-git-creds") {
		t.Fatal("expected gitops/modules path")
	}
	if argoSourcePathTakesModuleConfig("workloads/android/pipelines/builds/aaos_builder/helm") {
		t.Fatal("non-gitops path should not take MODULE_CONFIG merge")
	}
}

func TestSyncApplicationHelmValuesConfig_multiSourceGitOpsPath(t *testing.T) {
	app := &unstructured.Unstructured{}
	app.SetAPIVersion("argoproj.io/v1alpha1")
	app.SetKind("Application")
	app.SetNamespace("argocd")
	app.SetName("workloads-android")
	app.Object["spec"] = map[string]interface{}{
		"project": "horizon-sdv",
		"sources": []interface{}{
			map[string]interface{}{
				"repoURL":        "https://github.com/example/acn-horizon-sdv",
				"targetRevision": "env/sbx",
				"path":           "gitops/workloads/android",
				"helm": map[string]interface{}{
					"values": "config:\n  namespacePrefix: \"\"\n",
				},
			},
			map[string]interface{}{
				"repoURL":        "https://github.com/example/acn-horizon-sdv",
				"targetRevision": "env/sbx",
				"path":           "workloads/android/pipelines/builds/aaos_builder/helm",
				"helm": map[string]interface{}{
					"values": "namespacePrefix: \"\"\n",
				},
			},
		},
		"destination": map[string]interface{}{
			"server":    "https://kubernetes.default.svc",
			"namespace": "workflows",
		},
	}

	c := fake.NewClientBuilder().WithRuntimeObjects(app).Build()

	cfg := "namespacePrefix: \"pfx\"\ndomain: updated.example\n"
	if err := SyncApplicationHelmValuesConfig(context.Background(), c, c, "argocd", "workloads-android", cfg); err != nil {
		t.Fatal(err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetAPIVersion("argoproj.io/v1alpha1")
	updated.SetKind("Application")
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "argocd", Name: "workloads-android"}, updated); err != nil {
		t.Fatal(err)
	}
	sources, found, err := unstructured.NestedSlice(updated.Object, "spec", "sources")
	if err != nil || !found {
		t.Fatalf("spec.sources: err=%v found=%v", err, found)
	}
	first := sources[0].(map[string]interface{})
	helm := first["helm"].(map[string]interface{})
	got := helm["values"].(string)
	var root map[string]interface{}
	if err := yaml.Unmarshal([]byte(got), &root); err != nil {
		t.Fatal(err)
	}
	config := root["config"].(map[string]interface{})
	if config["domain"] != "updated.example" {
		t.Fatalf("expected MODULE_CONFIG in first gitops source, config=%v", config)
	}

	second := sources[1].(map[string]interface{})
	helm2 := second["helm"].(map[string]interface{})
	if helm2["values"].(string) != "namespacePrefix: \"\"\n" {
		t.Fatalf("non-gitops source should be unchanged, got %q", helm2["values"])
	}
}

func TestMergeTargetRevisionIntoHelmValuesYAML_updatesRepoRevision(t *testing.T) {
	values := `moduleName: "workloads-common"
config:
  namespacePrefix: "pfx-"
repo:
  url: "https://repo"
  revision: "env/dev"
`
	got, changed, err := mergeTargetRevisionIntoHelmValuesYAML(values, "env/sbx")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected revision change")
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal([]byte(got), &root); err != nil {
		t.Fatal(err)
	}
	repo := root["repo"].(map[string]interface{})
	if repo["revision"] != "env/sbx" {
		t.Fatalf("repo.revision: got %v", repo["revision"])
	}
	if root["config"] == nil {
		t.Fatal("lost config")
	}
}

func TestMergeTargetRevisionIntoHelmValuesYAML_noOpWhenEqual(t *testing.T) {
	y := "repo:\n  revision: main\n"
	got, changed, err := mergeTargetRevisionIntoHelmValuesYAML(y, "main")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("unexpected change, got %q", got)
	}
	if got != y {
		t.Fatalf("expected unchanged body, got %q", got)
	}
}

func TestSyncApplicationTargetRevision_parentApplication(t *testing.T) {
	app := &unstructured.Unstructured{}
	app.SetAPIVersion("argoproj.io/v1alpha1")
	app.SetKind("Application")
	app.SetNamespace("argocd")
	app.SetName("mod-workloads-common")
	app.SetLabels(map[string]string{"horizon-sdv.io/module": "workloads-common"})
	app.Object["spec"] = map[string]interface{}{
		"source": map[string]interface{}{
			"repoURL":        "https://github.com/example/acn-horizon-sdv",
			"targetRevision": "env/dev",
			"path":           "gitops/modules/workloads-common",
			"helm": map[string]interface{}{
				"values": "repo:\n  url: https://repo\n  revision: env/dev\n",
			},
		},
	}

	c := fake.NewClientBuilder().WithRuntimeObjects(app).Build()
	if err := SyncApplicationTargetRevision(context.Background(), c, c, "argocd", "mod-workloads-common", "env/sbx"); err != nil {
		t.Fatal(err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetAPIVersion("argoproj.io/v1alpha1")
	updated.SetKind("Application")
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "argocd", Name: "mod-workloads-common"}, updated); err != nil {
		t.Fatal(err)
	}
	rev, _, _ := unstructured.NestedString(updated.Object, "spec", "source", "targetRevision")
	if rev != "env/sbx" {
		t.Fatalf("targetRevision: got %q", rev)
	}
	src, _, _ := unstructured.NestedMap(updated.Object, "spec", "source")
	helm := src["helm"].(map[string]interface{})
	var root map[string]interface{}
	if err := yaml.Unmarshal([]byte(helm["values"].(string)), &root); err != nil {
		t.Fatal(err)
	}
	repo := root["repo"].(map[string]interface{})
	if repo["revision"] != "env/sbx" {
		t.Fatalf("repo.revision: got %v", repo["revision"])
	}
}

func TestSyncApplicationTargetRevision_noOpWhenAlreadyEqual(t *testing.T) {
	app := &unstructured.Unstructured{}
	app.SetAPIVersion("argoproj.io/v1alpha1")
	app.SetKind("Application")
	app.SetNamespace("argocd")
	app.SetName("mod-workloads-common")
	app.Object["spec"] = map[string]interface{}{
		"source": map[string]interface{}{
			"targetRevision": "env/sbx",
			"helm": map[string]interface{}{
				"values": "repo:\n  revision: env/sbx\n",
			},
		},
	}
	c := fake.NewClientBuilder().WithRuntimeObjects(app).Build()
	before := app.DeepCopy()
	if err := SyncApplicationTargetRevision(context.Background(), c, c, "argocd", "mod-workloads-common", "env/sbx"); err != nil {
		t.Fatal(err)
	}
	after := &unstructured.Unstructured{}
	after.SetAPIVersion("argoproj.io/v1alpha1")
	after.SetKind("Application")
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "argocd", Name: "mod-workloads-common"}, after); err != nil {
		t.Fatal(err)
	}
	if after.GetResourceVersion() != before.GetResourceVersion() {
		t.Fatal("expected no cluster update when revision already matches")
	}
}

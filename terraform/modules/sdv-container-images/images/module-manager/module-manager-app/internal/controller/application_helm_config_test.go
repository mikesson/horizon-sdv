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
	"testing"

	"gopkg.in/yaml.v3"
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

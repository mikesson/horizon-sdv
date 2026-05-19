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
//
package api

import "testing"

func TestWorkflowsOverviewNamespaceFromModuleConfig(t *testing.T) {
	t.Parallel()
	if got := workflowsOverviewNamespaceFromModuleConfig(""); got != "workflows" {
		t.Fatalf("empty config: got %q want workflows", got)
	}
	if got := workflowsOverviewNamespaceFromModuleConfig("namespacePrefix: pfx-\n"); got != "pfx-workflows" {
		t.Fatalf("got %q want pfx-workflows", got)
	}
	if got := workflowsOverviewNamespaceFromModuleConfig(`{"namespacePrefix":"env-"}`); got != "env-workflows" {
		t.Fatalf("json: got %q want env-workflows", got)
	}
}

func TestResolveOverviewServiceNamespace_workloadsCoLocatedWithMM(t *testing.T) {
	t.Parallel()
	// Legacy catalog still says "workflows"; Services are in module-manager.
	if got := ResolveOverviewServiceNamespace("workloads-android", "workflows", "domain: x\n", "module-manager"); got != "module-manager" {
		t.Fatalf("got %q want module-manager", got)
	}
	if got := ResolveOverviewServiceNamespace("workloads-android", "", "domain: x\n", "module-manager"); got != "module-manager" {
		t.Fatalf("got %q want module-manager", got)
	}
	// Catalog already correct.
	if got := ResolveOverviewServiceNamespace("workloads-android", "module-manager", "namespacePrefix: sbx-\n", "module-manager"); got != "module-manager" {
		t.Fatalf("got %q want module-manager", got)
	}
}

func TestResolveOverviewServiceNamespace_workloadsFallbackPrefixedWorkflows(t *testing.T) {
	t.Parallel()
	// No mmNamespace (tests); prefix present — legacy {prefix}workflows.
	if got := ResolveOverviewServiceNamespace("workloads-common", "workflows", "namespacePrefix: sbx-\n", ""); got != "sbx-workflows" {
		t.Fatalf("got %q want sbx-workflows", got)
	}
}

func TestResolveOverviewServiceNamespace_nonWorkloadsUsesCatalog(t *testing.T) {
	t.Parallel()
	cfg := "namespacePrefix: sbx-\n"
	if got := ResolveOverviewServiceNamespace("sample", "sample-module-hello", cfg, "module-manager"); got != "sample-module-hello" {
		t.Fatalf("got %q", got)
	}
	if got := ResolveOverviewServiceNamespace("sample", "workflows", cfg, "module-manager"); got != "sbx-workflows" {
		t.Fatalf("bare workflows for non-workloads: got %q want sbx-workflows", got)
	}
}

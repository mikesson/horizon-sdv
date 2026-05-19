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

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// namespacePrefixFromModuleConfig reads MODULE_CONFIG (Helm toYaml of config) as YAML or JSON.
func namespacePrefixFromModuleConfig(moduleConfig string) string {
	moduleConfig = strings.TrimSpace(moduleConfig)
	if moduleConfig == "" {
		return ""
	}
	var m map[string]interface{}
	switch {
	case yaml.Unmarshal([]byte(moduleConfig), &m) == nil:
	case json.Unmarshal([]byte(moduleConfig), &m) == nil:
	default:
		return ""
	}
	if m == nil {
		return ""
	}
	v, ok := m["namespacePrefix"]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

// workflowsOverviewNamespaceFromModuleConfig returns namespacePrefix + "workflows" from MODULE_CONFIG.
func workflowsOverviewNamespaceFromModuleConfig(moduleConfig string) string {
	return namespacePrefixFromModuleConfig(moduleConfig) + "workflows"
}

// ResolveOverviewServiceNamespace picks the namespace Module Manager uses for in-cluster overview HTTP.
//
// Workloads parent charts (mod-workloads-*) deploy overview Services in the same namespace as Module Manager
// (--namespace / chart .Values.namespace). The catalog should list that namespace; if it still has the bare
// name "workflows" or is empty, we fall back to mmNamespace. When MODULE_CONFIG has a namespacePrefix but the
// catalog was never updated, we still support the legacy {prefix}workflows guess only if mmNamespace is empty.
//
// Other modules use the catalog value, with a legacy fix when the catalog has the bare name "workflows" but
// MODULE_CONFIG declares a non-empty namespacePrefix (sample modules in prefixed workflows namespaces).
func ResolveOverviewServiceNamespace(moduleName, catalogOverviewNamespace, moduleConfig, mmNamespace string) string {
	moduleName = strings.TrimSpace(moduleName)
	ns := strings.TrimSpace(catalogOverviewNamespace)
	mmNamespace = strings.TrimSpace(mmNamespace)
	prefix := namespacePrefixFromModuleConfig(moduleConfig)
	wnsFromPrefix := prefix + "workflows"

	if strings.HasPrefix(moduleName, "workloads-") {
		if ns != "" && ns != "workflows" {
			return ns
		}
		if mmNamespace != "" {
			return mmNamespace
		}
		if prefix != "" {
			return wnsFromPrefix
		}
		if ns != "" {
			return ns
		}
		return "workflows"
	}
	if ns == "workflows" && prefix != "" && wnsFromPrefix != ns {
		return wnsFromPrefix
	}
	return ns
}

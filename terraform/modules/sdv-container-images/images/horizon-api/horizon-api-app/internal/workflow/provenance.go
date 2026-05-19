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
package workflow

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// workflowTemplateFromTemplateScope extracts workflow template name from Argo node templateScope.
// Examples:
// - namespaced/workflowtemplate/sample-soft-smoke-test -> sample-soft-smoke-test
// - clusterworkflowtemplate/my-cwt -> my-cwt
func workflowTemplateFromTemplateScope(scope string) string {
	s := strings.TrimSpace(scope)
	if s == "" {
		return ""
	}
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return ""
	}
	kindIdx := len(parts) - 2
	kind := strings.ToLower(strings.TrimSpace(parts[kindIdx]))
	if kind != "workflowtemplate" && kind != "clusterworkflowtemplate" {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

// workflowTemplateResourceNameFromNode returns the WorkflowTemplate (or ClusterWorkflowTemplate)
// resource name for module provenance: Argo sets templateRef.name on nodes that invoke another
// template; inner Pods often omit both templateRef and templateScope on ancestors only have templateRef.
func workflowTemplateResourceNameFromNode(m map[string]interface{}) string {
	if n, _, _ := unstructured.NestedString(m, "templateRef", "name"); strings.TrimSpace(n) != "" {
		return strings.TrimSpace(n)
	}
	scopeStr, _, _ := unstructured.NestedString(m, "templateScope")
	return workflowTemplateFromTemplateScope(scopeStr)
}

// buildNodeParentMap maps each child node id to its parent id using status.nodes[*].children.
func buildNodeParentMap(nodes map[string]interface{}) map[string]string {
	parentOf := make(map[string]string)
	for parentID, raw := range nodes {
		mm, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		children, _, _ := unstructured.NestedStringSlice(mm, "children")
		for _, cid := range children {
			if strings.TrimSpace(cid) != "" {
				parentOf[cid] = parentID
			}
		}
	}
	return parentOf
}

// effectiveWorkflowTemplateForNode returns the WorkflowTemplate name for provenance when a node's own
// templateScope/templateRef is empty — common for Pod steps inside a nested templateRef DAG. We read
// templateRef.name and templateScope on self, then ancestors (closest first), then the boundary node,
// so module labels are not all attributed to the root workflow template (e.g. sample-smoke-test vs sample-soft-smoke-test).
func effectiveWorkflowTemplateForNode(
	nodeID string,
	self map[string]interface{},
	nodes map[string]interface{},
	parentOf map[string]string,
	rootWorkflowTemplate string,
) string {
	if name := workflowTemplateResourceNameFromNode(self); name != "" {
		return name
	}
	for cur := parentOf[nodeID]; cur != ""; cur = parentOf[cur] {
		raw, ok := nodes[cur]
		if !ok {
			break
		}
		mm, ok := raw.(map[string]interface{})
		if !ok {
			break
		}
		if name := workflowTemplateResourceNameFromNode(mm); name != "" {
			return name
		}
	}
	if bid, _, _ := unstructured.NestedString(self, "boundaryID"); bid != "" && bid != nodeID {
		if raw, ok := nodes[bid]; ok {
			if bm, ok := raw.(map[string]interface{}); ok {
				if name := workflowTemplateResourceNameFromNode(bm); name != "" {
					return name
				}
			}
		}
	}
	return strings.TrimSpace(rootWorkflowTemplate)
}

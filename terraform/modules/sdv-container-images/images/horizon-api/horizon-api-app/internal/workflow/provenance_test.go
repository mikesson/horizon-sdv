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
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildNodeParentMap(t *testing.T) {
	t.Parallel()
	nodes := map[string]interface{}{
		"root": map[string]interface{}{
			"children": []interface{}{"mid"},
		},
		"mid": map[string]interface{}{
			"children": []interface{}{"leaf"},
		},
		"leaf": map[string]interface{}{},
	}
	p := buildNodeParentMap(nodes)
	if p["mid"] != "root" || p["leaf"] != "mid" {
		t.Fatalf("parent map: %#v", p)
	}
}

func TestEffectiveWorkflowTemplateForNode_inheritsFromAncestor(t *testing.T) {
	t.Parallel()
	rootTpl := "sample-smoke-test"
	nodes := map[string]interface{}{
		"boundary-1": map[string]interface{}{
			"templateScope": "workflows/workflowtemplate/sample-soft-smoke-test",
			"children":      []interface{}{"inner-pod"},
		},
		"inner-pod": map[string]interface{}{
			"type": "Pod",
			// Many Argo versions omit templateScope on inner Pod nodes under nested templateRef.
		},
	}
	parentOf := buildNodeParentMap(nodes)
	pod, _ := nodes["inner-pod"].(map[string]interface{})
	got := effectiveWorkflowTemplateForNode("inner-pod", pod, nodes, parentOf, rootTpl)
	if got != "sample-soft-smoke-test" {
		t.Fatalf("got %q want sample-soft-smoke-test", got)
	}
}

func TestEffectiveWorkflowTemplateForNode_inheritsTemplateRefFromAncestor(t *testing.T) {
	t.Parallel()
	rootTpl := "sample-smoke-test"
	nodes := map[string]interface{}{
		"invoke-step": map[string]interface{}{
			"type": "Steps",
			"templateRef": map[string]interface{}{
				"name":     "sample-soft-smoke-test",
				"template": "run-smoke-test",
			},
			"children": []interface{}{"inner-pod"},
		},
		"inner-pod": map[string]interface{}{
			"type": "Pod",
		},
	}
	parentOf := buildNodeParentMap(nodes)
	pod, _ := nodes["inner-pod"].(map[string]interface{})
	got := effectiveWorkflowTemplateForNode("inner-pod", pod, nodes, parentOf, rootTpl)
	if got != "sample-soft-smoke-test" {
		t.Fatalf("got %q want sample-soft-smoke-test", got)
	}
}

func TestEffectiveWorkflowTemplateForNode_selfTemplateRefPrecedence(t *testing.T) {
	t.Parallel()
	nodes := map[string]interface{}{
		"n": map[string]interface{}{
			"templateRef": map[string]interface{}{
				"name":     "external-wt",
				"template": "main",
			},
			"templateScope": "ns/workflowtemplate/other-wt",
		},
	}
	parentOf := buildNodeParentMap(nodes)
	nm, _ := nodes["n"].(map[string]interface{})
	got := effectiveWorkflowTemplateForNode("n", nm, nodes, parentOf, "root-wt")
	if got != "external-wt" {
		t.Fatalf("templateRef.name should win; got %q", got)
	}
}

func TestEffectiveWorkflowTemplateForNode_boundaryFallback(t *testing.T) {
	t.Parallel()
	rootTpl := "sample-smoke-test"
	nodes := map[string]interface{}{
		"boundary-root": map[string]interface{}{
			"templateRef": map[string]interface{}{
				"name":     "sample-soft-smoke-test",
				"template": "run-smoke-test",
			},
		},
		"leaf": map[string]interface{}{
			"type":        "Pod",
			"boundaryID":  "boundary-root",
			"children":    nil,
		},
	}
	parentOf := buildNodeParentMap(nodes)
	leaf, _ := nodes["leaf"].(map[string]interface{})
	got := effectiveWorkflowTemplateForNode("leaf", leaf, nodes, parentOf, rootTpl)
	if got != "sample-soft-smoke-test" {
		t.Fatalf("boundary fallback: got %q want sample-soft-smoke-test", got)
	}
}

func TestEffectiveWorkflowTemplateForNode_ownScopeWins(t *testing.T) {
	t.Parallel()
	nodes := map[string]interface{}{
		"p": map[string]interface{}{
			"children": []interface{}{"c"},
		},
		"c": map[string]interface{}{
			"templateScope": "ns/workflowtemplate/other-wt",
		},
	}
	parentOf := buildNodeParentMap(nodes)
	cm, _ := nodes["c"].(map[string]interface{})
	got := effectiveWorkflowTemplateForNode("c", cm, nodes, parentOf, "root-wt")
	if got != "other-wt" {
		t.Fatalf("got %q", got)
	}
}

func TestWorkflowTemplateFromTemplateScope(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"workflows/workflowtemplate/sample-soft-smoke-test": "sample-soft-smoke-test",
		"clusterworkflowtemplate/foo":                     "foo",
		"": "",
	}
	for in, want := range cases {
		if got := workflowTemplateFromTemplateScope(in); got != want {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestNestedStringSlice_children(t *testing.T) {
	t.Parallel()
	m := map[string]interface{}{
		"children": []interface{}{"a", "b"},
	}
	sl, found, err := unstructured.NestedStringSlice(m, "children")
	if err != nil || !found || len(sl) != 2 {
		t.Fatalf("NestedStringSlice: %v found=%v err=%v", sl, found, err)
	}
}

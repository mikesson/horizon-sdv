// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//  http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package workflow

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDisplayPhaseForNode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		rawPhase    string
		isAborted   bool
		message     string
		want        string
	}{
		// Aborted Scenarios
		{"workflow-level shutdown", "Failed", true, "Stopped with strategy 'Stop'", "Aborted"},
		{"pod shutdown message", "Failed", true, "workflow shutdown with strategy:  Stop", "Aborted"},
		{"shutdown-interrupted error", "Error", true, "workflow shutdown with strategy:  Stop", "Aborted"},
		{"stopped status", "Stopped", true, "workflow shutdown with strategy:  Stop", "Aborted"},
		{"empty message fallback", "Failed", true, "", "Aborted"},

		// Genuine States (No Override)
		{"succeeded unchanged", "Succeeded", true, "workflow shutdown with strategy:  Stop", "Succeeded"},
		{"not aborted flag", "Failed", false, "workflow shutdown with strategy:  Stop", "Failed"},
		{"genuine failure before abort", "Failed", true, "main: Error (exit code 1)", "Failed"},
		{"omitted node", "Omitted", true, "omitted: depends condition not met", "Omitted"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nodeInfo := map[string]interface{}{}
			if tc.message != "" {
				nodeInfo["message"] = tc.message
			}

			got := DisplayPhaseForNode(tc.rawPhase, tc.isAborted, nodeInfo)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// mminimal, static representation of an aborted sample smokte test workflow
func mockAbortedWorkflowCR() *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name": "webhook-smoke-4lkdf",
			},
			"spec": map[string]interface{}{
				"shutdown": "Stop",
				"workflowTemplateRef": map[string]interface{}{
					"name": "sample-smoke-test",
				},
			},
			"status": map[string]interface{}{
				"phase":   "Failed",
				"message": "Stopped with strategy 'Stop'",
				"nodes": map[string]interface{}{
					// The root DAG
					"webhook-smoke-4lkdf": map[string]interface{}{
						"type":        "DAG",
						"displayName": "webhook-smoke-4lkdf",
						"phase":       "Failed",
					},
					// The interrupted active pod
					"webhook-smoke-4lkdf-2594344499": map[string]interface{}{
						"type":        "Pod",
						"displayName": "log-parameters",
						"phase":       "Failed",
						"message":     "workflow shutdown with strategy:  Stop",
						"outputs": map[string]interface{}{
							"artifacts": []interface{}{
								map[string]interface{}{
									"name": "main-logs",
									"gcs": map[string]interface{}{
										"key": "webhook-smoke-4lkdf/webhook-smoke-4lkdf-log-parameters-2594344499/main.log",
									},
								},
							},
						},
					},
					// A downstream skipped step
					"webhook-smoke-4lkdf-2057196057": map[string]interface{}{
						"type":        "Skipped",
						"displayName": "verify-gcs",
						"phase":       "Omitted",
						"message":     "omitted: depends condition not met",
					},
				},
			},
		},
	}
}

func nodePhaseByDisplayName(d WorkflowDetail, displayName string) (string, bool) {
	for _, n := range d.Nodes {
		if n.DisplayName == displayName {
			return n.Phase, true
		}
	}
	return "", false
}

func TestDetail_abortedWorkflow(t *testing.T) {
	d := Detail(mockAbortedWorkflowCR(), "workflows", "artifacts-bucket", nil)

	t.Run("workflow phase remapped to Aborted", func(t *testing.T) {
		if d.Phase != "Aborted" {
			t.Fatalf("got %q want Aborted", d.Phase)
		}
	})

	t.Run("interrupted nodes remapped to Aborted", func(t *testing.T) {
		expectedAborts := []string{"webhook-smoke-4lkdf", "log-parameters"}
		for _, nodeName := range expectedAborts {
			ph, ok := nodePhaseByDisplayName(d, nodeName)
			if !ok {
				t.Fatalf("missing expected node %q", nodeName)
			}
			if ph != "Aborted" {
				t.Fatalf("node %q phase: got %q want Aborted", nodeName, ph)
			}
		}
	})

	t.Run("skipped downstream nodes stay Omitted", func(t *testing.T) {
		ph, ok := nodePhaseByDisplayName(d, "verify-gcs")
		if !ok {
			t.Fatal("missing expected node verify-gcs")
		}
		if ph != "Omitted" {
			t.Fatalf("got %q want Omitted", ph)
		}
	})

	t.Run("archived log step phase matches interrupted node", func(t *testing.T) {
		if d.ArchivedLogs == nil || len(d.ArchivedLogs.Steps) == 0 {
			t.Fatal("ArchivedLogs is nil or empty")
		}

		var targetStep *StepLogLink
		for _, step := range d.ArchivedLogs.Steps {
			if step.DisplayName == "log-parameters" {
				// Copy by value to safely take address, or index directly
				s := step 
				targetStep = &s
				break
			}
		}

		if targetStep == nil {
			t.Fatal("archivedLogs.steps missing 'log-parameters'")
		}
		if targetStep.Phase != "Aborted" {
			t.Fatalf("step phase: got %q want Aborted", targetStep.Phase)
		}
		if targetStep.GcsURI == "" {
			t.Fatal("step GcsURI is empty")
		}
	})
}
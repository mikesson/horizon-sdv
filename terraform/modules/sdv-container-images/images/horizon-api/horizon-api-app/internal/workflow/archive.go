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
	"sort"
	"strings"
	"time"

	"github.com/acn-horizon-sdv/horizon-api/internal/gcs"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// BuildArchivedLogLinks extracts gs:// URIs from Workflow status (outputs.artifacts with gcs keys).
// k8sPodNames may be nil (list endpoints); when set (e.g. GET /workflow/{name}), step PodName is filled via node-id suffix even if status omits podName.
func BuildArchivedLogLinks(u *unstructured.Unstructured, ns, defaultBucket string, k8sPodNames []string) *ArchivedLogLinks {
	name := u.GetName()
	out := &ArchivedLogLinks{Steps: nil}

	nodes, _, _ := unstructured.NestedMap(u.Object, "status", "nodes")
	for nodeID, raw := range nodes {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		typ, _, _ := unstructured.NestedString(m, "type")
		disp, _, _ := unstructured.NestedString(m, "displayName")
		tpl, _, _ := unstructured.NestedString(m, "templateName")
		ph, _, _ := unstructured.NestedString(m, "phase")
		pod, _, _ := unstructured.NestedString(m, "podName")

		arts, _, _ := unstructured.NestedSlice(m, "outputs", "artifacts")
		var stepURI, stepArtName string
		for _, a := range arts {
			am, ok := a.(map[string]interface{})
			if !ok {
				continue
			}
			aname, _, _ := unstructured.NestedString(am, "name")
			if !isLogArtifactName(aname) {
				continue
			}
			uri := artifactGCSURI(am, defaultBucket)
			if uri != "" && stepURI == "" {
				stepURI = uri
				stepArtName = aname
			}
		}
		if typ == "Pod" && stepURI != "" {
			pn := pod
			if pn == "" && len(k8sPodNames) > 0 {
				pn = matchPodNameForNodeID(nodeID, k8sPodNames)
			}
			out.Steps = append(out.Steps, StepLogLink{
				NodeID:       nodeID,
				DisplayName:  disp,
				TemplateName: tpl,
				Phase:        ph,
				PodName:      pn,
				GcsURI:       stepURI,
				ArtifactName: stepArtName,
			})
		}
		// Entry / workflow root node often has id == workflow name — use for combined if it carries a main log.
		if nodeID == name && typ != "Pod" {
			arts2, _, _ := unstructured.NestedSlice(m, "outputs", "artifacts")
			for _, a := range arts2 {
				am, ok := a.(map[string]interface{})
				if !ok {
					continue
				}
				if uri := artifactGCSURI(am, defaultBucket); uri != "" && isLogArtifactNameFromMap(am) {
					out.Combined = &LogURIRef{GcsURI: uri}
					break
				}
			}
		}
	}

	// Heuristic combined: reuse the only step's archived log when there is exactly one pod with logs.
	if out.Combined == nil && len(out.Steps) == 1 && out.Steps[0].GcsURI != "" {
		out.Combined = &LogURIRef{GcsURI: out.Steps[0].GcsURI}
	}

	if len(out.Steps) > 1 {
		sort.SliceStable(out.Steps, func(i, j int) bool {
			ti := stepStartedAt(nodes, out.Steps[i].NodeID)
			tj := stepStartedAt(nodes, out.Steps[j].NodeID)
			if ti.Equal(tj) || (ti.IsZero() && tj.IsZero()) {
				return out.Steps[i].NodeID < out.Steps[j].NodeID
			}
			if ti.IsZero() {
				return false
			}
			if tj.IsZero() {
				return true
			}
			return ti.Before(tj)
		})
	}

	// Multi-step: Argo archives one main.log per pod; there is no single merged object. Expose a shared GCS
	// path prefix (folder) so UIs can open the bucket path containing every step's log for this run.
	if out.Combined == nil && len(out.Steps) >= 2 {
		uris := make([]string, 0, len(out.Steps))
		for _, st := range out.Steps {
			if st.GcsURI != "" {
				uris = append(uris, st.GcsURI)
			}
		}
		if len(uris) >= 2 {
			if p := gcsURIsLongestCommonDirectoryPrefix(uris); p != "" {
				out.Combined = &LogURIRef{GcsURI: p}
			}
		}
	}

	if len(out.Steps) == 0 && (out.Combined == nil || out.Combined.GcsURI == "") {
		return nil
	}
	return out
}

func stepStartedAt(nodes map[string]interface{}, nodeID string) time.Time {
	raw, ok := nodes[nodeID]
	if !ok {
		return time.Time{}
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return time.Time{}
	}
	s, _, _ := unstructured.NestedString(m, "startedAt")
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func gcsURIsLongestCommonDirectoryPrefix(uris []string) string {
	if len(uris) < 2 {
		return ""
	}
	p := strings.TrimSpace(uris[0])
	for _, u := range uris[1:] {
		p = commonStringPrefix(p, strings.TrimSpace(u))
		if p == "" {
			return ""
		}
	}
	if !strings.HasPrefix(p, "gs://") {
		return ""
	}
	i := strings.LastIndex(p, "/")
	if i <= len("gs://") {
		return ""
	}
	out := p[:i+1]
	rest := strings.TrimPrefix(out, "gs://")
	rest = strings.TrimSuffix(rest, "/")
	if !strings.Contains(rest, "/") {
		return ""
	}
	return out
}

func commonStringPrefix(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}

// BuildOutputArtifacts lists all node output artifacts that map to a GCS URI (any name).
func BuildOutputArtifacts(u *unstructured.Unstructured, defaultBucket, rootWorkflowTemplate, rootModule string) []OutputArtifact {
	nodes, _, _ := unstructured.NestedMap(u.Object, "status", "nodes")
	parentOf := buildNodeParentMap(nodes)
	rootTpl := strings.TrimSpace(rootWorkflowTemplate)
	var out []OutputArtifact
	for nodeID, raw := range nodes {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		disp, _, _ := unstructured.NestedString(m, "displayName")
		tpl, _, _ := unstructured.NestedString(m, "templateName")
		originTpl := effectiveWorkflowTemplateForNode(nodeID, m, nodes, parentOf, rootTpl)
		arts, _, _ := unstructured.NestedSlice(m, "outputs", "artifacts")
		for _, a := range arts {
			am, ok := a.(map[string]interface{})
			if !ok {
				continue
			}
			aname, _, _ := unstructured.NestedString(am, "name")
			uri := artifactGCSURI(am, defaultBucket)
			if uri == "" {
				continue
			}
			oa := OutputArtifact{
				NodeID:       nodeID,
				Name:         aname,
				DisplayName:  disp,
				Module:       rootModule,
				TemplateName: tpl,
				WorkflowTemplate: originTpl,
				GcsURI:       uri,
			}
			if fn := gcs.ObjectBaseName(uri); fn != "" {
				oa.FileName = fn
			}
			out = append(out, oa)
		}
	}
	return out
}

func isLogArtifactName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	// Default Argo container / stdout archive name; no "log" substring.
	if n == "main" {
		return true
	}
	return strings.Contains(n, "log") || n == "main-logs" || n == "mainlogs"
}

func isLogArtifactNameFromMap(am map[string]interface{}) bool {
	n, _, _ := unstructured.NestedString(am, "name")
	return isLogArtifactName(n)
}

func artifactGCSURI(am map[string]interface{}, defaultBucket string) string {
	gcs, found, err := unstructured.NestedMap(am, "gcs")
	if err == nil && found && len(gcs) > 0 {
		b, _, _ := unstructured.NestedString(gcs, "bucket")
		k, _, _ := unstructured.NestedString(gcs, "key")
		if b == "" {
			b = defaultBucket
		}
		if b != "" && k != "" {
			return "gs://" + b + "/" + strings.TrimPrefix(k, "/")
		}
	}
	s3, s3Found, s3Err := unstructured.NestedMap(am, "s3")
	if s3Err == nil && s3Found && len(s3) > 0 && defaultBucket != "" {
		k, _, _ := unstructured.NestedString(s3, "key")
		if k != "" {
			return "gs://" + defaultBucket + "/" + strings.TrimPrefix(k, "/")
		}
	}
	return ""
}

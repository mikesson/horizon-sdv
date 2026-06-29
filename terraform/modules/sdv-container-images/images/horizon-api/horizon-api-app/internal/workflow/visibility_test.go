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
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestIsHorizonClientVisible(t *testing.T) {
	u1 := &unstructured.Unstructured{}
	u1.SetLabels(map[string]string{LabelSubmittedFrom: SubmittedFromAPI})
	if !IsHorizonClientVisible(u1) {
		t.Fatal("api label should be visible")
	}
	u2 := &unstructured.Unstructured{}
	u2.SetLabels(map[string]string{"workflows.argoproj.io/submit-from-ui": "true"})
	if IsHorizonClientVisible(u2) {
		t.Fatal("Argo UI label alone must not be visible")
	}
	u3 := &unstructured.Unstructured{}
	if IsHorizonClientVisible(u3) {
		t.Fatal("missing label must not be visible")
	}
}

func TestParseSubmittedByHeader(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	if v := ParseSubmittedByHeader(r); v != "" {
		t.Fatalf("default: got %q, want empty", v)
	}
	r.Header.Set(HeaderSubmittedBy, "jsmith")
	if v := ParseSubmittedByHeader(r); v != "jsmith" {
		t.Fatalf("username: got %q, want jsmith", v)
	}
}

func TestParseSubmittedFromHeader(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	v, err := ParseSubmittedFromHeader(r)
	if err != nil || v != SubmittedFromAPI {
		t.Fatalf("default: got %q err %v", v, err)
	}
	r.Header.Set("X-Horizon-Submitted-From", "developer-portal")
	v, err = ParseSubmittedFromHeader(r)
	if err != nil || v != SubmittedFromDeveloperPortal {
		t.Fatalf("portal: got %q err %v", v, err)
	}
	r.Header.Set("X-Horizon-Submitted-From", "not a valid label!!!")
	if _, err := ParseSubmittedFromHeader(r); err == nil {
		t.Fatal("expected error for invalid label header")
	}
}

func TestParseSubmittedFromValue(t *testing.T) {
	v, err := ParseSubmittedFromValue("rest-api")
	if err != nil || v != SubmittedFromAPI {
		t.Fatalf("rest-api: got %q err %v", v, err)
	}
	v, err = ParseSubmittedFromValue("portal")
	if err != nil || v != SubmittedFromDeveloperPortal {
		t.Fatalf("portal: got %q err %v", v, err)
	}
	v, err = ParseSubmittedFromValue("HORIZON_CLI")
	if err != nil || v != SubmittedFromHorizonCLI {
		t.Fatalf("horizon_cli: got %q err %v", v, err)
	}
	v, err = ParseSubmittedFromValue("workloads-android")
	if err != nil || v != "workloads-android" {
		t.Fatalf("custom label: got %q err %v", v, err)
	}
	v, err = ParseSubmittedFromValue("string")
	if err != nil || v != "string" {
		t.Fatalf("custom label string: got %q err %v", v, err)
	}
	if _, err := ParseSubmittedFromValue("not a label!!!"); err == nil {
		t.Fatal("expected error for invalid Kubernetes label value")
	}
	if _, err := ParseSubmittedFromValue(""); err == nil {
		t.Fatal("expected error for empty submitted-from value")
	}
}

func TestParseSubmittedFromHeaderCustom(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("X-Horizon-Submitted-From", "custom")
	if _, err := ParseSubmittedFromHeader(r); err == nil {
		t.Fatal("expected error when custom without detail")
	}
	r.Header.Set("X-Horizon-Submitted-From-Detail", "my-integration")
	v, err := ParseSubmittedFromHeader(r)
	if err != nil || v != "my-integration" {
		t.Fatalf("custom+detail: got %q err %v", v, err)
	}
}

func TestIsHorizonClientVisibleCustomLabel(t *testing.T) {
	u := &unstructured.Unstructured{}
	u.SetLabels(map[string]string{LabelSubmittedFrom: "my-ci"})
	if !IsHorizonClientVisible(u) {
		t.Fatal("custom valid label should be visible")
	}
	u2 := &unstructured.Unstructured{}
	u2.SetLabels(map[string]string{LabelSubmittedFrom: "bad!!!"})
	if IsHorizonClientVisible(u2) {
		t.Fatal("invalid label value must not be visible")
	}
}

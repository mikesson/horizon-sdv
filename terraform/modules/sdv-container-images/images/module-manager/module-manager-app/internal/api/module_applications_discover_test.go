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

package api

import (
	"testing"
)

func TestMergeModuleApplications_dedupeURL(t *testing.T) {
	cat := []ModuleApplication{{ID: "a", Title: "A", URL: "/a"}}
	argo := []ModuleApplication{{ID: "b", Title: "B", URL: "/a"}}
	got := mergeModuleApplications(cat, argo)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("got %#v, want single catalog entry when URL duplicates", got)
	}
}

func TestMergeModuleApplications_dedupeID(t *testing.T) {
	cat := []ModuleApplication{{ID: "same", Title: "C", URL: "/c"}}
	argo := []ModuleApplication{{ID: "same", Title: "D", URL: "/d"}}
	got := mergeModuleApplications(cat, argo)
	if len(got) != 1 || got[0].URL != "/c" {
		t.Fatalf("got %#v, want catalog wins on same id", got)
	}
}

func TestMergeModuleApplications_orderAndAppend(t *testing.T) {
	cat := []ModuleApplication{{ID: "z", Title: "", URL: "/z"}}
	argo := []ModuleApplication{{ID: "m", Title: "M", URL: "/m"}}
	got := mergeModuleApplications(cat, argo)
	if len(got) != 2 {
		t.Fatalf("got len %d", len(got))
	}
	if got[0].ID != "z" || got[1].ID != "m" {
		t.Fatalf("order: got %#v", got)
	}
}

func TestMergeModuleApplications_empty(t *testing.T) {
	if mergeModuleApplications(nil, nil) != nil {
		t.Fatal("expected nil")
	}
}

func TestDisplayTitleFromArgoAppName(t *testing.T) {
	if got := displayTitleFromArgoAppName("mod-sample-module-hello-world", "sample-module"); got != "hello-world" {
		t.Fatalf("got %q", got)
	}
	if got := displayTitleFromArgoAppName("other", "sample-module"); got != "other" {
		t.Fatalf("got %q", got)
	}
}

// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestE2E_MachineTypeListForwardsFilters(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/machine-types" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("gpu_support") != "true" || q.Get("architecture") != "x86" || q.Get("family") != "g2" {
			t.Errorf("filters not forwarded: %s", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"items":[{"id":"g2-standard-8","family":"g2","name":"G2 Standard 8",
			"category":"accelerator_optimized","architecture":"x86","vcpus":8,"memory_gb":32,
			"gpu_support":true,"gpu_mode":"integrated","integrated_gpu_type":"nvidia-l4",
			"integrated_gpu_count":1,"is_default":false,"is_enabled":true,"sort_order":10,
			"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]}`)
	})
	out, err := runCLI(t, backend, "machine-type", "list", "--gpu-support", "true", "--architecture", "x86", "--family", "g2")
	if err != nil {
		t.Fatalf("machine-type list: %v", err)
	}
	if !strings.Contains(out, "g2-standard-8") || !strings.Contains(out, "nvidia-l4 x1 (integrated)") {
		t.Errorf("table missing the entry or its GPU rendering:\n%s", out)
	}
}

func TestE2E_MachineTypeGetAndDefault(t *testing.T) {
	body := `{"id":"n1-standard-8","family":"n1","name":"N1 Standard 8","category":"general_purpose",
		"architecture":"x86","vcpus":8,"memory_gb":30,"gpu_support":true,"gpu_mode":"attachable",
		"default_gpu_type":"nvidia-tesla-t4","max_gpus":4,"is_default":true,"is_enabled":true,
		"sort_order":1,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
	var gotPath string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		gotPath = r.URL.Path
		fmt.Fprint(w, body)
	})

	out, err := runCLI(t, backend, "machine-type", "get", "n1-standard-8")
	if err != nil {
		t.Fatalf("machine-type get: %v", err)
	}
	if gotPath != "/v2/machine-types/n1-standard-8" {
		t.Errorf("get hit %s", gotPath)
	}
	if !strings.Contains(out, "nvidia-tesla-t4 (attachable, up to 4)") || !strings.Contains(out, "Default:      yes") {
		t.Errorf("detail output incomplete:\n%s", out)
	}

	if _, err := runCLI(t, backend, "machine-type", "default"); err != nil {
		t.Fatalf("machine-type default: %v", err)
	}
	if gotPath != "/v2/machine-types/default" {
		t.Errorf("default hit %s", gotPath)
	}
}

func TestE2E_MachineTypeFamilies(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/machine-families" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"families":[{"family":"g2","name":"G2","category":"accelerator_optimized",
			"architecture":"x86","gpu_support":true,"type_count":7}]}`)
	})
	out, err := runCLI(t, backend, "machine-type", "families")
	if err != nil {
		t.Fatalf("machine-type families: %v", err)
	}
	if !strings.Contains(out, "g2") || !strings.Contains(out, "7") {
		t.Errorf("families table incomplete:\n%s", out)
	}
}

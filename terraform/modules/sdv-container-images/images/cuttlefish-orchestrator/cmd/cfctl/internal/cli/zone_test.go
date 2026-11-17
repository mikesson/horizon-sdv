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

func TestE2E_ZoneList(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/zones/availability" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		q := r.URL.Query()
		fmt.Fprintf(w, `{"zones":[{"zone":"europe-west4-a","region":"europe-west4","status":"available",
			"is_enabled":true,"machine_type_available":%t,"gpu_available":%t,"gpu_max_count":4}]}`,
			q.Get("machine_type") != "", q.Get("gpu_type") != "")
	})

	// Without filters the availability columns are absent. Their wire fields
	// are omitempty, so a missing value would print as false.
	out, err := runCLI(t, backend, "zone", "list")
	if err != nil {
		t.Fatalf("zone list: %v", err)
	}
	if !strings.Contains(out, "europe-west4-a") || strings.Contains(out, "MACHINE TYPE") || strings.Contains(out, "MAX GPUS") {
		t.Errorf("filterless table wrong:\n%s", out)
	}

	out, err = runCLI(t, backend, "zone", "list", "--machine-type", "g2-standard-8", "--gpu-type", "nvidia-l4")
	if err != nil {
		t.Fatalf("zone list with filters: %v", err)
	}
	if !strings.Contains(out, "MACHINE TYPE") || !strings.Contains(out, "MAX GPUS") || !strings.Contains(out, "available") {
		t.Errorf("filtered table missing availability columns:\n%s", out)
	}
}

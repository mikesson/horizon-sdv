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

func TestE2E_QuotaShow(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/quota" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"scope":"platform","scope_id":"global","max_hosts":20,"used_hosts":7,
			"available":13,"percentage":35,"high_water_used_hosts":11,"high_water_at":"2026-07-29T10:00:00Z"}`)
	})
	out, err := runCLI(t, backend, "quota", "show")
	if err != nil {
		t.Fatalf("quota show: %v", err)
	}
	for _, want := range []string{"Max Hosts:    20", "Used:         7", "Available:    13", "Usage:        35%", "11 hosts at 2026-07-29T10:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

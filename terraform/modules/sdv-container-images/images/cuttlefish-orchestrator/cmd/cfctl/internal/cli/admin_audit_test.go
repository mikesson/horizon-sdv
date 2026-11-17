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
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	adminfeature "cloud-cuttlefish-orchestrator/internal/features/admin"
)

func TestE2E_AdminAuditSendsFiltersAndPrintsPage(t *testing.T) {
	var gotQuery url.Values
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.URL.Path != "/v2/admin/audit" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `{"items":[
			{"id":"a1","timestamp":"2026-07-31T10:00:00Z","actor_email":"x@y","action":"device.create","resource_type":"device","resource_id":"d1"},
			{"id":"a2","timestamp":"2026-07-31T09:00:00Z","actor_email":"z@y","action":"system_config.update","resource_type":"system"}],
			"total":120,"limit":2,"offset":0}`)
	})
	out, err := runCLI(t, backend, "admin", "audit",
		"--actor", "x@y", "--resource-type", "device", "--since", "2026-07-30T00:00:00Z", "--limit", "2")
	if err != nil {
		t.Fatalf("admin audit: %v", err)
	}
	for param, want := range map[string]string{
		"actor_email": "x@y", "resource_type": "device",
		"start_time": "2026-07-30T00:00:00Z", "limit": "2",
	} {
		if got := gotQuery.Get(param); got != want {
			t.Errorf("query %s = %q, want %q", param, got, want)
		}
	}
	for _, want := range []string{"device.create", "device/d1", "system_config.update", "Showing 2 of 120"} {
		if !strings.Contains(out, want) {
			t.Errorf("audit output missing %q:\n%s", want, out)
		}
	}
}

func TestAdminAuditRejectsBadFilters(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("rejected invocation still sent %s %s", r.Method, r.URL.Path)
	})
	cases := [][]string{
		{"admin", "audit", "--since", "yesterday"},
		{"admin", "audit", "--until", "2026-07-31"},
		{"admin", "audit", "--limit", "0"},
		{"admin", "audit", "--limit", "201"},
		{"admin", "audit", "--resource-type", "cluster"},
	}
	for _, args := range cases {
		if _, err := runCLI(t, backend, args...); err == nil {
			t.Errorf("%v must be rejected", args)
		}
	}
}

// The CLI copies the server's resource-type vocabulary and page cap into
// struct tags; both copies are live, so this pins them together.
func TestAdminAuditFilterTagsMatchServer(t *testing.T) {
	server, ok := reflect.TypeFor[adminfeature.ListAuditInput]().FieldByName("ResourceType")
	if !ok {
		t.Fatal("server ListAuditInput lost its ResourceType field")
	}
	client, ok := reflect.TypeFor[AdminAuditCmd]().FieldByName("ResourceType")
	if !ok {
		t.Fatal("AdminAuditCmd lost its ResourceType field")
	}
	if s, c := server.Tag.Get("enum"), client.Tag.Get("enum"); s != c {
		t.Errorf("resource-type enum drifted: server %q, cli %q", s, c)
	}

	limit, ok := reflect.TypeFor[adminfeature.ListAuditInput]().FieldByName("Limit")
	if !ok {
		t.Fatal("server ListAuditInput lost its Limit field")
	}
	serverMax, err := strconv.Atoi(limit.Tag.Get("maximum"))
	if err != nil {
		t.Fatalf("server Limit maximum tag: %v", err)
	}
	if serverMax != maxAuditLimit {
		t.Errorf("audit page cap drifted: server %d, cli %d", serverMax, maxAuditLimit)
	}
}

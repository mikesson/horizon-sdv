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
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	hostconfigfeature "cloud-cuttlefish-orchestrator/internal/features/hostconfig"
)

const hostImageN1 = `{"machine_family":"n1","image_family":"cco-host","source_project":"example-project",
	"description":"default host image","enabled":true,
	"created_at":"2026-07-01T00:00:00Z","updated_at":"2026-07-31T08:00:00Z","updated_by":"x@y",
	"resolved":{"ok":true,"project":"example-project","image_name":"cco-host-20260731","architecture":"x86"}}`

func TestE2E_AdminHostImageListAndGet(t *testing.T) {
	var listQuery url.Values
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch r.URL.Path {
		case "/v2/admin/host-image-configs":
			listQuery = r.URL.Query()
			fmt.Fprintf(w, `{"items":[%s],"limit":100,"offset":0,"total":150}`, hostImageN1)
		case "/v2/admin/host-image-configs/n1":
			fmt.Fprint(w, hostImageN1)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	out, err := runCLI(t, backend, "admin", "host-image", "list", "--enabled-only")
	if err != nil {
		t.Fatalf("host-image list: %v", err)
	}
	if listQuery.Get("enabled_only") != "true" {
		t.Errorf("--enabled-only not sent: %v", listQuery)
	}
	if !strings.Contains(out, "cco-host-20260731") || !strings.Contains(out, "Showing 1 of 150") {
		t.Errorf("list output incomplete:\n%s", out)
	}

	out, err = runCLI(t, backend, "admin", "host-image", "get", "n1")
	if err != nil {
		t.Fatalf("host-image get: %v", err)
	}
	if !strings.Contains(out, "default host image") || !strings.Contains(out, "by x@y") ||
		!strings.Contains(out, "cco-host-20260731 (x86)") {
		t.Errorf("get output incomplete:\n%s", out)
	}
}

func TestE2E_AdminHostImageCreate(t *testing.T) {
	var createBody string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.Method != http.MethodPost || r.URL.Path != "/v2/admin/host-image-configs" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		createBody = string(b)
		fmt.Fprint(w, `{"machine_family":"c4a","image_family":"cco-host-arm","enabled":false,
			"created_at":"2026-07-31T09:00:00Z","updated_at":"2026-07-31T09:00:00Z"}`)
	})
	if _, err := runCLI(t, backend, "admin", "host-image", "create", "c4a", "cco-host-arm", "--disabled"); err != nil {
		t.Fatalf("host-image create: %v", err)
	}
	if !strings.Contains(createBody, `"machine_family":"c4a"`) || !strings.Contains(createBody, `"enabled":false`) {
		t.Errorf("create body incomplete: %s", createBody)
	}
}

// The PUT replaces every mutable field, so updating one flag must send the
// current values of the others.
func TestE2E_AdminHostImageUpdateOverlaysUnnamedFields(t *testing.T) {
	var putBody string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, hostImageN1)
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			putBody = string(b)
			fmt.Fprint(w, hostImageN1)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	if _, err := runCLI(t, backend, "admin", "host-image", "update", "n1", "--no-enabled"); err != nil {
		t.Fatalf("host-image update: %v", err)
	}
	for _, want := range []string{
		`"image_family":"cco-host"`, `"source_project":"example-project"`,
		`"description":"default host image"`, `"enabled":false`,
	} {
		if !strings.Contains(putBody, want) {
			t.Errorf("update body missing %s (overlay broken): %s", want, putBody)
		}
	}
}

func TestAdminHostImageRejectsBadInvocations(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("rejected invocation still sent %s %s", r.Method, r.URL.Path)
	})
	if _, err := runCLI(t, backend, "admin", "host-image", "update", "n1"); err == nil {
		t.Error("flagless update must be rejected")
	}
	if _, err := runCLI(t, backend, "admin", "host-image", "list", "--limit", "201"); err == nil {
		t.Error("limit above the page cap must be rejected")
	}
}

func TestE2E_AdminHostImageDeleteConfirms(t *testing.T) {
	deleted := false
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.Method != http.MethodDelete || r.URL.Path != "/v2/admin/host-image-configs/n1" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		deleted = true
		fmt.Fprint(w, `{"message":"deleted"}`)
	})
	if _, err := runCLI(t, backend, "admin", "host-image", "delete", "n1", "--force"); err != nil {
		t.Fatalf("host-image delete: %v", err)
	}
	if !deleted {
		t.Error("delete never reached the server")
	}
}

// The CLI copies the server's listing page cap; this pins the two together.
func TestAdminHostImageLimitMatchesServer(t *testing.T) {
	limit, ok := reflect.TypeFor[hostconfigfeature.ListHostImageConfigsInput]().FieldByName("Limit")
	if !ok {
		t.Fatal("server ListHostImageConfigsInput lost its Limit field")
	}
	serverMax, err := strconv.Atoi(limit.Tag.Get("maximum"))
	if err != nil {
		t.Fatalf("server Limit maximum tag: %v", err)
	}
	if serverMax != maxHostImageLimit {
		t.Errorf("host-image page cap drifted: server %d, cli %d", serverMax, maxHostImageLimit)
	}
}

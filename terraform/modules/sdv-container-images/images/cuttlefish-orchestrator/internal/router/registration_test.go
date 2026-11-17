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

// The tests here drive the REAL router.New over the REAL slice registrations,
// which the rest of this package's tests do not: openapi_test.go asserts spec
// shape against a mockHandler, so nothing there calls router.New at all.
//
// External test package because the wiring lives in tests/specwire, which
// imports this package for the Config type.
package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/router"
	"cloud-cuttlefish-orchestrator/tests/specwire"
)

// chiOnlyHandlers are the Config handler fields that register plain chi routes
// and no Huma operations, so they cannot reach the OpenAPI spec and their
// absence from the spec wiring is correct rather than an oversight.
var chiOnlyHandlers = map[string]string{
	"LegacyCOHandler": "byte-exact legacy /v1 surface, chi routes only",
}

// nonHandlerFields are the Config fields that carry no handler at all.
var nonHandlerFields = map[string]bool{
	"AuthConfig": true,
	"Logger":     true,
}

// TestNewDoesNotPanicOnRealRegistrations guards the whole class of Huma
// registration faults, which land at BOOT rather than on a request: huma.Register
// panics eleven different ways while reflecting over the input and output types
// (a pointer form/header/path/query parameter, a non-struct input or output, a
// non-int status field, an unsupported parameter type, and so on).
//
// Nothing else in the backend gates that. Five of the twelve feature slices
// register no routes in their own tests, so `go test ./...` links their
// handlers without ever registering them. Only the cross-stack contract job
// caught this before, by noticing the spec server failed to come up.
func TestNewDoesNotPanicOnRealRegistrations(t *testing.T) {
	t.Parallel()

	var stack []byte
	var recovered any
	handler, err := func() (h http.Handler, err error) {
		defer func() {
			if r := recover(); r != nil {
				recovered, stack = r, debug.Stack()
			}
		}()
		return router.New(specwire.Config())
	}()

	if recovered != nil {
		t.Fatalf("router.New panicked registering the real slices: %v\n\n%s", recovered, stack)
	}
	if err != nil {
		t.Fatalf("router.New: %v", err)
	}
	if handler == nil {
		t.Fatal("router.New returned a nil handler")
	}
}

// TestConfigRegistersEverySlice holds the line that nothing else does.
// registerHumaHandlers nil-checks every field, so a slice left out of the spec
// wiring is silently absent from /openapi.json, and the console's schema-drift
// check then compares its Zod schemas against nothing and reports success by
// not looking. A missing slice is invisible in every other gate.
//
// Fields are classified by name so a new slice is covered the day it is added
// instead of the day someone remembers to extend a list here; an unclassifiable
// field fails instead of being skipped.
func TestConfigRegistersEverySlice(t *testing.T) {
	t.Parallel()

	cfg := specwire.Config()
	v := reflect.ValueOf(cfg)
	typ := v.Type()

	var checked int
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		switch {
		case nonHandlerFields[name]:
		case chiOnlyHandlers[name] != "":
		case strings.HasSuffix(name, "Handler"):
			if v.Field(i).IsNil() {
				t.Errorf("Config.%s is nil in the spec wiring: its operations are absent from "+
					"/openapi.json and the schema-drift check cannot see them", name)
			}
			checked++
		default:
			t.Errorf("Config.%s is neither a *Handler nor a known non-handler field; "+
				"classify it here so it is either registered or explicitly exempt", name)
		}
	}

	// A rename that made every field fall through the classifier would leave
	// the loop asserting nothing at all.
	if checked == 0 {
		t.Fatal("classified no handler fields; the name-based classification broke")
	}
}

// TestSpecCoversEveryDeclaredTag proves the registrations actually reached the
// spec. A slice whose Register became a no-op would still satisfy the wiring
// assertion above while contributing no operations; here its documentation
// group would be left orphaned.
func TestSpecCoversEveryDeclaredTag(t *testing.T) {
	t.Parallel()

	handler, err := router.New(specwire.Config())
	if err != nil {
		t.Fatalf("router.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json = %d, want 200", rec.Code)
	}

	var spec struct {
		Tags  []struct{ Name string } `json:"tags"`
		Paths map[string]map[string]struct {
			Tags []string `json:"tags"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("parsing spec: %v", err)
	}
	if len(spec.Tags) == 0 {
		t.Fatal("spec declares no tags")
	}

	used := make(map[string]bool)
	for _, item := range spec.Paths {
		for _, op := range item {
			for _, tag := range op.Tags {
				used[tag] = true
			}
		}
	}
	for _, tag := range spec.Tags {
		if !used[tag.Name] {
			t.Errorf("tag %q is declared but no operation carries it: the slice that owns "+
				"that group registered nothing", tag.Name)
		}
	}
}

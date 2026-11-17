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

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/router"
	"cloud-cuttlefish-orchestrator/tests/specwire"
)

// TestDumpSpecEmitsOpenAPI30 pins what scripts/generate-ccogen.sh consumes: a
// 3.0.x document (generators do not get the native 3.1) with the operation
// surface actually registered. An empty or 401'd body would otherwise
// generate an empty model set with a green run.
func TestDumpSpecEmitsOpenAPI30(t *testing.T) {
	t.Parallel()
	handler, err := router.New(specwire.Config())
	if err != nil {
		t.Fatalf("creating router: %v", err)
	}

	var buf bytes.Buffer
	if err := dumpSpec(&buf, handler); err != nil {
		t.Fatalf("dumpSpec: %v", err)
	}

	var spec struct {
		OpenAPI string                     `json:"openapi"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(buf.Bytes(), &spec); err != nil {
		t.Fatalf("dump is not valid JSON: %v", err)
	}
	if !strings.HasPrefix(spec.OpenAPI, "3.0.") {
		t.Errorf("openapi version = %q, want a 3.0.x downconvert", spec.OpenAPI)
	}
	if len(spec.Paths) == 0 {
		t.Error("dump has no paths; the router registered nothing")
	}
	if _, ok := spec.Paths["/v2/devices"]; !ok {
		t.Error("dump is missing /v2/devices; wrong document or wiring")
	}
}

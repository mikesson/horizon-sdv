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

package router

import (
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestNewHumaAPI_OpenAPIServerURLOverride(t *testing.T) {
	t.Setenv("CCO_OPENAPI_SERVER_URL", "")
	defaultAPI := NewHumaAPI(chi.NewRouter(), DefaultHumaConfig())
	if got := defaultAPI.OpenAPI().Servers; len(got) != 1 || got[0].URL != "/" {
		t.Fatalf("default Servers = %+v, want single server URL %q", got, "/")
	}

	t.Setenv("CCO_OPENAPI_SERVER_URL", "/cuttlefish-orchestrator/")
	subpathAPI := NewHumaAPI(chi.NewRouter(), DefaultHumaConfig())
	if got := subpathAPI.OpenAPI().Servers; len(got) != 1 || got[0].URL != "/cuttlefish-orchestrator/" {
		t.Fatalf("overridden Servers = %+v, want single server URL %q", got, "/cuttlefish-orchestrator/")
	}

	prefixedAPI := NewHumaAPIWithPrefix(chi.NewRouter(), DefaultHumaConfig(), "/custom-prefix")
	if got := prefixedAPI.OpenAPI().Servers; len(got) != 1 || got[0].URL != "/custom-prefix" {
		t.Fatalf("prefixed Servers = %+v, want single server URL %q", got, "/custom-prefix")
	}
}

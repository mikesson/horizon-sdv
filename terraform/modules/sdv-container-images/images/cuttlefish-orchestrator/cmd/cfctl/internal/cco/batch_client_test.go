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

package cco

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClient_CreateBatch checks the batch create's method, path and request
// body. idempotency_test.go covers the Idempotency-Key header.
func TestClient_CreateBatch(t *testing.T) {
	t.Parallel()
	var gotPath, gotMethod, gotPrefix string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		var req CreateBatchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotPrefix = req.NamePrefix
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(BatchSummary{BatchID: "b-1", Total: 4})
	}))
	defer srv.Close()
	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}

	got, err := c.CreateBatch(t.Context(), CreateBatchRequest{NamePrefix: "ci", Count: 4}, "run-99")
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if got.BatchID != "b-1" {
		t.Errorf("BatchID = %q, want b-1", got.BatchID)
	}
	if gotMethod != http.MethodPost || gotPath != "/v2/batches" {
		t.Errorf("request = %s %s, want POST /v2/batches", gotMethod, gotPath)
	}
	if gotPrefix != "ci" {
		t.Errorf("name_prefix = %q, want ci", gotPrefix)
	}
}

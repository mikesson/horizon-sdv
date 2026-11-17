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

func TestE2E_StorageLsBucketsAndObjects(t *testing.T) {
	var objectsQueries []string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch r.URL.Path {
		case "/v2/storage/buckets":
			fmt.Fprint(w, `{"buckets":[{"name":"cco-artifacts","location":"EUROPE-WEST4","created_at":"2026-06-01T00:00:00Z"}]}`)
		case "/v2/storage/buckets/cco-artifacts/objects":
			objectsQueries = append(objectsQueries, r.URL.RawQuery)
			if r.URL.Query().Get("page_token") == "tok" {
				fmt.Fprint(w, `{"objects":[
					{"name":"bugreports/dev-2.zip","display_name":"dev-2.zip","bucket":"cco-artifacts",
					 "size":2048,"is_folder":false,"gs_uri":"gs://cco-artifacts/bugreports/dev-2.zip","public_url":""}]}`)
				return
			}
			fmt.Fprint(w, `{"objects":[
				{"name":"bugreports/dev-1.zip","display_name":"dev-1.zip","bucket":"cco-artifacts",
				 "size":1048576,"is_folder":false,"updated_at":"2026-07-31T08:00:00Z",
				 "gs_uri":"gs://cco-artifacts/bugreports/dev-1.zip","public_url":""}],
				"next_page_token":"tok"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	out, err := runCLI(t, backend, "storage", "ls")
	if err != nil {
		t.Fatalf("storage ls: %v", err)
	}
	if !strings.Contains(out, "cco-artifacts") || !strings.Contains(out, "EUROPE-WEST4") {
		t.Errorf("bucket list incomplete:\n%s", out)
	}

	out, err = runCLI(t, backend, "storage", "ls", "cco-artifacts", "--prefix", "bugreports/")
	if err != nil {
		t.Fatalf("storage ls bucket: %v", err)
	}
	want := []string{
		"page_size=1000&prefix=bugreports%2F",
		"page_size=1000&page_token=tok&prefix=bugreports%2F",
	}
	if strings.Join(objectsQueries, " ") != strings.Join(want, " ") {
		t.Errorf("object listing queries = %q, want %q", objectsQueries, want)
	}
	// Both pages print, and nothing claims the list was cut short.
	if !strings.Contains(out, "bugreports/dev-1.zip") || !strings.Contains(out, "1048576") ||
		!strings.Contains(out, "bugreports/dev-2.zip") {
		t.Errorf("object list incomplete:\n%s", out)
	}
	if strings.Contains(out, "More objects") {
		t.Errorf("a complete listing still says more objects exist:\n%s", out)
	}
}

func TestE2E_StorageRmEscapesNestedNames(t *testing.T) {
	var deletedPath string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		deletedPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	})
	if _, err := runCLI(t, backend, "storage", "rm", "cco-artifacts", "bugreports/dev-1.zip", "--force"); err != nil {
		t.Fatalf("storage rm: %v", err)
	}
	// The nested name must stay in the single {name} path segment, so the
	// slash is escaped on the wire.
	if deletedPath != "/v2/storage/buckets/cco-artifacts/objects/bugreports%2Fdev-1.zip" {
		t.Errorf("delete path wrong: %s", deletedPath)
	}
}

func TestStorageLsRejectsPrefixWithoutBucket(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("rejected invocation still sent %s %s", r.Method, r.URL.Path)
	})
	if _, err := runCLI(t, backend, "storage", "ls", "--prefix", "x"); err == nil {
		t.Error("--prefix without BUCKET must be rejected")
	}
}

// TestSplitBucketPath covers a gs:// URL, a bucket/prefix path and a bare
// bucket.
func TestSplitBucketPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		arg        string
		flagPrefix string
		wantBucket string
		wantPrefix string
	}{
		{"bare bucket", "builds", "", "builds", ""},
		{"bucket and prefix", "builds/android16", "", "builds", "android16"},
		{"gs url", "gs://builds/android16/", "", "builds", "android16/"},
		{"gs url bucket only", "gs://builds", "", "builds", ""},
		{"flag prefix alone", "builds", "android16/", "builds", "android16/"},
		{"path joins ahead of flag", "builds/a", "b", "builds", "a/b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bucket, prefix := splitBucketPath(tt.arg, tt.flagPrefix)
			if bucket != tt.wantBucket || prefix != tt.wantPrefix {
				t.Errorf("splitBucketPath(%q, %q) = (%q, %q), want (%q, %q)",
					tt.arg, tt.flagPrefix, bucket, prefix, tt.wantBucket, tt.wantPrefix)
			}
		})
	}
}

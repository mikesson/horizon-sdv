// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcs

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/storage"

	storagedomain "cloud-cuttlefish-orchestrator/internal/domain/storage"
)

type gcsFakeServer struct {
	mu          sync.Mutex
	buckets     []string
	objects     map[string][]byte
	ctypes      map[string]string
	denyWrite   bool
	denyBuckets map[string]bool
	srv         *httptest.Server
}

func newGCSFakeServer(t *testing.T) *gcsFakeServer {
	t.Helper()
	f := &gcsFakeServer{
		buckets:     []string{"bucket-a", "bucket-b"},
		objects:     make(map[string][]byte),
		ctypes:      make(map[string]string),
		denyBuckets: make(map[string]bool),
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *gcsFakeServer) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	p := r.URL.Path

	// List buckets: GET /storage/v1/b
	if r.Method == http.MethodGet && p == "/storage/v1/b" {
		type bItem struct {
			Name     string `json:"name"`
			Location string `json:"location"`
		}
		items := make([]bItem, 0, len(f.buckets))
		for _, b := range f.buckets {
			items = append(items, bItem{Name: b, Location: "US"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"kind": "storage#buckets", "items": items})
		return
	}

	// Upload endpoint: POST /upload/storage/v1/b/{bucket}/o
	if r.Method == http.MethodPost && strings.HasPrefix(p, "/upload/storage/v1/b/") {
		rest := strings.TrimPrefix(p, "/upload/storage/v1/b/")
		bucket := strings.TrimSuffix(rest, "/o")
		if f.denyWrite || f.denyBuckets[bucket] {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"write denied"}}`))
			return
		}
		name := r.URL.Query().Get("name")
		ctype := "application/octet-stream"
		var payload []byte
		if mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && strings.HasPrefix(mediaType, "multipart/") {
			mr := multipart.NewReader(r.Body, params["boundary"])
			for idx := 0; ; idx++ {
				part, err := mr.NextPart()
				if err != nil {
					break
				}
				partBytes, _ := io.ReadAll(part)
				if idx == 0 {
					var meta struct {
						Name        string `json:"name"`
						ContentType string `json:"contentType"`
					}
					if json.Unmarshal(partBytes, &meta) == nil {
						if meta.Name != "" {
							name = meta.Name
						}
						if meta.ContentType != "" {
							ctype = meta.ContentType
						}
					}
				} else {
					payload = partBytes
					if partCT := part.Header.Get("Content-Type"); partCT != "" {
						ctype = partCT
					}
				}
			}
		} else {
			payload, _ = io.ReadAll(r.Body)
			if name == "" {
				name = ".cco-write-probe"
			}
		}
		f.objects[bucket+"/"+name] = payload
		f.ctypes[bucket+"/"+name] = ctype
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind":        "storage#object",
			"bucket":      bucket,
			"name":        name,
			"size":        strconv.Itoa(len(f.objects[bucket+"/"+name])),
			"contentType": "application/octet-stream",
		})
		return
	}

	// Direct object metadata/download via storage/v1, download/storage/v1, or XML/emulator /{bucket}/{object}
	if strings.HasPrefix(p, "/storage/v1/b/") || strings.HasPrefix(p, "/download/storage/v1/b/") {
		trimmed := strings.TrimPrefix(p, "/download")
		rest := strings.TrimPrefix(trimmed, "/storage/v1/b/")
		parts := strings.SplitN(rest, "/o/", 2)
		if len(parts) == 1 && strings.HasSuffix(rest, "/o") {
			bucket := strings.TrimSuffix(rest, "/o")
			if f.denyBuckets[bucket] {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":403,"message":"denied"}}`))
				return
			}
			f.handleListObjects(w, r, bucket)
			return
		}
		if len(parts) == 2 {
			bucket, name := parts[0], parts[1]
			f.serveObject(w, r, bucket, name)
			return
		}
	}

	if r.Method == http.MethodGet {
		trimmed := strings.TrimPrefix(p, "/")
		if parts := strings.SplitN(trimmed, "/", 2); len(parts) == 2 {
			if _, ok := f.objects[parts[0]+"/"+parts[1]]; ok {
				f.serveObject(w, r, parts[0], parts[1])
				return
			}
		}
	}

	w.WriteHeader(http.StatusNotFound)
}

func (f *gcsFakeServer) serveObject(w http.ResponseWriter, r *http.Request, bucket, name string) {
	if f.denyBuckets[bucket] {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"denied"}}`))
		return
	}
	key := bucket + "/" + name
	switch r.Method {
	case http.MethodGet:
		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
			return
		}
		if r.URL.Query().Get("alt") == "media" || !strings.HasPrefix(r.URL.Path, "/storage/v1/") {
			w.Header().Set("Content-Type", f.ctypes[key])
			_, _ = w.Write(data)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind":        "storage#object",
			"bucket":      bucket,
			"name":        name,
			"size":        strconv.Itoa(len(data)),
			"contentType": f.ctypes[key],
		})
	case http.MethodDelete:
		if _, ok := f.objects[key]; !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
			return
		}
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *gcsFakeServer) handleListObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	prefix := r.URL.Query().Get("prefix")
	delim := r.URL.Query().Get("delimiter")
	prefixSet := map[string]bool{}
	var items []map[string]any
	for k, v := range f.objects {
		if !strings.HasPrefix(k, bucket+"/") {
			continue
		}
		name := strings.TrimPrefix(k, bucket+"/")
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rem := strings.TrimPrefix(name, prefix)
		if delim != "" {
			if idx := strings.Index(rem, delim); idx >= 0 {
				prefixSet[prefix+rem[:idx+len(delim)]] = true
				continue
			}
		}
		items = append(items, map[string]any{
			"kind":        "storage#object",
			"bucket":      bucket,
			"name":        name,
			"size":        strconv.Itoa(len(v)),
			"contentType": f.ctypes[k],
		})
	}
	var prefixes []string
	for p := range prefixSet {
		prefixes = append(prefixes, p)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"kind":     "storage#objects",
		"items":    items,
		"prefixes": prefixes,
	})
}

func newEmulatorClient(t *testing.T, srv *gcsFakeServer) *Client {
	t.Helper()
	t.Setenv("STORAGE_EMULATOR_HOST", strings.TrimPrefix(srv.srv.URL, "http://"))
	sdk, err := storage.NewClient(t.Context())
	if err != nil {
		t.Fatalf("storage.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdk.Close() })
	return NewClient(sdk, "test-project")
}

func TestClient_BucketsObjectsUploadGetExistsDeleteAndWriteProbe(t *testing.T) {
	srv := newGCSFakeServer(t)
	c := newEmulatorClient(t, srv)

	// ListBuckets
	bucketsResp, err := c.ListBuckets(t.Context())
	if err != nil {
		t.Fatalf("ListBuckets failed: %v", err)
	}
	if len(bucketsResp.Buckets) != 2 || bucketsResp.Buckets[0].Name != "bucket-a" {
		t.Fatalf("unexpected ListBuckets response: %+v", bucketsResp)
	}

	// UploadObject
	if err := c.UploadObject(t.Context(), "bucket-a", "builds/b1/img.zip", strings.NewReader("zip-bytes"), "application/zip"); err != nil {
		t.Fatalf("UploadObject failed: %v", err)
	}
	if err := c.UploadObject(t.Context(), "bucket-a", "builds/readme.txt", strings.NewReader("hello"), "text/plain"); err != nil {
		t.Fatalf("UploadObject(readme) failed: %v", err)
	}
	// Self-prefix entry should be skipped by ListObjects.
	srv.mu.Lock()
	srv.objects["bucket-a/builds/"] = nil
	srv.mu.Unlock()

	// ObjectExists true & false
	exists, err := c.ObjectExists(t.Context(), "bucket-a", "builds/readme.txt")
	if err != nil || !exists {
		t.Fatalf("ObjectExists(present) = (%v, %v), want (true, nil)", exists, err)
	}
	exists, err = c.ObjectExists(t.Context(), "bucket-a", "builds/missing.txt")
	if err != nil || exists {
		t.Fatalf("ObjectExists(missing) = (%v, %v), want (false, nil)", exists, err)
	}

	// ListObjects sees folder prefix "builds/b1/" and file "builds/readme.txt" while skipping "builds/".
	listed, err := c.ListObjects(t.Context(), storagedomain.ListObjectsRequest{
		Bucket: "bucket-a",
		Prefix: "builds/",
	})
	if err != nil {
		t.Fatalf("ListObjects failed: %v", err)
	}
	if len(listed.Prefixes) != 1 || listed.Prefixes[0] != "builds/b1/" {
		t.Errorf("Prefixes = %v, want [builds/b1/]", listed.Prefixes)
	}

	// GetObject returns content + ContentType, and missing returns ErrNotFound.
	rc, _, err := c.GetObject(t.Context(), "bucket-a", "builds/readme.txt")
	if err != nil {
		t.Fatalf("GetObject failed: %v", err)
	}
	gotBytes, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(gotBytes) != "hello" {
		t.Errorf("GetObject content = %q, want hello", string(gotBytes))
	}
	if _, _, err := c.GetObject(t.Context(), "bucket-a", "builds/nope"); !errors.Is(err, storagedomain.ErrNotFound) {
		t.Errorf("GetObject(missing) err = %v, want ErrNotFound", err)
	}

	// VerifyAccess with CheckWrite=true succeeds and cleans marker object.
	writeOK := c.VerifyAccess(t.Context(), storagedomain.VerifyAccessRequest{
		Bucket:     "bucket-a",
		CheckWrite: true,
	})
	if !writeOK.CanProceed {
		t.Fatalf("VerifyAccess(CheckWrite=true) failed: %+v", writeOK)
	}
	srv.mu.Lock()
	_, markerLeft := srv.objects["bucket-a/.cco-write-probe"]
	srv.mu.Unlock()
	if markerLeft {
		t.Error("expected .cco-write-probe marker deleted after VerifyAccess")
	}

	// VerifyAccess with CheckWrite=true fails cleanly when writes are denied.
	srv.mu.Lock()
	srv.denyWrite = true
	srv.mu.Unlock()
	writeDenied := c.VerifyAccess(t.Context(), storagedomain.VerifyAccessRequest{
		Bucket:     "bucket-a",
		CheckWrite: true,
	})
	if writeDenied.CanProceed || !strings.Contains(writeDenied.Message, "cannot write") {
		t.Errorf("expected write denial report, got %+v", writeDenied)
	}

	// DeleteObject deletes existing and maps missing to ErrNotFound.
	if err := c.DeleteObject(t.Context(), "bucket-a", "builds/readme.txt"); err != nil {
		t.Fatalf("DeleteObject failed: %v", err)
	}
	if err := c.DeleteObject(t.Context(), "bucket-a", "builds/readme.txt"); !errors.Is(err, storagedomain.ErrNotFound) {
		t.Errorf("DeleteObject(missing) err = %v, want ErrNotFound", err)
	}
}

func TestExtractDisplayName(t *testing.T) {
	t.Parallel()
	if got := extractDisplayName("a/b/c/", true); got != "c/" {
		t.Errorf("extractDisplayName(folder) = %q, want c/", got)
	}
	if got := extractDisplayName("a/b/file.zip", false); got != "file.zip" {
		t.Errorf("extractDisplayName(file) = %q, want file.zip", got)
	}
}

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
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// buildPushBackend fakes the two API calls and the GCS session endpoint in
// one handler, so `build push` exercises its real wiring end to end.
type buildPushBackend struct {
	mu            sync.Mutex
	alreadyStaged bool
	requests      int               // every request received, any endpoint
	uploads       map[string][]byte // received bytes by session id
	registered    []byte            // create-build request body
	deviceCreated []byte            // create-device request body
	// onStage runs when an upload session is started, after the client has
	// hashed the file and before it uploads.
	onStage func()
}

// gcsCRC32CMatches checks an X-Goog-Hash crc32c value against body the way GCS
// does. A request without the header is accepted, as GCS accepts it.
func gcsCRC32CMatches(header string, body []byte) bool {
	v, ok := strings.CutPrefix(header, "crc32c=")
	if !ok {
		return true
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], crc32.Checksum(body, crc32.MakeTable(crc32.Castagnoli)))
	return v == base64.StdEncoding.EncodeToString(b[:])
}

func (b *buildPushBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requests++
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v2/builds/uploads":
		var req struct {
			FileName string `json:"file_name"`
			SHA256   string `json:"sha256"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if b.onStage != nil {
			b.onStage()
		}
		objectURL := "gs://bkt/staging/sha256/" + req.SHA256 + "/" + req.FileName
		w.WriteHeader(http.StatusCreated)
		if b.alreadyStaged {
			fmt.Fprintf(w, `{"object_url":%q,"already_exists":true}`, objectURL)
			return
		}
		fmt.Fprintf(w, `{"object_url":%q,"session_url":"http://%s/_session/%s","already_exists":false}`,
			objectURL, r.Host, req.SHA256)

	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/_session/"):
		// An empty-body PUT with "bytes */N" is the resumable-protocol probe;
		// GCS reuses 308 as "Resume Incomplete", and with no Range header it
		// means "nothing persisted yet, send from zero".
		if r.ContentLength == 0 {
			w.WriteHeader(http.StatusPermanentRedirect)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !gcsCRC32CMatches(r.Header.Get("X-Goog-Hash"), body) {
			http.Error(w, "Provided CRC32C doesn't match calculated CRC32C", http.StatusBadRequest)
			return
		}
		if b.uploads == nil {
			b.uploads = make(map[string][]byte)
		}
		b.uploads[strings.TrimPrefix(r.URL.Path, "/_session/")] = body
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodPost && r.URL.Path == "/v2/builds":
		b.registered, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id":"build-1","name":"pushed","source":"url","type":"custom","target":""}`)

	case r.Method == http.MethodPost && r.URL.Path == "/v2/devices":
		b.deviceCreated, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"d1","name":"d1","host_id":"h1","status":"creating"}`)

	default:
		http.NotFound(w, r)
	}
}

func writePushArtifacts(t *testing.T) (imgZip, hostPkg string, contents map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	imgZip = filepath.Join(dir, "aosp_cf_x86_64_only_phone-img-123.zip")
	hostPkg = filepath.Join(dir, "cvd-host_package.tar.gz")
	contents = map[string][]byte{
		imgZip:  []byte("fake-images-zip-content"),
		hostPkg: []byte("fake-host-package-content"),
	}
	for path, data := range contents {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return imgZip, hostPkg, contents
}

func TestBuildPush(t *testing.T) {
	backend := &buildPushBackend{}
	imgZip, hostPkg, contents := writePushArtifacts(t)

	out, err := runCLI(t, backend, "build", "push", imgZip, hostPkg)
	if err != nil {
		t.Fatalf("build push: %v", err)
	}
	if !strings.Contains(out, "Build registered") {
		t.Errorf("output missing registration confirmation:\n%s", out)
	}

	// Every artifact's exact bytes must have reached its session.
	for path, data := range contents {
		sum := sha256.Sum256(data)
		got, ok := backend.uploads[hex.EncodeToString(sum[:])]
		if !ok {
			t.Fatalf("no upload received for %s", filepath.Base(path))
		}
		if string(got) != string(data) {
			t.Errorf("uploaded bytes for %s do not match the file", filepath.Base(path))
		}
	}

	var reg struct {
		Source        string `json:"source"`
		Fingerprint   string `json:"fingerprint"`
		ArtifactSpecs []struct {
			Type, URL, Role string
		} `json:"artifact_specs"`
	}
	if err := json.Unmarshal(backend.registered, &reg); err != nil {
		t.Fatalf("decoding register request: %v", err)
	}
	if reg.Source != "url" || reg.Fingerprint == "" {
		t.Errorf("register request = %+v, want url source with a fingerprint", reg)
	}
	roles := map[string]string{}
	for _, s := range reg.ArtifactSpecs {
		roles[s.Role] = s.URL
		if s.Type != "gcs" || !strings.HasPrefix(s.URL, "gs://bkt/staging/sha256/") {
			t.Errorf("spec %+v: want gcs type and a staged gs:// URL", s)
		}
	}
	if !strings.Contains(roles["default_build"], "-img-") || !strings.Contains(roles["host_package"], "cvd-host_package") {
		t.Errorf("role inference wrong: %v", roles)
	}
}

func TestBuildPushAlreadyStagedSkipsUpload(t *testing.T) {
	backend := &buildPushBackend{alreadyStaged: true}
	imgZip, hostPkg, _ := writePushArtifacts(t)

	if _, err := runCLI(t, backend, "build", "push", imgZip, hostPkg); err != nil {
		t.Fatalf("build push: %v", err)
	}
	if len(backend.uploads) != 0 {
		t.Errorf("expected no session PUTs for already-staged artifacts, got %d", len(backend.uploads))
	}
	if backend.registered == nil {
		t.Error("expected registration to still happen")
	}
}

func TestBuildPushDistDirFindsBothArtifacts(t *testing.T) {
	backend := &buildPushBackend{}
	imgZip, _, contents := writePushArtifacts(t)

	out, err := runCLI(t, backend, "build", "push", "--dist-dir", filepath.Dir(imgZip))
	if err != nil {
		t.Fatalf("build push --dist-dir: %v", err)
	}
	if !strings.Contains(out, "Build registered") {
		t.Errorf("output missing registration confirmation:\n%s", out)
	}
	if len(backend.uploads) != len(contents) {
		t.Errorf("uploads = %d, want both artifacts pushed", len(backend.uploads))
	}
}

func TestBuildPushDistDirErrors(t *testing.T) {
	rejecting := &buildPushBackend{}

	empty := t.TempDir()
	if _, err := runCLI(t, rejecting, "build", "push", "--dist-dir", empty); err == nil ||
		!strings.Contains(err.Error(), "no images zip") {
		t.Errorf("empty dir: err = %v, want the missing-zip guidance", err)
	}

	ambiguous := t.TempDir()
	for _, name := range []string{"a-img-1.zip", "b-img-2.zip", "cvd-host_package.tar.gz"} {
		if err := os.WriteFile(filepath.Join(ambiguous, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runCLI(t, rejecting, "build", "push", "--dist-dir", ambiguous); err == nil ||
		!strings.Contains(err.Error(), "2 images zips") {
		t.Errorf("ambiguous dir: err = %v, want the two-zips rejection", err)
	}

	noHostPkg := t.TempDir()
	if err := os.WriteFile(filepath.Join(noHostPkg, "a-img-1.zip"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, rejecting, "build", "push", "--dist-dir", noHostPkg); err == nil ||
		!strings.Contains(err.Error(), "m hosttar") {
		t.Errorf("missing host package: err = %v, want the hosttar hint", err)
	}

	imgZip, hostPkg, _ := writePushArtifacts(t)
	if _, err := runCLI(t, rejecting, "build", "push", "--dist-dir", filepath.Dir(imgZip), hostPkg); err == nil ||
		!strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("dist-dir + file: err = %v, want the exclusivity rule", err)
	}

	if _, err := runCLI(t, rejecting, "build", "push"); err == nil ||
		!strings.Contains(err.Error(), "--dist-dir") {
		t.Errorf("no args: err = %v, want guidance naming --dist-dir", err)
	}
}

func TestDeviceCreateLocalImageComposesPushAndCreate(t *testing.T) {
	backend := &buildPushBackend{}
	imgZip, hostPkg, _ := writePushArtifacts(t)

	_, err := runCLI(t, backend,
		"device", "create", "--host", testHostID, "--name", "d1",
		"--local-image", imgZip, "--local-image", hostPkg)
	if err != nil {
		t.Fatalf("device create --local-image: %v", err)
	}
	if backend.registered == nil {
		t.Fatal("expected a build registration before the create")
	}
	var create struct {
		BuildID string `json:"build_id"`
		HostID  string `json:"host_id"`
	}
	if err := json.Unmarshal(backend.deviceCreated, &create); err != nil {
		t.Fatalf("decoding create request: %v", err)
	}
	if create.BuildID != "build-1" {
		t.Errorf("create build_id = %q, want the pushed build's id", create.BuildID)
	}
}

func TestDeviceCreateLocalImageExcludesBuildFlags(t *testing.T) {
	imgZip, hostPkg, _ := writePushArtifacts(t)
	_, err := runCLI(t, &buildPushBackend{},
		"device", "create", "--host", testHostID, "--name", "d1",
		"--build-id", "reg-1", "--local-image", imgZip, "--local-image", hostPkg)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want mutual-exclusion refusal", err)
	}
}

// A dry run with --local-image must refuse before any upload or build
// registration.
func TestDeviceCreateLocalImageRejectsDryRun(t *testing.T) {
	backend := &buildPushBackend{}
	imgZip, hostPkg, _ := writePushArtifacts(t)

	_, err := runCLI(t, backend,
		"device", "create", "--host", testHostID, "--name", "d1", "--dry-run",
		"--local-image", imgZip, "--local-image", hostPkg)
	if err == nil || !strings.Contains(err.Error(), "--dry-run") {
		t.Fatalf("err = %v, want the dry-run refusal", err)
	}
	if backend.requests != 0 {
		t.Errorf("a rejected dry run made %d HTTP requests, want 0", backend.requests)
	}
}

func TestBuildPushRefusesUnknownArtifact(t *testing.T) {
	dir := t.TempDir()
	odd := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(odd, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, &buildPushBackend{}, "build", "push", odd)
	if err == nil || !strings.Contains(err.Error(), "cannot infer a role") {
		t.Fatalf("err = %v, want role-inference refusal", err)
	}
}

func TestE2E_BuildGetAndDelete(t *testing.T) {
	var deleted bool
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireJWT(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/builds/b1":
			fmt.Fprint(w, `{"id":"b1","name":"local-eng","target":"aosp_cf_x86_64_phone-userdebug",
				"type":"user_provided","source":"url","is_default":true,
				"artifact_specs":[{"type":"gcs","url":"gs://bkt/img.zip","role":"images"}]}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/v2/builds/b1":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	out, err := runCLI(t, backend, "build", "get", "b1")
	if err != nil {
		t.Fatalf("build get: %v", err)
	}
	for _, want := range []string{"local-eng", "aosp_cf_x86_64_phone-userdebug", "[gcs] gs://bkt/img.zip", "Default:      yes"} {
		if !strings.Contains(out, want) {
			t.Errorf("build get missing %q:\n%s", want, out)
		}
	}

	if _, err := runCLI(t, backend, "build", "delete", "b1", "--force"); err != nil {
		t.Fatalf("build delete: %v", err)
	}
	if !deleted {
		t.Error("delete endpoint never called")
	}
}

// A file rewritten between hashing and upload must be refused by the storage
// checksum, so the staging object named by the stale digest is never written.
func TestBuildPushRejectsAFileThatChangedAfterHashing(t *testing.T) {
	backend := &buildPushBackend{}
	imgZip, hostPkg, contents := writePushArtifacts(t)
	backend.onStage = func() {
		// Same length, so only the checksum can tell.
		changed := []byte(strings.ToUpper(string(contents[imgZip])))
		if err := os.WriteFile(imgZip, changed, 0o600); err != nil {
			t.Error(err)
		}
	}

	_, err := runCLI(t, backend, "build", "push", imgZip, hostPkg)
	if err == nil || !strings.Contains(err.Error(), "the file changed during the push") {
		t.Fatalf("err = %v, want the file-changed refusal", err)
	}
	if len(backend.uploads) != 0 {
		t.Errorf("stale-digest object written: %v", backend.uploads)
	}
}

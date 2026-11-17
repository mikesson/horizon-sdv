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
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

// androidShell lays out a product-out directory holding every default
// required image and a host-out directory holding the host package, and
// points the lunch variables at them. It also isolates the temp directory so
// a test can see what the push leaves behind.
func androidShell(t *testing.T) (productOut, hostOut, tmp string) {
	t.Helper()
	root := t.TempDir()
	productOut = filepath.Join(root, "out", "target", "product", "vsoc_x86_64")
	hostOut = filepath.Join(root, "out", "host", "linux-x86")
	tmp = filepath.Join(root, "tmp")
	for _, dir := range []string{productOut, hostOut, tmp} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range defaultRequiredImages {
		if err := os.WriteFile(filepath.Join(productOut, name), []byte("image:"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// An image outside the list must not be packed.
	if err := os.WriteFile(filepath.Join(productOut, "system.img"), []byte("unlisted"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The unpacked host package directory predates the tarball, as after
	// 'm hosttar'.
	hostPkgDir := filepath.Join(hostOut, "cvd-host_package")
	if err := os.Mkdir(hostPkgDir, 0o750); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(hostPkgDir, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostOut, "cvd-host_package.tar.gz"), []byte("host-package"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envProductOut, productOut)
	t.Setenv(envHostOut, hostOut)
	t.Setenv(envBuildTop, "")
	t.Setenv(envTargetProduct, "aosp_cf_x86_64_phone")
	t.Setenv("TMPDIR", tmp)
	return productOut, hostOut, tmp
}

// leftoverZips counts the temporary directories a push left in tmp.
func leftoverZips(t *testing.T, tmp string) int {
	t.Helper()
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "cfctl-product-out-") {
			n++
		}
	}
	return n
}

// pushedImagesZip returns the bytes and file name of the default_build
// artifact a push registered.
func pushedImagesZip(t *testing.T, backend *buildPushBackend) (data []byte, fileName string) {
	t.Helper()
	var reg struct {
		ArtifactSpecs []struct{ URL, Role string } `json:"artifact_specs"`
	}
	if err := json.Unmarshal(backend.registered, &reg); err != nil {
		t.Fatalf("decoding register request: %v", err)
	}
	for _, s := range reg.ArtifactSpecs {
		if s.Role != "default_build" {
			continue
		}
		// gs://bkt/staging/sha256/<sha>/<file name>
		parts := strings.Split(s.URL, "/")
		if len(parts) < 2 {
			t.Fatalf("unexpected staged URL %q", s.URL)
		}
		sha, name := parts[len(parts)-2], parts[len(parts)-1]
		data, ok := backend.uploads[sha]
		if !ok {
			t.Fatalf("no upload for the images zip %s", s.URL)
		}
		return data, name
	}
	t.Fatalf("no default_build artifact registered: %s", backend.registered)
	return nil, ""
}

func TestBuildPushProductOutPacksTheRequiredImages(t *testing.T) {
	productOut, _, tmp := androidShell(t)
	backend := &buildPushBackend{}

	if _, err := runCLI(t, backend, "build", "push", "--product-out"); err != nil {
		t.Fatalf("build push --product-out: %v", err)
	}

	data, name := pushedImagesZip(t, backend)
	if name != "aosp_cf_x86_64_phone-img-local.zip" {
		t.Errorf("images zip name = %q, want <TARGET_PRODUCT>-img-local.zip", name)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("pushed images zip does not open: %v", err)
	}
	got := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		got = append(got, f.Name)
		if !f.Modified.Equal(productOutZipTime) {
			t.Errorf("%s modified %v, want the fixed %v", f.Name, f.Modified, productOutZipTime)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening %s: %v", f.Name, err)
		}
		content, _ := io.ReadAll(rc)
		_ = rc.Close()
		want, _ := os.ReadFile(filepath.Join(productOut, f.Name))
		if !bytes.Equal(content, want) {
			t.Errorf("%s content differs from the product-out file", f.Name)
		}
	}
	want := slices.Sorted(slices.Values(defaultRequiredImages))
	if !slices.Equal(got, want) {
		t.Errorf("zip entries = %v, want the required images in sorted order %v", got, want)
	}

	var reg struct {
		ArtifactSpecs []struct{ URL, Role string } `json:"artifact_specs"`
	}
	_ = json.Unmarshal(backend.registered, &reg)
	var hostPkg bool
	for _, s := range reg.ArtifactSpecs {
		hostPkg = hostPkg || (s.Role == "host_package" && strings.HasSuffix(s.URL, "/cvd-host_package.tar.gz"))
	}
	if !hostPkg {
		t.Errorf("host package from $ANDROID_HOST_OUT not registered: %s", backend.registered)
	}

	if n := leftoverZips(t, tmp); n != 0 {
		t.Errorf("the push left %d temporary directories, want the zip removed", n)
	}
}

// Unchanged images must give the same zip bytes, whatever their mtimes, so a
// re-push dedupes on the content hash.
func TestBuildPushProductOutIsDeterministic(t *testing.T) {
	productOut, _, _ := androidShell(t)

	first := &buildPushBackend{}
	if _, err := runCLI(t, first, "build", "push", "--product-out"); err != nil {
		t.Fatalf("first push: %v", err)
	}
	later := time.Now().Add(time.Hour)
	for _, name := range defaultRequiredImages {
		if err := os.Chtimes(filepath.Join(productOut, name), later, later); err != nil {
			t.Fatal(err)
		}
	}
	second := &buildPushBackend{}
	if _, err := runCLI(t, second, "build", "push", "--product-out"); err != nil {
		t.Fatalf("second push: %v", err)
	}

	a, _ := pushedImagesZip(t, first)
	b, _ := pushedImagesZip(t, second)
	if !bytes.Equal(a, b) {
		t.Error("re-packing unchanged images produced different zip bytes")
	}
	if !bytes.Equal(first.registered, second.registered) {
		t.Errorf("registrations differ:\n%s\n%s", first.registered, second.registered)
	}
}

func TestBuildPushProductOutTakesAnExplicitDirectory(t *testing.T) {
	productOut, _, _ := androidShell(t)
	t.Setenv(envProductOut, "")
	t.Setenv(envTargetProduct, "")

	for _, args := range [][]string{
		{"build", "push", "--product-out=" + productOut},
		{"build", "push", "--product-out", productOut},
	} {
		backend := &buildPushBackend{}
		if _, err := runCLI(t, backend, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if _, name := pushedImagesZip(t, backend); name != "vsoc_x86_64-img-local.zip" {
			t.Errorf("%v: zip name = %q, want the directory's base name without $TARGET_PRODUCT", args, name)
		}
	}
}

func TestBuildPushProductOutHostPackageOverride(t *testing.T) {
	_, _, _ = androidShell(t)
	t.Setenv(envHostOut, "")
	explicit := filepath.Join(t.TempDir(), "my-cvd-host_package.tar.gz")
	if err := os.WriteFile(explicit, []byte("explicit-host-package"), 0o600); err != nil {
		t.Fatal(err)
	}

	backend := &buildPushBackend{}
	if _, err := runCLI(t, backend, "build", "push", "--product-out", "--host-package", explicit); err != nil {
		t.Fatalf("build push --host-package: %v", err)
	}
	if !strings.Contains(string(backend.registered), "my-cvd-host_package.tar.gz") {
		t.Errorf("registration does not carry the --host-package file: %s", backend.registered)
	}
}

func TestBuildPushProductOutReadsTheTreeList(t *testing.T) {
	_, _, _ = androidShell(t)
	top := t.TempDir()
	listPath := filepath.Join(top, requiredImagesPath)
	if err := os.MkdirAll(filepath.Dir(listPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(listPath, []byte("# comment\nsuper.img\n\nboot.img\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envBuildTop, top)

	backend := &buildPushBackend{}
	if _, err := runCLI(t, backend, "build", "push", "--product-out"); err != nil {
		t.Fatalf("build push: %v", err)
	}
	data, _ := pushedImagesZip(t, backend)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		got = append(got, f.Name)
	}
	if !slices.Equal(got, []string{"boot.img", "super.img"}) {
		t.Errorf("zip entries = %v, want the tree's list", got)
	}
}

func TestBuildPushProductOutErrors(t *testing.T) {
	productOut, hostOut, _ := androidShell(t)
	imgZip, hostPkg, _ := writePushArtifacts(t)

	cases := []struct {
		name  string
		setup func(t *testing.T)
		args  []string
		want  string
	}{
		{
			name:  "no directory",
			setup: func(t *testing.T) { t.Helper(); t.Setenv(envProductOut, "") },
			args:  []string{"build", "push", "--product-out"},
			want:  "$ANDROID_PRODUCT_OUT",
		},
		{
			name: "missing image",
			setup: func(t *testing.T) {
				t.Helper()
				if err := os.Remove(filepath.Join(productOut, "vendor_boot.img")); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = os.WriteFile(filepath.Join(productOut, "vendor_boot.img"), []byte("image:vendor_boot.img"), 0o600)
				})
			},
			args: []string{"build", "push", "--product-out"},
			want: "vendor_boot.img",
		},
		{
			name:  "no host out",
			setup: func(t *testing.T) { t.Helper(); t.Setenv(envHostOut, "") },
			args:  []string{"build", "push", "--product-out"},
			want:  "--host-package",
		},
		{
			name: "stale host package",
			setup: func(t *testing.T) {
				t.Helper()
				tarPath := filepath.Join(hostOut, "cvd-host_package.tar.gz")
				older := time.Now().Add(-2 * time.Hour)
				if err := os.Chtimes(tarPath, older, older); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { now := time.Now(); _ = os.Chtimes(tarPath, now, now) })
			},
			args: []string{"build", "push", "--product-out"},
			want: "is older than",
		},
		{
			name: "no host package directory",
			setup: func(t *testing.T) {
				t.Helper()
				dir := filepath.Join(hostOut, "cvd-host_package")
				if err := os.Remove(dir); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = os.Mkdir(dir, 0o750)
					old := time.Now().Add(-time.Hour)
					_ = os.Chtimes(dir, old, old)
				})
			},
			args: []string{"build", "push", "--product-out"},
			want: "cannot check",
		},
		{
			name: "named files",
			args: []string{"build", "push", imgZip, hostPkg, "--product-out"},
			want: "mutually exclusive",
		},
		{
			name: "dist dir",
			args: []string{"build", "push", "--product-out", "--dist-dir", filepath.Dir(imgZip)},
			want: "mutually exclusive",
		},
		{
			name: "host package alone",
			args: []string{"build", "push", "--host-package", hostPkg, imgZip},
			want: "--product-out",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup(t)
			}
			backend := &buildPushBackend{}
			_, err := runCLI(t, backend, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			if backend.requests != 0 {
				t.Errorf("a refused push made %d requests, want 0", backend.requests)
			}
		})
	}
}

func TestDeviceCreateProductOutComposesPushAndCreate(t *testing.T) {
	_, _, tmp := androidShell(t)
	backend := &buildPushBackend{}

	if _, err := runCLI(t, backend, "device", "create", "--host", testHostID, "--product-out"); err != nil {
		t.Fatalf("device create --product-out: %v", err)
	}
	if _, name := pushedImagesZip(t, backend); !strings.HasSuffix(name, "-img-local.zip") {
		t.Errorf("images zip name = %q", name)
	}
	var create struct {
		BuildID string `json:"build_id"`
	}
	if err := json.Unmarshal(backend.deviceCreated, &create); err != nil {
		t.Fatalf("decoding create request: %v", err)
	}
	if create.BuildID != "build-1" {
		t.Errorf("create build_id = %q, want the pushed build's id", create.BuildID)
	}
	if n := leftoverZips(t, tmp); n != 0 {
		t.Errorf("the create left %d temporary directories, want the zip removed", n)
	}
}

func TestDeviceCreateProductOutExclusions(t *testing.T) {
	_, _, _ = androidShell(t)
	imgZip, hostPkg, _ := writePushArtifacts(t)

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--product-out", "--local-image", imgZip}, "can't be used together"},
		{[]string{"--product-out", "--build-id", "12345678", "--build-target", "t"}, "mutually exclusive"},
		{[]string{"--product-out", "--dry-run"}, "can't be used together"},
		{[]string{"--host-package", hostPkg}, "--product-out"},
	}
	for _, tc := range cases {
		backend := &buildPushBackend{}
		args := append([]string{"device", "create", "--host", testHostID}, tc.args...)
		_, err := runCLI(t, backend, args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want it to mention %q", tc.args, err, tc.want)
		}
		if backend.requests != 0 {
			t.Errorf("%v: a refused create made %d requests, want 0", tc.args, backend.requests)
		}
	}
}

// --product-out takes the next token as its directory only when that token
// is a value.
func TestProductOutFlagParsing(t *testing.T) {
	cfg := &config.Config{Server: "https://example.com"}
	cases := []struct {
		args    []string
		wantDir string
		wantSet bool
	}{
		{[]string{"device", "create", "--product-out"}, "", true},
		{[]string{"device", "create", "--product-out", "--wait"}, "", true},
		{[]string{"device", "create", "--product-out=/p"}, "/p", true},
		{[]string{"device", "create", "--product-out", "/p", "--wait"}, "/p", true},
		{[]string{"device", "create"}, "", false},
	}
	for _, tc := range cases {
		c, _, err := parseCLI(t, tc.args, cfg)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		got := c.Device.Create.ProductOut
		if got.Set() != tc.wantSet || got.dir != tc.wantDir {
			t.Errorf("%v: set=%v dir=%q, want set=%v dir=%q", tc.args, got.Set(), got.dir, tc.wantSet, tc.wantDir)
		}
	}
}

// Kong would decode a set CFCTL_PRODUCT_OUT, even an empty one, as
// --product-out, and a CFCTL_HOST_PACKAGE would refuse every plain push.
func TestProductOutFlagsIgnoreTheEnvironment(t *testing.T) {
	imgZip, hostPkg, _ := writePushArtifacts(t)
	t.Setenv("CFCTL_PRODUCT_OUT", "")
	t.Setenv("CFCTL_HOST_PACKAGE", hostPkg)

	backend := &buildPushBackend{}
	if _, err := runCLI(t, backend, "build", "push", imgZip, hostPkg); err != nil {
		t.Fatalf("build push with ambient CFCTL_PRODUCT_OUT and CFCTL_HOST_PACKAGE: %v", err)
	}
	if !strings.Contains(string(backend.registered), filepath.Base(imgZip)) {
		t.Errorf("registration does not carry the named images zip: %s", backend.registered)
	}
}

func TestParseRequiredImages(t *testing.T) {
	got, err := parseRequiredImages(strings.NewReader("# header\n\n boot.img \nsuper.img\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"boot.img", "super.img"}) {
		t.Errorf("got %v", got)
	}
	for _, bad := range []string{"../etc/passwd\n", "sub/boot.img\n", "# only a comment\n"} {
		if _, err := parseRequiredImages(strings.NewReader(bad)); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

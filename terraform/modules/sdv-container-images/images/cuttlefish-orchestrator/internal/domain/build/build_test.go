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

package build

import (
	"errors"
	"testing"
)

func TestBuildValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		build   Build
		wantErr bool
	}{
		{
			name: "Valid build with BuildID",
			build: Build{
				Name:    "test-build",
				BuildID: "12345678",
				Target:  "aosp_cf_x86_64_phone-userdebug",
				Source:  SourceAB,
			},
			wantErr: false,
		},
		{
			name: "Valid build with Branch",
			build: Build{
				Name:   "test-build",
				Branch: "aosp-main",
				Target: "aosp_cf_x86_64_phone-userdebug",
				Source: SourceAB,
			},
			wantErr: false,
		},
		{
			name: "Missing name",
			build: Build{
				BuildID: "12345678",
				Target:  "aosp_cf_x86_64_phone-userdebug",
				Source:  SourceAB,
			},
			wantErr: true,
		},
		{
			name: "Missing target",
			build: Build{
				Name:    "test-build",
				BuildID: "12345678",
				Source:  SourceAB,
			},
			wantErr: true,
		},
		{
			name: "Missing source",
			build: Build{
				Name:    "test-build",
				BuildID: "12345678",
				Target:  "aosp_cf_x86_64_phone-userdebug",
			},
			wantErr: true,
		},
		{
			name: "AB build missing BuildID and Branch",
			build: Build{
				Name:   "test-build",
				Target: "aosp_cf_x86_64_phone-userdebug",
				Source: SourceAB,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.build.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// urlBuild makes a URL-source build carrying the given specs.
func urlBuild(specs ...ArtifactSpec) Build {
	return Build{Name: "pushed", Source: SourceURL, ArtifactSpecs: specs}
}

var (
	validImages  = ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/x-img-1.zip", Role: ArtifactRoleDefaultBuild}
	validHostPkg = ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/cvd-host_package.tar.gz", Role: ArtifactRoleHostPackage}
	// The directory form: a prefix, not an object.
	namespaceImages = ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/dist/", Role: ArtifactRoleDefaultBuild}
)

// sixtyFourHex is a well-formed "#sha256=" payload; only its shape matters here.
const sixtyFourHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestBuildValidateURLSource pins the URL-source rules to the host-side cvd
// fetch contract: a spec set that passes here must be materializable by the
// host, so violations fail at registration instead of opaquely inside a
// launch.
func TestBuildValidateURLSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		build   Build
		wantErr bool
	}{
		{"valid two-artifact set", urlBuild(validImages, validHostPkg), false},
		{"valid with kernel zip selector", urlBuild(validImages, validHostPkg,
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/kernel.zip{Image}", Role: ArtifactRoleKernelBuild}), false},
		{"valid with plain-file kernel", urlBuild(validImages, validHostPkg,
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/Image", Role: ArtifactRoleKernelBuild}), false},
		{"valid https images", urlBuild(
			ArtifactSpec{Type: ArtifactSpecTypeHTTPS, URL: "https://h/x-img-1.zip", Role: ArtifactRoleDefaultBuild},
			validHostPkg), false},
		{"no specs", urlBuild(), true},
		{"missing host_package", urlBuild(validImages), true},
		{"missing default_build", urlBuild(validHostPkg), true},
		{"duplicate role", urlBuild(validImages, validHostPkg,
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/y-img-2.zip", Role: ArtifactRoleDefaultBuild}), true},
		{"default_build not a zip", urlBuild(
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/images.tar.gz", Role: ArtifactRoleDefaultBuild},
			validHostPkg), true},
		{"default_build with selector", urlBuild(
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/x-img-1.zip{boot.img}", Role: ArtifactRoleDefaultBuild},
			validHostPkg), true},
		{"host_package not a tarball", urlBuild(validImages,
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/host.zip", Role: ArtifactRoleHostPackage}), true},
		{"tar-family target artifact", urlBuild(validImages, validHostPkg,
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/kernel.tar.xz", Role: ArtifactRoleKernelBuild}), true},
		{"selector on non-zip", urlBuild(validImages, validHostPkg,
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/kernel.img{Image}", Role: ArtifactRoleKernelBuild}), true},
		{"empty selector", urlBuild(validImages, validHostPkg,
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/kernel.zip{}", Role: ArtifactRoleKernelBuild}), true},
		{"wrong prefix for type", urlBuild(
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "https://h/x-img-1.zip", Role: ArtifactRoleDefaultBuild},
			validHostPkg), true},

		// The directory form: one gs:// prefix names a whole artifact set, so
		// the host takes the host package out of it and no second spec is due.
		{"gcs directory default_build alone", urlBuild(namespaceImages), false},
		{
			"gcs directory default_build with explicit host_package",
			urlBuild(namespaceImages, validHostPkg), false,
		},
		{"https directory cannot be listed", urlBuild(
			ArtifactSpec{Type: ArtifactSpecTypeHTTPS, URL: "https://h/dist/", Role: ArtifactRoleDefaultBuild}), true},
		{"directory host_package with object default_build", urlBuild(validImages,
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/dist/", Role: ArtifactRoleHostPackage}), false},

		// The digest fragment and a pre-signed query string both sit outside
		// the name the extension rules read.
		{"default_build with sha256 fragment", urlBuild(
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/x-img-1.zip#sha256=" + sixtyFourHex, Role: ArtifactRoleDefaultBuild},
			validHostPkg), false},
		{"host_package with sha256 fragment", urlBuild(validImages,
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/cvd-host_package.tar.gz#sha256=" + sixtyFourHex, Role: ArtifactRoleHostPackage}), false},
		{"pre-signed https default_build", urlBuild(
			ArtifactSpec{Type: ArtifactSpecTypeHTTPS, URL: "https://h/x-img-1.zip?X-Goog-Signature=abc", Role: ArtifactRoleDefaultBuild},
			validHostPkg), false},
		{"selector then fragment", urlBuild(validImages, validHostPkg,
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/kernel.zip{Image}#sha256=" + sixtyFourHex, Role: ArtifactRoleKernelBuild}), false},

		// A comma truncates the value inside cvd's comma-separated build flags.
		{"comma in URL", urlBuild(
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/x,y-img-1.zip", Role: ArtifactRoleDefaultBuild},
			validHostPkg), true},

		// A system image is read out of a target_files zip whose name the host
		// resolves before downloading, on both builds.
		{"system_image_build as object", urlBuild(validImages, validHostPkg,
			ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/x-target_files-1.zip", Role: ArtifactRoleSystemImageBuild}), true},
		{
			"system_image_build namespace with object default_build",
			urlBuild(validImages, validHostPkg,
				ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/dist2/", Role: ArtifactRoleSystemImageBuild}), true,
		},
		{
			"system_image_build namespace with namespace default_build",
			urlBuild(namespaceImages,
				ArtifactSpec{Type: ArtifactSpecTypeGCS, URL: "gs://b/dist2/", Role: ArtifactRoleSystemImageBuild}), false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.build.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				var ve *ValidationError
				if !errors.As(err, &ve) {
					t.Errorf("Validate() error is %T, want *ValidationError", err)
				}
			}
		})
	}
}

func TestSpecsFingerprint(t *testing.T) {
	t.Parallel()
	a := []ArtifactSpec{validImages, validHostPkg}
	b := []ArtifactSpec{validHostPkg, validImages}
	if SpecsFingerprint(a) != SpecsFingerprint(b) {
		t.Error("fingerprint must be order-independent")
	}
	c := []ArtifactSpec{
		{Type: ArtifactSpecTypeGCS, URL: "gs://b/x-img-2.zip", Role: ArtifactRoleDefaultBuild},
		validHostPkg,
	}
	if SpecsFingerprint(a) == SpecsFingerprint(c) {
		t.Error("fingerprint must change when a URL changes")
	}
}

func TestBuildSources(t *testing.T) {
	t.Parallel()
	tests := []struct {
		source   Source
		expected string
	}{
		{SourceAB, "android_build"},
		{SourceURL, "url"},
	}

	for _, tt := range tests {
		t.Run(string(tt.source), func(t *testing.T) {
			if string(tt.source) != tt.expected {
				t.Errorf("Source = %v, want %v", string(tt.source), tt.expected)
			}
		})
	}
}

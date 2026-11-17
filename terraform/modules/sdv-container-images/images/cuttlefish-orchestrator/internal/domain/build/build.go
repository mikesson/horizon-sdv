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

// Package build provides domain types and logic for Android build management.
package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"
)

// Build represents an Android build available for deployment.
// JSON tags are snake_case: this entity is served directly as the /v2 wire
// shape, and the API convention (devices, hosts, inputs) is snake_case.
type Build struct {
	ID      string `json:"id"`
	Name    string `json:"name"`               // User-friendly display name
	BuildID string `json:"build_id,omitempty"` // Android Build ID from ci.android.com
	Target  string `json:"target"`             // e.g., aosp_cf_x86_64_phone-userdebug
	Branch  string `json:"branch,omitempty"`   // e.g., aosp-android-latest-release
	Flavor  string `json:"flavor,omitempty"`   // e.g., userdebug, user
	Source  Source `json:"source"`             // android_build or url
	// ArtifactSpecs contains URL-based artifact locations for SourceURL.
	// Each spec defines a type (gcs/https) and URL to fetch.
	ArtifactSpecs []ArtifactSpec `json:"artifact_specs,omitempty"`
	Fingerprint   string         `json:"fingerprint,omitempty"`
	// CreatedBy is the email of the creator (informational/audit only).
	CreatedBy   *string        `json:"created_by,omitempty"`
	Description string         `json:"description,omitempty"`
	IsDefault   bool           `json:"is_default,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// Source is where a build's artifacts are fetched from.
type Source string

// The two source values, as they appear in a build's `source` field on the
// wire. Source decides which other fields a build must carry: a CI reference
// (build ID or branch, plus target), or a set of GCS/HTTPS artifact URLs.
// It is also a build's provenance (android_build is an Android CI artifact
// set, url is an operator-supplied one), and display surfaces derive their
// labels from it. There is no default. Validate requires the field and
// rejects any value outside this set.
const (
	SourceAB  Source = "android_build"
	SourceURL Source = "url"
)

// ArtifactSpecType identifies the type of artifact specification.
type ArtifactSpecType string

const (
	// ArtifactSpecTypeGCS represents a Google Cloud Storage URL (gs://).
	ArtifactSpecTypeGCS ArtifactSpecType = "gcs"
	// ArtifactSpecTypeHTTPS represents an HTTPS URL.
	ArtifactSpecTypeHTTPS ArtifactSpecType = "https"
)

// ArtifactRole identifies the semantic purpose of an artifact in a build.
// These roles map directly to Host Orchestrator env_config fields.
type ArtifactRole string

const (
	// ArtifactRoleHostPackage is the cvd-host_package.tar.gz containing host tools.
	// Maps to env_config common.host_package.
	ArtifactRoleHostPackage ArtifactRole = "host_package"
	// ArtifactRoleDefaultBuild is the main device system image.
	// Maps to env_config instances[*].disk.default_build.
	ArtifactRoleDefaultBuild ArtifactRole = "default_build"
	// ArtifactRoleKernelBuild is a custom kernel image.
	// Maps to env_config instances[*].boot.kernel.build.
	ArtifactRoleKernelBuild ArtifactRole = "kernel_build"
	// ArtifactRoleBootBuild is a custom boot image.
	// Maps to env_config instances[*].boot.build.
	ArtifactRoleBootBuild ArtifactRole = "boot_build"
	// ArtifactRoleBootloaderBuild is a custom bootloader.
	// Maps to env_config instances[*].boot.bootloader.build.
	ArtifactRoleBootloaderBuild ArtifactRole = "bootloader_build"
	// ArtifactRoleSystemImageBuild is a custom system image.
	// Maps to env_config instances[*].disk.super.system.
	ArtifactRoleSystemImageBuild ArtifactRole = "system_image_build"
)

// ValidArtifactRoles contains all valid artifact roles.
var ValidArtifactRoles = []ArtifactRole{
	ArtifactRoleHostPackage,
	ArtifactRoleDefaultBuild,
	ArtifactRoleKernelBuild,
	ArtifactRoleBootBuild,
	ArtifactRoleBootloaderBuild,
	ArtifactRoleSystemImageBuild,
}

// ArtifactSpec represents a URL-based artifact location for build registration.
type ArtifactSpec struct {
	// Type identifies the spec type (gcs, https).
	Type ArtifactSpecType `json:"type"`
	// URL is the full URL to the artifact (gs://... or https://...).
	URL string `json:"url"`
	// Role identifies the semantic purpose of this artifact.
	Role ArtifactRole `json:"role"`
}

// FetchKey is the cvd @ab fetch reference grammar: how a create names
// Android Build artifacts to the host. One copy on the domain side because
// three consumers must agree byte-for-byte: the create payload that ships
// it, the affinity ledger that records it, and the placement query that
// matches recorded against wanted.
func FetchKey(ref, target string) string {
	return "@ab/" + ref + "/" + target
}

// PinnedFetchKey returns the fetch key whose host-side cache residency is
// knowable at placement time, ok=false for everything else: a tracking
// (branch-keyed) build resolves to a build id on the host, a URL build never
// fetches through @ab, and a record without a target would ship a
// device-dependent key. Exactly the keys this yields earn affinity-ledger
// rows.
func (b *Build) PinnedFetchKey() (string, bool) {
	if b.Source != SourceAB || b.BuildID == "" || b.Target == "" {
		return "", false
	}
	return FetchKey(b.BuildID, b.Target), true
}

// WarmKey is one fetch key the warm plane prefetches onto standby hosts.
// Domain vocabulary because it crosses the firewall: the device feature's
// resolver plans it, the warm plane job consumes it.
type WarmKey struct {
	FetchKey string
	// RegistryID is the build row behind a pinned key, the ledger's
	// informational column. Empty for tracking keys.
	RegistryID string
	// Pinned reports a build-id-keyed fetch: the only warmth the affinity
	// ledger records, because only a pinned key's residency is knowable at
	// placement time.
	Pinned bool
}

// Validate ensures the build data is valid. URL-source rules mirror the
// host-side cvd fetch contract so an unbootable registration fails here as
// a 400 instead of minutes later inside a launch: without a host_package
// the fetcher falls back to downloading the default build's object under
// the host-package name and fails to untar it, and cvd extracts only .zip
// archives for target artifacts (tar-family is refused; the host package
// .tar.gz has its own extraction path).
func (b *Build) Validate() error {
	if b.Name == "" {
		return &ValidationError{Message: "name is required"}
	}
	if b.Source == "" {
		return &ValidationError{Message: "source is required"}
	}
	switch b.Source {
	case SourceAB:
		// BuildID can be empty for branch-based builds (uses latest from branch)
		if b.BuildID == "" && b.Branch == "" {
			return &ValidationError{Message: "either build ID or branch is required for Android Build source"}
		}
		if b.Target == "" {
			return &ValidationError{Message: "target is required for Android Build source"}
		}
	case SourceURL:
		return validateArtifactSpecs(b.ArtifactSpecs)
	default:
		return &ValidationError{Message: "invalid source type"}
	}
	return nil
}

// validateArtifactSpecs checks a URL-source build's spec set: every spec
// well-formed, one spec per role, and the two roles a boot requires present.
func validateArtifactSpecs(specs []ArtifactSpec) error {
	if len(specs) == 0 {
		return &ValidationError{Message: "at least one artifact spec is required for URL source"}
	}
	seen := make(map[ArtifactRole]bool, len(specs))
	namespaceRoles := make(map[ArtifactRole]bool, len(specs))
	for _, spec := range specs {
		if err := validateSpecFields(spec); err != nil {
			return err
		}
		if seen[spec.Role] {
			return &ValidationError{Message: "duplicate artifact role " + string(spec.Role)}
		}
		seen[spec.Role] = true
		if err := validateSpecShape(spec); err != nil {
			return err
		}
		if base, _, _ := urlShape(spec.URL); isNamespaceURL(base) {
			namespaceRoles[spec.Role] = true
		}
	}
	if !seen[ArtifactRoleDefaultBuild] {
		return &ValidationError{Message: "URL-source builds require a default_build artifact (the images .zip, or a gs:// directory holding them)"}
	}
	// A directory default_build names a whole artifact set, so the host takes
	// the host package from the same prefix. Every other shape must say where
	// it comes from, or the launch fails deep inside cvd looking for binaries.
	if !seen[ArtifactRoleHostPackage] && !namespaceRoles[ArtifactRoleDefaultBuild] {
		return &ValidationError{Message: "URL-source builds require a host_package artifact (cvd-host_package.tar.gz), unless default_build is a gs:// directory that holds one"}
	}
	// The host reads the system image out of a target_files zip and resolves
	// that name before it downloads anything, on the system build and on the
	// default build alike. Only a listable prefix can answer for it.
	if seen[ArtifactRoleSystemImageBuild] &&
		(!namespaceRoles[ArtifactRoleSystemImageBuild] || !namespaceRoles[ArtifactRoleDefaultBuild]) {
		return &ValidationError{Message: "system_image_build requires both it and default_build to be gs:// directories, because the host resolves a target_files zip name from each"}
	}
	return nil
}

// urlShape splits a spec URL the way cvd parses one: the trailing
// "#sha256=<hex>" fragment comes off first, then a "{filepath}" selector, so
// whatever query string remains belongs to the URL itself.
func urlShape(raw string) (string, string, bool) {
	if i := strings.LastIndex(raw, "#"); i >= 0 {
		raw = raw[:i]
	}
	base, selector, hasSelector := splitSelector(raw)
	if i := strings.Index(base, "?"); i >= 0 {
		base = base[:i]
	}
	return base, selector, hasSelector
}

// isNamespaceURL reports the directory form: a path ending in "/" names the
// prefix holding a build's artifacts rather than one object.
func isNamespaceURL(base string) bool {
	return strings.HasSuffix(base, "/")
}

// validateSpecFields checks a single spec's type, URL, and role are present,
// known, and mutually consistent.
func validateSpecFields(spec ArtifactSpec) error {
	if spec.Type == "" {
		return &ValidationError{Message: "artifact spec type is required"}
	}
	if spec.URL == "" {
		return &ValidationError{Message: "artifact spec URL is required"}
	}
	if spec.Type != ArtifactSpecTypeGCS && spec.Type != ArtifactSpecTypeHTTPS {
		return &ValidationError{Message: "artifact spec type must be 'gcs' or 'https'"}
	}
	if spec.Role == "" {
		return &ValidationError{Message: "artifact spec role is required"}
	}
	if !slices.Contains(ValidArtifactRoles, spec.Role) {
		return &ValidationError{Message: "invalid artifact spec role"}
	}
	if spec.Type == ArtifactSpecTypeGCS && !strings.HasPrefix(spec.URL, "gs://") {
		return &ValidationError{Message: "GCS artifact URL must start with gs://"}
	}
	if spec.Type == ArtifactSpecTypeHTTPS && !strings.HasPrefix(spec.URL, "https://") {
		return &ValidationError{Message: "HTTPS artifact URL must start with https://"}
	}
	// cvd's build flags are comma-separated and split their values before
	// parsing them, so a literal comma truncates the URL on the host. Signers
	// percent-encode it; a raw one is a caller mistake worth a 400 here.
	if strings.Contains(spec.URL, ",") {
		return &ValidationError{Message: "artifact spec URL must not contain a comma"}
	}
	return nil
}

// validateSpecShape checks a spec's URL against what the host's fetcher can
// consume for its role. A trailing {filepath} selector extracts one member
// from a .zip.
func validateSpecShape(spec ArtifactSpec) error {
	base, selector, hasSelector := urlShape(spec.URL)
	if hasSelector && selector == "" {
		return &ValidationError{Message: string(spec.Role) + ": empty {filepath} selector"}
	}
	if isNamespaceURL(base) {
		// A gs:// prefix lists, so the host picks artifacts out of it by name.
		// Plain HTTPS offers no listing, so nothing under a bare prefix can be
		// discovered.
		if spec.Type != ArtifactSpecTypeGCS {
			return &ValidationError{Message: string(spec.Role) + ": a directory URL must be gs://, because an https:// prefix cannot be listed"}
		}
		return nil
	}
	switch spec.Role {
	case ArtifactRoleHostPackage:
		if hasSelector {
			return &ValidationError{Message: "host_package does not take a {filepath} selector"}
		}
		if !strings.HasSuffix(base, ".tar.gz") {
			return &ValidationError{Message: "host_package URL must end in .tar.gz"}
		}
	case ArtifactRoleDefaultBuild:
		if hasSelector {
			return &ValidationError{Message: "default_build does not take a {filepath} selector (it extracts a single file; a default build needs the full image set)"}
		}
		if !strings.HasSuffix(base, ".zip") {
			return &ValidationError{Message: "default_build URL must end in .zip"}
		}
	default:
		for _, ext := range []string{".tar", ".tar.gz", ".tgz", ".tar.xz", ".tar.bz2"} {
			if strings.HasSuffix(base, ext) {
				return &ValidationError{Message: string(spec.Role) + ": the host extracts only .zip archives for target artifacts, not " + ext}
			}
		}
		if hasSelector && !strings.HasSuffix(base, ".zip") {
			return &ValidationError{Message: string(spec.Role) + ": a {filepath} selector requires a .zip URL"}
		}
	}
	return nil
}

// splitSelector splits a trailing {filepath} selector off a URL, returning
// the base URL, the selector, and whether one was present.
func splitSelector(url string) (string, string, bool) {
	if !strings.HasSuffix(url, "}") {
		return url, "", false
	}
	i := strings.LastIndex(url, "{")
	if i < 0 {
		return url, "", false
	}
	return url[:i], url[i+1 : len(url)-1], true
}

// SpecsFingerprint derives a deterministic fingerprint for a spec set:
// SHA-256 over the sorted (role, type, url) triples. Register uses it to
// dedup URL-source registrations, which carry no Android build ID for the
// usual idempotency check. A caller-supplied fingerprint (e.g. computed
// from artifact content digests) takes precedence over this derivation.
func SpecsFingerprint(specs []ArtifactSpec) string {
	lines := make([]string, 0, len(specs))
	for _, s := range specs {
		lines = append(lines, string(s.Role)+"|"+string(s.Type)+"|"+s.URL)
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// CreateRequest represents a request to register a new build. It is an
// internal DTO (the wire input struct lives in the feature handler), so it
// carries no JSON tags.
type CreateRequest struct {
	Name    string
	BuildID string
	Target  string
	Branch  string
	Flavor  string
	Source  Source
	// ArtifactSpecs for SourceURL. Each specifies type (gcs/https) and URL.
	ArtifactSpecs []ArtifactSpec
	Fingerprint   string
	// CreatedBy is the email of the creator (informational/audit only).
	CreatedBy   string
	Description string
	IsDefault   bool
	Metadata    map[string]any
}

// UpdateRequest holds the mutable fields of a build. A nil field is left
// unchanged; a non-nil ArtifactSpecs replaces the existing specs.
type UpdateRequest struct {
	Name          *string
	Description   *string
	IsDefault     *bool
	ArtifactSpecs []ArtifactSpec
}

// ListFilter provides filtering options for listing builds
type ListFilter struct {
	Target    *string
	Branch    *string
	Source    *Source
	IsDefault *bool
	Limit     int
	Offset    int

	// AfterCreatedAt and AfterID are the sort key of the last row of the
	// previous keyset page. A non-empty AfterID returns only rows after it in
	// (created_at DESC, build_id DESC) order. Count ignores them.
	AfterCreatedAt time.Time
	AfterID        string
}

// Reader is the read side of the build registry, for consumers that resolve
// builds but never write them (the device feature's admission checks and
// declarative build resolution). The build repository satisfies it.
type Reader interface {
	// Get retrieves a build by ID
	Get(ctx context.Context, id string) (*Build, error)

	// List retrieves builds matching the filter
	List(ctx context.Context, filter ListFilter) ([]*Build, error)

	// Count returns the number of builds matching the filter, ignoring
	// pagination (same predicates as List).
	Count(ctx context.Context, filter ListFilter) (int64, error)
}

// Repository defines the data access interface for builds
type Repository interface {
	Reader

	// GetByBuildID retrieves a build by Android build ID
	GetByBuildID(ctx context.Context, buildID string) (*Build, error)

	// GetByFingerprint retrieves a build by its artifact fingerprint.
	GetByFingerprint(ctx context.Context, fingerprint string) (*Build, error)

	// Create stores a new build
	Create(ctx context.Context, build *Build) error

	// Update modifies an existing build
	Update(ctx context.Context, build *Build) error

	// Delete removes a build
	Delete(ctx context.Context, id string) error
}

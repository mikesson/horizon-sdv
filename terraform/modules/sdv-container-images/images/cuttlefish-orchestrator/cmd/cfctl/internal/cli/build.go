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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// BuildCmd contains build registry subcommands.
type BuildCmd struct {
	Push     BuildPushCmd     `cmd:"" help:"Push local build artifacts to the cloud and register them as a build"`
	Register BuildRegisterCmd `cmd:"" help:"Register an Android CI build by its build ID and target"`
	List     BuildListCmd     `cmd:"" help:"List registered builds"`
	Get      BuildGetCmd      `cmd:"" aliases:"show,info" help:"Get build details"`
	Update   BuildUpdateCmd   `cmd:"" help:"Change a build's name, description, default flag or artifacts"`
	Delete   BuildDeleteCmd   `cmd:"" aliases:"rm,remove" help:"Delete a build from the registry"`
}

// BuildPushCmd implements 'cfctl build push'. It pushes the images zip and
// cvd-host_package.tar.gz, inferring roles from the file names. Content is
// addressed by SHA-256, so an identical set returns the existing build.
type BuildPushCmd struct {
	Files       []string       `arg:"" optional:"" type:"existingfile" help:"Artifacts to push: the images zip (*-img-*.zip) and cvd-host_package.tar.gz"`
	DistDir     string         `name:"dist-dir" type:"existingdir" placeholder:"DIR" help:"Find the two artifacts in an 'm dist' output directory instead of naming them"`
	ProductOut  ProductOutFlag `name:"product-out" help:"Pack the required images from an Android product-out directory (--product-out=DIR, or $ANDROID_PRODUCT_OUT when no DIR is given) and push them with the host package"`
	HostPackage string         `name:"host-package" type:"existingfile" placeholder:"FILE" help:"Host package for --product-out (default: $ANDROID_HOST_OUT/cvd-host_package.tar.gz)"`
	Name        string         `help:"Build display name (default: derived from the images zip)"`
	Description string         `help:"Build description"`
	Default     bool           `help:"Mark as the default build"`
}

// resolveDistDir finds the two push artifacts in an `m dist` output
// directory: exactly one images zip, plus the host package.
func resolveDistDir(dir string) ([]string, error) {
	zips, _ := filepath.Glob(filepath.Join(dir, "*-img-*.zip"))
	switch len(zips) {
	case 1:
	case 0:
		return nil, fmt.Errorf("no images zip (*-img-*.zip) in %s. Is this an 'm dist' output directory?", dir)
	default:
		return nil, fmt.Errorf("%d images zips in %s. Name the files explicitly instead of --dist-dir", len(zips), dir)
	}
	hostPkg := filepath.Join(dir, "cvd-host_package.tar.gz")
	if _, err := os.Stat(hostPkg); err != nil {
		return nil, fmt.Errorf("no cvd-host_package.tar.gz in %s (build it with 'm hosttar')", dir)
	}
	return []string{zips[0], hostPkg}, nil
}

// Run executes the build push command.
func (c *BuildPushCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	opts := pushOptions{Name: c.Name, Description: c.Description, Default: c.Default}
	var b *cco.Build
	var err error
	if c.ProductOut.Set() {
		if c.DistDir != "" || len(c.Files) > 0 {
			return errors.New("--product-out is mutually exclusive with named files and --dist-dir")
		}
		b, err = pushProductOut(ctx, cli, client, c.ProductOut, c.HostPackage, opts)
	} else {
		var files []string
		if files, err = c.pushFiles(); err != nil {
			return err
		}
		b, err = pushBuild(ctx, cli, client, files, opts)
	}
	if err != nil {
		return err
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(b))
	}
	output.Success("Build registered: %s (%s)", b.Name, b.ID)
	cli.Hint("Create a device from it with 'cfctl device create --build-id %s'", b.ID)
	return nil
}

// pushFiles returns the named files or the files found in --dist-dir.
func (c *BuildPushCmd) pushFiles() ([]string, error) {
	switch {
	case c.HostPackage != "":
		return nil, errors.New("--host-package only applies to --product-out; name the host package as a file instead")
	case c.DistDir != "" && len(c.Files) > 0:
		return nil, errors.New("--dist-dir and named files are mutually exclusive")
	case c.DistDir != "":
		return resolveDistDir(c.DistDir)
	case len(c.Files) == 0:
		return nil, errors.New("name the artifacts to push, use --dist-dir DIR to find them in an 'm dist' output directory, or --product-out to pack a local build")
	}
	return c.Files, nil
}

// BuildRegisterCmd implements 'cfctl build register'. It registers an
// Android CI build. Registering a build ID again returns the existing record.
type BuildRegisterCmd struct {
	BuildID     string `name:"build-id" required:"" placeholder:"BUILD_ID" help:"Android CI build ID (from ci.android.com)"`
	Target      string `required:"" placeholder:"TARGET" help:"Build target (e.g., aosp_cf_x86_64_phone-trunk_staging-userdebug)"`
	Branch      string `placeholder:"BRANCH" help:"Branch the build came from (informational)"`
	Name        string `placeholder:"NAME" help:"Build display name (default: TARGET-BUILD_ID)"`
	Description string `placeholder:"TEXT" help:"Build description"`
	Default     bool   `help:"Mark as the default build for its target"`
}

// Run registers the build.
func (c *BuildRegisterCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	b, err := registerAndroidBuild(ctx, client, androidBuild{
		buildID: c.BuildID, target: c.Target, branch: c.Branch,
		name: c.Name, description: c.Description, isDefault: c.Default,
	})
	if err != nil {
		return err
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(b))
	}
	output.Success("Build registered: %s (%s)", b.Name, b.ID)
	cli.Hint("Create a device from it with 'cfctl device create --build-id %s'", b.ID)
	return nil
}

// androidBuild is the registration of an Android CI build.
type androidBuild struct {
	buildID, target, branch string
	name, description       string
	isDefault               bool
}

// registerAndroidBuild registers an android_build source build. The server
// returns the existing record for a build ID it already holds, whatever
// target that record has, so a mismatch is reported instead of passed over.
func registerAndroidBuild(ctx context.Context, client *cco.Client, ab androidBuild) (*cco.Build, error) {
	name := ab.name
	if name == "" {
		name = ab.target + "-" + ab.buildID
	}
	b, err := client.RegisterBuild(ctx, cco.RegisterBuildRequest{
		Name:        name,
		Source:      "android_build",
		BuildID:     ab.buildID,
		Target:      ab.target,
		Branch:      ab.branch,
		Description: ab.description,
		IsDefault:   ab.isDefault,
	})
	if err != nil {
		return nil, fmt.Errorf("registering build %s: %w", ab.buildID, err)
	}
	if b.Target != "" && b.Target != ab.target {
		output.Warn("build %s is already registered as %s for target %s; the requested target %s is ignored", ab.buildID, b.ID, b.Target, ab.target)
	}
	return b, nil
}

// pushOptions carries the registration fields of a push.
type pushOptions struct {
	Name        string
	Description string
	Default     bool
}

// pushBuild stages the artifacts and registers them as a URL-source build.
// Shared by 'build push' and 'device create --local-image'.
func pushBuild(ctx context.Context, cli *CLI, client *cco.Client, files []string, opts pushOptions) (*cco.Build, error) {
	artifacts := make([]pushArtifact, 0, len(files))
	for _, path := range files {
		base := filepath.Base(path)
		role, err := inferArtifactRole(base)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		artifacts = append(artifacts, pushArtifact{name: base, role: role, file: cco.UploadFile{Path: path, Size: info.Size()}})
	}

	specs := make([]cco.ArtifactSpec, 0, len(artifacts))
	digests := make([]string, 0, len(artifacts))
	for i := range artifacts {
		a := &artifacts[i]
		sha, objectURL, err := stageArtifact(ctx, cli, client, a)
		if err != nil {
			return nil, err
		}
		digests = append(digests, a.role+"|"+sha)
		specs = append(specs, cco.ArtifactSpec{Type: "gcs", URL: objectURL, Role: a.role})
	}

	// Identical bytes in identical roles dedup to the existing build,
	// regardless of file paths or push order.
	slices.Sort(digests)
	fpSum := sha256.Sum256([]byte(strings.Join(digests, "\n")))

	name := opts.Name
	if name == "" {
		name = defaultBuildName(artifacts[0].name, files)
	}
	b, err := client.RegisterBuild(ctx, cco.RegisterBuildRequest{
		Name:          name,
		Source:        "url",
		ArtifactSpecs: specs,
		Fingerprint:   hex.EncodeToString(fpSum[:]),
		Description:   opts.Description,
		IsDefault:     opts.Default,
	})
	if err != nil {
		return nil, fmt.Errorf("registering build: %w", err)
	}
	return b, nil
}

// pushArtifact is one local file of a push.
type pushArtifact struct {
	name, role string
	file       cco.UploadFile
}

// stageArtifact hashes the file, starts its staging upload and sends the
// bytes unless the content is already staged. It returns the SHA-256 and the
// staged object URL.
func stageArtifact(ctx context.Context, cli *CLI, client *cco.Client, a *pushArtifact) (string, string, error) {
	if !cli.JSON {
		output.Info("Hashing %s (%s)...", a.name, formatBytes(a.file.Size))
	}
	d, err := fileDigests(a.file.Path)
	if err != nil {
		return "", "", err
	}
	if d.size != a.file.Size {
		return "", "", fmt.Errorf("%s: %w", a.name, errFileChangedDuringPush)
	}
	a.file.CRC32C = d.crc32c

	staged, err := client.StartBuildUpload(ctx, cco.StartBuildUploadRequest{
		FileName:  a.name,
		SizeBytes: a.file.Size,
		SHA256:    d.sha256,
	})
	if err != nil {
		return "", "", fmt.Errorf("starting upload for %s: %w", a.name, err)
	}
	if staged.AlreadyExists {
		if !cli.JSON {
			output.Info("%s already staged, skipping upload", a.name)
		}
		return d.sha256, staged.ObjectURL, nil
	}
	err = uploadWithProgress(ctx, cli, staged.SessionURL, a.name, a.file)
	if errors.Is(err, cco.ErrUploadChecksumMismatch) {
		err = errFileChangedDuringPush
	}
	if err != nil {
		return "", "", fmt.Errorf("uploading %s: %w", a.name, err)
	}
	return d.sha256, staged.ObjectURL, nil
}

// inferArtifactRole maps an artifact filename to its build role. The server
// enforces one host_package and one default_build.
func inferArtifactRole(name string) (string, error) {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, "cvd-host_package.tar.gz") || strings.HasSuffix(lower, "cvd-host-package.tar.gz") {
		return "host_package", nil
	}
	if strings.Contains(lower, "-img-") && strings.HasSuffix(lower, ".zip") {
		return "default_build", nil
	}
	return "", fmt.Errorf("cannot infer a role for %q: push the images zip (<target>-img-<build>.zip) and cvd-host_package.tar.gz; other artifact roles are registered via the API or console", name)
}

// defaultBuildName derives a display name from the images zip, falling back
// to the first file pushed.
func defaultBuildName(first string, files []string) string {
	for _, f := range files {
		base := filepath.Base(f)
		if strings.Contains(base, "-img-") {
			return strings.TrimSuffix(base, ".zip")
		}
	}
	return strings.TrimSuffix(strings.TrimSuffix(first, ".tar.gz"), ".zip")
}

// errFileChangedDuringPush reports content that no longer matches the
// digests computed at the start of the push. GCS refuses the bytes, so the
// staging object for that digest stays unwritten and a rerun is safe.
var errFileChangedDuringPush = errors.New("the file changed during the push; rerun once it is stable")

// fileDigest is a file's SHA-256 (lowercase hex), CRC32C (Castagnoli) and
// byte count, taken in one read pass.
type fileDigest struct {
	sha256 string
	crc32c uint32
	size   int64
}

// fileDigests reads path once and returns its digests.
func fileDigests(path string) (fileDigest, error) {
	f, err := os.Open(path)
	if err != nil {
		return fileDigest{}, fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	sha := sha256.New()
	crc := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	n, err := io.Copy(io.MultiWriter(sha, crc), f)
	if err != nil {
		return fileDigest{}, fmt.Errorf("hashing %s: %w", path, err)
	}
	return fileDigest{sha256: hex.EncodeToString(sha.Sum(nil)), crc32c: crc.Sum32(), size: n}, nil
}

// uploadWithProgress runs the resumable upload and reports progress in 10%
// steps.
func uploadWithProgress(ctx context.Context, cli *CLI, sessionURL, name string, file cco.UploadFile) error {
	size := file.Size
	if !cli.JSON {
		output.Info("Uploading %s (%s)...", name, formatBytes(size))
	}
	var lastStep int64 = -1
	progress := func(sent int64) {
		if cli.JSON || size == 0 {
			return
		}
		step := sent * 10 / size
		if step > lastStep && step < 10 {
			lastStep = step
			output.Info("  %s: %d%%", name, step*10)
		}
	}
	if err := cco.UploadFileToSession(ctx, sessionURL, file, progress); err != nil {
		return fmt.Errorf("session upload: %w", err)
	}
	return nil
}

// formatBytes renders a byte count with a binary unit.
func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// BuildListCmd implements 'cfctl build list'.
type BuildListCmd struct{}

// Run executes the build list command.
func (c *BuildListCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	builds, err := client.ListBuilds(ctx)
	if err != nil {
		return fmt.Errorf("failed to list builds: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(builds))
	}
	return printBuilds(builds)
}

// BuildGetCmd implements 'cfctl build get'.
type BuildGetCmd struct {
	ID string `arg:"" required:"" placeholder:"BUILD_ID" help:"Build ID"`
}

// Run shows one build.
func (c *BuildGetCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	b, err := client.GetBuild(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to get build: %w", err)
	}
	if cli.JSON {
		return wrapJSONErr(output.PrintJSON(b))
	}
	printBuild(b)
	return nil
}

// BuildDeleteCmd implements 'cfctl build delete'.
type BuildDeleteCmd struct {
	ID    string `arg:"" required:"" placeholder:"BUILD_ID" help:"Build ID"`
	Force bool   `short:"f" help:"Skip confirmation prompt"`
}

// Run deletes the build.
func (c *BuildDeleteCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	return deleteFlow{
		resource: "build", id: c.ID, force: c.Force,
		del: client.DeleteBuild,
	}.run(ctx, cli)
}

// artifactRoles is the CLI's copy of the server's artifact-role vocabulary;
// a reflection test pins the two together.
var artifactRoles = []string{
	"host_package", "default_build", "kernel_build",
	"boot_build", "bootloader_build", "system_image_build",
}

// parseArtifactSpecs turns ROLE=URL arguments into specs, inferring the
// spec type from the URL scheme.
func parseArtifactSpecs(args []string) ([]cco.ArtifactSpec, error) {
	specs := make([]cco.ArtifactSpec, 0, len(args))
	for _, a := range args {
		role, u, ok := strings.Cut(a, "=")
		if !ok {
			return nil, fmt.Errorf("--artifact takes ROLE=URL, got %q", a)
		}
		if !slices.Contains(artifactRoles, role) {
			return nil, fmt.Errorf("unknown artifact role %q (roles: %s)", role, strings.Join(artifactRoles, ", "))
		}
		var typ string
		switch {
		case strings.HasPrefix(u, "gs://"):
			typ = "gcs"
		case strings.HasPrefix(u, "https://"):
			typ = "https"
		default:
			return nil, fmt.Errorf("artifact URL must start with gs:// or https://, got %q", u)
		}
		specs = append(specs, cco.ArtifactSpec{Type: typ, URL: u, Role: role})
	}
	return specs, nil
}

// BuildUpdateCmd implements 'cfctl build update'. The PATCH is tri-state:
// an omitted flag leaves that field alone, and --artifact replaces the
// whole artifact set.
type BuildUpdateCmd struct {
	ID          string   `arg:"" placeholder:"BUILD_ID" help:"Build ID"`
	Name        *string  `help:"New display name"`
	Description *string  `help:"New description"`
	Default     bool     `help:"Make this the default build for its target"`
	Artifact    []string `placeholder:"ROLE=URL" help:"Replace the artifact set with these specs (repeatable; roles: host_package, default_build, kernel_build, boot_build, bootloader_build, system_image_build; gs:// or https:// URLs)"`
}

// Run patches the build.
func (c *BuildUpdateCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	req := cco.UpdateBuildRequest{Name: c.Name, Description: c.Description}
	if c.Default {
		t := true
		req.IsDefault = &t
	}
	if len(c.Artifact) > 0 {
		specs, err := parseArtifactSpecs(c.Artifact)
		if err != nil {
			return err
		}
		req.ArtifactSpecs = specs
	}
	if req.Name == nil && req.Description == nil && req.IsDefault == nil && req.ArtifactSpecs == nil {
		return errors.New("nothing to change: pass --name, --description, --default or --artifact")
	}
	b, err := client.UpdateBuild(ctx, c.ID, req)
	if err != nil {
		return fmt.Errorf("failed to update build: %w", err)
	}
	if cli.JSON {
		return printJSON(b)
	}
	output.Success("Updated build %s.", b.ID)
	printBuild(b)
	return nil
}

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
	"bufio"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alecthomas/kong"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// ProductOutFlag is the --product-out flag. Given alone it reads
// $ANDROID_PRODUCT_OUT; --product-out=DIR and --product-out DIR name the
// directory. It must never get an env tag: Kong decodes any set variable, even
// an empty one, as the flag being given.
type ProductOutFlag struct {
	set bool
	dir string
}

// Decode implements kong.MapperValue. It takes the next token as the
// directory only when that token is a value and not another flag.
func (p *ProductOutFlag) Decode(ctx *kong.DecodeContext) error {
	p.set = true
	tok := ctx.Scan.Peek()
	if tok.Type != kong.FlagValueToken && (tok.InferredType() != kong.UntypedToken || !tok.IsValue()) {
		return nil
	}
	ctx.Scan.Pop()
	dir, ok := tok.Value.(string)
	if !ok {
		return fmt.Errorf("expected a directory, got %v", tok.Value)
	}
	p.dir = dir
	return nil
}

// IsBool lets --product-out stand without a value.
func (p *ProductOutFlag) IsBool() bool { return true }

// Set reports whether --product-out was given.
func (p *ProductOutFlag) Set() bool { return p.set }

// Environment variables an Android build shell sets after lunch.
const (
	envProductOut    = "ANDROID_PRODUCT_OUT"
	envHostOut       = "ANDROID_HOST_OUT"
	envBuildTop      = "ANDROID_BUILD_TOP"
	envTargetProduct = "TARGET_PRODUCT"
)

// requiredImagesPath is the list of images a Cuttlefish launch needs,
// relative to the Android tree. cvdr reads the same file.
const requiredImagesPath = "device/google/cuttlefish/required_images"

// defaultRequiredImages is a copy of requiredImagesPath for when the tree is
// not available.
var defaultRequiredImages = []string{
	"android-info.txt",
	"boot.img",
	"init_boot.img",
	"super.img",
	"userdata.img",
	"vbmeta.img",
	"vbmeta_system_dlkm.img",
	"vbmeta_system.img",
	"vbmeta_vendor_dlkm.img",
	"vendor_boot.img",
}

// productOutZipTime is the modification time of every entry in a packed
// images zip. A fixed time keeps the bytes, and so the content hash, the same
// when the images are unchanged.
var productOutZipTime = time.Date(2008, 1, 1, 0, 0, 0, 0, time.UTC)

// productOutSource is a resolved --product-out push: the images directory,
// the image names to pack, the zip's target name and the host package.
type productOutSource struct {
	dir         string
	images      []string
	target      string
	hostPackage string
}

// resolveProductOut finds the product-out directory, the images to pack and
// the host package, and checks that every one of them exists.
func resolveProductOut(flag ProductOutFlag, hostPackage string) (*productOutSource, error) {
	dir := flag.dir
	if dir == "" {
		dir = os.Getenv(envProductOut)
	}
	if dir == "" {
		return nil, fmt.Errorf("--product-out needs a directory: pass --product-out=DIR, or run lunch so $%s is set", envProductOut)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("product-out directory %s does not exist", dir)
	}

	images, err := requiredImages()
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, name := range images {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || !info.Mode().IsRegular() {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%s is missing %s. Build the images with 'm' first", dir, strings.Join(missing, ", "))
	}

	hostPkg, err := resolveHostPackage(hostPackage)
	if err != nil {
		return nil, err
	}

	target := os.Getenv(envTargetProduct)
	if target == "" || strings.ContainsAny(target, `/\`) {
		target = filepath.Base(filepath.Clean(dir))
	}
	return &productOutSource{dir: dir, images: images, target: target, hostPackage: hostPkg}, nil
}

// requiredImages reads the image list from $ANDROID_BUILD_TOP when the tree
// carries it, and falls back to the built-in copy otherwise.
func requiredImages() ([]string, error) {
	top := os.Getenv(envBuildTop)
	if top == "" {
		return slices.Clone(defaultRequiredImages), nil
	}
	f, err := os.Open(filepath.Join(top, requiredImagesPath))
	if errors.Is(err, os.ErrNotExist) {
		return slices.Clone(defaultRequiredImages), nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening the required images list: %w", err)
	}
	defer func() { _ = f.Close() }()
	return parseRequiredImages(f)
}

// parseRequiredImages reads one image name per line, skipping blank lines
// and comments. A name with a path separator is refused, since every image
// sits directly in the product-out directory.
func parseRequiredImages(r io.Reader) ([]string, error) {
	var images []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.ContainsAny(line, `/\`) || line == ".." {
			return nil, fmt.Errorf("required images list names %q, which is not a file name", line)
		}
		images = append(images, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading the required images list: %w", err)
	}
	if len(images) == 0 {
		return nil, errors.New("the required images list is empty")
	}
	return images, nil
}

// resolveHostPackage returns the --host-package path, or the host package in
// $ANDROID_HOST_OUT. The default location is checked against its unpacked
// directory, as cvdr does, because 'm' rebuilds the directory without
// repacking the tarball.
func resolveHostPackage(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	hostOut := os.Getenv(envHostOut)
	if hostOut == "" {
		return "", fmt.Errorf("no host package: pass --host-package FILE, or run lunch so $%s is set", envHostOut)
	}
	path := filepath.Join(hostOut, "cvd-host_package.tar.gz")
	tarInfo, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("no cvd-host_package.tar.gz in %s (build it with 'm hosttar')", hostOut)
	}
	dirInfo, err := os.Stat(filepath.Join(hostOut, "cvd-host_package"))
	if err != nil {
		return "", fmt.Errorf("cannot check %s against the cvd-host_package directory beside it (%w); build with 'm hosttar' or pass --host-package FILE", path, err)
	}
	if tarInfo.ModTime().Before(dirInfo.ModTime()) {
		return "", fmt.Errorf("%s is older than the cvd-host_package directory beside it; rebuild it with 'm hosttar'", path)
	}
	return path, nil
}

// packImages writes the images into outDir/<target>-img-local.zip and returns
// its path. Entries are sorted, with a fixed time, mode and compression
// level, so unchanged images produce a byte-identical zip.
func packImages(src *productOutSource, outDir string) (string, error) {
	zipPath := filepath.Join(outDir, src.target+"-img-local.zip")
	f, err := os.OpenFile(zipPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("creating %s: %w", zipPath, err)
	}
	err = writeImagesZip(f, src)
	if cerr := f.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("closing %s: %w", zipPath, cerr)
	}
	if err != nil {
		return "", err
	}
	return zipPath, nil
}

// writeImagesZip writes the sorted images as a zip to w.
func writeImagesZip(w io.Writer, src *productOutSource) error {
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestSpeed)
	})
	for _, name := range slices.Sorted(slices.Values(src.images)) {
		if err := addZipEntry(zw, filepath.Join(src.dir, name), name); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("finishing the images zip: %w", err)
	}
	return nil
}

// addZipEntry copies one file into the zip under name.
func addZipEntry(zw *zip.Writer, path, name string) error {
	in, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = in.Close() }()
	hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: productOutZipTime}
	hdr.SetMode(0o644)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return fmt.Errorf("adding %s to the images zip: %w", name, err)
	}
	if _, err := io.Copy(w, in); err != nil {
		return fmt.Errorf("packing %s: %w", path, err)
	}
	return nil
}

// pushProductOut packs the product-out images into a temporary images zip,
// pushes it with the host package and removes the zip afterwards. Shared by
// 'build push' and 'device create'.
func pushProductOut(ctx context.Context, cli *CLI, client *cco.Client, flag ProductOutFlag, hostPackage string, opts pushOptions) (*cco.Build, error) {
	src, err := resolveProductOut(flag, hostPackage)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "cfctl-product-out-")
	if err != nil {
		return nil, fmt.Errorf("creating a temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if !cli.JSON {
		output.Info("Packing %d images from %s...", len(src.images), src.dir)
	}
	zipPath, err := packImages(src, tmp)
	if err != nil {
		return nil, err
	}
	return pushBuild(ctx, cli, client, []string{zipPath, src.hostPackage}, opts)
}

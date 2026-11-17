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
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
)

// StorageCmd browses the artifact buckets. ls lists buckets or a bucket's
// objects, and rm deletes one object.
type StorageCmd struct {
	Ls StorageLsCmd `cmd:"" help:"List buckets, or a bucket's objects when BUCKET is given"`
	Rm StorageRmCmd `cmd:"" help:"Delete an object from a bucket"`
}

// StorageLsCmd implements 'cfctl storage ls'.
type StorageLsCmd struct {
	Bucket string `arg:"" optional:"" placeholder:"BUCKET" help:"Bucket to list, optionally as bucket/prefix or a gs:// URL; omit to list the buckets themselves"`
	Prefix string `help:"Only objects under this name prefix (with BUCKET)"`
}

// splitBucketPath splits "gs://bucket/prefix/", "bucket/prefix" or "bucket"
// into bucket and prefix. A prefix in arg is joined ahead of flagPrefix.
func splitBucketPath(arg, flagPrefix string) (string, string) {
	arg = strings.TrimPrefix(arg, "gs://")
	bucket, prefix, _ := strings.Cut(arg, "/")
	switch {
	case prefix == "":
		prefix = flagPrefix
	case flagPrefix != "":
		prefix = strings.TrimSuffix(prefix, "/") + "/" + flagPrefix
	}
	return bucket, prefix
}

// Run lists buckets or objects.
func (c *StorageLsCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	if c.Bucket == "" {
		if c.Prefix != "" {
			return errors.New("--prefix filters objects; name the BUCKET to list")
		}
		resp, err := client.ListStorageBuckets(ctx)
		if err != nil {
			return fmt.Errorf("failed to list buckets: %w", err)
		}
		if cli.JSON {
			return printJSON(resp)
		}
		return table("BUCKET\tLOCATION\tCREATED", func(w *tabwriter.Writer) {
			for _, b := range resp.Buckets {
				created := ""
				if !b.CreatedAt.IsZero() {
					created = b.CreatedAt.Format(time.RFC3339)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", b.Name, b.Location, created)
			}
		})
	}

	bucket, prefix := splitBucketPath(c.Bucket, c.Prefix)
	resp, err := client.ListStorageObjects(ctx, bucket, prefix)
	if err != nil {
		return fmt.Errorf("failed to list objects in %s: %w", bucket, err)
	}
	if cli.JSON {
		return printJSON(resp)
	}
	return table("NAME\tSIZE\tUPDATED", func(w *tabwriter.Writer) {
		for _, o := range resp.Objects {
			size := ""
			if !o.IsFolder {
				size = strconv.FormatInt(o.Size, 10)
			}
			updated := ""
			if !o.UpdatedAt.IsZero() {
				updated = o.UpdatedAt.Format(time.RFC3339)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", o.Name, size, updated)
		}
	})
}

// StorageRmCmd implements 'cfctl storage rm'.
type StorageRmCmd struct {
	Bucket string `arg:"" placeholder:"BUCKET" help:"Bucket holding the object"`
	Object string `arg:"" placeholder:"OBJECT" help:"Object name (full path within the bucket)"`
	Force  bool   `short:"f" help:"Skip confirmation"`
}

// Run deletes the object.
func (c *StorageRmCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	return deleteFlow{
		resource: "object", id: c.Bucket + "/" + c.Object, force: c.Force,
		del: func(ctx context.Context, _ string) error {
			return client.DeleteStorageObject(ctx, c.Bucket, c.Object)
		},
	}.run(ctx, cli)
}

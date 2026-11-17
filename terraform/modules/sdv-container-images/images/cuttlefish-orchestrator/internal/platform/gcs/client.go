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

package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"

	storagedomain "cloud-cuttlefish-orchestrator/internal/domain/storage"
)

// gsURIFormat renders an object's gs:// URI from bucket and object path.
const gsURIFormat = "gs://%s/%s"

// mapGCSError translates a GCS SDK error into the storage domain sentinels so
// a missing object is a 404 and a denied bucket a 403, never a masked 500.
// Unclassified errors are wrapped with the resource for context and keep their
// 500 mapping.
func mapGCSError(err error, resource string) error {
	if errors.Is(err, storage.ErrObjectNotExist) || errors.Is(err, storage.ErrBucketNotExist) {
		return fmt.Errorf("%w: %s", storagedomain.ErrNotFound, resource)
	}
	if gerr, ok := errors.AsType[*googleapi.Error](err); ok {
		switch gerr.Code {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s", storagedomain.ErrNotFound, resource)
		case http.StatusForbidden:
			return fmt.Errorf("%w: %s", storagedomain.ErrPermissionDenied, resource)
		}
	}
	return fmt.Errorf("%s: %w", resource, err)
}

// Client provides GCS-backed storage operations for the configured project.
type Client struct {
	client    *storage.Client
	projectID string
}

// NewClient wraps the SDK client for storage operations in the given project.
//
// The adapter does not log: every failure path returns an error carrying the
// gs:// resource it concerns (mapGCSError), and the caller owns the decision
// of what to record.
func NewClient(client *storage.Client, projectID string) *Client {
	return &Client{
		client:    client,
		projectID: projectID,
	}
}

// ListBuckets lists the project's buckets.
func (c *Client) ListBuckets(ctx context.Context) (*storagedomain.ListBucketsResponse, error) {
	var buckets []storagedomain.Bucket
	it := c.client.Buckets(ctx, c.projectID)
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, mapGCSError(err, "listing buckets in project "+c.projectID)
		}
		buckets = append(buckets, storagedomain.Bucket{
			Name:      attrs.Name,
			Location:  attrs.Location,
			CreatedAt: attrs.Created,
		})
	}
	return &storagedomain.ListBucketsResponse{Buckets: buckets}, nil
}

// defaultObjectPageSize is the page size when a caller sends a page token
// without a size.
const defaultObjectPageSize = 1000

// ListObjects lists one folder level of a bucket: objects under the request
// prefix plus the sub-prefixes, delimiter defaulting to "/". With neither
// MaxItems nor PageToken set it returns the whole level in one response.
func (c *Client) ListObjects(ctx context.Context, req storagedomain.ListObjectsRequest) (*storagedomain.ListObjectsResponse, error) {
	bucket := c.client.Bucket(req.Bucket)
	query := &storage.Query{
		Prefix:    req.Prefix,
		Delimiter: req.Delimiter,
	}
	if query.Delimiter == "" {
		query.Delimiter = "/"
	}
	it := bucket.Objects(ctx, query)

	entries, next, err := listEntries(it, req.MaxItems, req.PageToken)
	if err != nil {
		return nil, mapGCSError(err, fmt.Sprintf("listing gs://%s/%s", req.Bucket, req.Prefix))
	}

	resp := &storagedomain.ListObjectsResponse{NextPageToken: next}
	for _, attrs := range entries {
		if attrs.Prefix != "" {
			resp.Prefixes = append(resp.Prefixes, attrs.Prefix)
			resp.Objects = append(resp.Objects, storagedomain.Object{
				Name:        attrs.Prefix,
				DisplayName: extractDisplayName(attrs.Prefix, true),
				Bucket:      req.Bucket,
				IsFolder:    true,
				GSURI:       fmt.Sprintf(gsURIFormat, req.Bucket, attrs.Prefix),
			})
			continue
		}

		if attrs.Name == req.Prefix {
			continue
		}

		resp.Objects = append(resp.Objects, storagedomain.Object{
			Name:        attrs.Name,
			DisplayName: extractDisplayName(attrs.Name, false),
			Bucket:      req.Bucket,
			Size:        attrs.Size,
			ContentType: attrs.ContentType,
			IsFolder:    false,
			UpdatedAt:   attrs.Updated,
			GSURI:       fmt.Sprintf(gsURIFormat, req.Bucket, attrs.Name),
			PublicURL:   fmt.Sprintf("https://storage.googleapis.com/%s/%s", req.Bucket, url.PathEscape(attrs.Name)),
			Generation:  attrs.Generation,
		})
	}
	return resp, nil
}

// listEntries drains the iterator, or reads one page of it when either a
// page size or a page token is set.
func listEntries(it *storage.ObjectIterator, pageSize int, pageToken string) ([]*storage.ObjectAttrs, string, error) {
	var entries []*storage.ObjectAttrs
	if pageSize > 0 || pageToken != "" {
		if pageSize <= 0 {
			pageSize = defaultObjectPageSize
		}
		next, err := iterator.NewPager(it, pageSize, pageToken).NextPage(&entries)
		if err != nil {
			return nil, "", fmt.Errorf("reading a page: %w", err)
		}
		return entries, next, nil
	}
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return entries, "", nil
		}
		if err != nil {
			return nil, "", fmt.Errorf("reading the listing: %w", err)
		}
		entries = append(entries, attrs)
	}
}

// GetObject opens a reader over an object, returning its content type.
func (c *Client) GetObject(ctx context.Context, bucket, name string) (io.ReadCloser, string, error) {
	uri := fmt.Sprintf(gsURIFormat, bucket, name)
	obj := c.client.Bucket(bucket).Object(name)
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		return nil, "", mapGCSError(err, uri)
	}
	rc, err := obj.NewReader(ctx)
	if err != nil {
		return nil, "", mapGCSError(err, uri)
	}
	return rc, attrs.ContentType, nil
}

// UploadObject streams content into an object, overwriting any existing one.
func (c *Client) UploadObject(ctx context.Context, bucket, name string, content io.Reader, contentType string) error {
	uri := fmt.Sprintf(gsURIFormat, bucket, name)
	w := c.client.Bucket(bucket).Object(name).NewWriter(ctx)
	w.ContentType = contentType
	if _, err := io.Copy(w, content); err != nil {
		// Best effort: close writer to release resources; main error already
		// captured.
		_ = w.Close()
		return mapGCSError(err, "uploading "+uri)
	}
	if err := w.Close(); err != nil {
		return mapGCSError(err, "uploading "+uri)
	}
	return nil
}

// ObjectExists reports whether an object is present, without opening it.
func (c *Client) ObjectExists(ctx context.Context, bucket, name string) (bool, error) {
	_, err := c.client.Bucket(bucket).Object(name).Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return false, nil
	}
	if err != nil {
		return false, mapGCSError(err, fmt.Sprintf(gsURIFormat, bucket, name))
	}
	return true, nil
}

// StartResumableUpload initiates a GCS resumable-upload session for the
// object and returns the session URL. That URL is a bearer capability (single
// object, roughly one week) that a client PUTs the bytes to directly,
// bypassing this service's ingress and its request-size cap. The SDK
// performs resumable uploads internally but never exposes the session, so
// this speaks the JSON API with the ambient credentials.
func (c *Client) StartResumableUpload(ctx context.Context, bucket, name, contentType string, sizeBytes int64) (string, error) {
	uri := fmt.Sprintf(gsURIFormat, bucket, name)
	ts, err := google.DefaultTokenSource(ctx, storage.ScopeReadWrite)
	if err != nil {
		return "", fmt.Errorf("resolving credentials for %s: %w", uri, err)
	}
	initURL := fmt.Sprintf(
		"https://storage.googleapis.com/upload/storage/v1/b/%s/o?uploadType=resumable&name=%s",
		url.PathEscape(bucket), url.QueryEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("building session request for %s: %w", uri, err)
	}
	req.Header.Set("X-Upload-Content-Type", contentType)
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(sizeBytes, 10))
	resp, err := oauth2.NewClient(ctx, ts).Do(req)
	if err != nil {
		return "", fmt.Errorf("starting upload session for %s: %w", uri, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("starting upload session for %s: HTTP %d: %s", uri, resp.StatusCode, string(body))
	}
	session := resp.Header.Get("Location")
	if session == "" {
		return "", fmt.Errorf("starting upload session for %s: response carried no session URL", uri)
	}
	return session, nil
}

// DeleteObject removes an object.
func (c *Client) DeleteObject(ctx context.Context, bucket, name string) error {
	if err := c.client.Bucket(bucket).Object(name).Delete(ctx); err != nil {
		return mapGCSError(err, fmt.Sprintf(gsURIFormat, bucket, name))
	}
	return nil
}

// VerifyAccess performs a real read against GCS with the orchestrator's own
// credentials: object Attrs for an exact path, a one-result prefix listing for
// a folder path or bare bucket. Host-side access is not probed (hosts use
// their own service account; the orchestrator cannot impersonate it), which
// the response reflects by leaving HostAccess unset.
func (c *Client) VerifyAccess(ctx context.Context, req storagedomain.VerifyAccessRequest) *storagedomain.VerifyAccessResponse {
	res := &storagedomain.VerifyAccessResponse{GSURI: fmt.Sprintf(gsURIFormat, req.Bucket, req.ObjectPath)}
	check := &storagedomain.AccessCheckResult{
		ServiceAccount: "orchestrator",
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	res.OrchestratorAccess = check

	err := c.probeRead(ctx, req.Bucket, req.ObjectPath)
	switch {
	case err == nil:
		check.HasAccess = true
		res.CanProceed = true
	case errors.Is(err, storage.ErrObjectNotExist):
		// Readable bucket, missing object: access is fine, the path is not.
		check.HasAccess = true
		res.Message = "object not found: " + res.GSURI
	default:
		check.Error = err.Error()
		res.Message = "orchestrator cannot read " + res.GSURI
	}

	if req.CheckWrite && check.HasAccess {
		if err := c.probeWrite(ctx, req.Bucket); err != nil {
			check.HasAccess = false
			check.Error = err.Error()
			res.CanProceed = false
			res.Message = "orchestrator cannot write to gs://" + req.Bucket
		}
	}
	return res
}

// probeWrite verifies writability by writing and deleting a marker object.
// The marker name is fixed: concurrent probes just overwrite each other, and
// a leaked marker from a crashed probe is overwritten by the next one.
func (c *Client) probeWrite(ctx context.Context, bucket string) error {
	const marker = ".cco-write-probe"
	obj := c.client.Bucket(bucket).Object(marker)
	w := obj.NewWriter(ctx)
	if _, err := w.Write([]byte("cco write probe")); err != nil {
		_ = w.Close()
		return mapGCSError(err, fmt.Sprintf("writing gs://%s/%s", bucket, marker))
	}
	if err := w.Close(); err != nil {
		return mapGCSError(err, fmt.Sprintf("writing gs://%s/%s", bucket, marker))
	}
	if err := obj.Delete(ctx); err != nil {
		return mapGCSError(err, fmt.Sprintf("deleting gs://%s/%s", bucket, marker))
	}
	return nil
}

// probeRead verifies readability: exact object Attrs first, then a one-result
// prefix listing (covering folder paths and the bare bucket).
func (c *Client) probeRead(ctx context.Context, bucket, objectPath string) error {
	if objectPath != "" && !strings.HasSuffix(objectPath, "/") {
		_, err := c.client.Bucket(bucket).Object(objectPath).Attrs(ctx)
		if err == nil || !errors.Is(err, storage.ErrObjectNotExist) {
			return err //nolint:wrapcheck // Sentinel must reach the caller unwrapped.
		}
		// Fall through: the exact object is missing, but the path may be a
		// folder prefix.
		objectPath += "/"
	}
	it := c.client.Bucket(bucket).Objects(ctx, &storage.Query{Prefix: objectPath})
	if _, err := it.Next(); err != nil && !errors.Is(err, iterator.Done) {
		return fmt.Errorf("listing gs://%s/%s: %w", bucket, objectPath, err)
	}
	return nil
}

func extractDisplayName(fullPath string, isFolder bool) string {
	cleanPath := strings.TrimSuffix(fullPath, "/")
	name := path.Base(cleanPath)
	if isFolder {
		name += "/"
	}
	return name
}

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

package storagefeature

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/storage"
	"cloud-cuttlefish-orchestrator/internal/platform/gcs"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// tagStorage groups the storage operations in the OpenAPI document.
const tagStorage = "Storage"

// StorageHandler handles storage-related requests.
type StorageHandler struct {
	store *gcs.Client
}

// NewStorageHandler creates a new storage handler.
func NewStorageHandler(store *gcs.Client) *StorageHandler {
	return &StorageHandler{
		store: store,
	}
}

// Register registers storage operations with the Huma API.
func (h *StorageHandler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-buckets",
		Method:      http.MethodGet,
		Path:        "/v2/storage/buckets",
		Summary:     "List buckets",
		Description: "Returns a list of available storage buckets",
		Tags:        []string{tagStorage},
		Security:    web.BearerAuth(),
	}, h.ListBucketsHuma)

	huma.Register(api, huma.Operation{
		OperationID: "list-objects",
		Method:      http.MethodGet,
		Path:        "/v2/storage/buckets/{bucket}/objects",
		Summary:     "List objects",
		Description: "Returns one folder level of a bucket. Set page_size to page it; next_page_token is empty on the last page",
		Tags:        []string{tagStorage},
		Security:    web.BearerAuth(),
	}, h.ListObjectsHuma)

	huma.Register(api, huma.Operation{
		OperationID: "delete-object",
		Method:      http.MethodDelete,
		Path:        "/v2/storage/buckets/{bucket}/objects/{name}",
		Summary:     "Delete an object",
		Description: "Removes an object from a bucket",
		Tags:        []string{tagStorage},
		Security:    web.BearerAuth(),
	}, h.DeleteObjectHuma)

	huma.Register(api, huma.Operation{
		OperationID: "verify-access",
		Method:      http.MethodPost,
		Path:        "/v2/storage/verify-access",
		Summary:     "Verify GCS access",
		Description: "Checks if the orchestrator and/or host has access to a GCS resource",
		Tags:        []string{tagStorage},
		Security:    web.BearerAuth(),
	}, h.VerifyAccessHuma)

	// GetObject and UploadObject stream object bytes, which Huma cannot
	// express; RegisterRaw binds them as raw chi routes.
}

// RegisterRaw binds the object byte-stream routes as raw chi handlers. They
// stream request/response bodies without buffering, which Huma cannot express.
func (h *StorageHandler) RegisterRaw(r chi.Router) {
	r.Get("/v2/storage/buckets/{bucket}/objects/*", h.GetObject)
	r.Post("/v2/storage/buckets/{bucket}/objects/*", h.UploadObject)
}

// ListBucketsHuma returns a list of storage buckets (Huma handler).
func (h *StorageHandler) ListBucketsHuma(ctx context.Context, _ *ListBucketsInput) (*ListBucketsOutput, error) {
	resp, err := h.store.ListBuckets(ctx)
	if err != nil {
		return nil, web.MapError(err)
	}
	return &ListBucketsOutput{Body: resp}, nil
}

// ListObjectsHuma returns a list of objects in a bucket (Huma handler).
func (h *StorageHandler) ListObjectsHuma(ctx context.Context, input *ListObjectsInput) (*ListObjectsOutput, error) {
	resp, err := h.store.ListObjects(ctx, storage.ListObjectsRequest{
		Bucket:    input.Bucket,
		Prefix:    input.Prefix,
		MaxItems:  input.PageSize,
		PageToken: input.PageToken,
	})
	if err != nil {
		return nil, web.MapError(err)
	}
	return &ListObjectsOutput{Body: resp}, nil
}

// VerifyAccessHuma checks if the orchestrator and/or host has access to a GCS resource (Huma handler).
func (h *StorageHandler) VerifyAccessHuma(ctx context.Context, input *VerifyAccessInput) (*VerifyAccessOutput, error) {
	resp := h.store.VerifyAccess(ctx, storage.VerifyAccessRequest{
		Bucket:     input.Body.Bucket,
		ObjectPath: input.Body.ObjectPath,
		HostID:     input.Body.HostID,
		CheckWrite: input.Body.CheckWrite,
	})
	return &VerifyAccessOutput{Body: resp}, nil
}

// DeleteObjectHuma removes an object from a bucket (Huma handler).
func (h *StorageHandler) DeleteObjectHuma(ctx context.Context, input *DeleteObjectInput) (*DeleteObjectOutput, error) {
	if input.Name == "" || strings.HasSuffix(input.Name, "/") {
		return nil, web.NewBadRequestError("Valid object name is required")
	}

	if err := h.store.DeleteObject(ctx, input.Bucket, input.Name); err != nil {
		return nil, web.MapError(err)
	}
	return &DeleteObjectOutput{}, nil
}

// Streaming HTTP handlers (Huma doesn't support streaming, so these remain as chi handlers)

// GetObject handles GET /storage/buckets/{bucket}/objects/* (streaming)
func (h *StorageHandler) GetObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	name := chi.URLParam(r, "*")

	reader, contentType, err := h.store.GetObject(r.Context(), bucket, name)
	if err != nil {
		web.RespondError(w, err)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", contentType)
	// Best effort: stream content to client; if fails, client likely disconnected
	_, _ = io.Copy(w, reader)
}

// UploadObject handles POST /storage/buckets/{bucket}/objects/* (streaming)
func (h *StorageHandler) UploadObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	name := chi.URLParam(r, "*")

	if name == "" {
		web.RespondError(w, web.NewBadRequestError("Object name is required"))
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	err := h.store.UploadObject(r.Context(), bucket, name, r.Body, contentType)
	if err != nil {
		web.RespondError(w, err)
		return
	}

	web.RespondCreated(w, map[string]string{
		"url": fmt.Sprintf("gs://%s/%s", bucket, name),
	})
}

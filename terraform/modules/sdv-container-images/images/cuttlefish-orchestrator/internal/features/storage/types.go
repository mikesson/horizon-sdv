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

import "cloud-cuttlefish-orchestrator/internal/domain/storage"

// ListBucketsInput represents the input for listing storage buckets.
type ListBucketsInput struct{}

// ListBucketsOutput represents the output for listing storage buckets.
type ListBucketsOutput struct {
	Body *storage.ListBucketsResponse
}

// ListObjectsInput represents the input for listing objects in a bucket.
type ListObjectsInput struct {
	Bucket    string `path:"bucket" required:"true" doc:"Bucket name"`
	Prefix    string `query:"prefix" doc:"Object name prefix filter"`
	PageSize  int    `query:"page_size" minimum:"0" maximum:"1000" doc:"Maximum entries per page; 0 with no page_token returns the whole folder level"`
	PageToken string `query:"page_token" doc:"Opaque token from a previous page's next_page_token"`
}

// ListObjectsOutput represents the output for listing objects.
type ListObjectsOutput struct {
	Body *storage.ListObjectsResponse
}

// The object byte streams (GET and PUT) carry no Huma input or output types:
// they are raw chi routes bound by RegisterRaw, because Huma cannot express a
// streaming body.

// DeleteObjectInput represents the input for deleting an object.
type DeleteObjectInput struct {
	Bucket string `path:"bucket" required:"true" doc:"Bucket name"`
	Name   string `path:"name" required:"true" doc:"Object name (path)"`
}

// DeleteObjectOutput represents the output for deleting an object.
type DeleteObjectOutput struct{}

// VerifyAccessInput represents the input for verifying access to a GCS resource.
type VerifyAccessInput struct {
	Body struct {
		Bucket     string `json:"bucket" required:"true" doc:"Bucket name"`
		ObjectPath string `json:"object_path,omitempty" doc:"Object path (optional)"`
		HostID     string `json:"host_id,omitempty" doc:"Host ID to check access for (optional)"`
		CheckWrite bool   `json:"check_write,omitempty" doc:"Additionally probe writability (write + delete a marker object)"`
	}
}

// VerifyAccessOutput represents the output for access verification.
type VerifyAccessOutput struct {
	Body *storage.VerifyAccessResponse
}

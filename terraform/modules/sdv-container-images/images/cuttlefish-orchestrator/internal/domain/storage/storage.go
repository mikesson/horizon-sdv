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

// Package storage provides domain types for cloud storage operations.
package storage

import "time"

// Bucket represents a GCS bucket
type Bucket struct {
	Name      string    `json:"name"`
	Location  string    `json:"location,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// Object represents a GCS object (file or folder)
type Object struct {
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Bucket      string    `json:"bucket"`
	Size        int64     `json:"size,omitempty"`
	ContentType string    `json:"content_type,omitempty"`
	IsFolder    bool      `json:"is_folder"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
	GSURI       string    `json:"gs_uri"`
	PublicURL   string    `json:"public_url"`
	Generation  int64     `json:"generation,omitempty"`
}

// ListBucketsResponse is the response for listing buckets
type ListBucketsResponse struct {
	Buckets []Bucket `json:"buckets"`
}

// ListObjectsRequest is the request for listing objects
type ListObjectsRequest struct {
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix,omitempty"`
	Delimiter string `json:"delimiter,omitempty"`
	MaxItems  int    `json:"max_items,omitempty"`
	PageToken string `json:"page_token,omitempty"`
}

// ListObjectsResponse is the response for listing objects
type ListObjectsResponse struct {
	Objects       []Object `json:"objects"`
	Prefixes      []string `json:"prefixes,omitempty"`
	NextPageToken string   `json:"next_page_token,omitempty"`
}

// AccessCheckResult represents the result of checking GCS access
type AccessCheckResult struct {
	ServiceAccount string   `json:"service_account"`
	HasAccess      bool     `json:"has_access"`
	Permissions    []string `json:"permissions,omitempty"`
	MissingPerms   []string `json:"missing_perms,omitempty"`
	Error          string   `json:"error,omitempty"`
	CheckedAt      string   `json:"checked_at"`
}

// VerifyAccessRequest is the request to verify GCS access
type VerifyAccessRequest struct {
	Bucket     string `json:"bucket"`
	ObjectPath string `json:"object_path,omitempty"`
	HostID     string `json:"host_id,omitempty"`
	// CheckWrite additionally probes writability (write + delete a marker
	// object), because artifact-capture buckets need write access as well as read.
	CheckWrite bool `json:"check_write,omitempty"`
}

// VerifyAccessResponse contains access check results
type VerifyAccessResponse struct {
	GSURI              string             `json:"gs_uri"`
	OrchestratorAccess *AccessCheckResult `json:"orchestrator_access"`
	HostAccess         *AccessCheckResult `json:"host_access,omitempty"`
	CanProceed         bool               `json:"can_proceed"`
	Message            string             `json:"message,omitempty"`
}

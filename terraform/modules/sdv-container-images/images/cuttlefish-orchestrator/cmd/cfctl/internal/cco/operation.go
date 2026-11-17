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

package cco

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Operation states as reported to waitFor.
const (
	// StatusOpRunning means the operation has not settled.
	StatusOpRunning = "running"
	// StatusOpDone means the operation settled successfully.
	StatusOpDone = "done"
	// StatusOpFailed means the operation settled with an error.
	StatusOpFailed = "failed"
)

// Operation is a durable handle for a long-running device verb.
type Operation struct {
	Name     string            `json:"name"`
	Done     bool              `json:"done"`
	Metadata OperationMetadata `json:"metadata"`
	Response json.RawMessage   `json:"response,omitempty"`
	Error    *OperationProblem `json:"error,omitempty"`
}

// OperationMetadata is an operation's derived progress.
type OperationMetadata struct {
	Verb          string     `json:"verb"`
	ResourceType  string     `json:"resource_type"`
	ResourceID    string     `json:"resource_id"`
	CreateTime    time.Time  `json:"create_time"`
	InstanceName  string     `json:"instance_name,omitempty"`
	Attempt       int        `json:"attempt,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
}

// OperationProblem is the RFC 9457 problem document a failed verb records.
type OperationProblem struct {
	Type   string `json:"type,omitempty"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// status reports the operation as running, done or failed. The separate
// failed state keeps `--wait` on a failed verb from exiting zero.
func (o *Operation) status() string {
	switch {
	case !o.Done:
		return StatusOpRunning
	case o.Error != nil:
		return StatusOpFailed
	default:
		return StatusOpDone
	}
}

// failureDetail returns the problem document's detail, or the last transient
// error when there is no problem document.
func (o *Operation) failureDetail() string {
	if o.Error != nil {
		if o.Error.Detail != "" {
			return o.Error.Detail
		}
		return o.Error.Title
	}
	return o.Metadata.LastError
}

// ID returns the operation id without the "operations/" prefix.
func (o *Operation) ID() string {
	if after, ok := strings.CutPrefix(o.Name, "operations/"); ok {
		return after
	}
	return o.Name
}

// GetOperation reads one operation by id.
func (c *Client) GetOperation(ctx context.Context, id string) (*Operation, error) {
	var out Operation
	if err := c.do(ctx, http.MethodGet, "/v2/operations/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListOperations lists operations, newest first.
func (c *Client) ListOperations(ctx context.Context, resourceID string, limit int) ([]Operation, error) {
	q := url.Values{}
	if resourceID != "" {
		q.Set("resource_id", resourceID)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/v2/operations"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Operations []Operation `json:"operations"`
		Count      int         `json:"count"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Operations, nil
}

// WaitOperation polls an operation until it settles and returns an error when
// it failed.
func (c *Client) WaitOperation(ctx context.Context, id string) (*Operation, error) {
	return waitFor(ctx, c.pollInterval, StatusOpDone, []string{StatusOpFailed},
		func(ctx context.Context) (*Operation, error) { return c.GetOperation(ctx, id) },
		func(o *Operation) (string, string) { return o.status(), o.failureDetail() })
}

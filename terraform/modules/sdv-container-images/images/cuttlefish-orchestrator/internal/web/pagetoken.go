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

package web

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// PageCursor is the sort key of the last row a keyset page returned, for
// lists ordered by (created_at DESC, id DESC).
type PageCursor struct {
	CreatedAt time.Time
	ID        string
}

// EncodePageToken renders the cursor as an opaque base64url token.
func EncodePageToken(c PageCursor) string {
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + " " + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodePageToken parses a page_token query value. An empty token is the first
// page and returns nil. A token sent with a non-zero offset is a 400, since the
// two place the page in different ways.
func DecodePageToken(token string, offset int) (*PageCursor, error) {
	if token == "" {
		return nil, nil //nolint:nilnil // No token means the first page.
	}
	if offset != 0 {
		return nil, huma.Error400BadRequest("page_token cannot be combined with a non-zero offset")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, huma.Error400BadRequest("page_token is malformed")
	}
	stamp, id, ok := strings.Cut(string(raw), " ")
	if !ok || id == "" {
		return nil, huma.Error400BadRequest("page_token is malformed")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return nil, huma.Error400BadRequest("page_token is malformed")
	}
	return &PageCursor{CreatedAt: createdAt, ID: id}, nil
}

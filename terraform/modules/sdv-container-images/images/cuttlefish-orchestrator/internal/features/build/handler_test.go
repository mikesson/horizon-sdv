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

package buildfeature

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	buildDomain "cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// pagingBuildRepo records the list filter and honors its limit and keyset
// cursor, so the handler's trim and token logic sees real page boundaries.
type pagingBuildRepo struct {
	*mockBuildRepo
	lastFilter *buildDomain.ListFilter
}

func (p *pagingBuildRepo) List(_ context.Context, filter buildDomain.ListFilter) ([]*buildDomain.Build, error) {
	p.lastFilter = &filter
	rows := make([]*buildDomain.Build, 0, len(p.builds))
	for _, b := range p.builds {
		rows = append(rows, b)
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.After(rows[j].CreatedAt)
		}
		return rows[i].ID > rows[j].ID
	})
	var out []*buildDomain.Build
	for _, b := range rows {
		if filter.AfterID != "" {
			after := b.CreatedAt.Before(filter.AfterCreatedAt) ||
				(b.CreatedAt.Equal(filter.AfterCreatedAt) && b.ID < filter.AfterID)
			if !after {
				continue
			}
		}
		out = append(out, b)
	}
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func newPagingHandler(t *testing.T, ids ...string) (*BuildHandler, *pagingBuildRepo) {
	t.Helper()
	repo := &pagingBuildRepo{mockBuildRepo: newMockBuildRepo()}
	created := time.Date(2026, 9, 1, 12, 0, 0, 123456000, time.UTC)
	for _, id := range ids {
		repo.builds[id] = &buildDomain.Build{ID: id, CreatedAt: created}
	}
	svc, err := NewBuildService(repo)
	if err != nil {
		t.Fatalf("NewBuildService: %v", err)
	}
	return NewBuildHandler(svc), repo
}

func listIDs(out *ListBuildsOutput) []string {
	ids := make([]string, 0, len(out.Body.Items))
	for _, b := range out.Body.Items {
		ids = append(ids, b.ID)
	}
	return ids
}

func requireStatus(t *testing.T, err error, want int) {
	t.Helper()
	var se huma.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want a status error", err)
	}
	if got := se.GetStatus(); got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestBuildHandler_ListBuilds_LastPage(t *testing.T) {
	t.Parallel()
	h, repo := newPagingHandler(t, "b-1", "b-2")

	out, err := h.ListBuilds(t.Context(), &ListBuildsInput{Limit: 5})
	if err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	if repo.lastFilter == nil || repo.lastFilter.Limit != 6 {
		t.Fatalf("repository limit = %+v, want 6", repo.lastFilter)
	}
	if len(out.Body.Items) != 2 {
		t.Fatalf("items = %v, want 2", listIDs(out))
	}
	if out.Body.NextPageToken != "" {
		t.Fatalf("next_page_token = %q, want empty", out.Body.NextPageToken)
	}
}

func TestBuildHandler_ListBuilds_TokenWalk(t *testing.T) {
	t.Parallel()
	h, _ := newPagingHandler(t, "b-1", "b-2", "b-3")

	var seen []string
	token := ""
	for range 5 {
		out, err := h.ListBuilds(t.Context(), &ListBuildsInput{Limit: 2, PageToken: token})
		if err != nil {
			t.Fatalf("ListBuilds: %v", err)
		}
		if len(out.Body.Items) > 2 {
			t.Fatalf("page holds %d rows, want at most 2", len(out.Body.Items))
		}
		seen = append(seen, listIDs(out)...)
		token = out.Body.NextPageToken
		if token == "" {
			break
		}
	}
	want := []string{"b-3", "b-2", "b-1"}
	if len(seen) != len(want) {
		t.Fatalf("walked %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("walked %v, want %v", seen, want)
		}
	}
}

func TestBuildHandler_ListBuilds_TokenCursor(t *testing.T) {
	t.Parallel()
	h, repo := newPagingHandler(t, "b-1", "b-2")

	first, err := h.ListBuilds(t.Context(), &ListBuildsInput{Limit: 1})
	if err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	if first.Body.NextPageToken == "" {
		t.Fatal("a full page must carry a next_page_token")
	}
	if _, err := h.ListBuilds(t.Context(), &ListBuildsInput{Limit: 1, PageToken: first.Body.NextPageToken}); err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	last := first.Body.Items[0]
	if repo.lastFilter.AfterID != last.ID || !repo.lastFilter.AfterCreatedAt.Equal(last.CreatedAt) {
		t.Fatalf("cursor = (%v, %q), want (%v, %q)",
			repo.lastFilter.AfterCreatedAt, repo.lastFilter.AfterID, last.CreatedAt, last.ID)
	}
}

func TestBuildHandler_ListBuilds_TokenWithOffset(t *testing.T) {
	t.Parallel()
	h, repo := newPagingHandler(t, "b-1")
	token := web.EncodePageToken(web.PageCursor{CreatedAt: time.Now(), ID: "b-1"})

	_, err := h.ListBuilds(t.Context(), &ListBuildsInput{Limit: 20, Offset: 20, PageToken: token})
	requireStatus(t, err, http.StatusBadRequest)
	if repo.lastFilter != nil {
		t.Fatal("the rejection must precede any repository read")
	}
}

func TestBuildHandler_ListBuilds_MalformedToken(t *testing.T) {
	t.Parallel()
	h, repo := newPagingHandler(t, "b-1")

	_, err := h.ListBuilds(t.Context(), &ListBuildsInput{Limit: 20, PageToken: "not-a-token"})
	requireStatus(t, err, http.StatusBadRequest)
	if repo.lastFilter != nil {
		t.Fatal("the rejection must precede any repository read")
	}
}

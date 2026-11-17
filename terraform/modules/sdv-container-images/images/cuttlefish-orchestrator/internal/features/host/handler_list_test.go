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

package hostfeature

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/web"
)

func TestListHostsPageToken(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 9, 1, 12, 0, 0, 123456000, time.UTC)

	t.Run("a full page carries a token for its last row", func(t *testing.T) {
		t.Parallel()
		repo := newAtomicHostRepo(
			&host.Host{ID: "host-a", Name: "host-a", Status: host.StatusRunning, CreatedAt: created},
			&host.Host{ID: "host-b", Name: "host-b", Status: host.StatusRunning, CreatedAt: created},
		)
		svc, _ := newTestService(t, repo, nil)
		out, err := NewHostHandler(svc).ListHosts(t.Context(), &ListHostsInput{Limit: 1})
		if err != nil {
			t.Fatalf("ListHosts: %v", err)
		}
		if len(out.Body.Items) != 1 {
			t.Fatalf("items = %d, want 1 (the row past the limit only signals a next page)", len(out.Body.Items))
		}
		want := web.EncodePageToken(web.PageCursor{CreatedAt: created, ID: out.Body.Items[0].ID})
		if out.Body.NextPageToken != want {
			t.Errorf("next_page_token = %q, want %q", out.Body.NextPageToken, want)
		}
		// The fake ignores Limit, so the row past the page is asserted here.
		if got := repo.LastListFilter; got == nil || got.Limit != 2 {
			t.Errorf("repository filter = %+v, want Limit 2 (one row past the page)", got)
		}
	})

	t.Run("the last page has no token", func(t *testing.T) {
		t.Parallel()
		repo := newAtomicHostRepo(&host.Host{ID: "host-a", Name: "host-a", Status: host.StatusRunning, CreatedAt: created})
		svc, _ := newTestService(t, repo, nil)
		out, err := NewHostHandler(svc).ListHosts(t.Context(), &ListHostsInput{Limit: 1})
		if err != nil {
			t.Fatalf("ListHosts: %v", err)
		}
		if out.Body.NextPageToken != "" || len(out.Body.Items) != 1 {
			t.Errorf("got %d items and token %q, want 1 item and no token", len(out.Body.Items), out.Body.NextPageToken)
		}
	})

	// ListErr makes any repository read a 500, so a 400 proves the token was
	// refused before the read.
	for name, input := range map[string]*ListHostsInput{
		"page_token with an offset": {
			Limit: 1, Offset: 20,
			PageToken: web.EncodePageToken(web.PageCursor{CreatedAt: created, ID: "host-a"}),
		},
		"a malformed page_token": {Limit: 1, PageToken: "not-a-token"},
	} {
		t.Run(name+" is rejected", func(t *testing.T) {
			t.Parallel()
			repo := newAtomicHostRepo()
			repo.ListErr = errors.New("the repository must not be read")
			svc, _ := newTestService(t, repo, nil)
			_, err := NewHostHandler(svc).ListHosts(t.Context(), input)
			se, ok := errors.AsType[huma.StatusError](err)
			if !ok || se.GetStatus() != http.StatusBadRequest {
				t.Errorf("err = %v, want a 400", err)
			}
		})
	}
}

func TestListHostsNameFilter(t *testing.T) {
	t.Parallel()

	t.Run("name reaches the repository filter", func(t *testing.T) {
		t.Parallel()
		repo := newAtomicHostRepo()
		svc, _ := newTestService(t, repo, nil)
		if _, err := NewHostHandler(svc).ListHosts(t.Context(), &ListHostsInput{Limit: 1, Name: "ci-host"}); err != nil {
			t.Fatalf("ListHosts: %v", err)
		}
		if got := repo.LastListFilter; got == nil || got.Name == nil || *got.Name != "ci-host" {
			t.Errorf("repository filter = %+v, want Name ci-host", got)
		}
	})

	t.Run("no name leaves the filter unset", func(t *testing.T) {
		t.Parallel()
		repo := newAtomicHostRepo()
		svc, _ := newTestService(t, repo, nil)
		if _, err := NewHostHandler(svc).ListHosts(t.Context(), &ListHostsInput{Limit: 1}); err != nil {
			t.Fatalf("ListHosts: %v", err)
		}
		if got := repo.LastListFilter; got == nil || got.Name != nil {
			t.Errorf("repository filter = %+v, want no Name", got)
		}
	})
}

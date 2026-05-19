// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package overviewfetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildInClusterOverviewURL(t *testing.T) {
	got, err := BuildInClusterOverviewURL("mod-sample-overview", "sample-module-hello")
	if err != nil {
		t.Fatal(err)
	}
	want := "http://mod-sample-overview.sample-module-hello.svc.cluster.local:80/"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if _, err := BuildInClusterOverviewURL("", "ns"); err == nil {
		t.Fatal("expected error for empty service")
	}
}

func TestFetchHTTPOverview_ok(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html></html>"))
	}))
	t.Cleanup(srv.Close)
	b, err := FetchHTTPOverview(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "<html></html>" {
		t.Fatalf("got %q", b)
	}
}

func TestFetchHTTPOverview_notFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	t.Cleanup(srv.Close)
	_, err := FetchHTTPOverview(context.Background(), srv.Client(), srv.URL)
	if !errors.Is(err, ErrOverviewNotFound) {
		t.Fatalf("got %v want ErrOverviewNotFound", err)
	}
}

func TestFetchHTTPOverview_upstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	_, err := FetchHTTPOverview(context.Background(), srv.Client(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("expected 502 in error, got %v", err)
	}
}

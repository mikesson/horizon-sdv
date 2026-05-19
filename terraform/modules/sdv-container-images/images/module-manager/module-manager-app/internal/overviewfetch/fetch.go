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
//
// Package overviewfetch loads module overview HTML from an in-cluster HTTP endpoint.
package overviewfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// MaxOverviewBytes caps HTML size returned to clients.
const MaxOverviewBytes = 2 << 20

// ErrOverviewNotFound means the HTTP server returned 404 for the overview URL.
var ErrOverviewNotFound = errors.New("overview not found")

// BuildInClusterOverviewURL returns http://<service>.<namespace>.svc.cluster.local:80/ (fixed port and path).
func BuildInClusterOverviewURL(service, namespace string) (string, error) {
	service = strings.TrimSpace(service)
	namespace = strings.TrimSpace(namespace)
	if service == "" || namespace == "" {
		return "", errors.New("overviewService and overviewServiceNamespace are required")
	}
	host := service + "." + namespace + ".svc.cluster.local"
	u := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, "80"),
		Path:   "/",
	}
	return u.String(), nil
}

// FetchHTTPOverview performs GET url and returns the body for status 200.
func FetchHTTPOverview(ctx context.Context, client *http.Client, pageURL string) ([]byte, error) {
	if strings.TrimSpace(pageURL) == "" {
		return nil, errors.New("empty overview URL")
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxOverviewBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxOverviewBytes {
		return nil, fmt.Errorf("overview response exceeds %d bytes", MaxOverviewBytes)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return body, nil
	case http.StatusNotFound:
		return nil, ErrOverviewNotFound
	default:
		return nil, fmt.Errorf("overview HTTP %s", resp.Status)
	}
}

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
package argo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

const defaultTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// Client calls Argo Server REST with the pod's Kubernetes service account bearer token (--auth-mode=client on Argo server).
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
	TokenPath  string
}

func (c *Client) token() (string, error) {
	p := c.TokenPath
	if p == "" {
		p = defaultTokenPath
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (c *Client) joinURL(relPath string, q url.Values) (string, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", err
	}
	bp := strings.TrimSuffix(base.Path, "/")
	rp := strings.TrimPrefix(relPath, "/")
	base.Path = path.Join(bp, rp)
	base.RawQuery = q.Encode()
	return base.String(), nil
}

func (c *Client) do(ctx context.Context, fullURL string) (*http.Response, error) {
	tok, err := c.token()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return hc.Do(req)
}

// WorkflowPhase returns .status.phase and metadata.uid from GET /api/v1/workflows/{ns}/{name}.
func (c *Client) WorkflowPhase(ctx context.Context, namespace, name string) (phase string, uid string, err error) {
	rel := path.Join("api/v1/workflows", url.PathEscape(namespace), url.PathEscape(name))
	u, err := c.joinURL(rel, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := c.do(ctx, u)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("argo get workflow: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var wf struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wf); err != nil {
		return "", "", err
	}
	return wf.Status.Phase, wf.Metadata.UID, nil
}

// OpenLogStream opens the Argo NDJSON log stream (caller must Close body).
func (c *Client) OpenLogStream(ctx context.Context, namespace, workflowName, podName, container string, follow bool) (io.ReadCloser, error) {
	rel := path.Join("api/v1/workflows", url.PathEscape(namespace), url.PathEscape(workflowName), "log")
	q := url.Values{}
	if strings.TrimSpace(podName) != "" {
		q.Set("podName", podName)
	}
	q.Set("logOptions.container", container)
	q.Set("logOptions.follow", strconv.FormatBool(follow))
	u, err := c.joinURL(rel, q)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, u)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("argo log: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return resp.Body, nil
}

// NewHTTPClient returns an HTTP client with no global timeout (streaming).
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 0,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          32,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

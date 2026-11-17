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

// legacyparity records the upstream Cloud Orchestrator's exact wire behavior.
//
// It boots the real upstream server (pkg/app from the UPSTREAM checkout)
// hermetically, with a username-only account manager, an in-memory database
// and a fake instance manager scripted per case. It replays the parity corpus
// against that server and writes each request/response pair as a recording
// under internal/compat/legacy/testdata/parity/. parity_test.go then replays
// those recordings against CCO's /v1 handler and byte-diffs the answers.
//
//	go -C tools/legacyparity run .          # regenerate the recordings
//	go -C tools/legacyparity run . -check   # fail if they are stale
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	apiv1 "github.com/google/cloud-android-orchestration/api/v1"
	"github.com/google/cloud-android-orchestration/pkg/app"
	"github.com/google/cloud-android-orchestration/pkg/app/accounts"
	"github.com/google/cloud-android-orchestration/pkg/app/config"
	"github.com/google/cloud-android-orchestration/pkg/app/database"
	"github.com/google/cloud-android-orchestration/pkg/app/instances"

	"cloud-cuttlefish-orchestrator/tests/parityfile"
)

const (
	defaultOut  = "../../internal/compat/legacy/testdata/parity"
	upstreamDir = "../../UPSTREAM/cloud-android-orchestration"
)

func main() {
	out := flag.String("out", defaultOut, "directory the recordings live in")
	check := flag.Bool("check", false, "regenerate in memory and fail if the committed recordings differ")
	flag.Parse()

	if err := run(*out, *check); err != nil {
		fmt.Fprintln(os.Stderr, "legacyparity:", err)
		os.Exit(1)
	}
}

func run(out string, check bool) error {
	rev, err := upstreamRevision()
	if err != nil {
		return fmt.Errorf("resolving upstream revision: %w", err)
	}
	if strings.HasSuffix(rev, "-dirty") {
		fmt.Fprintln(os.Stderr, "legacyparity: warning: the upstream checkout is dirty; the recordings will say so")
	}

	echo := httptest.NewServer(parityfile.EchoHostOrchestrator())
	defer echo.Close()
	echoURL, err := url.Parse(echo.URL)
	if err != nil {
		return fmt.Errorf("parsing echo server URL: %w", err)
	}

	var stale []string
	for _, c := range corpus(echoURL) {
		rec, err := record(c, rev)
		if err != nil {
			return fmt.Errorf("case %s: %w", c.name, err)
		}
		data, err := rec.Marshal()
		if err != nil {
			return fmt.Errorf("case %s: %w", c.name, err)
		}
		path := filepath.Join(out, c.name+".txt")
		if check {
			committed, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(committed, data) {
				stale = append(stale, c.name)
			}
			continue
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		fmt.Println("recorded", path)
	}
	if len(stale) > 0 {
		return fmt.Errorf("recordings out of date (regenerate with: go -C tools/legacyparity run .): %s",
			strings.Join(stale, ", "))
	}
	return nil
}

// record boots one hermetic upstream server for the case and captures its
// exact answer.
func record(c parityCase, rev string) (*parityfile.Recording, error) {
	upstream := app.NewApp(
		c.im,
		accounts.NewUsernameOnlyAccountManager(),
		nil, // oauth2: only the /auth flow uses it
		nil, // encryption: only build-API credential storage uses it
		database.NewInMemoryDBService(),
		"",  // static files
		nil, // CORS origins
		apiv1.InfraConfig{IceServers: []apiv1.IceServer{{URLs: []string{stunServer}}}},
		&config.Config{
			AccountManager:  accounts.Config{Type: accounts.UsernameOnlyAMType},
			InstanceManager: instances.Config{Type: "GCP"},
		},
	)
	server := httptest.NewServer(upstream.Handler())
	defer server.Close()

	var body io.Reader
	if c.body != "" {
		body = strings.NewReader(c.body)
	}
	req, err := http.NewRequestWithContext(context.Background(), c.method, server.URL+c.target, body)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("issuing request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	reqHeaders := http.Header{}
	for k, v := range c.headers {
		reqHeaders.Set(k, v)
	}
	return &parityfile.Recording{
		Comment: fmt.Sprintf(
			"# Legacy /v1 parity recording: the upstream Cloud Orchestrator's exact\n"+
				"# answer for this request, replayed against CCO by parity_test.go.\n"+
				"# Generated by tools/legacyparity. Do not edit by hand.\n"+
				"# upstream: cloud-android-orchestration @ %s\n", rev),
		Method:      c.method,
		Target:      c.target,
		ReqHeaders:  reqHeaders,
		ReqBody:     []byte(c.body),
		Status:      resp.StatusCode,
		RespHeaders: resp.Header,
		RespBody:    respBody,
	}, nil
}

func upstreamRevision() (string, error) {
	cmd := exec.CommandContext(context.Background(),
		"git", "-C", upstreamDir, "describe", "--always", "--dirty", "--abbrev=12")
	outBytes, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git describe in %s: %w", upstreamDir, err)
	}
	return strings.TrimSpace(string(outBytes)), nil
}

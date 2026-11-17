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

package cli

import (
	"net/http"
	"slices"
	"testing"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

// The IAP tunnel must be the default because hosts never expose port 22 to
// the internet.
func TestBuildSSHArgs(t *testing.T) {
	t.Parallel()
	host := &cco.Host{
		Name:            "display-name",
		GCEInstanceName: "cco-host-abc123",
		Zone:            "europe-west4-a",
		GCEProject:      "example-project",
	}

	got := buildSSHArgs(host, true, nil)
	want := []string{"compute", "ssh", "cco-host-abc123", "--zone", "europe-west4-a", "--tunnel-through-iap", "--project", "example-project"}
	if !slices.Equal(got, want) {
		t.Errorf("tunneled args = %v, want %v", got, want)
	}

	got = buildSSHArgs(host, false, nil)
	if slices.Contains(got, "--tunnel-through-iap") {
		t.Errorf("--no-tunnel args still carry the tunnel flag: %v", got)
	}

	// Fallbacks: display name without a GCE instance name, no project.
	bare := &cco.Host{Name: "n1", Zone: "z"}
	got = buildSSHArgs(bare, true, nil)
	want = []string{"compute", "ssh", "n1", "--zone", "z", "--tunnel-through-iap"}
	if !slices.Equal(got, want) {
		t.Errorf("fallback args = %v, want %v", got, want)
	}
}

func TestHostSSHPassthroughCommand(t *testing.T) {
	// The remote command reaches gcloud as one --command argument, each word
	// single-quoted so the remote shell cannot re-split it.
	host := &cco.Host{Name: "n", GCEInstanceName: "vm-1", Zone: "z", GCEProject: "p"}
	got := buildSSHArgs(host, true, []string{"systemctl", "status", "cuttlefish"})
	want := []string{"compute", "ssh", "vm-1", "--zone", "z", "--tunnel-through-iap", "--project", "p", "--command", "'systemctl' 'status' 'cuttlefish'"}
	if !slices.Equal(got, want) {
		t.Errorf("buildSSHArgs with command = %v, want %v", got, want)
	}

	// Spaces, metacharacters and embedded single quotes survive as one word.
	got = buildSSHArgs(host, true, []string{"sh", "-c", "echo it's $HOME; ls"})
	if cmd := got[len(got)-1]; cmd != `'sh' '-c' 'echo it'\''s $HOME; ls'` {
		t.Errorf("quoted --command = %s", cmd)
	}

	// Flags before -- still parse; everything after -- is the remote command.
	cfg := &config.Config{Server: "https://cco.example.com"}
	c, _, err := parseCLI(t, []string{"host", "ssh", "h1", "--no-tunnel", "--", "systemctl", "status", "cuttlefish"}, cfg)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Host.SSH.Tunnel {
		t.Error("--no-tunnel before -- did not parse as a flag")
	}
	// Kong keeps the "--" separator in a passthrough slice; consumers see
	// it stripped.
	if !slices.Equal(passthroughCommand(c.Host.SSH.Command), []string{"systemctl", "status", "cuttlefish"}) {
		t.Errorf("passthrough command = %v", c.Host.SSH.Command)
	}
}

func TestDeviceConnectPassthroughParsesAndRejectsNoAttach(t *testing.T) {
	cfg := &config.Config{Server: "https://cco.example.com"}
	c, _, err := parseCLI(t, []string{"device", "connect", "d1", "--", "shell", "getprop", "ro.build.id"}, cfg)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Equal(passthroughCommand(c.Device.Connect.Command), []string{"shell", "getprop", "ro.build.id"}) {
		t.Errorf("passthrough command = %v", c.Device.Connect.Command)
	}

	// The contradiction is rejected before any request or listener.
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("rejected invocation still sent %s %s", r.Method, r.URL.Path)
	})
	if _, err := runCLI(t, backend, "device", "connect", "d1", "--no-attach", "--", "shell", "id"); err == nil {
		t.Error("--no-attach with a passthrough command must be rejected")
	}
}

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

package main

// Integration coverage against a real turn-rs, in Docker.
//
// The unit tests fake the relay; this one does not. It runs the shipped
// container, points it at this process's hook over loopback exactly as the
// module does, and answers from the real orchestrator handler. What is faked
// is the device repository and the ID token. Everything between the relay's
// wire and the derived key is production code.
//
// It earns its keep on three things nothing else can check. The gRPC bindings
// are generated from a vendored proto and had never met the server they were
// generated for. The key derivation is agreement between two codebases, and a
// mismatch shows up only as a rejected MESSAGE-INTEGRITY. And the relay config
// is rendered from the module's own template, so a template that turn-rs will
// not parse fails here rather than on a VM.
//
// Skipped unless Docker is present, like the emulator-backed tests, and like
// them the skip becomes a failure under CCO_REQUIRE_DOCKER.

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"

	devicedomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	hostdomain "cloud-cuttlefish-orchestrator/internal/domain/host"
	relayfeature "cloud-cuttlefish-orchestrator/internal/features/relay"
	"cloud-cuttlefish-orchestrator/internal/platform/relay/turnrs"
	"cloud-cuttlefish-orchestrator/internal/platform/turn"
	"cloud-cuttlefish-orchestrator/tests/turnclient"
)

const (
	// Pinned to the SAME turn-rs the relay image builds, whose tag lives in
	// deployment/packer/relay-image.pkr.hcl. Upstream publishes :latest from
	// every docker-* tag, so pointing this at :latest tested whatever was
	// pushed most recently rather than what production runs, and a behavior
	// change there arrived here as a failure with no local cause. Bump both
	// together, never one.
	turnrsImage = "ghcr.io/mycrl/turn-server:4.1.4"
	itSecret    = "integration-shared-secret"
	itRealm     = "cco.test"
	itDevice    = "11111111-2222-3333-4444-555555555555"
	itEmail     = "integration@example.com"
	hookPort    = 3101
	turnPort    = 3478
)

// requireDocker skips this suite when Docker is unavailable. With
// CCO_REQUIRE_DOCKER set (CI), an unavailable Docker FAILS instead: this is
// the only executed verification of the generated gRPC bindings, the key
// derivation and the module's config template, and a silent skip would report
// green while running none of it.
func requireDocker(t *testing.T) {
	t.Helper()
	required := os.Getenv("CCO_REQUIRE_DOCKER") != ""
	unavailable := func(reason, remedy string) {
		t.Helper()
		if required {
			t.Fatalf("%s, but CCO_REQUIRE_DOCKER is set: the relay suite must run, not skip. %s", reason, remedy)
		}
		t.Skip(reason)
	}

	if os.Getenv("CCO_SKIP_DOCKER_TESTS") != "" {
		unavailable("CCO_SKIP_DOCKER_TESTS set", "Unset CCO_SKIP_DOCKER_TESTS.")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		unavailable("docker not available", "Install docker, or put it on PATH.")
	}
	if err := exec.CommandContext(t.Context(), "docker", "info").Run(); err != nil {
		unavailable("docker daemon not reachable", "Start the daemon.")
	}
}

// relayConfig renders the module's own template, so this test fails if the
// template stops being something turn-rs accepts.
func relayConfig(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "terraform", "modules", "relay",
		"templates", "turn-server.toml.tftpl"))
	if err != nil {
		t.Fatalf("reading the module template: %v", err)
	}
	cfg := string(raw)
	for placeholder, value := range map[string]string{
		"${realm}":            itRealm,
		"${external_address}": "127.0.0.1",
		"${turns_external}":   "127.0.0.1",
		"${hook_endpoint}":    fmt.Sprintf("http://127.0.0.1:%d", hookPort),
	} {
		cfg = strings.ReplaceAll(cfg, placeholder, value)
	}
	if strings.Contains(cfg, "${") {
		t.Fatalf("template has placeholders this test does not substitute:\n%s", cfg)
	}
	// TURNS needs a certificate this test does not issue. Dropping the block
	// keeps the rest of the rendered config under test.
	if i := strings.Index(cfg, "# TURNS on 443."); i >= 0 {
		cfg = cfg[:i] + cfg[strings.Index(cfg[i:], "[log]")+i:]
	}
	return cfg
}

// fixedDevice stands in for the device repository; everything else in the
// authorization path is production code.
type fixedDevice struct{ status devicedomain.Status }

func (f fixedDevice) Get(_ context.Context, id string) (*devicedomain.Device, error) {
	return &devicedomain.Device{ID: id, Status: f.status}, nil
}

// fixedHost stands in for the host repository, which the legacy surface's
// host-scoped credentials resolve against.
type fixedHost struct{ status hostdomain.Status }

func (f fixedHost) Get(_ context.Context, id string) (*hostdomain.Host, error) {
	return &hostdomain.Host{ID: id, Status: f.status}, nil
}

// startOrchestrator serves the real authorize endpoint over HTTP.
func startOrchestrator(t *testing.T, status devicedomain.Status) *httptest.Server {
	t.Helper()
	creds := turn.NewCredentialService(itSecret, time.Hour)
	service := relayfeature.NewService(creds, fixedDevice{status: status},
		fixedHost{status: hostdomain.StatusRunning}, itRealm,
		slog.New(slog.DiscardHandler))

	r := chi.NewRouter()
	relayfeature.NewHandler(service).Register(humachi.New(r, huma.DefaultConfig("Test", "1.0.0")))

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// startHook serves the real hook on loopback, as the relay VM does.
func startHook(t *testing.T, orchestrator string) {
	t.Helper()
	svc := newHookService(&authority{
		client:   http.DefaultClient,
		endpoint: orchestrator + "/v2/relay/authorize",
		timeout:  800 * time.Millisecond,
	}, 5*time.Minute, slog.New(slog.DiscardHandler))

	var lc net.ListenConfig
	lis, err := lc.Listen(t.Context(), "tcp", fmt.Sprintf("127.0.0.1:%d", hookPort))
	if err != nil {
		t.Fatalf("listening for the hook: %v", err)
	}
	server := grpc.NewServer()
	turnrs.RegisterTurnHooksServiceServer(server, svc)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
}

// startRelay runs turn-rs on the host network so its loopback is ours, which
// is the same arrangement the module produces on a VM.
func startRelay(t *testing.T, config string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "turn-server.toml")
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatalf("writing the relay config: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("opening the config directory to the container: %v", err)
	}

	// Reap any relay this suite leaked earlier. These run on the HOST network,
	// so a survivor still owns port 3478: the container started below then
	// fails to bind and exits, and every assertion afterwards is answered by
	// the survivor instead. That is not a hypothetical. A leaked relay
	// carrying the hooks config made the static-secret test look as though
	// upstream had started enforcing the TURN REST expiry, which it has not.
	// The name prefix is this suite's own, so nothing else can be caught by it.
	reapLeakedRelays(t)

	name := fmt.Sprintf("cco-relay-it-%d", time.Now().UnixNano())
	cmd := exec.CommandContext(t.Context(), "docker", "run", "--rm", "-d", "--name", name,
		"--network", "host",
		// The image's own command reads /etc/turn-server/config.toml, so the
		// rendered config is mounted there rather than passed as an argument.
		"-v", path+":/etc/turn-server/config.toml:ro",
		turnrsImage)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("starting turn-rs: %v\n%s", err, out)
	}
	// --rm means a container that dies is also gone, so the logs have to be
	// read before the removal rather than after it. A cleanup that ran `docker
	// logs` on an exited-and-reaped container printed "No such container" and
	// took the only evidence with it.
	t.Cleanup(func() {
		t.Logf("turn-rs logs:\n%s", relayLogs(t, name))
		_ = exec.CommandContext(context.WithoutCancel(t.Context()), "docker", "rm", "-f", name).Run()
	})

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		// The container's own state, not a dial: this listener is UDP, and a UDP
		// dial to a closed port succeeds. Waiting on one meant a relay that
		// exited on a config it could not parse still "came up", and every
		// assertion after it failed as though the relay had refused it.
		state, err := exec.CommandContext(t.Context(), "docker", "inspect",
			"-f", "{{.State.Running}}", name).CombinedOutput()
		if err != nil {
			t.Fatalf("turn-rs exited before it served:\n%s", relayLogs(t, name))
		}
		if strings.TrimSpace(string(state)) == "true" {
			// Running, but not necessarily bound; give it a moment to listen.
			time.Sleep(2 * time.Second)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("turn-rs did not come up:\n%s", relayLogs(t, name))
}

// reapLeakedRelays removes any relay container this suite left behind. A run
// killed between `docker run` and its cleanup leaves one holding the host
// network's TURN port, and the next run then measures that survivor.
func reapLeakedRelays(t *testing.T) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "docker", "ps", "-aq",
		"--filter", "name=cco-relay-it-").Output()
	if err != nil {
		return
	}
	for id := range strings.FieldsSeq(string(out)) {
		t.Logf("removing a leaked relay container from an earlier run: %s", id)
		_ = exec.CommandContext(t.Context(), "docker", "rm", "-f", id).Run()
	}
}

// relayLogs reports whatever the container has written, or why it cannot be
// asked. A failure here is never the finding, only what makes the finding
// readable.
func relayLogs(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.CommandContext(context.WithoutCancel(t.Context()), "docker", "logs", name).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(logs unavailable: %v)\n%s", err, out)
	}
	return string(out)
}

func username(expiry time.Time) string {
	return fmt.Sprintf("%d:%s:%s", expiry.Unix(), itEmail, turn.DeviceSubject(itDevice))
}

// TestRelayAcceptsOrchestratorCredential is the claim the whole design rests
// on: a credential minted here authenticates against a real relay, using a key
// the relay asked this process for.
func TestRelayAcceptsOrchestratorCredential(t *testing.T) {
	requireDocker(t)

	srv := startOrchestrator(t, devicedomain.StatusRunning)
	startHook(t, srv.URL)
	startRelay(t, relayConfig(t))

	user := username(time.Now().Add(time.Hour))
	conn, err := turnclient.DialUDP(fmt.Sprintf("127.0.0.1:%d", turnPort))
	if err != nil {
		t.Fatalf("dialing the relay: %v", err)
	}
	defer conn.Close()

	session, err := turnclient.Allocate(conn, user, turnclient.Credential(itSecret, user))
	if err != nil {
		t.Fatalf("Allocate with a valid credential failed: %v", err)
	}
	if session == nil {
		t.Fatal("Allocate returned no session")
	}
}

// TestRelayRejectsExpiredCredential is the finding this design exists for.
// turn-rs does not check the expiry itself; on the static-secret path the same
// credential is accepted. It is refused here only because the orchestrator is
// asked, which is what makes the TTL real.
func TestRelayRejectsExpiredCredential(t *testing.T) {
	requireDocker(t)

	srv := startOrchestrator(t, devicedomain.StatusRunning)
	startHook(t, srv.URL)
	startRelay(t, relayConfig(t))

	user := username(time.Now().Add(-48 * time.Hour))
	conn, err := turnclient.DialUDP(fmt.Sprintf("127.0.0.1:%d", turnPort))
	if err != nil {
		t.Fatalf("dialing the relay: %v", err)
	}
	defer conn.Close()

	if _, err := turnclient.Allocate(conn, user, turnclient.Credential(itSecret, user)); err == nil {
		t.Fatal("an expired credential was accepted; the orchestrator's TTL is not being enforced")
	}
}

// A credential for a device that is gone must not open an allocation, even
// while the credential itself is still within its lifetime.
func TestRelayRejectsCredentialForDeletedDevice(t *testing.T) {
	requireDocker(t)

	srv := startOrchestrator(t, devicedomain.StatusDeleted)
	startHook(t, srv.URL)
	startRelay(t, relayConfig(t))

	user := username(time.Now().Add(time.Hour))
	conn, err := turnclient.DialUDP(fmt.Sprintf("127.0.0.1:%d", turnPort))
	if err != nil {
		t.Fatalf("dialing the relay: %v", err)
	}
	defer conn.Close()

	if _, err := turnclient.Allocate(conn, user, turnclient.Credential(itSecret, user)); err == nil {
		t.Fatal("a credential for a deleted device was accepted")
	}
}

// A credential minted under a different secret must fail even though the hook
// allowed the username: the relay verifies MESSAGE-INTEGRITY itself, so the
// two checks are independent.
func TestRelayRejectsWrongSecret(t *testing.T) {
	requireDocker(t)

	srv := startOrchestrator(t, devicedomain.StatusRunning)
	startHook(t, srv.URL)
	startRelay(t, relayConfig(t))

	user := username(time.Now().Add(time.Hour))
	conn, err := turnclient.DialUDP(fmt.Sprintf("127.0.0.1:%d", turnPort))
	if err != nil {
		t.Fatalf("dialing the relay: %v", err)
	}
	defer conn.Close()

	if _, err := turnclient.Allocate(conn, user, turnclient.Credential("a-different-secret", user)); err == nil {
		t.Fatal("a credential minted under the wrong secret was accepted")
	}
}

// The relay's actual job. Two clients allocate on the same instance and a
// payload crosses between them.
//
// This is also the constraint that makes server-authoritative ICE a
// correctness requirement rather than a refinement: turn-rs resolves a peer by
// port lookup inside its own session table, so it can only carry traffic
// between its own clients. A device that is not itself a TURN client cannot be
// reached, whatever the browser was told.
func TestRelayCarriesTrafficBetweenItsOwnClients(t *testing.T) {
	requireDocker(t)

	srv := startOrchestrator(t, devicedomain.StatusRunning)
	startHook(t, srv.URL)
	startRelay(t, relayConfig(t))

	dial := func(who string) *turnclient.Session {
		t.Helper()
		user := username(time.Now().Add(time.Hour))
		conn, err := turnclient.DialUDP(fmt.Sprintf("127.0.0.1:%d", turnPort))
		if err != nil {
			t.Fatalf("%s dialing the relay: %v", who, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		session, err := turnclient.Allocate(conn, user, turnclient.Credential(itSecret, user))
		if err != nil {
			t.Fatalf("%s allocating: %v", who, err)
		}
		return session
	}

	browser, device := dial("browser"), dial("device")

	if err := browser.CreatePermission(device.RelayIP(), device.RelayPort()); err != nil {
		t.Fatalf("browser permission for the device: %v", err)
	}
	if err := device.CreatePermission(browser.RelayIP(), browser.RelayPort()); err != nil {
		t.Fatalf("device permission for the browser: %v", err)
	}

	payload := []byte("media")
	if err := browser.SendTo(device.RelayIP(), device.RelayPort(), payload); err != nil {
		t.Fatalf("browser sending: %v", err)
	}
	got, _, err := device.AwaitData(5 * time.Second)
	if err != nil {
		t.Fatalf("device receiving: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("device received %q, want %q", got, payload)
	}
}

// TestRelayItselfDoesNotEnforceExpiry asserts the premise of this component.
//
// Configured the ordinary way, with a shared secret on the relay, turn-rs
// accepts a credential that expired two days ago, because it does not parse
// the expiry at all. That is why the orchestrator is asked instead, and why
// the module refuses to render a static secret.
//
// A failure here means upstream started enforcing the expiry, and the design
// should be revisited.
func TestRelayItselfDoesNotEnforceExpiry(t *testing.T) {
	requireDocker(t)

	// This is the configuration the module exists to prevent.
	config := fmt.Sprintf(`[server]
realm = "%s"
port-range = "49152..65535"

[[server.interfaces]]
transport = "udp"
listen = "0.0.0.0:%d"
external = "127.0.0.1:%d"

[log]
level = "info"
stdout = true

[auth]
static-auth-secret = "%s"
`, itRealm, turnPort, turnPort, itSecret)

	startRelay(t, config)

	user := username(time.Now().Add(-48 * time.Hour))
	conn, err := turnclient.DialUDP(fmt.Sprintf("127.0.0.1:%d", turnPort))
	if err != nil {
		t.Fatalf("dialing the relay: %v", err)
	}
	defer conn.Close()

	if _, err := turnclient.Allocate(conn, user, turnclient.Credential(itSecret, user)); err != nil {
		t.Fatalf("turn-rs refused an expired credential on the static-secret path: %v\n"+
			"Check the relay logs above before concluding anything about expiry: a "+
			"container that never served fails here identically. If it did serve and "+
			"still refused, upstream has started enforcing the TURN REST expiry (its "+
			"static_auth_secret path does not, by design), and the case for a "+
			"mandatory authorization hook should be revisited.", err)
	}
}

// The derived key is agreement between two codebases. If the orchestrator's
// digest ever stops matching the relay's, every allocation fails with nothing
// to point at, so compare the key directly.
func TestDerivedKeyMatchesTheRelayFormat(t *testing.T) {
	user := username(time.Now().Add(time.Hour))
	creds := turn.NewCredentialService(itSecret, time.Hour)

	got, err := creds.DeriveKey(user, itRealm, turn.AlgorithmMD5)
	if err != nil {
		t.Fatalf("DeriveKey() error = %v", err)
	}
	want := turnclient.LongTermKey(user, itRealm, turnclient.Credential(itSecret, user))
	if base64.StdEncoding.EncodeToString(got) != base64.StdEncoding.EncodeToString(want) {
		t.Errorf("DeriveKey() = %x, want %x", got, want)
	}
}

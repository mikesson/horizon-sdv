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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alecthomas/kong"

	"cloud-cuttlefish-orchestrator/internal/router"
	"cloud-cuttlefish-orchestrator/tests/specwire"
)

// commandFor maps every backend operation the CLI reaches to the command that
// reaches it. The gate checks that each command path exists in the Kong tree.
var commandFor = map[string]string{
	"get-me": "auth whoami",

	"list-batches":       "batch list",
	"create-batch":       "batch create",
	"get-batch":          "batch get",
	"delete-batch":       "batch delete",
	"list-batch-devices": "batch devices",

	"list-builds":        "build list",
	"create-build":       "build register",
	"start-build-upload": "build push",
	"get-build":          "build get",
	"update-build":       "build update",
	"delete-build":       "build delete",

	"list-devices":             "device list",
	"create-device":            "device create",
	"get-device":               "device get",
	"delete-device":            "device delete",
	"start-device":             "device start",
	"stop-device":              "device stop",
	"restart-instance":         "device restart",
	"powerwash-instance":       "device powerwash",
	"powerbtn-instance":        "device powerbtn",
	"bugreport-device":         "device bugreport",
	"get-display-screenshot":   "device screenshot",
	"list-instance-recordings": "device record list",
	"start-instance-recording": "device record start",
	"stop-instance-recording":  "device record stop",
	"fetch-instance-recording": "device record fetch",
	"take-device-snapshot":     "device snapshot take",
	"list-device-snapshots":    "device snapshot list",
	"delete-device-snapshot":   "device snapshot delete",
	"list-displays":            "device display list",
	"add-display":              "device display add",
	"remove-display":           "device display remove",
	"restore-device-snapshot":  "device snapshot restore",
	"extend-device":            "device extend",
	"pin-device":               "device pin",
	"unpin-device":             "device unpin",
	"get-device-lifecycle":     "device lifecycle",

	"list-hosts":     "host list",
	"get-host":       "host get",
	"create-host":    "host create",
	"delete-host":    "host delete",
	"start-host":     "host start",
	"stop-host":      "host stop",
	"retry-host":     "host retry",
	"update-host":    "host update",
	"extend-host":    "host extend",
	"sync-host":      "host sync",
	"list-host-cvds": "host cvds",

	"get-operation":   "operation get",
	"list-operations": "operation list",

	"list-machine-types":       "machine-type list",
	"get-machine-type":         "machine-type get",
	"get-default-machine-type": "machine-type default",
	"list-machine-families":    "machine-type families",
	"list-zone-availability":   "zone list",
	"get-current-quota":        "quota show",
	"update-platform-quota":    "quota set",

	"admin-list-failed-tasks":   "admin tasks list",
	"admin-retry-task":          "admin tasks retry",
	"admin-delete-task":         "admin tasks delete",
	"admin-task-stats":          "admin tasks stats",
	"admin-stats":               "admin stats",
	"admin-provisioning-stats":  "admin provisioning",
	"admin-reclaimable":         "admin reclaimable",
	"admin-list-audit":          "admin audit",
	"admin-control-plane":       "admin control-plane",
	"admin-capacity":            "admin capacity",
	"admin-list-zones":          "admin zone list",
	"admin-set-enabled-zones":   "admin zone enable",
	"admin-clear-zone-cooldown": "admin zone clear-cooldown",
	"admin-update-machine-type": "admin machine-type update",
	"admin-catalog-freshness":   "admin catalog-freshness",

	"list-host-image-configs":  "admin host-image list",
	"get-host-image-config":    "admin host-image get",
	"create-host-image-config": "admin host-image create",
	"update-host-image-config": "admin host-image update",
	"delete-host-image-config": "admin host-image delete",

	"get-zone-pin":                    "admin config get",
	"get-quota-margin":                "admin config get",
	"get-v1-zone-hint":                "admin config get",
	"get-spot-auto-default":           "admin config get",
	"get-default-gpu-machine-type":    "admin config get",
	"get-default-upstream-build":      "admin config get",
	"get-spot-min-uptime":             "admin config get",
	"get-artifact-buckets":            "admin config get",
	"get-sweeps":                      "admin config get",
	"get-fleet":                       "admin config get",
	"get-lifetimes":                   "admin config get",
	"get-docker-host":                 "admin config get",
	"update-zone-pin":                 "admin config set",
	"update-quota-margin":             "admin config set",
	"update-v1-zone-hint":             "admin config set",
	"update-spot-auto-default":        "admin config set",
	"update-default-gpu-machine-type": "admin config set",
	"update-default-upstream-build":   "admin config set",
	"update-spot-min-uptime":          "admin config set",
	"update-artifact-buckets":         "admin config set",
	"update-sweeps":                   "admin config set",
	"update-fleet-autoscaler":         "admin config set",
	"disable-fleet-pool":              "admin config set",
	"update-fleet-tuning":             "admin config set",
	// The CLI can only clear schedules (`admin config set fleet
	// schedules=none`). Authoring a window is done in the console.
	"update-fleet-schedules": "admin config set",
	"update-fleet-ladder":    "admin config set",
	"update-lifetimes":       "admin config set",
	"update-docker-host":     "admin config set",

	"list-buckets":  "storage ls",
	"list-objects":  "storage ls",
	"delete-object": "storage rm",

	"get-health":    "status",
	"get-info":      "status",
	"get-ready":     "doctor",
	"verify-access": "doctor",
}

// notExposed lists operations kept out of the CLI, each with the reason.
var notExposed = map[string]string{
	"admin-list-machine-families": "console surface: the curation table's own read, differing from the machine-type list only by the families and shapes the fleet does not offer, which makes it a table's subject rather than a command's",
	"admin-update-machine-family": "console surface: admitting a series is a decision made against that table, beside the shapes it turns out to hold",
	"authorize-relay-credential":  "internal: TURN-credential handshake between relay VM and server, never user-invoked",
	"get-ice-config":              "internal: consumed by the WebRTC player the console and 'device webrtc' open, never user-invoked",
	"get-platform-quota":          "duplicate read: the handler serves the identical body on /v2/quota, which 'quota show' prints",
}

// pendingExposure lists operations the CLI does not reach yet, mapped to the
// planned command, or "" while undecided. The gate fails once a planned
// command exists until the operation moves to commandFor.
var pendingExposure = map[string]string{
	// The autoscaler page's two reads. Neither command has been designed yet.
	"admin-autoscaler":             "admin autoscaler",
	"admin-standby-recommendation": "admin standby-recommendation",
}

const (
	// idempotencyKeyHeader is the header the server declares on creates it
	// deduplicates. idempotencyKeyFlag is the cfctl flag that fills it.
	idempotencyKeyHeader = "Idempotency-Key"
	idempotencyKeyFlag   = "idempotency-key"
)

// specOperation is one spec operation as the coverage gates read it.
type specOperation struct {
	id string
	// idempotencyKey reports whether the operation declares the
	// Idempotency-Key request header.
	idempotencyKey bool
}

// specOperations builds the real OpenAPI spec in-process and returns every
// operation in it.
func specOperations(t *testing.T) []specOperation {
	t.Helper()

	handler, err := router.New(specwire.Config())
	if err != nil {
		t.Fatalf("building router: %v", err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json: %d\n%s", rec.Code, rec.Body.String())
	}

	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("decoding spec: %v", err)
	}

	var ops []specOperation
	methods := []string{"get", "post", "put", "patch", "delete"}
	for path, item := range spec.Paths {
		for _, m := range methods {
			raw, ok := item[m]
			if !ok {
				continue
			}
			var op struct {
				OperationID string `json:"operationId"`
				Parameters  []struct {
					Name string `json:"name"`
					In   string `json:"in"`
				} `json:"parameters"`
			}
			if err := json.Unmarshal(raw, &op); err != nil {
				t.Fatalf("decoding %s %s: %v", m, path, err)
			}
			if op.OperationID == "" {
				t.Fatalf("%s %s has no operationId", m, path)
			}
			entry := specOperation{id: op.OperationID}
			for _, p := range op.Parameters {
				if p.In == "header" && p.Name == idempotencyKeyHeader {
					entry.idempotencyKey = true
				}
			}
			ops = append(ops, entry)
		}
	}
	return ops
}

// commandNodes walks the production Kong tree and returns every canonical
// command path, such as "host list", with its node.
func commandNodes(t *testing.T) map[string]*kong.Node {
	t.Helper()

	var c CLI
	parser, err := NewParser(&c, nil, "test")
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	paths := map[string]*kong.Node{}
	var walk func(prefix string, node *kong.Node)
	walk = func(prefix string, node *kong.Node) {
		for _, child := range node.Children {
			if child.Type != kong.CommandNode {
				continue
			}
			path := child.Name
			if prefix != "" {
				path = prefix + " " + child.Name
			}
			paths[path] = child
			walk(path, child)
		}
	}
	walk("", parser.Model.Node)
	return paths
}

// TestEveryOperationHasACoverageVerdict checks that every backend operation is
// in exactly one of commandFor, notExposed or pendingExposure, and that every
// entry still exists in the spec.
func TestEveryOperationHasACoverageVerdict(t *testing.T) {
	ops := specOperations(t)
	tree := commandNodes(t)

	claimed := map[string]string{}
	for op := range commandFor {
		claimed[op] = "commandFor"
	}
	for op := range notExposed {
		if prev, dup := claimed[op]; dup {
			t.Errorf("operation %q is in both %s and notExposed", op, prev)
		}
		claimed[op] = "notExposed"
	}
	for op := range pendingExposure {
		if prev, dup := claimed[op]; dup {
			t.Errorf("operation %q is in both %s and pendingExposure", op, prev)
		}
		claimed[op] = "pendingExposure"
	}

	inSpec := map[string]bool{}
	for _, op := range ops {
		inSpec[op.id] = true
		if _, ok := claimed[op.id]; !ok {
			t.Errorf("operation %q has no coverage verdict: add it to commandFor, notExposed, or pendingExposure", op.id)
		}
	}
	for op, where := range claimed {
		if !inSpec[op] {
			t.Errorf("%s entry %q is not in the spec anymore: delete it", where, op)
		}
	}

	for op, cmd := range commandFor {
		if tree[cmd] == nil {
			t.Errorf("commandFor[%q] = %q: no such command in the tree", op, cmd)
		}
	}
	for op, planned := range pendingExposure {
		if planned != "" && tree[planned] != nil {
			t.Errorf("pendingExposure[%q]: planned command %q now exists. Move the operation to commandFor if the command reaches it, or re-plan", op, planned)
		}
	}

	t.Logf("operations: %d total, %d reached, %d not exposed, %d pending",
		len(ops), len(commandFor), len(notExposed), len(pendingExposure))
}

// TestIdempotentCreatesExposeAKey checks that cfctl exposes --idempotency-key
// on exactly the commands whose operations declare the Idempotency-Key header.
// retryRoundTripper replays a POST only when the header is present, so a key on
// a non-deduplicated command would let it run twice.
func TestIdempotentCreatesExposeAKey(t *testing.T) {
	tree := commandNodes(t)

	keyed := map[string]bool{}
	for _, op := range specOperations(t) {
		if op.idempotencyKey {
			keyed[op.id] = true
		}
	}
	if len(keyed) == 0 {
		t.Fatal("no operation declares an Idempotency-Key header: the spec walk found nothing to hold cfctl to")
	}

	flagged := map[string]bool{}
	for path, node := range tree {
		for _, f := range node.Flags {
			if f.Name == idempotencyKeyFlag {
				flagged[path] = true
			}
		}
	}

	wantFlag := map[string]bool{}
	for op := range keyed {
		cmd, ok := commandFor[op]
		if !ok {
			t.Errorf("operation %q accepts an %s but reaches no command", op, idempotencyKeyHeader)
			continue
		}
		wantFlag[cmd] = true
		if !flagged[cmd] {
			t.Errorf("operation %q accepts an %s but command %q does not expose --%s",
				op, idempotencyKeyHeader, cmd, idempotencyKeyFlag)
		}
	}
	for cmd := range flagged {
		if !wantFlag[cmd] {
			t.Errorf("command %q exposes --%s but reaches no operation that accepts one",
				cmd, idempotencyKeyFlag)
		}
	}

	t.Logf("idempotent creates: %d operations, %d commands", len(keyed), len(flagged))
}

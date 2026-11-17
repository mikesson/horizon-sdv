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

// Package fakegce is an in-memory Compute Engine API stand-in for fault
// harnesses and integration tests. It serves the instance/operation subset
// host provisioning and the host reconciler consume, records every effect for
// post-run orphan checking, and exposes drift injection (vanish a VM, emit a
// preemption) so reconcilers can be exercised against out-of-band cloud
// changes.
package fakegce

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Instance is one fake VM.
type Instance struct {
	Name      string
	Zone      string
	IP        string // unique loopback address; doubles as the host's HO address
	Status    string // RUNNING | TERMINATED
	Labels    map[string]string
	CreatedAt time.Time
}

// Server is the fake compute endpoint. Point compute REST clients at URL()
// with option.WithEndpoint + option.WithoutAuthentication.
type Server struct {
	srv *httptest.Server

	mu         sync.Mutex
	instances  map[string]*Instance // keyed by name
	preempts   []operation
	nextIP     int
	nextOp     int
	created    []string        // ledger: every instance ever inserted
	deleted    []string        // ledger: every instance deleted through the API
	vanished   []string        // ledger: instances removed by drift injection
	stockouts  map[string]bool // zones whose inserts fail with zoneResourcePoolExhausted
	unlisted   map[string]bool // instances listings omit while point reads still find them
	insertHold time.Duration
	onRemoved  func(ip string) // fires whenever an instance ceases to exist

	// opResults records named operations that completed with an error (e.g. a
	// stockout), so op polling (GET .../operations/{name}) and :wait replay
	// the failure. Names absent here read as generic DONE successes.
	opResults map[string]map[string]any
}

// OnInstanceRemoved registers a hook fired (outside the lock) with the IP of
// every instance that ceases to exist through an API delete, a drift vanish or
// a preemption. Harnesses use it to tear down per-VM state elsewhere, e.g. the
// fake Host Orchestrator that would have died with the VM.
func (s *Server) OnInstanceRemoved(fn func(ip string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onRemoved = fn
}

func (s *Server) notifyRemoved(ip string) {
	s.mu.Lock()
	fn := s.onRemoved
	s.mu.Unlock()
	if fn != nil && ip != "" {
		fn(ip)
	}
}

// JSON keys and values shared across the fake's operation and listing
// payloads, plus the status GCE reports for a live instance.
const (
	opKeyName    = "name"
	opKeyStatus  = "status"
	opStatusDone = "DONE"

	listKeyItems    = "items"
	instanceRunning = "RUNNING"
)

type operation struct {
	id         int
	opType     string
	targetLink string
	insertTime time.Time
	user       string
}

// recordOp appends an operation to the ops ledger. Callers hold s.mu.
func (s *Server) recordOp(opType, name, user string) {
	s.nextOp++
	s.preempts = append(s.preempts, operation{
		id:         s.nextOp,
		opType:     opType,
		targetLink: s.srv.URL + "/instances/" + name,
		insertTime: time.Now(),
		user:       user,
	})
}

// New starts the fake. Close it with Close.
func New() *Server {
	s := &Server{
		instances: make(map[string]*Instance),
		stockouts: make(map[string]bool),
		unlisted:  make(map[string]bool),
		opResults: make(map[string]map[string]any),
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// StockoutZone makes every insert into the zone fail with the
// zoneResourcePoolExhausted rejection real GCE returns when a zone is out of
// capacity. Zone failover classifies that signal and reacts to it.
func (s *Server) StockoutZone(zone string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stockouts[zone] = true
}

// RestoreZone lifts a zone's stockout.
func (s *Server) RestoreZone(zone string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.stockouts, zone)
}

// InstanceZone returns the zone a live instance was inserted into, or "".
func (s *Server) InstanceZone(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if inst, ok := s.instances[name]; ok {
		return inst.Zone
	}
	return ""
}

// URL is the endpoint for compute clients.
func (s *Server) URL() string { return s.srv.URL }

// Close shuts the HTTP server down.
func (s *Server) Close() { s.srv.Close() }

// Vanish removes a VM out-of-band, as if deleted in the cloud console. The
// operations ledger records an instances.delete by a HUMAN principal, which
// is what distinguishes user intent from a Spot reclaim. The host reconciler
// must observe the loss and cascade (mark the host lost), never recover it.
// Returns false if absent.
func (s *Server) Vanish(name string) bool {
	s.mu.Lock()
	inst, ok := s.instances[name]
	if !ok {
		s.mu.Unlock()
		return false
	}
	delete(s.instances, name)
	s.recordOp("compute.instances.delete", name, "operator@example.com")
	s.vanished = append(s.vanished, name)
	s.mu.Unlock()
	s.notifyRemoved(inst.IP)
	return true
}

// Preempt removes a VM and records the operations real GCE leaves behind: a
// compute.instances.preempted operation AND the system-principal
// instances.delete of the reclaim tail (CCO provisions Spot instances with
// InstanceTerminationAction=DELETE, so a preempted VM is deleted, its
// recovery re-provision inserts a fresh instance, and the Host Orchestrator
// dies with it). Real GCE does NOT guarantee the preempted operation appears
// at all with the DELETE action, and the system delete is the reliable
// channel, so harness scenarios that emit only the delete stay
// representative. Returns false if absent.
func (s *Server) Preempt(name string) bool {
	s.mu.Lock()
	inst, ok := s.instances[name]
	if !ok {
		s.mu.Unlock()
		return false
	}
	delete(s.instances, name)
	s.recordOp("compute.instances.preempted", name, "")
	s.recordOp("compute.instances.delete", name, "system")
	s.vanished = append(s.vanished, name)
	s.mu.Unlock()
	s.notifyRemoved(inst.IP)
	return true
}

// OmitFromListings models a stale or degraded listing: zonal and aggregated
// lists leave the instance out, while a point read still returns it.
func (s *Server) OmitFromListings(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unlisted[name] = true
}

// RandomLive returns any live (RUNNING) instance name, or "".
func (s *Server) RandomLive() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, inst := range s.instances {
		if inst.Status == instanceRunning {
			return name
		}
	}
	return ""
}

// Live returns the names of instances that still exist.
func (s *Server) Live() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.instances))
	for name := range s.instances {
		out = append(out, name)
	}
	return out
}

// Ledger returns the created, API-deleted, and drift-vanished name lists,
// in that order.
func (s *Server) Ledger() ([]string, []string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.created...),
		append([]string(nil), s.deleted...),
		append([]string(nil), s.vanished...)
}

// handle routes the compute REST subset:
//
//	POST   .../zones/{z}/instances                  insert
//	GET    .../zones/{z}/instances                  list
//	GET    .../zones/{z}/instances/{name}           get
//	DELETE .../zones/{z}/instances/{name}           delete
//	POST   .../zones/{z}/instances/{name}/{verb}    start/stop (no-op DONE)
//	GET    .../zones/{z}/operations                 preemption list
//	POST   .../zones/{z}/operations/{op}/wait       op wait (always DONE)
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case strings.Contains(path, "/aggregated/instances"):
		s.aggregatedList(w)
	case strings.Contains(path, "/operations"):
		s.handleOperations(w, r)
	case strings.Contains(path, "/instances"):
		s.handleInstances(w, r, path)
	default:
		http.NotFound(w, r)
	}
}

// aggregatedList serves GET .../aggregated/instances, returning instances
// bucketed by zone scope, the shape the host reconciler's orphan sweep reads.
func (s *Server) aggregatedList(w http.ResponseWriter) {
	s.mu.Lock()
	byZone := make(map[string]map[string]any)
	for _, inst := range s.instances {
		if s.unlisted[inst.Name] {
			continue
		}
		zone := inst.Zone
		if zone == "" {
			zone = "harness-zone"
		}
		key := "zones/" + zone
		scope, ok := byZone[key]
		if !ok {
			scope = map[string]any{"instances": []map[string]any{}}
			byZone[key] = scope
		}
		scope["instances"] = append(scope["instances"].([]map[string]any), instanceJSON(inst))
	}
	s.mu.Unlock()
	writeJSON(w, map[string]any{listKeyItems: byZone})
}

// PlantOrphan creates an instance out-of-band (no insert ledger entry, no
// host row anywhere) with a backdated creation time, which is what the
// orphan-GC sweep looks for. Labels should match the sync service's
// managed-instance labels.
func (s *Server) PlantOrphan(name, zone string, labels map[string]string, age time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextIP++
	s.instances[name] = &Instance{
		Name:      name,
		Zone:      zone,
		IP:        fmt.Sprintf("127.77.%d.%d", s.nextIP/250+1, s.nextIP%250+1),
		Status:    instanceRunning,
		Labels:    labels,
		CreatedAt: time.Now().Add(-age),
	}
}

// zoneFromPath extracts {z} from a .../zones/{z}/... compute path ("" when
// the segment is absent).
func zoneFromPath(path string) string {
	_, after, ok := strings.Cut(path, "/zones/")
	if !ok {
		return ""
	}
	zone, _, _ := strings.Cut(after, "/")
	return zone
}

func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request, path string) {
	rest := path[strings.Index(path, "/instances")+len("/instances"):]
	rest = strings.TrimPrefix(rest, "/")
	switch {
	case r.Method == http.MethodPost && rest == "":
		s.insert(w, r, zoneFromPath(path))
	case r.Method == http.MethodGet && rest == "":
		s.list(w)
	case r.Method == http.MethodGet:
		s.get(w, rest)
	case r.Method == http.MethodDelete:
		s.delete(w, rest)
	case r.Method == http.MethodPost && strings.Contains(rest, "/"): // {name}/start, {name}/stop
		name, _, _ := strings.Cut(rest, "/")
		s.mu.Lock()
		if inst, ok := s.instances[name]; ok {
			if strings.HasSuffix(rest, "/stop") {
				inst.Status = "TERMINATED"
			} else {
				inst.Status = instanceRunning
			}
		}
		s.mu.Unlock()
		writeOp(w)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) insert(w http.ResponseWriter, r *http.Request, zone string) {
	var body struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, `{"error":{"message":"bad instance body"}}`, http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	hold := s.insertHold
	if s.stockouts[zone] {
		// Mirrors real GCE stockout: the insert call SUCCEEDS and the
		// returned operation completes with a ZONE_RESOURCE_POOL_EXHAUSTED
		// error, which op polling replays (an HTTP-level 503 would be retried
		// inside the compute client and never reach the provisioning failure
		// path). The client flattens this into a googleapi error whose
		// MESSAGE carries the code, which is the shape the classifier reads.
		s.nextOp++
		op := map[string]any{
			opKeyName:             fmt.Sprintf("op-stockout-%d", s.nextOp),
			opKeyStatus:           opStatusDone,
			"httpErrorStatusCode": http.StatusServiceUnavailable,
			"httpErrorMessage":    "SERVICE UNAVAILABLE",
			"error": map[string]any{"errors": []map[string]any{{
				"code":    "ZONE_RESOURCE_POOL_EXHAUSTED",
				"message": "The zone does not have enough resources available to fulfill the request (ZONE_RESOURCE_POOL_EXHAUSTED).",
			}}},
		}
		s.opResults[op[opKeyName].(string)] = op
		s.mu.Unlock()
		writeJSON(w, op)
		return
	}
	if _, exists := s.instances[body.Name]; exists {
		s.mu.Unlock()
		// Mirrors GCE's duplicate-insert error; provisioning treats
		// alreadyExists as success on retry.
		http.Error(w, `{"error":{"code":409,"message":"alreadyExists: instance exists","errors":[{"reason":"alreadyExists"}]}}`, http.StatusConflict)
		return
	}
	s.nextIP++
	inst := &Instance{
		Name:      body.Name,
		Zone:      zone,
		IP:        fmt.Sprintf("127.77.%d.%d", s.nextIP/250+1, s.nextIP%250+1),
		Status:    instanceRunning,
		Labels:    body.Labels,
		CreatedAt: time.Now(),
	}
	s.instances[body.Name] = inst
	s.created = append(s.created, body.Name)
	s.mu.Unlock()
	if hold > 0 {
		time.Sleep(hold)
	}
	writeOp(w)
}

func (s *Server) list(w http.ResponseWriter) {
	s.mu.Lock()
	items := make([]map[string]any, 0, len(s.instances))
	for _, inst := range s.instances {
		if s.unlisted[inst.Name] {
			continue
		}
		items = append(items, instanceJSON(inst))
	}
	s.mu.Unlock()
	writeJSON(w, map[string]any{listKeyItems: items})
}

func (s *Server) get(w http.ResponseWriter, name string) {
	s.mu.Lock()
	inst, ok := s.instances[name]
	s.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":{"code":404,"message":"notFound: instance not found","errors":[{"reason":"notFound"}]}}`, http.StatusNotFound)
		return
	}
	writeJSON(w, instanceJSON(inst))
}

func (s *Server) delete(w http.ResponseWriter, name string) {
	s.mu.Lock()
	inst, ok := s.instances[name]
	if ok {
		delete(s.instances, name)
		s.deleted = append(s.deleted, name)
	}
	s.mu.Unlock()
	if ok {
		s.notifyRemoved(inst.IP)
	}
	if !ok {
		http.Error(w, `{"error":{"code":404,"message":"notFound: instance not found","errors":[{"reason":"notFound"}]}}`, http.StatusNotFound)
		return
	}
	writeOp(w)
}

func (s *Server) handleOperations(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if r.Method == http.MethodPost { // .../operations/{op}/wait
		name, _, _ := strings.Cut(pathAfterOperations(path), "/")
		s.writeNamedOp(w, name)
		return
	}
	// GET of a single operation (op.Wait polls this before anything else);
	// a bare .../operations GET is the zone-operations listing below.
	if name := pathAfterOperations(path); name != "" {
		s.writeNamedOp(w, name)
		return
	}
	s.mu.Lock()
	items := make([]map[string]any, 0, len(s.preempts))
	// Newest first, matching the orderBy=creationTimestamp desc contract the
	// scanner requests (it early-breaks at the lookback boundary).
	for _, op := range slices.Backward(s.preempts) {
		items = append(items, map[string]any{
			// protojson renders uint64 as a string.
			"id":            strconv.Itoa(op.id),
			"name":          fmt.Sprintf("operation-%d", op.id),
			"operationType": op.opType,
			"targetLink":    op.targetLink,
			"insertTime":    op.insertTime.Format(time.RFC3339),
			"user":          op.user,
			"status":        "DONE",
		})
	}
	s.mu.Unlock()
	// The filter param is ignored on purpose (mixed types returned). The host
	// reconciler must filter by operationType client-side, and returning the
	// unfiltered list is what proves it does.
	writeJSON(w, map[string]any{listKeyItems: items})
}

func instanceJSON(inst *Instance) map[string]any {
	return map[string]any{
		"name":              inst.Name,
		"status":            inst.Status,
		"labels":            inst.Labels,
		"creationTimestamp": inst.CreatedAt.Format(time.RFC3339),
		"networkInterfaces": []map[string]any{{
			"networkIP": inst.IP,
			"accessConfigs": []map[string]any{{
				"natIP": inst.IP,
			}},
		}},
	}
}

// pathAfterOperations returns the path segment(s) after "/operations/", or
// "" for the bare collection path.
func pathAfterOperations(path string) string {
	_, after, ok := strings.Cut(path, "/operations/")
	if !ok {
		return ""
	}
	return after
}

// writeNamedOp replays a recorded operation outcome; unknown names read as
// generic DONE successes.
func (s *Server) writeNamedOp(w http.ResponseWriter, name string) {
	s.mu.Lock()
	op, ok := s.opResults[name]
	s.mu.Unlock()
	if ok {
		writeJSON(w, op)
		return
	}
	if name == "" {
		name = "op-done"
	}
	writeJSON(w, map[string]any{opKeyName: name, opKeyStatus: opStatusDone})
}

func writeOp(w http.ResponseWriter) {
	writeJSON(w, map[string]any{opKeyName: "op-done", opKeyStatus: opStatusDone})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v) //nolint:errchkjson // best-effort fake response
}

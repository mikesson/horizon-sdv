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

// Package fakeho is an in-memory Host Orchestrator stand-in serving every
// endpoint hoclient calls, in the shapes the real one uses.
//
// One listener impersonates every host VM: fake GCE assigns each instance a
// unique 127.0.0.0/8 address, all of which route to this wildcard-bound
// listener, and the handlers key state by the connection's local (destination)
// IP. Effects are recorded per IP for post-run orphan checking, and per-IP
// fault injection (down, error, hang, poison) exercises client retry, circuit
// breaking, and health monitoring.
//
// # Fidelity to upstream
//
// The route table in Routes mirrors the upstream Host Orchestrator's
// (github.com/google/android-cuttlefish,
// frontend/src/host_orchestrator/orchestrator/controller.go)
// so the two can be read side by side, and the async/sync split is copied
// rather than guessed. Clients depend on that split, and it is not obvious.
// Display add, list and remove answer DIRECTLY, while the screenshot on the
// very same resource answers with an OPERATION whose result carries the image.
// A fake that returns an image from the request itself lets a client ship that
// reads it there, which is exactly the defect this package exists to prevent.
//
// ServedRoutes reports the route patterns actually exercised, so a test suite
// can assert it covers what the client calls instead of trusting that it does.
package fakeho

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Group statuses mirrored from the real HO's cvd status strings.
const (
	statusStarting = "Starting"
	statusRunning  = "Running"
	statusStopped  = "Stopped"
)

// CVD is one fake device group member.
type CVD struct {
	Group  string `json:"group"`
	Name   string `json:"name"`
	Status string `json:"status"`
	ADB    string `json:"adb_serial"`

	readyAt time.Time // slow-load mode: when a Starting group becomes Running
}

// refresh settles a slow load whose window has elapsed. Callers hold s.mu.
func (c *CVD) refresh(now time.Time) {
	if c.Status == statusStarting && !c.readyAt.IsZero() && now.After(c.readyAt) {
		c.Status = statusRunning
	}
}

// display is one virtual display, held in the upstream wire shape's terms.
type display struct {
	width, height, dpi, refreshRateHz int
}

// hostState is everything one impersonated host VM holds.
type hostState struct {
	cvds       map[string]*CVD              // group -> cvd
	displays   map[string]map[int]display   // "group/name" -> number -> display
	recordings map[string]map[string][]byte // "group/name" -> recording name -> bytes
	logs       map[string]map[string][]byte // "group/name" -> log file name -> bytes
	bugreports map[string][]byte            // bugreport id -> zip bytes
	snapshots  map[string]bool              // snapshot id -> exists (a flat host-global namespace upstream)
	conns      map[string][]any             // polled connection id -> queued messages
}

func newHostState() *hostState {
	return &hostState{
		cvds:       make(map[string]*CVD),
		displays:   make(map[string]map[int]display),
		recordings: make(map[string]map[string][]byte),
		logs:       make(map[string]map[string][]byte),
		bugreports: make(map[string][]byte),
		snapshots:  make(map[string]bool),
		conns:      make(map[string][]any),
	}
}

// Server is the fake HO endpoint.
type Server struct {
	ln   net.Listener
	http *http.Server

	mu         sync.Mutex
	hosts      map[string]*hostState // ip -> state
	ops        map[string]*operation // operation name -> record
	created    map[string][]string   // ledger: ip -> groups ever created
	deleted    map[string][]string   // ledger: ip -> groups deleted
	down       map[string]bool       // ip -> refuse (host unreachable)
	poisoned   map[string]bool       // ip -> creates fail with the tap/registration signature until /reset
	noCVDsAPI  map[string]bool       // ip -> /cvds 404s, forcing the operator /devices fallback
	opFailure  map[string]string     // ip -> stored failure every new operation completes with
	snapFail   map[string]string     // ip -> stored failure only snapshot takes complete with
	opWedged   map[string]bool       // ip -> operations never complete
	blankShot  map[string]bool       // ip -> captures complete carrying no image
	errAll     bool                  // 500 every request
	hang       time.Duration         // delay before answering
	nextID     int
	loadDelay  time.Duration // slow-load mode: time from create to Running
	waitHold   time.Duration // slow-load mode: :wait long-poll hold before 503
	violations []string      // actuations against in-flight (Starting) groups
	served     map[string]bool
}

// New starts the fake on an ephemeral port bound to all interfaces (required:
// connections arrive addressed to many 127.x.y.z aliases).
func New() (*Server, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", ":0")
	if err != nil {
		return nil, fmt.Errorf("listening: %w", err)
	}
	s := &Server{
		ln:        ln,
		hosts:     make(map[string]*hostState),
		ops:       make(map[string]*operation),
		created:   make(map[string][]string),
		deleted:   make(map[string][]string),
		down:      make(map[string]bool),
		poisoned:  make(map[string]bool),
		noCVDsAPI: make(map[string]bool),
		opFailure: make(map[string]string),
		snapFail:  make(map[string]string),
		opWedged:  make(map[string]bool),
		blankShot: make(map[string]bool),
		served:    make(map[string]bool),
	}
	s.http = &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, localIPKey{}, ipOf(c.LocalAddr()))
		},
	}
	go func() { _ = s.http.Serve(ln) }()
	return s, nil
}

type localIPKey struct{}

// SingleHostIP is the address a Single() consumer's host row should carry:
// unit tests dial plain loopback, so the wildcard listener keys their state
// under this IP.
const SingleHostIP = "127.0.0.1"

// Single starts the fake for one implicit host, the plain-loopback shape a
// unit test wants, without the harness's IP-per-VM keying. Seed state with
// SeedGroup(SingleHostIP, ...) and point the host row at SingleHostIP:Port().
func Single() (*Server, error) {
	return New()
}

// routes mirrors the upstream controller's table, in its order. Only the
// endpoints hoclient calls are served: an unserved one 404s, which is what a
// client asking for something this fake does not model should see.
func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(s.injectFaults)
	r.Use(middleware.NoCache)

	r.Route("/cvds", func(r chi.Router) {
		r.Get("/", s.listCVDs)
		r.Post("/", s.createCVD)
		r.Delete("/{group}", s.deleteCVD)
		r.Delete("/{group}/{name}", s.deleteCVD)
		r.Post("/{group}/:bugreport", s.createBugreport)

		// Group verbs. Same verb spelling as the instance routes below,
		// different handler upstream: this one selects the whole group and
		// decodes no body at all, where the instance :start decodes one and
		// rejects an absent one. A fake that served only the instance route
		// would let a client start one member and call the group up.
		r.Post("/{group}/:start", s.groupVerb(":start", statusRunning))
		r.Post("/{group}/:stop", s.groupVerb(":stop", statusStopped))

		// Instance verbs. :start alone reads a request body, and rejects an
		// absent one, because upstream decodes StartCVDRequest unconditionally.
		r.Post("/{group}/{name}/:start", s.startInstance)
		r.Post("/{group}/{name}/:stop", s.instanceVerb(":stop"))
		r.Post("/{group}/{name}/:restart", s.instanceVerb(":restart"))
		r.Post("/{group}/{name}/:powerwash", s.instanceVerb(":powerwash"))
		r.Post("/{group}/{name}/:powerbtn", s.instanceVerb(":powerbtn"))
		r.Post("/{group}/{name}/:start_screen_recording", s.startRecording)
		r.Post("/{group}/{name}/:stop_screen_recording", s.stopRecording)

		r.Post("/{group}/{name}/snapshots", s.createSnapshot)

		// Displays answer directly; the screenshot on the same resource does
		// not. See the package comment.
		r.Get("/{group}/{name}/displays", s.listDisplays)
		r.Post("/{group}/{name}/displays", s.addDisplay)
		r.Delete("/{group}/{name}/displays/{displayNumber}", s.removeDisplay)
		r.Get("/{group}/{name}/displays/{displayNumber}/:screenshot", s.screenshot)

		r.Get("/{group}/{name}/screen_recordings", s.listRecordings)
		r.Get("/{group}/{name}/screen_recordings/{recording}", s.downloadRecording)

		// GET-only like upstream (a HEAD 405s there before the file server);
		// upstream resolves the directory through `cvd fleet` first, so a
		// group not in the fleet 404s before any file is consulted.
		r.Get("/{group}/{name}/logs/{file}", s.serveLogFile)
	})

	r.Post("/operations/{name}/:wait", s.waitOperation)

	// Snapshots are a host-global id namespace upstream, not nested under the
	// CVD that produced them.
	r.Delete("/snapshots/{id}", s.deleteSnapshot)

	r.Get("/cvdbugreports/{id}", s.downloadBugreport)
	r.Delete("/cvdbugreports/{id}", s.deleteBugreport)

	r.Post("/reset", s.resetHost)

	// The Cuttlefish operator's signaling surface, which hoclient reaches on
	// the same base URL.
	r.Post("/polled_connections", s.createPolledConnection)
	r.Post("/polled_connections/{connID}/:forward", s.forwardSignaling)
	r.Get("/polled_connections/{connID}/messages", s.signalingMessages)

	// The operator's device listing, hoclient's fallback when /cvds 404s.
	r.Get("/devices", s.listDevices)

	return r
}

// injectFaults applies the per-IP and global failure modes ahead of routing,
// and records the route each served request matched.
func (s *Server) injectFaults(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := localIP(r)

		s.mu.Lock()
		down, errAll, hang := s.down[ip], s.errAll, s.hang
		s.mu.Unlock()

		if hang > 0 {
			time.Sleep(hang)
		}
		if down {
			// Sever without a response: to the client the host is gone.
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			writeErr(w, http.StatusServiceUnavailable, "host down", "")
			return
		}
		if errAll {
			writeErr(w, http.StatusInternalServerError, "injected failure", "")
			return
		}

		next.ServeHTTP(w, r)

		if pattern := chi.RouteContext(r.Context()).RoutePattern(); pattern != "" {
			s.mu.Lock()
			s.served[r.Method+" "+pattern] = true
			s.mu.Unlock()
		}
	})
}

// ServedRoutes lists every "METHOD /pattern" this server has actually served.
//
// It keeps a hand-rolled fake from creeping back in. A suite that believes it
// covers the client's surface can assert that here.
func (s *Server) ServedRoutes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.served))
	for route := range s.served {
		out = append(out, route)
	}
	return out
}

// SeedGroup plants a live CVD group on a host. The group stands for state a
// prior create attempt or another instance left behind, and it is planted
// without going through the create API (no ledger entry, like a group whose
// creating task died mid-flight).
func (s *Server) SeedGroup(ip, group, name, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostLocked(ip).cvds[group] = &CVD{Group: group, Name: name, Status: status, ADB: "0.0.0.0:6520"}
}

// SeedLogFile plants one file in a group's logs directory (kernel.log,
// launcher.log), served by the logs route with real Range semantics.
func (s *Server) SeedLogFile(ip, group, name, file string, content []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hostLocked(ip)
	key := group + "/" + name
	if h.logs[key] == nil {
		h.logs[key] = make(map[string][]byte)
	}
	h.logs[key][file] = content
}

// Port is the port hosts must carry as host_orchestrator_port.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Close stops the server.
func (s *Server) Close() error {
	if err := s.http.Close(); err != nil {
		return fmt.Errorf("closing fake HO: %w", err)
	}
	return nil
}

// SetDown marks one host IP unreachable (connection-level failure), driving
// the client's circuit breaker and the device controller.
func (s *Server) SetDown(ip string, down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down[ip] = down
}

// Reset drops all live CVD state for one host IP AND marks the address dead.
// The real Host Orchestrator dies with its VM, and nothing ever answers on
// its address again (fake GCE never reuses IPs). Without the dead-mark the
// wildcard listener keeps serving a deleted VM's IP, so a create retried
// against a preempted host's stale address would mint a group no VM hosts.
// That cannot happen in production, where the connection is refused. The
// created/deleted ledgers are preserved.
func (s *Server) Reset(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.hosts, ip)
	s.down[ip] = true
}

// SetErrorAll makes every request 500 while enabled.
func (s *Server) SetErrorAll(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errAll = on
}

// SetHang delays every response by d (0 clears), simulating a slow HO.
func (s *Server) SetHang(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hang = d
}

// SetCVDsAPIUnsupported makes /cvds answer 404 on one host, which is how an
// older image without the Host Orchestrator API behaves. That is the condition
// hoclient's fallback to the operator's /devices listing exists for.
func (s *Server) SetCVDsAPIUnsupported(ip string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noCVDsAPI[ip] = on
}

// SetOperationFailure makes every operation subsequently STARTED on this host
// complete with a stored failure. That is the shape of an async verb that
// fails after its request was accepted, which a client can only learn from
// :wait. Empty msg clears it.
func (s *Server) SetOperationFailure(ip, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg == "" {
		delete(s.opFailure, ip)
		return
	}
	s.opFailure[ip] = msg
}

// SetSnapshotFailure makes only snapshot takes subsequently started on this
// host complete with a stored failure, with every other verb (the
// compensating restart in particular) still serving normally. It is the shape
// of the live suspend failure, where one verb dies and the launcher survives.
// Empty msg clears it.
func (s *Server) SetSnapshotFailure(ip, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg == "" {
		delete(s.snapFail, ip)
		return
	}
	s.snapFail[ip] = msg
}

// SetOperationsWedged makes every operation on this host stay in progress
// forever: each :wait answers 503 at its hold, as a healthy-but-busy host
// does, and never resolves. It is the shape of a CVD whose launcher socket is
// blocked. The host answers, so it is not unreachable, but no operation it
// accepts will ever finish.
func (s *Server) SetOperationsWedged(ip string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opWedged[ip] = on
}

// SetEmptyScreenshot makes captures complete carrying no image, modeling a
// host that reports success and produces nothing. Clients that trusted it
// wrote 0-byte PNGs, which is why the harness can reproduce it.
func (s *Server) SetEmptyScreenshot(ip string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blankShot[ip] = on
}

// SetLoadDelay enables slow-load mode. Creates register their group as
// Starting, becoming Running only after delay, and the create operation's
// :wait long-polls up to hold before answering 503 in-progress, which is the
// real HO's WaitOperationDuration semantics. The adopt-race lives in that
// window. An attempt abandoned mid-load leaves a Starting group a retry must
// NOT actuate on (violations are ledgered in ActuationViolations). delay 0
// restores instant creates.
func (s *Server) SetLoadDelay(delay, hold time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadDelay = delay
	s.waitHold = hold
}

// ActuationViolations lists every start/stop/delete issued against a group
// whose launch was still in flight. Each one is the adopt-race collision
// that poisons a real host (leaked launcher processes holding tap devices).
func (s *Server) ActuationViolations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.violations...)
}

// LiveGroups returns ip -> live group names.
func (s *Server) LiveGroups() map[string][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]string, len(s.hosts))
	for ip, h := range s.hosts {
		for g := range h.cvds {
			out[ip] = append(out[ip], g)
		}
	}
	return out
}

// Ledger returns per-IP created and deleted group name lists, in that order.
func (s *Server) Ledger() (map[string][]string, map[string][]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := make(map[string][]string, len(s.created))
	for ip, gs := range s.created {
		c[ip] = append([]string(nil), gs...)
	}
	d := make(map[string][]string, len(s.deleted))
	for ip, gs := range s.deleted {
		d[ip] = append([]string(nil), gs...)
	}
	return c, d
}

// Poison makes every create on the host at ip fail with the leaked-tap /
// stale-registration signature until the host is /reset.
func (s *Server) Poison(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.poisoned[ip] = true
}

// hostLocked returns the host's state, creating it. Callers hold s.mu.
func (s *Server) hostLocked(ip string) *hostState {
	h := s.hosts[ip]
	if h == nil {
		h = newHostState()
		s.hosts[ip] = h
	}
	return h
}

// nextIDLocked returns a monotonic suffix for generated names. Callers hold s.mu.
func (s *Server) nextIDLocked() int {
	s.nextID++
	return s.nextID
}

func localIP(r *http.Request) string {
	ip, _ := r.Context().Value(localIPKey{}).(string)
	return ip
}

func ipOf(addr net.Addr) string {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.IP.String()
	}
	return ""
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v) //nolint:errchkjson // best-effort fake response
}

// writeErr emits the upstream error envelope: {"error":..., "details":...}.
func writeErr(w http.ResponseWriter, status int, msg, details string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]string{"error": msg}
	if details != "" {
		body["details"] = details
	}
	_ = json.NewEncoder(w).Encode(body) //nolint:errchkjson // best-effort fake response
}

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

// Package main serves the orchestrator's OpenAPI spec without any cloud
// dependencies, for development tooling such as the frontend schema-drift
// check:
//
//	go run ./tests/specgen &
//	cd web/cuttlefish-console && npm run check:schema-drift
//
// With -dump it instead writes the OpenAPI 3.0 downconvert to stdout and
// exits, for scripts/generate-ccogen.sh. Same router, no listener.
//
// -addr takes any listen address, including a :0 port. scripts/ci.sh runs it
// that way and reads the port back off the "serving OpenAPI spec at <url>"
// line below, so a leftover server can never hold the port the next run wants
// nor answer its readiness probe with a spec built from older code. That log
// line is the contract between the two; keep the URL the last field.
//
// The wiring lives in tests/specwire, shared with the router's registration
// test. Handlers are wired with nil dependencies, so only route registration
// runs and the handlers themselves never do.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"cloud-cuttlefish-orchestrator/internal/router"
	"cloud-cuttlefish-orchestrator/tests/specwire"
)

func main() {
	dump := flag.Bool("dump", false, "write the OpenAPI 3.0 spec to stdout and exit")
	addr := flag.String("addr", "localhost:8888", "listen address; port 0 picks a free one")
	flag.Parse()

	handler, err := router.New(specwire.Config())
	if err != nil {
		log.Fatalf("creating router: %v", err)
	}

	if *dump {
		if err := dumpSpec(os.Stdout, handler); err != nil {
			log.Fatalf("dumping spec: %v", err)
		}
		return
	}

	// Listen before serving so a :0 port is resolved to the real one and can be
	// reported. Callers cannot probe an address only the kernel knows.
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", *addr)
	if err != nil {
		log.Fatalf("listening on %s: %v", *addr, err)
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("serving OpenAPI spec at http://%s/openapi.json", listener.Addr())
	log.Fatal(server.Serve(listener))
}

// dumpSpec fetches the 3.0 downconvert in-process. Generators consume 3.0
// rather than the native 3.1 document because Go generator support for 3.1
// is still uneven; Huma serves both from the same registrations.
func dumpSpec(w io.Writer, handler http.Handler) error {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/openapi-3.0.json", http.NoBody))
	if rec.Code != http.StatusOK {
		return fmt.Errorf("GET /openapi-3.0.json returned %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := w.Write(rec.Body.Bytes()); err != nil {
		return fmt.Errorf("writing spec: %w", err)
	}
	return nil
}

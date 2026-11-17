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

package fakeho

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// operation is one async host operation and the result :wait will serve.
//
// Upstream stores a value or an error per operation and hands the value
// straight back from :wait, so what a verb returns is decided when it is
// STARTED and never when it is waited on. That is why an async verb can answer
// 200 and still fail.
type operation struct {
	ip string

	// group gates completion on a launch: while that group is Starting the
	// operation is not done, which is what makes the create :wait long-poll.
	group string

	// value is the stored result. Upstream's instance verbs store an empty
	// object, creates store the CVD list, a bugreport stores its bare ID
	// string, and a screenshot stores the image response.
	value any

	// failure, when set, is the operation's own stored error: :wait reports it
	// as 500, the terminal outcome a client must not keep polling.
	failure string
}

// newOperationLocked registers a completed-value operation and returns its
// name. A host under SetOperationFailure gets a stored failure instead.
// Callers hold s.mu.
func (s *Server) newOperationLocked(ip string, value any) string {
	name := fmt.Sprintf("op-%d", s.nextIDLocked())
	op := &operation{ip: ip, value: value}
	if msg := s.opFailure[ip]; msg != "" {
		op.failure = msg
	}
	s.ops[name] = op
	return name
}

// newCreateOperationLocked registers an operation whose completion follows a
// group's launch. Callers hold s.mu.
func (s *Server) newCreateOperationLocked(ip, group string) string {
	name := fmt.Sprintf("op-%d", s.nextIDLocked())
	op := &operation{ip: ip, group: group}
	if msg := s.opFailure[ip]; msg != "" {
		op.failure = msg
	}
	s.ops[name] = op
	return name
}

// waitOperation serves POST /operations/{name}/:wait.
//
// The contract copied from upstream: the operation's stored value on success,
// its stored error as 500, 404 for an unknown name, and, only at the end of
// the hold window, 503 to say "still running". That 503 is the sole
// in-progress signal, so a client's per-poll timeout has to outlive the hold.
// A shorter one turns every slow operation into a fake connection failure.
func (s *Server) waitOperation(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	s.mu.Lock()
	op := s.ops[name]
	hold := s.waitHold
	wedged := op != nil && s.opWedged[op.ip]
	s.mu.Unlock()

	if wedged {
		writeErr(w, http.StatusServiceUnavailable, "Wait for operation timed out", "")
		return
	}
	if op == nil {
		writeErr(w, http.StatusNotFound, "Operation not found", "unknown operation "+name)
		return
	}
	if op.failure != "" {
		writeErr(w, http.StatusInternalServerError, op.failure, "operation "+name+" failed")
		return
	}
	if op.group == "" {
		writeJSON(w, op.value)
		return
	}

	s.waitForLaunch(w, op, hold)
}

// waitForLaunch long-polls a create operation until its group is out of
// Starting, then answers with the CVD list the client expects.
func (s *Server) waitForLaunch(w http.ResponseWriter, op *operation, hold time.Duration) {
	deadline := time.Now().Add(hold)
	for {
		now := time.Now()
		s.mu.Lock()
		var c *CVD
		if h := s.hosts[op.ip]; h != nil {
			c = h.cvds[op.group]
		}
		if c != nil {
			c.refresh(now)
		}
		s.mu.Unlock()

		switch {
		case c == nil:
			// Group vanished mid-operation (deleted): resolve empty-done, the
			// shape production tolerates.
			writeJSON(w, map[string]any{})
			return
		case c.Status != statusStarting:
			writeJSON(w, map[string]any{"cvds": []*CVD{c}})
			return
		case now.After(deadline):
			writeErr(w, http.StatusServiceUnavailable, "Wait for operation timed out", "")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

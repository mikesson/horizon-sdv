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

package cco

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A failed operation must end the wait with an error. With a single "done"
// state, --wait on a failed powerwash would print success and exit zero.
func TestWaitOperationFailsOnASettledFailure(t *testing.T) {
	t.Parallel()
	c := waitTestClient(t, "/v2/operations/op-1", func(w http.ResponseWriter, poll int) {
		if poll < 2 {
			fmt.Fprint(w, `{"name":"operations/op-1","done":false,"metadata":{"verb":"device.powerwash"}}`)
			return
		}
		fmt.Fprint(w, `{"name":"operations/op-1","done":true,`+
			`"metadata":{"verb":"device.powerwash"},`+
			`"error":{"title":"Bad Gateway","status":502,"detail":"cvd powerwash failed"}}`)
	})

	op, err := c.WaitOperation(t.Context(), "op-1")
	if err == nil {
		t.Fatalf("a failed powerwash reported success (op=%+v); this is the bug the tri-valued status exists to prevent", op)
	}
	if !strings.Contains(err.Error(), "cvd powerwash failed") {
		t.Errorf("the stored problem detail is the only diagnostic and it was dropped: %v", err)
	}
	if _, ok := errors.AsType[*WaitFailedError](err); !ok {
		t.Errorf("err = %#v, want a *WaitFailedError so the CLI exits with the failure code", err)
	}
}

func TestWaitOperationSucceedsOnASettledSuccess(t *testing.T) {
	t.Parallel()
	c := waitTestClient(t, "/v2/operations/op-1", func(w http.ResponseWriter, poll int) {
		if poll < 3 {
			fmt.Fprint(w, `{"name":"operations/op-1","done":false,"metadata":{"verb":"device.restart","attempt":1}}`)
			return
		}
		fmt.Fprint(w, `{"name":"operations/op-1","done":true,`+
			`"metadata":{"verb":"device.restart"},`+
			`"response":{"restarted_at":"2026-07-26T00:00:00Z"}}`)
	})

	op, err := c.WaitOperation(t.Context(), "op-1")
	if err != nil {
		t.Fatalf("WaitOperation: %v", err)
	}
	if !op.Done {
		t.Error("returned an unsettled operation")
	}
	if !strings.Contains(string(op.Response), "restarted_at") {
		t.Errorf("response payload lost: %s", op.Response)
	}
}

// waitFor keys off status(), so each of the three states is checked directly.
func TestOperationStatusIsTriValued(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		op   Operation
		want string
	}{
		{"in flight", Operation{Done: false}, StatusOpRunning},
		{"settled ok", Operation{Done: true}, StatusOpDone},
		{"settled failed", Operation{Done: true, Error: &OperationProblem{Title: "boom", Status: 502}}, StatusOpFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.op.status(); got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
		})
	}
	if StatusOpDone == StatusOpFailed {
		t.Fatal("the two terminal states must differ, or waitFor cannot tell success from failure")
	}
}

// failureDetail prefers the stored problem and falls back to the last
// transient error.
func TestOperationFailureDetail(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		op   Operation
		want string
	}{
		{"problem detail", Operation{Error: &OperationProblem{Title: "Bad Gateway", Detail: "host unreachable"}}, "host unreachable"},
		{"problem title only", Operation{Error: &OperationProblem{Title: "Bad Gateway"}}, "Bad Gateway"},
		{"falls back to last error", Operation{Metadata: OperationMetadata{LastError: "attempt 2 failed"}}, "attempt 2 failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.op.failureDetail(); got != tc.want {
				t.Errorf("failureDetail = %q, want %q", got, tc.want)
			}
		})
	}
}

// ID strips the "operations/" prefix that the name field carries.
func TestOperationIDStripsTheCollectionPrefix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, want string }{
		{"operations/abc", "abc"},
		{"abc", "abc"},
		{"", ""},
	} {
		op := Operation{Name: tc.name}
		if got := op.ID(); got != tc.want {
			t.Errorf("ID() for %q = %q, want %q", tc.name, got, tc.want)
		}
	}
}

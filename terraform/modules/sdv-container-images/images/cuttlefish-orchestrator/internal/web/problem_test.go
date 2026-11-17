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

package web_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	devicedomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// The case web.Problem exists for: an error MapError does not classify. MapError
// returns it unchanged and Huma, not MapError, supplies the 500, so anything
// marshaling MapError's result directly stores a raw Go error where a problem
// document belongs. The document is served to clients, so the unclassified text
// is withheld from it exactly as the synchronous half withholds it.
func TestProblemSynthesizesTheFallbackMapErrorDoesNot(t *testing.T) {
	t.Parallel()

	p := web.Problem(errors.New("gcs upload failed: dial tcp 10.0.0.5:443: connection reset"))
	if p == nil {
		t.Fatal("Problem returned nil for a real error")
	}
	if p.Status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", p.Status)
	}
	if p.Title == "" {
		t.Error("no title: a problem document with an empty title is not one")
	}
	if p.Detail == "" {
		t.Error("a problem document with no detail at all is not one")
	}
	for _, fragment := range []string{"gcs upload failed", "10.0.0.5:443", "connection reset"} {
		if strings.Contains(p.Detail, fragment) {
			t.Errorf("stored detail leaked %q: %s", fragment, p.Detail)
		}
	}
}

// A classified domain error keeps its status, so a stored failure reads the same
// as the synchronous response the same error produces today.
func TestProblemKeepsAClassifiedStatus(t *testing.T) {
	t.Parallel()

	p := web.Problem(devicedomain.ErrNotFound)
	if p.Status != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (the status MapError assigns this family)", p.Status)
	}
}

// hoclient classifies an unreachable host as 502 and an exhausted operation wait
// as 504. Those are most verb failures, so they are the statuses that will
// actually land in stored operations. They are also the guard on the 5xx
// withholding above: these are upstream's own words about a classified failure,
// they are what the synchronous response serves, and a rule that blanked every
// 5xx detail would silently empty most stored failures.
func TestProblemPreservesHostClientClassification(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		err        error
		want       int
		wantDetail string
	}{
		// StatusCode 0 = no HTTP exchange happened (dial failure, open
		// breaker); HostError.GetStatus renders that as 502.
		{"unreachable host", &hoclient.HostError{Message: "unreachable", Cause: hoclient.ErrHostUnreachable}, http.StatusBadGateway, "unreachable"},
		{"exhausted wait", &hoclient.HostError{StatusCode: http.StatusGatewayTimeout, Message: "wait exhausted", Cause: hoclient.ErrOperationTimeout}, http.StatusGatewayTimeout, "wait exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := web.Problem(tc.err)
			if p.Status != tc.want {
				t.Errorf("status = %d, want %d", p.Status, tc.want)
			}
			if p.Detail != tc.wantDetail {
				t.Errorf("detail = %q, want %q; the host's own reason is the diagnostic", p.Detail, tc.wantDetail)
			}
		})
	}
}

func TestProblemOnNilIsNil(t *testing.T) {
	t.Parallel()

	if p := web.Problem(nil); p != nil {
		t.Errorf("Problem(nil) = %+v, want nil", p)
	}
}

// A context abort is the class the fallback was written for: it carries no
// status, so without the synthesis it would reach a stored operation as a bare
// Go error string with no status at all.
func TestProblemHandlesAContextAbort(t *testing.T) {
	t.Parallel()

	p := web.Problem(context.Canceled)
	if p.Status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", p.Status)
	}
}

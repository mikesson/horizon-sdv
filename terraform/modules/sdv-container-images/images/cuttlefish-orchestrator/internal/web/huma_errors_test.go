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

package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/idempotency"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	"cloud-cuttlefish-orchestrator/internal/domain/storage"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

func TestMapError_NilError(t *testing.T) {
	t.Parallel()
	if result := MapError(nil); result != nil {
		t.Errorf("MapError(nil) = %v, want nil", result)
	}
}

// TestMapError_Table is the regression net for the error policy: every domain
// error family maps to its status code, as the bare sentinel, wrapped, as a
// populated typed instance, and as a wrapped typed instance. Domain errors
// carry no HTTP knowledge themselves, so this table IS the status contract.
func TestMapError_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want int
	}{
		// Auth scoping family (the 401 arm is pinned separately in
		// TestMapError_AuthScoping: its fixed detail is not the sentinel text).
		{"auth creator-scope conflict sentinel", auth.ErrCreatorScopeConflict, http.StatusBadRequest},
		{"auth creator-scope conflict wrapped", fmt.Errorf("listing batches: %w", auth.ErrCreatorScopeConflict), http.StatusBadRequest},

		// Device family.
		{"device not-found sentinel", device.ErrNotFound, http.StatusNotFound},
		{"device not-found wrapped", fmt.Errorf("getting device: %w", device.ErrNotFound), http.StatusNotFound},
		{"device not-found typed", &device.NotFoundError{ID: "d-1"}, http.StatusNotFound},
		{"device not-found typed wrapped", fmt.Errorf("getting device: %w", &device.NotFoundError{ID: "d-1"}), http.StatusNotFound},
		{"device validation typed", &device.ValidationError{Message: "host_id is required"}, http.StatusBadRequest},
		{"device validation wrapped", fmt.Errorf("admitting: %w", &device.ValidationError{Message: "bad"}), http.StatusBadRequest},
		{"device invalid-state sentinel wrapped", fmt.Errorf("cannot stop: %w", device.ErrInvalidState), http.StatusConflict},
		{"device invalid-state typed", &device.InvalidStateError{Current: device.StatusRunning, Expected: device.StatusStopped}, http.StatusConflict},
		{"device already-exists sentinel", device.ErrAlreadyExists, http.StatusConflict},
		{"device already-exists typed", &device.AlreadyExistsError{Name: "d", HostID: "h"}, http.StatusConflict},

		// Build family.
		{"build validation typed", &build.ValidationError{Message: "host_package URL must end in .tar.gz"}, http.StatusBadRequest},
		{"build validation wrapped", fmt.Errorf("registering: %w", &build.ValidationError{Message: "bad"}), http.StatusBadRequest},

		// Host family.
		{"host not-found sentinel wrapped", fmt.Errorf("getting host: %w", host.ErrNotFound), http.StatusNotFound},
		{"host not-found typed", &host.NotFoundError{ID: "h-1"}, http.StatusNotFound},
		{"host reserved-name typed", &host.ReservedNameError{Name: "cf-x"}, http.StatusBadRequest},
		{"host reserved-name wrapped", fmt.Errorf("validating host name: %w", &host.ReservedNameError{Name: "cf-x"}), http.StatusBadRequest},
		{"host id-shaped name wrapped", fmt.Errorf("validating host name: %w", &host.IDShapedNameError{Name: "abcdef0123456789abcdef0123456789"}), http.StatusBadRequest},
		{"host invalid-state sentinel wrapped", fmt.Errorf("cannot start: %w", host.ErrInvalidState), http.StatusConflict},
		{"host invalid-state typed", &host.InvalidStateError{Message: "host is deleting"}, http.StatusConflict},
		{"host capacity sentinel wrapped", fmt.Errorf("no room: %w", host.ErrCapacityExceeded), http.StatusServiceUnavailable},
		{"host capacity typed", &host.CapacityExceededError{HostID: "h", CurrentCount: 4, MaxCapacity: 4}, http.StatusServiceUnavailable},
		{"host not-retryable typed", &host.NotRetryableError{HostID: "h", Status: "running"}, http.StatusConflict},
		{"host degraded typed", &host.DegradedError{HostID: "h"}, http.StatusConflict},
		{"host degraded wrapped", fmt.Errorf("placement: %w", &host.DegradedError{HostID: "h"}), http.StatusConflict},
		{"host catalog-validation typed", &host.CatalogValidationError{}, http.StatusUnprocessableEntity},

		// Batch family.
		{"batch not-found sentinel", batch.ErrNotFound, http.StatusNotFound},
		{"batch failed wrapped", fmt.Errorf("%w: b-1 (quota); delete the batch", batch.ErrFailed), http.StatusConflict},
		{"batch invalid-spec wrapped", fmt.Errorf("%w: count must be positive", batch.ErrInvalidSpec), http.StatusBadRequest},

		// Idempotency.
		{"idempotency conflict sentinel", idempotency.ErrConflict, http.StatusUnprocessableEntity},
		{"idempotency conflict wrapped", fmt.Errorf("%w: batch b-1", idempotency.ErrConflict), http.StatusUnprocessableEntity},

		// Build family.
		{"build not-found sentinel wrapped", fmt.Errorf("resolving build: %w", build.ErrNotFound), http.StatusNotFound},
		{"build not-found typed", &build.NotFoundError{ID: "b-1"}, http.StatusNotFound},

		// Machine type.
		{"machinetype not-found wrapped", fmt.Errorf("machine type n2-weird: %w", machinetype.ErrNotFound), http.StatusNotFound},
		{"machinetype GPU policy wrapped", fmt.Errorf("updating machine family curation: %w",
			&machinetype.GPUPolicyError{Family: "n1", DefaultGPUCount: 2, MaxGPUs: 1}), http.StatusBadRequest},

		// GCE catalog family.
		{"catalog zone policy typed", &gcecatalog.ZonePolicyError{Message: "not a zone"}, http.StatusBadRequest},
		{"catalog zone policy wrapped", fmt.Errorf("zone admin: %w", &gcecatalog.ZonePolicyError{Message: "not a zone"}), http.StatusBadRequest},

		// Quota family.
		{"quota exceeded typed", &quota.ExceededError{Requested: 1, Available: 0, MaxHosts: 10}, http.StatusTooManyRequests},
		{"quota exceeded wrapped", fmt.Errorf("creating host: %w", &quota.ExceededError{}), http.StatusTooManyRequests},
		{"quota not-found typed", &quota.NotFoundError{Scope: quota.ScopePlatform, ScopeID: quota.GlobalScopeID}, http.StatusNotFound},

		// Storage family.
		{"storage not-found wrapped", fmt.Errorf("%w: gs://bucket/missing.img", storage.ErrNotFound), http.StatusNotFound},
		{"storage permission wrapped", fmt.Errorf("%w: gs://locked-bucket", storage.ErrPermissionDenied), http.StatusForbidden},

		// Host image config family.
		{"hostconfig not-found typed", &hostconfig.NotFoundError{MachineFamily: "n1"}, http.StatusNotFound},
		{"hostconfig validation typed", &hostconfig.ValidationError{Field: "image_family", Message: "required"}, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := MapError(tt.err)

			statusErr, ok := result.(StatusError)
			if !ok {
				t.Fatalf("MapError() = %T, does not implement StatusError", result)
			}
			if got := statusErr.GetStatus(); got != tt.want {
				t.Errorf("GetStatus() = %d, want %d", got, tt.want)
			}
			// The original classification text must survive into the response
			// detail (5xx sanitization is RespondError's job, not MapError's).
			if !strings.Contains(result.Error(), tt.err.Error()) {
				t.Errorf("mapped detail %q lost the original message %q", result.Error(), tt.err.Error())
			}
		})
	}
}

// TestMapError_CatalogValidationDetails pins the 422 problem-details shape:
// per-failure errors[] entries carrying field locations and suggestions, plus
// the alternative-zones entry.
func TestMapError_CatalogValidationDetails(t *testing.T) {
	t.Parallel()
	cve := &host.CatalogValidationError{
		Errors: []host.CatalogValidationDetail{
			{Field: "machine_type", Code: "MACHINE_TYPE_NOT_AVAILABLE", Message: "n9-mega-96 is not available", Suggestion: "Pick an enabled machine type"},
			{Field: "gpu_type", Code: "GPU_NOT_AVAILABLE", Message: "no such GPU"},
		},
		AlternativeZones: []string{"europe-west4-b", "europe-west4-c"},
	}

	model, ok := errors.AsType[*huma.ErrorModel](MapError(cve))
	if !ok {
		t.Fatalf("MapError() = %T, want *huma.ErrorModel", MapError(cve))
	}
	if model.Status != http.StatusUnprocessableEntity {
		t.Errorf("Status = %d, want 422", model.Status)
	}
	if model.Detail != "n9-mega-96 is not available" {
		t.Errorf("Detail = %q, want the first failure message", model.Detail)
	}
	if len(model.Errors) != 3 {
		t.Fatalf("len(Errors) = %d, want 3 (2 failures + zones)", len(model.Errors))
	}
	if model.Errors[0].Location != "machine_type" || !strings.Contains(model.Errors[0].Message, "Pick an enabled machine type") {
		t.Errorf("Errors[0] = %+v, want machine_type location with the suggestion folded in", model.Errors[0])
	}
	if model.Errors[1].Location != "gpu_type" || model.Errors[1].Message != "no such GPU" {
		t.Errorf("Errors[1] = %+v, want the bare gpu_type failure", model.Errors[1])
	}
	if model.Errors[2].Location != "zone" || !strings.Contains(model.Errors[2].Message, "europe-west4-b, europe-west4-c") {
		t.Errorf("Errors[2] = %+v, want the alternative-zones entry", model.Errors[2])
	}
}

// TestMapError_AuthScoping pins the auth arms' wire details byte-for-byte:
// the 400 serves the sentinel text, and the 401 serves the middleware's fixed
// "Authentication required". Whatever internal context is wrapped around
// ErrUnauthenticated stays off the wire.
func TestMapError_AuthScoping(t *testing.T) {
	t.Parallel()

	conflict, ok := errors.AsType[*huma.ErrorModel](MapError(auth.ErrCreatorScopeConflict))
	if !ok {
		t.Fatalf("MapError(conflict) is not a *huma.ErrorModel")
	}
	if conflict.Status != http.StatusBadRequest || conflict.Detail != "created_by and mine are mutually exclusive" {
		t.Errorf("conflict = %d %q, want 400 with the exact sentinel text", conflict.Status, conflict.Detail)
	}

	wrapped := fmt.Errorf("%w: missing X-Goog-IAP-JWT-Assertion header", auth.ErrUnauthenticated)
	unauth, ok := errors.AsType[*huma.ErrorModel](MapError(wrapped))
	if !ok {
		t.Fatalf("MapError(unauthenticated) is not a *huma.ErrorModel")
	}
	if unauth.Status != http.StatusUnauthorized || unauth.Detail != "Authentication required" {
		t.Errorf("unauthenticated = %d %q, want 401 with the fixed detail", unauth.Status, unauth.Detail)
	}
}

// TestMapError_StatusErrorPassthrough pins the two halves of the
// status-carrier contract: Huma's own error model passes through unmapped,
// while foreign carriers (transport *AppErrors) keep their status but are
// re-rendered as problem details, because Huma would otherwise serialize their
// raw struct shape on a /v2 route.
func TestMapError_StatusErrorPassthrough(t *testing.T) {
	t.Parallel()

	humaErr := huma.Error409Conflict("already mapped")
	if result := MapError(humaErr); !errors.Is(result, humaErr) {
		t.Errorf("MapError() must return Huma's own errors untouched")
	}

	tests := []struct {
		name       string
		err        error
		want       int
		wantDetail string
	}{
		{"AppError", NewNotFoundError("gone"), http.StatusNotFound, "not_found: gone"},
		{"wrapped status error", fmt.Errorf("context: %w", NewBadRequestError("bad")), http.StatusBadRequest, "context: bad_request: bad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model, ok := errors.AsType[*huma.ErrorModel](MapError(tt.err))
			if !ok {
				t.Fatalf("MapError() = %T, want a *huma.ErrorModel conversion", MapError(tt.err))
			}
			if model.Status != tt.want || model.Detail != tt.wantDetail {
				t.Errorf("converted = %d %q, want %d %q", model.Status, model.Detail, tt.want, tt.wantDetail)
			}
		})
	}
}

// TestMapError_HostErrorProblemDetails pins the hoclient family: the upstream
// status survives, and the detail is the upstream message with the details
// suffix, never the raw *HostError struct Huma would otherwise serialize.
func TestMapError_HostErrorProblemDetails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		want       int
		wantDetail string
	}{
		{
			"upstream 500 with details",
			&hoclient.HostError{StatusCode: 500, Message: "operation failed", Details: "crosvm exited"},
			http.StatusInternalServerError,
			"operation failed; crosvm exited",
		},
		{
			"upstream 404",
			&hoclient.HostError{StatusCode: 404, Message: "CVD not found"},
			http.StatusNotFound,
			"CVD not found",
		},
		{
			"unreachable host exit wrap",
			&hoclient.HostError{StatusCode: 0, Message: "host orchestrator unreachable", Details: "dial tcp 10.0.0.5:2080: i/o timeout"},
			http.StatusBadGateway,
			"host orchestrator unreachable; dial tcp 10.0.0.5:2080: i/o timeout",
		},
		{
			"wrapped by handler context",
			fmt.Errorf("pressing power button: %w", &hoclient.HostError{StatusCode: 502, Message: "bad gateway"}),
			http.StatusBadGateway,
			"bad gateway",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model, ok := errors.AsType[*huma.ErrorModel](MapError(tt.err))
			if !ok {
				t.Fatalf("MapError() = %T, want *huma.ErrorModel", MapError(tt.err))
			}
			if model.Status != tt.want || model.Detail != tt.wantDetail {
				t.Errorf("converted = %d %q, want %d %q", model.Status, model.Detail, tt.want, tt.wantDetail)
			}
		})
	}
}

// TestMapError_TransitionRefusalIs409 closes the path end to end: an
// entity-level transition refusal is what a service returns unwrapped (the
// provision task's mid-delete case is the reachable one), and it must arrive as
// a conflict rather than as an unclassified 500. Nothing between the domain and
// this mapper decides it.
func TestMapError_TransitionRefusalIs409(t *testing.T) {
	t.Parallel()

	deleted := &device.Device{Status: device.StatusDeleted}
	deleting := &host.Host{Status: host.StatusDeleting}

	tests := []struct {
		name string
		err  error
	}{
		{"device deleted -> running", deleted.TransitionTo(device.StatusRunning)},
		{"host deleting -> provisioning", deleting.TransitionTo(host.StatusProvisioning)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.err == nil {
				t.Fatal("the transition was allowed; this test no longer exercises a refusal")
			}
			statusErr, ok := MapError(tt.err).(StatusError)
			if !ok {
				t.Fatalf("MapError() = %T, does not implement StatusError", MapError(tt.err))
			}
			if got := statusErr.GetStatus(); got != http.StatusConflict {
				t.Errorf("GetStatus() = %d, want 409", got)
			}
		})
	}
}

// TestMapError_NoMessageHeuristic pins that infrastructure errors merely
// mentioning "not found" are NOT mapped to 404. They must surface as 500.
func TestMapError_NoMessageHeuristic(t *testing.T) {
	t.Parallel()
	result := MapError(errors.New("GCE network not found"))
	if _, ok := result.(StatusError); ok {
		t.Errorf("infrastructure error was mapped to a status; want passthrough")
	}
}

// TestMapError_UnknownErrors tests that unknown errors pass through unchanged.
func TestMapError_UnknownErrors(t *testing.T) {
	t.Parallel()
	unknownErr := errors.New("some internal error")
	if result := MapError(unknownErr); !errors.Is(result, unknownErr) {
		t.Errorf("MapError() should return original error for unknown errors")
	}
}

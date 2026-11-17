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
	"strings"

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
	"cloud-cuttlefish-orchestrator/internal/domain/operation"
	"cloud-cuttlefish-orchestrator/internal/domain/quota"
	storagedomain "cloud-cuttlefish-orchestrator/internal/domain/storage"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// StatusError is the interface Huma uses to determine HTTP status codes.
// Errors implementing this interface will have their status code used
// in the HTTP response.
type StatusError interface {
	GetStatus() int
}

// MapError is THE mapper from domain error families to Huma RFC 9457
// responses. Domain errors carry no HTTP knowledge; every handler routes
// service errors through here (directly, or via RespondError on the raw chi
// routes), so a family's status code is decided in exactly one place. The
// families themselves live in errorRules.
//
// Errors that already implement StatusError keep their status: Huma's own
// error model passes through untouched, while foreign carriers (hoclient's
// *HostError forwarding the upstream Host Orchestrator status, and transport
// *AppErrors) are re-rendered as problem details (see
// problemFromStatusError).
//
// For unrecognized errors, the original error is returned and Huma will
// default to 500.
func MapError(err error) error {
	if err == nil {
		return nil
	}

	// Already carries a status; keep it, converting foreign shapes to
	// RFC 9457. Stays errors.As: StatusError does not embed error, so it
	// cannot satisfy errors.AsType's constraint.
	var statusErr StatusError
	if errors.As(err, &statusErr) {
		return problemFromStatusError(err, statusErr)
	}

	for _, rule := range errorRules {
		if mapped := rule(err); mapped != nil {
			return mapped
		}
	}

	// Default: return original error (Huma will use 500 Internal Server Error).
	// There is no message-based fallback here on purpose: an infrastructure
	// error mentioning "not found" (a missing GCE network, say) is a 500, not
	// a 404, and mapping it by substring hides the failure class.
	return err
}

// errorRule renders one domain error family, and returns nil for an error
// that is not its own.
type errorRule func(err error) error

// humaProblem is the shape of the huma.ErrorNNN constructors.
type humaProblem func(msg string, errs ...error) huma.StatusError

// errorRules is the status contract, one entry per domain error family.
// MapError walks it in order, so where two rules could both match an error the
// earlier one decides its status. Keep a family's rules adjacent.
var errorRules = []errorRule{
	// Auth errors: list-scoping rejections from auth.CreatorScope.
	sentinelRule(huma.Error400BadRequest, auth.ErrCreatorScopeConflict),
	unauthenticatedRule,

	// Device errors.
	sentinelRule(huma.Error404NotFound, device.ErrNotFound),
	typedRule[*device.ValidationError](huma.Error400BadRequest),
	sentinelRule(huma.Error409Conflict, device.ErrInvalidState, device.ErrAlreadyExists),
	typedRule[*device.SnapshotNotFoundError](huma.Error404NotFound),
	typedRule[*device.SnapshotNotRestorableError](huma.Error409Conflict),
	typedRule[*device.SnapshotLimitError](huma.Error409Conflict),

	// Host errors.
	sentinelRule(huma.Error404NotFound, host.ErrNotFound),
	sentinelRule(huma.Error409Conflict, host.ErrInvalidState),
	sentinelRule(huma.Error503ServiceUnavailable, host.ErrCapacityExceeded),
	sentinelRule(huma.Error409Conflict, host.ErrNotRetryable, host.ErrDegraded),
	catalogValidationRule,
	typedRule[*host.ReservedNameError](huma.Error400BadRequest),
	typedRule[*host.IDShapedNameError](huma.Error400BadRequest),
	typedRule[*host.BootDiskSizeError](huma.Error400BadRequest),
	typedRule[*host.UnavailablePoolError](huma.Error400BadRequest),

	// Batch errors.
	sentinelRule(huma.Error404NotFound, batch.ErrNotFound),
	sentinelRule(huma.Error409Conflict, batch.ErrFailed),
	sentinelRule(huma.Error400BadRequest, batch.ErrInvalidSpec),

	// Idempotency-Key reuse with a different request.
	sentinelRule(huma.Error422UnprocessableEntity, idempotency.ErrConflict),

	// Build errors.
	typedRule[*build.ValidationError](huma.Error400BadRequest),
	sentinelRule(huma.Error404NotFound, build.ErrNotFound),

	// Machine type errors. An instance that has not yet read what Compute
	// Engine offers is unavailable, not empty: a 404 there would tell the
	// caller their machine type is unsupported when what is true is that this
	// instance has not looked yet, and 503 is the one a client retries.
	sentinelRule(huma.Error404NotFound, machinetype.ErrNotFound),
	sentinelRule(huma.Error503ServiceUnavailable, machinetype.ErrCatalogUnavailable),
	sentinelRule(huma.Error404NotFound, machinetype.ErrFamilyNotFound),
	typedRule[*machinetype.GPUPolicyError](huma.Error400BadRequest),

	// GCE catalog errors.
	typedRule[*gcecatalog.ZonePolicyError](huma.Error400BadRequest),

	// Quota errors.
	typedRule[*quota.ExceededError](huma.Error429TooManyRequests),
	typedRule[*quota.NotFoundError](huma.Error404NotFound),

	// Storage errors (the feature layer translates GCS SDK errors to these).
	sentinelRule(huma.Error404NotFound, storagedomain.ErrNotFound),
	sentinelRule(huma.Error403Forbidden, storagedomain.ErrPermissionDenied),

	// Host image config errors.
	typedRule[*hostconfig.NotFoundError](huma.Error404NotFound),
	typedRule[*hostconfig.ValidationError](huma.Error400BadRequest),
	sentinelRule(huma.Error412PreconditionFailed, hostconfig.ErrSettingChanged),
	// A machine family with no bootable image: the request is well formed and
	// names nothing missing, but this deployment cannot serve it until an
	// operator sets the image the message names.
	typedRule[*hostconfig.ImageUnavailableError](huma.Error422UnprocessableEntity),

	// Operation errors. ErrInFlight is AIP-151's ABORTED: a resource refusing a
	// parallel operation must explain the situation, and admission has already
	// read the in-flight row, so its name rides in the message. That is what
	// turns the 409 from a rejection into a redirect the client can poll.
	sentinelRule(huma.Error404NotFound, operation.ErrNotFound),
	sentinelRule(huma.Error409Conflict, operation.ErrInFlight),
}

// sentinelRule matches any of a family's sentinels and serves the error's own
// text as the detail. Typed domain errors reach these arms too: their Is
// methods make every instance match the family sentinel.
func sentinelRule(problem humaProblem, targets ...error) errorRule {
	return func(err error) error {
		for _, target := range targets {
			if errors.Is(err, target) {
				return problem(err.Error())
			}
		}
		return nil
	}
}

// typedRule matches a domain error type that carries no sentinel.
func typedRule[T error](problem humaProblem) errorRule {
	return func(err error) error {
		if _, ok := errors.AsType[T](err); !ok {
			return nil
		}
		return problem(err.Error())
	}
}

// unauthenticatedRule serves the same fixed detail as the auth middleware:
// the sentinel text and anything wrapped around it are internal
// classifications that get logged, never put on the wire.
func unauthenticatedRule(err error) error {
	if !errors.Is(err, auth.ErrUnauthenticated) {
		return nil
	}
	return huma.Error401Unauthorized("Authentication required")
}

// problemFromStatusError renders an error that already carries an HTTP
// status. Huma serializes whatever StatusError errors.As finds as that
// value's own JSON. For foreign types that is a raw Go struct dump
// (observed live: a proxied HO 500 served {"StatusCode":500,...}) instead of
// RFC 9457. Huma's own error model passes through; anything else converts to
// a problem-details error with the same status. Upstream Host Orchestrator
// messages stay in the detail, since they are the upstream's errors, already
// on the wire today, and the generic-5xx policy protects this service's
// internals only.
func problemFromStatusError(err error, statusErr StatusError) error {
	if _, ok := errors.AsType[*huma.ErrorModel](err); ok {
		return err
	}
	if hostErr, ok := errors.AsType[*hoclient.HostError](err); ok {
		detail := hostErr.Message
		if hostErr.Details != "" {
			detail += "; " + hostErr.Details
		}
		return huma.NewError(hostErr.GetStatus(), detail)
	}
	return huma.NewError(statusErr.GetStatus(), err.Error())
}

// catalogValidationRule folds a CatalogValidationError into a 422
// problem-details response: one errors[] entry per validation failure
// (location = the offending field, suggestion appended to the message) plus
// one entry naming the alternative zones when the catalog knows any.
func catalogValidationRule(err error) error {
	cve, ok := errors.AsType[*host.CatalogValidationError](err)
	if !ok {
		return nil
	}

	details := make([]error, 0, len(cve.Errors)+1)
	for _, d := range cve.Errors {
		msg := d.Message
		if d.Suggestion != "" {
			msg += " (" + d.Suggestion + ")"
		}
		details = append(details, &huma.ErrorDetail{Location: d.Field, Message: msg})
	}
	if len(cve.AlternativeZones) > 0 {
		details = append(details, &huma.ErrorDetail{
			Location: "zone",
			Message:  "alternative zones with capacity: " + strings.Join(cve.AlternativeZones, ", "),
		})
	}
	return huma.Error422UnprocessableEntity(cve.Error(), details...)
}

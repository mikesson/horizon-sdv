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

package gce

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"github.com/googleapis/gax-go/v2/apierror"
	"google.golang.org/api/googleapi"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
)

// waitReissuePause spaces re-issued wait RPCs apart. zoneOperations.wait is
// best-effort: it normally holds server-side (up to ~2 minutes) until the
// operation is DONE, but is documented to sometimes return earlier (even
// immediately) with the operation still running, so back-to-back re-issues
// need a floor to not hot-spin against an overloaded server.
const waitReissuePause = 2 * time.Second

// WaitZoneOperation blocks until the zone operation completes, using the
// server-side blocking zoneOperations.wait RPC rather than get-polling: the
// server answers the moment the operation reaches DONE, where the SDK's
// Operation.Wait polls on an exponential backoff (1s initial, 1m cap) that
// notices completion up to tens of seconds late. Only status DONE ends the
// wait, because the RPC may return early with the operation still running.
// A DONE operation's stored failure is returned in the same flattened
// googleapi.Error shape the SDK produces, which is what ClassifyError's
// message rules (stockout, quota) parse.
func WaitZoneOperation(ctx context.Context, ops *compute.ZoneOperationsClient, project, zone, name string) error {
	op, err := AwaitZoneOperation(ctx, ops, project, zone, name)
	if err != nil {
		return err
	}
	return OperationFailure(op)
}

// AwaitZoneOperation is WaitZoneOperation for a caller that must tell a failed
// operation from a failed wait: it returns the DONE operation, and an error
// only when the wait itself could not complete. OperationFailure reads the
// operation's own outcome.
func AwaitZoneOperation(ctx context.Context, ops *compute.ZoneOperationsClient, project, zone, name string) (*computepb.Operation, error) {
	for {
		op, err := ops.Wait(ctx, &computepb.WaitZoneOperationRequest{
			Project:   project,
			Zone:      zone,
			Operation: name,
		})
		if err != nil {
			return nil, fmt.Errorf("waiting for operation %s: %w", name, err)
		}
		if op.GetStatus() == computepb.Operation_DONE {
			return op, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for operation %s: %w", name, ctx.Err())
		case <-time.After(waitReissuePause):
		}
	}
}

// OperationError carries the structured detail a failed operation reports
// alongside the flattened error every message rule already parses. Rendering
// the error proto into a string is lossy in one way that matters: a stockout
// names the zones that DO have capacity in
// error_info.metadatas["zonesAvailable"], and that is an observed answer where
// zone ranking can only offer a modeled one.
type OperationError struct {
	Err error

	// Reason is error_info.reason, e.g. "resource_availability". Finer than
	// the operation-level code: it says which resource ran out.
	Reason string

	// ZonesAvailable are the zones GCE reports as currently able to take the
	// request. Empty when GCE named none, which is itself an answer.
	ZonesAvailable []string

	// QuotaAxis names the exhausted quota pool in the catalog's own identity,
	// empty when the failure carried no quota detail. It is the only place the
	// pool is stated exactly: the message names the generic metric and drops
	// the dimension, so a C4 CPU refusal reads "Quota CPUS_PER_VM_FAMILY
	// exceeded" with no C4 anywhere in the text.
	QuotaAxis string
}

func (e *OperationError) Error() string { return e.Err.Error() }

func (e *OperationError) Unwrap() error { return e.Err }

// OperationFailure converts a DONE operation's stored error into the same
// error the SDK's Operation.Wait returns: a googleapi.Error carrying the
// operation's HTTP status with the error proto flattened into the message,
// upgraded to an apierror. It is wrapped so the structured detail survives
// alongside it. A DONE operation without an HTTP error status succeeded.
func OperationFailure(op *computepb.Operation) error {
	if op.HttpErrorStatusCode == nil {
		return nil
	}
	code := int(op.GetHttpErrorStatusCode())
	if code >= 200 && code <= 299 {
		return nil
	}
	aErr := &googleapi.Error{
		Code:    code,
		Message: fmt.Sprintf("%s: %v", op.GetHttpErrorMessage(), op.GetError()),
	}
	var failure error = aErr
	if apiErr, ok := apierror.FromError(aErr); ok {
		failure = apiErr
	}

	detail := operationErrorDetail(op.GetError())
	if detail.reason == "" && len(detail.zonesAvailable) == 0 && detail.quotaAxis == "" {
		return failure
	}
	return &OperationError{
		Err:            failure,
		Reason:         detail.reason,
		ZonesAvailable: detail.zonesAvailable,
		QuotaAxis:      detail.quotaAxis,
	}
}

// errorDetail is what a failed operation's structured detail says, beyond the
// message the error proto flattens into.
type errorDetail struct {
	reason         string
	zonesAvailable []string
	quotaAxis      string
}

// operationErrorDetail reads the structured detail GCE attaches to a failed
// operation: the error_info naming the class of failure and the zones that do
// have capacity, and the quota_info naming the pool a refusal exhausted.
//
// zonesAvailable arrives as a comma-separated zone list, and an empty value
// means no zone in the region can currently take the request.
func operationErrorDetail(opErr *computepb.Error) errorDetail {
	var out errorDetail
	for _, item := range opErr.GetErrors() {
		for _, detail := range item.GetErrorDetails() {
			if quota := detail.GetQuotaInfo(); quota != nil && out.quotaAxis == "" {
				out.quotaAxis = gcecatalog.QuotaTypeForMetricName(
					quota.GetMetricName(), quota.GetDimensions())
			}
			info := detail.GetErrorInfo()
			if info == nil {
				continue
			}
			if out.reason == "" {
				out.reason = info.GetReason()
			}
			for zone := range strings.SplitSeq(info.GetMetadatas()["zonesAvailable"], ",") {
				if zone = strings.TrimSpace(zone); zone != "" && !slices.Contains(out.zonesAvailable, zone) {
					out.zonesAvailable = append(out.zonesAvailable, zone)
				}
			}
		}
	}
	return out
}

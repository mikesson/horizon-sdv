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
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
)

// defaultRateLimitDelay backs off a rate-limited call when the server sends
// no Retry-After header.
const defaultRateLimitDelay = time.Minute

// SuggestedRetryDelay extracts a server-guided retry delay from a GCE error.
// A 429 must not come back on the generic backoff curve, so it returns the
// Retry-After header's delay when present, else a conservative default.
// Non-rate-limit errors report false, meaning generic backoff applies.
func SuggestedRetryDelay(err error) (time.Duration, bool) {
	apiErr, ok := errors.AsType[*googleapi.Error](err)
	if !ok || apiErr.Code != http.StatusTooManyRequests {
		return 0, false
	}
	if v := apiErr.Header.Get("Retry-After"); v != "" {
		if secs, aerr := strconv.Atoi(v); aerr == nil && secs > 0 {
			return time.Duration(secs) * time.Second, true
		}
		if at, terr := http.ParseTime(v); terr == nil {
			if d := time.Until(at); d > 0 {
				return d, true
			}
		}
	}
	return defaultRateLimitDelay, true
}

// ClassifyError parses a GCE API error and returns a classified error.
// Returns nil if err is nil.
func ClassifyError(err error) *ClassifiedError {
	if err == nil {
		return nil
	}

	classified := classifyByShape(err)

	// A failed operation names the zones that currently have capacity, and a
	// refused one names the quota pool it exhausted. Take both over anything
	// derived from the message, because they are what the platform stated
	// while a substring search only reconstructs a guess. The dimensioned
	// pools cannot be reconstructed at all, because their message names the
	// generic metric and drops the dimension.
	if opErr, ok := errors.AsType[*OperationError](err); ok && classified != nil {
		classified.AlternativeZones = opErr.ZonesAvailable
		if quotaType := quotaErrorType(opErr.QuotaAxis); quotaType != "" {
			classified.QuotaAxis = opErr.QuotaAxis
			classified.Type = quotaType
		}
	}
	return classified
}

// quotaErrorType classifies a named quota pool, or "" for a pool this
// deployment has no distinct reaction for (which leaves the message rules'
// answer standing).
func quotaErrorType(quotaAxis string) ErrorType {
	switch {
	case quotaAxis == "":
		return ""
	case gcecatalog.IsCPUQuotaType(quotaAxis):
		return ErrorTypeQuotaCPU
	case gcecatalog.IsGPUQuotaType(quotaAxis):
		return ErrorTypeQuotaGPU
	}
	switch quotaAxis {
	case gcecatalog.QuotaTypeSSDTotalGB:
		return ErrorTypeQuotaSSD
	case gcecatalog.QuotaTypeHDBTotalGB:
		return ErrorTypeQuotaHyperdisk
	case gcecatalog.QuotaTypeInstances:
		return ErrorTypeQuotaInstance
	case gcecatalog.QuotaTypeInUseAddresses:
		return ErrorTypeQuotaIP
	default:
		return ""
	}
}

// classifyByShape dispatches on how the error arrived.
func classifyByShape(err error) *ClassifiedError {
	// An organization policy refusal names its constraint wherever the reason
	// code lands (conditionNotMet, a 403, a flattened operation error), and the
	// constraint is the whole diagnosis, so it is matched before anything the
	// surrounding words would otherwise claim.
	if c := matchRules(strings.ToLower(err.Error()), orgPolicyRules); c != nil {
		return c
	}

	// Try googleapi.Error first (REST API errors).
	if apiErr, ok := errors.AsType[*googleapi.Error](err); ok {
		return classifyGoogleAPIError(apiErr)
	}

	// Try gRPC status error (gRPC API errors).
	if s, ok := status.FromError(err); ok {
		return classifyGRPCError(s)
	}

	// Fall back to string matching.
	return classifyByMessage(err.Error())
}

// classifyGoogleAPIError classifies a Google API error (REST).
func classifyGoogleAPIError(err *googleapi.Error) *ClassifiedError {
	// Check structured errors first (most reliable).
	for _, e := range err.Errors {
		if classified := classifyByReason(e.Reason, e.Message); classified != nil {
			classified.Code = e.Reason
			return classified
		}
	}

	// Operation-level failures arrive with EMPTY structured errors: the
	// operation wait (WaitZoneOperation, matching the SDK's own mapping)
	// flattens the operation's error proto into the message
	// (googleapi.Error{Code: httpErrorStatusCode, Message: "...:
	// errors:{code:"ZONE_RESOURCE_POOL_EXHAUSTED" ...}}), so a stockout or
	// quota failure surfaced by the wait carries its reason only there. Match
	// the message before degrading to the bare status code. Otherwise every
	// operation-level capacity failure reads as a generic retryable 503 and
	// the reactions keyed on the exact classes (zone failover, alternative
	// zones) never fire. A 429 is exempt: its message describes throttling
	// ("Quota exceeded ... per minute"), not an operation failure, and the
	// quota wording must not classify it as a regional quota signal.
	if len(err.Errors) == 0 && err.Code != http.StatusTooManyRequests {
		msg := strings.ToLower(err.Message)
		// The stockout code check runs before the generic rules: the flattened
		// message also carries "SERVICE UNAVAILABLE", which the zone-down rule
		// would otherwise claim.
		if containsAny(msg, []string{"resource_pool_exhausted", reasonStockout}) {
			return classifyByReason(reasonStockout, err.Message)
		}
		if isQuotaExceededMessage(msg) {
			return classifyQuotaError(err.Message)
		}
		if c := matchRules(msg, messageRules); c != nil {
			return c
		}
	}

	// Fall back to HTTP status code.
	return classifyByHTTPStatus(err.Code, err.Message)
}

// classifyGRPCError classifies a gRPC status error.
func classifyGRPCError(s *status.Status) *ClassifiedError {
	switch s.Code() {
	case codes.NotFound:
		return classifyByMessage(s.Message())
	case codes.PermissionDenied:
		return &ClassifiedError{
			Type:        ErrorTypePermissionDenied,
			Code:        "PERMISSION_DENIED",
			Message:     msgPermissionDenied,
			Suggestion:  "Check service account permissions for Compute Engine",
			IsRetryable: false,
		}
	case codes.ResourceExhausted:
		// gRPC uses RESOURCE_EXHAUSTED for throttling AND quota; only the
		// message tells them apart, and throttling must never classify as a
		// quota (capacity) signal.
		if containsAny(strings.ToLower(s.Message()), []string{"rate limit", "too many requests", "per minute"}) {
			return &ClassifiedError{
				Type:        ErrorTypeRateLimit,
				Code:        "RESOURCE_EXHAUSTED",
				Message:     "Rate limit exceeded",
				Suggestion:  suggestRetry,
				IsRetryable: true,
			}
		}
		return classifyQuotaError(s.Message())
	case codes.FailedPrecondition:
		return classifyByMessage(s.Message())
	case codes.Unavailable:
		return &ClassifiedError{
			Type:        ErrorTypeResourceNotReady,
			Code:        "UNAVAILABLE",
			Message:     "Service temporarily unavailable",
			Suggestion:  suggestRetry,
			IsRetryable: true,
		}
	case codes.DeadlineExceeded:
		return &ClassifiedError{
			Type:        ErrorTypeResourceNotReady,
			Code:        "DEADLINE_EXCEEDED",
			Message:     "Request timed out",
			Suggestion:  suggestRetry,
			IsRetryable: true,
		}
	default:
		return &ClassifiedError{
			Type:        ErrorTypeUnknown,
			Code:        s.Code().String(),
			Message:     s.Message(),
			IsRetryable: false,
		}
	}
}

// classifyByReason classifies an error by its GCE reason code.
func classifyByReason(reason, message string) *ClassifiedError {
	switch reason {
	case "resourceNotReady":
		// Resource-scoped transient: a disk, subnet or instance still
		// settling. This is not a zone signal. ZoneDown is
		// failover-actionable, so classifying it that way would burn the
		// zone's availability cell and could move the host over a single
		// resource that is not ready yet.
		return &ClassifiedError{
			Type:        ErrorTypeResourceNotReady,
			Message:     "Resource is not ready",
			Suggestion:  suggestRetry,
			IsRetryable: true,
		}

	case "quotaExceeded":
		return classifyQuotaError(message)

	case "resourceNotFound":
		return classifyNotFoundError(message)

	case "notFound":
		return classifyNotFoundError(message)

	case "zoneResourcePoolExhausted", reasonStockout:
		return &ClassifiedError{
			Type:        ErrorTypeResourceExhausted,
			Message:     "Zone capacity exhausted for requested resources",
			Suggestion:  "Try a different zone or machine type",
			IsRetryable: false,
		}

	case "unsupportedOperation":
		return &ClassifiedError{
			Type:        ErrorTypeGPUNotAttachable,
			Message:     "GPU cannot be attached to this machine type",
			Suggestion:  "Use a compatible machine type (N1) for GPU attachment",
			IsRetryable: false,
		}

	case "conditionNotMet":
		return classifyByMessage(message)

	case "invalidParameter", "invalid":
		return classifyByMessage(message)

	case "accessDenied", "forbidden":
		return &ClassifiedError{
			Type:        ErrorTypePermissionDenied,
			Message:     "Access denied",
			Suggestion:  "Check service account permissions",
			IsRetryable: false,
		}
	}

	return nil
}

// classifyByHTTPStatus classifies an error by HTTP status code.
func classifyByHTTPStatus(code int, message string) *ClassifiedError {
	switch code {
	case http.StatusForbidden:
		return &ClassifiedError{
			Type:        ErrorTypePermissionDenied,
			Code:        "403",
			Message:     msgPermissionDenied,
			Suggestion:  "Check service account permissions for Compute Engine",
			IsRetryable: false,
		}

	case http.StatusNotFound:
		return classifyNotFoundError(message)

	case http.StatusTooManyRequests:
		return &ClassifiedError{
			Type:        ErrorTypeRateLimit,
			Code:        "429",
			Message:     "Rate limit exceeded",
			Suggestion:  suggestRetry,
			IsRetryable: true,
		}

	case http.StatusConflict:
		return &ClassifiedError{
			Type:        ErrorTypeResourceNotReady,
			Code:        "409",
			Message:     "Resource conflict",
			Suggestion:  "Wait for the current operation to complete",
			IsRetryable: true,
		}

	case http.StatusServiceUnavailable:
		return &ClassifiedError{
			Type:        ErrorTypeResourceNotReady,
			Code:        "503",
			Message:     "Service temporarily unavailable",
			Suggestion:  suggestRetry,
			IsRetryable: true,
		}
	}

	return &ClassifiedError{
		Type:        ErrorTypeUnknown,
		Code:        "",
		Message:     message,
		IsRetryable: false,
	}
}

// Suggestion strings reused across multiple classifications.
const (
	suggestRetry         = "Wait and retry the operation"
	suggestQuotaIncrease = "Request a quota increase or try a different region"
)

// msgPermissionDenied is the classification message for a refused call,
// whichever surface reported it (gRPC status, HTTP 403, or message match).
const msgPermissionDenied = "Permission denied"

// gpuTokens are the words GCE uses for an attached accelerator in error text.
var gpuTokens = []string{"accelerator", "gpu"}

// reasonStockout is GCE's short stockout reason code (the long form is
// zoneResourcePoolExhausted).
const reasonStockout = "stockout"

// messageRule classifies an error message. It matches when every group in allOf
// has at least one of its substrings present in the lowercased message. That
// is OR within a group, AND across groups.
type messageRule struct {
	allOf  [][]string
	result ClassifiedError
}

// isQuotaExceededMessage reports whether msg (lowercased) claims an actually
// exceeded quota. The bare word "quota" is not enough: the quota classes are
// failover-actionable, so a message that merely mentions quota machinery
// ("could not verify quota", "quota project") would record a capacity failure
// against the (zone, family) cell and can move the host. Every real GCE quota
// failure carries exceedance wording (QUOTA_EXCEEDED, "Quota 'CPUS' exceeded",
// "would be exceeded").
func isQuotaExceededMessage(msg string) bool {
	return strings.Contains(msg, "quota") && strings.Contains(msg, "exceed")
}

func containsAny(msg string, subs []string) bool {
	for _, s := range subs {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// matchRules returns a copy of the first matching rule's result, or nil if none
// match. Rules are evaluated in order, so more specific rules must come first.
func matchRules(msg string, rules []messageRule) *ClassifiedError {
	for i := range rules {
		matched := true
		for _, group := range rules[i].allOf {
			if !containsAny(msg, group) {
				matched = false
				break
			}
		}
		if matched {
			c := rules[i].result
			return &c
		}
	}
	return nil
}

// classifyQuotaError classifies a quota-related error.
func classifyQuotaError(message string) *ClassifiedError {
	if c := matchRules(strings.ToLower(message), quotaRules); c != nil {
		return c
	}
	return &ClassifiedError{
		Type:        ErrorTypeResourceExhausted,
		Message:     "Quota exceeded",
		Suggestion:  suggestQuotaIncrease,
		IsRetryable: false,
	}
}

var quotaRules = []messageRule{
	{allOf: [][]string{{"cpus", "cpu"}}, result: ClassifiedError{
		Type: ErrorTypeQuotaCPU, Message: "CPU quota exceeded in this region",
		Suggestion: suggestQuotaIncrease,
	}},
	{allOf: [][]string{{"gpu", "nvidia", "accelerator"}}, result: ClassifiedError{
		Type: ErrorTypeQuotaGPU, Message: "GPU quota exceeded in this region",
		Suggestion: suggestQuotaIncrease,
	}},
	{allOf: [][]string{{"in_use_addresses", "ip"}}, result: ClassifiedError{
		Type: ErrorTypeQuotaIP, Message: "IP address quota exceeded in this region",
		Suggestion: "Request a quota increase or release unused IP addresses",
	}},
	// Hyperdisk precedes the SSD/disk rule and cannot be folded into it. The
	// hyperdisk pool is raised by its own quota request, and its own name
	// contains "disk", so the rule below would otherwise claim every hyperdisk
	// refusal for the SSD pool. A refusal naming HDB_TOTAL_GB, which contains
	// neither word, fell past every quota rule to a generic exhaustion.
	{allOf: [][]string{{"hdb", "hyperdisk"}}, result: ClassifiedError{
		Type: ErrorTypeQuotaHyperdisk, Message: "Hyperdisk quota exceeded in this region",
		Suggestion: "Request an HDB-TOTAL-GB quota increase or reduce disk size",
	}},
	{allOf: [][]string{{"ssd", "disk"}}, result: ClassifiedError{
		Type: ErrorTypeQuotaSSD, Message: "Disk quota exceeded in this region",
		Suggestion: "Request a quota increase or reduce disk size",
	}},
	{allOf: [][]string{{"instance"}}, result: ClassifiedError{
		Type: ErrorTypeQuotaInstance, Message: "Instance quota exceeded in this region",
		Suggestion: "Request a quota increase or delete unused instances",
	}},
}

// orgPolicyRules match the organization policy constraints that stop a host
// VM from being created at all. Neither is retryable, and neither is about the
// zone, so they classify as permission refusals and never feed failover.
var orgPolicyRules = []messageRule{
	{allOf: [][]string{{"constraints/compute.trustedimageprojects"}}, result: ClassifiedError{
		Type: ErrorTypePermissionDenied, Code: "constraints/compute.trustedImageProjects",
		Message:    "Organization policy constraints/compute.trustedImageProjects does not trust the host image's project",
		Suggestion: "Add the project named by CCO_HOST_IMAGE_PROJECT (or the machine family's source_project) to the policy's trusted image projects",
	}},
	{allOf: [][]string{{"constraints/compute.disablenestedvirtualization"}}, result: ClassifiedError{
		Type: ErrorTypePermissionDenied, Code: "constraints/compute.disableNestedVirtualization",
		Message:    "Organization policy constraints/compute.disableNestedVirtualization forbids nested virtualization, which every Cuttlefish host needs",
		Suggestion: "Turn the constraint off for this project; hosts cannot run devices without nested virtualization",
	}},
}

// classifyNotFoundError classifies a resource-not-found error.
func classifyNotFoundError(message string) *ClassifiedError {
	if c := matchRules(strings.ToLower(message), notFoundRules); c != nil {
		return c
	}
	return &ClassifiedError{
		Type:        ErrorTypeUnknown,
		Message:     "Resource not found",
		Suggestion:  "Verify the resource exists and you have access",
		IsRetryable: false,
	}
}

// notFoundRules: "network" precedes "subnet" because "subnetwork" contains
// "network". That preserves the original first-match ordering.
var notFoundRules = []messageRule{
	{allOf: [][]string{{"zone"}}, result: ClassifiedError{
		Type: ErrorTypeZoneNotFound, Message: "Zone not found",
		Suggestion: "Select a valid zone from the available options",
	}},
	{allOf: [][]string{{"machine", "machinetype"}}, result: ClassifiedError{
		Type: ErrorTypeMachineTypeNotInZone, Message: "Machine type not available in this zone",
		Suggestion: "Select a different machine type or zone",
	}},
	{allOf: [][]string{{"image"}}, result: ClassifiedError{
		Type: ErrorTypeImageNotFound, Message: "Image not found",
		Suggestion: "Select a valid image from the available options",
	}},
	{allOf: [][]string{{"network"}}, result: ClassifiedError{
		Type: ErrorTypeNetworkNotFound, Message: "Network not found",
		Suggestion: "Check network configuration in GCP console",
	}},
	{allOf: [][]string{{"subnet", "subnetwork"}}, result: ClassifiedError{
		Type: ErrorTypeSubnetNotFound, Message: "Subnet not found",
		Suggestion: "Check subnet configuration in GCP console",
	}},
	{allOf: [][]string{gpuTokens}, result: ClassifiedError{
		Type: ErrorTypeGPUNotInZone, Message: "GPU type not available in this zone",
		Suggestion: "Select a different GPU type or zone",
	}},
}

// classifyByMessage classifies an error by its message text.
func classifyByMessage(message string) *ClassifiedError {
	msg := strings.ToLower(message)

	// Timeouts and cancellations first: a wrapped "context deadline exceeded"
	// must not reach the quota path just because it contains "exceeded".
	if containsAny(msg, []string{"deadline exceeded", "context canceled"}) {
		return &ClassifiedError{
			Type:        ErrorTypeResourceNotReady,
			Message:     "Request timed out or was canceled",
			Suggestion:  suggestRetry,
			IsRetryable: true,
		}
	}
	if isQuotaExceededMessage(msg) {
		return classifyQuotaError(message)
	}
	if containsAny(msg, []string{"not found", "notfound"}) {
		return classifyNotFoundError(message)
	}
	if c := matchRules(msg, messageRules); c != nil {
		return c
	}

	// Default: unknown error.
	return &ClassifiedError{
		Type:        ErrorTypeUnknown,
		Message:     message,
		IsRetryable: false,
	}
}

// messageRules: the two GPU rules require a second qualifier, so a GPU message
// with neither falls through to the permission/exhausted rules (and finally the
// caller's default), matching the original nested control flow.
var messageRules = []messageRule{
	{allOf: [][]string{{"zone"}, {"unavailable"}}, result: ClassifiedError{
		Type: ErrorTypeZoneDown, Message: "Zone is temporarily unavailable",
		Suggestion: "Try a different zone or wait and retry", IsRetryable: true,
	}},
	{allOf: [][]string{gpuTokens, {"not supported", "unsupported"}}, result: ClassifiedError{
		Type: ErrorTypeGPUNotAttachable, Message: "GPU not supported for this configuration",
		Suggestion: "Use a compatible machine type for GPU attachment",
	}},
	{allOf: [][]string{gpuTokens, {"count", "maximum"}}, result: ClassifiedError{
		Type: ErrorTypeGPUCountExceeded, Message: "GPU count exceeds maximum for this machine type",
		Suggestion: "Reduce the GPU count or use a different machine type",
	}},
	{allOf: [][]string{{"permission", "denied", "forbidden"}}, result: ClassifiedError{
		Type: ErrorTypePermissionDenied, Message: msgPermissionDenied,
		Suggestion: "Check service account permissions",
	}},
	{allOf: [][]string{{"exhausted", reasonStockout}}, result: ClassifiedError{
		Type: ErrorTypeResourceExhausted, Message: "Resources exhausted in this zone",
		Suggestion: "Try a different zone",
	}},
}

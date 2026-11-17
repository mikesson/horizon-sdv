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
	"fmt"
	"net/http"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClassifyByMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		msg  string
		want ErrorType
	}{
		{"cpu quota", "Quota 'CPUS' exceeded", ErrorTypeQuotaCPU},
		{"gpu quota", "quota exceeded: nvidia accelerator", ErrorTypeQuotaGPU},
		{"generic quota", "quota exceeded for something", ErrorTypeResourceExhausted},
		// The word "quota" alone is not a capacity signal: quota classes are
		// failover-actionable, and a message that merely mentions quota
		// machinery must not burn zone_health or move the host.
		{"quota mention without exceedance", "error checking quota for project", ErrorTypeUnknown},
		// "exceeded" alone must not classify as a quota error.
		{"deadline exceeded is not quota", "context deadline exceeded", ErrorTypeResourceNotReady},
		{"wrapped deadline exceeded", `Get "https://compute.googleapis.com": context deadline exceeded`, ErrorTypeResourceNotReady},
		{"context canceled", "operation aborted: context canceled", ErrorTypeResourceNotReady},
		{"zone not found", "resource not found: zone bad-zone", ErrorTypeZoneNotFound},
		{"zone unavailable needs both words", "zone is unavailable", ErrorTypeZoneDown},
		{"gpu not supported", "accelerator not supported here", ErrorTypeGPUNotAttachable},
		{"gpu count", "gpu count exceeds maximum", ErrorTypeGPUCountExceeded},
		// GPU with no qualifier must fall through, not match a GPU rule.
		{"gpu falls through to permission", "gpu request denied", ErrorTypePermissionDenied},
		{"gpu falls through to default", "gpu weirdness", ErrorTypeUnknown},
		{"permission", "permission denied", ErrorTypePermissionDenied},
		{"exhausted", "zone stockout", ErrorTypeResourceExhausted},
		{"unknown", "totally unexpected failure", ErrorTypeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyByMessage(tt.msg).Type; got != tt.want {
				t.Errorf("classifyByMessage(%q).Type = %q, want %q", tt.msg, got, tt.want)
			}
		})
	}
}

func TestClassifyByMessage_TimeoutRetryable(t *testing.T) {
	t.Parallel()
	for _, msg := range []string{"context deadline exceeded", "rpc error: context canceled"} {
		c := classifyByMessage(msg)
		if !c.IsRetryable {
			t.Errorf("classifyByMessage(%q).IsRetryable = false, want true", msg)
		}
		if c.Type != ErrorTypeResourceNotReady {
			t.Errorf("classifyByMessage(%q).Type = %q, want %q", msg, c.Type, ErrorTypeResourceNotReady)
		}
	}
}

func TestClassifyNotFoundError_Ordering(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		msg  string
		want ErrorType
	}{
		// "subnetwork" contains "network", so the network rule wins first.
		// That preserves the original first-match ordering.
		{"subnetwork matches network first", "subnetwork foo not found", ErrorTypeNetworkNotFound},
		{"subnet matches subnet", "subnet foo missing", ErrorTypeSubnetNotFound},
		{"machine type", "machinetype not available", ErrorTypeMachineTypeNotInZone},
		{"image", "image deleted", ErrorTypeImageNotFound},
		{"generic", "widget gone", ErrorTypeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyNotFoundError(tt.msg).Type; got != tt.want {
				t.Errorf("classifyNotFoundError(%q).Type = %q, want %q", tt.msg, got, tt.want)
			}
		})
	}
}

// SuggestedRetryDelay turns 429s, and only 429s, into a server-guided
// retry floor, honoring Retry-After when present.
func TestSuggestedRetryDelay(t *testing.T) {
	t.Parallel()

	d, ok := SuggestedRetryDelay(&googleapi.Error{Code: 429})
	if !ok || d != time.Minute {
		t.Errorf("bare 429 = (%v, %v), want (1m, true)", d, ok)
	}

	hdr := http.Header{}
	hdr.Set("Retry-After", "30")
	d, ok = SuggestedRetryDelay(fmt.Errorf("wrapped: %w", &googleapi.Error{Code: 429, Header: hdr}))
	if !ok || d != 30*time.Second {
		t.Errorf("429 with Retry-After: 30 = (%v, %v), want (30s, true)", d, ok)
	}

	if _, ok := SuggestedRetryDelay(&googleapi.Error{Code: 503}); ok {
		t.Error("503 must not produce a hint; generic backoff applies")
	}
	if _, ok := SuggestedRetryDelay(errors.New("plain")); ok {
		t.Error("non-API error must not produce a hint")
	}
}

// gRPC uses RESOURCE_EXHAUSTED for both throttling and resource quota; the
// message is the only distinguisher, and throttling must never classify as a
// quota (capacity) signal.
func TestClassifyGRPCResourceExhausted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		msg  string
		want ErrorType
	}{
		{"rate limit", "Quota exceeded for quota metric 'Queries' and limit 'Queries per minute'", ErrorTypeRateLimit},
		{"resource quota", "Quota 'CPUS' exceeded. Limit: 24.0 in region europe-west4.", ErrorTypeQuotaCPU},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := status.New(codes.ResourceExhausted, tt.msg).Err()
			if got := ClassifyError(err).Type; got != tt.want {
				t.Errorf("ClassifyError(%q).Type = %q, want %q", tt.msg, got, tt.want)
			}
		})
	}
}

// Operation-level failures reach the classifier as googleapi errors with
// EMPTY structured Errors, because the compute client flattens the operation's
// error proto into the message, so the reason must be read from the message
// before the bare status code decides. Without this, every op-level stockout or
// quota failure classified as a generic retryable 503 and the reactions
// keyed on the exact classes (zone failover, alternative zones) never
// fired.
func TestClassifyOperationErrorMessages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  *googleapi.Error
		want ErrorType
	}{
		{
			"op-level stockout",
			&googleapi.Error{Code: 503, Message: `SERVICE UNAVAILABLE: errors:{code:"ZONE_RESOURCE_POOL_EXHAUSTED" message:"The zone does not have enough resources available to fulfill the request."}`},
			ErrorTypeResourceExhausted,
		},
		{
			"op-level cpu quota",
			&googleapi.Error{Code: 403, Message: `QUOTA EXCEEDED: errors:{code:"QUOTA_EXCEEDED" message:"Quota 'CPUS' exceeded. Limit: 24.0 in region europe-west4."}`},
			ErrorTypeQuotaCPU,
		},
		{
			"plain 503 stays a transient",
			&googleapi.Error{Code: 503, Message: "backend error"},
			ErrorTypeResourceNotReady,
		},
		{
			"structured reason still wins",
			&googleapi.Error{Code: 503, Errors: []googleapi.ErrorItem{{Reason: "zoneResourcePoolExhausted"}}},
			ErrorTypeResourceExhausted,
		},
		// A 429's message describes throttling in quota vocabulary ("Quota
		// exceeded ... per minute"); it must classify as a rate limit, never
		// as a regional quota signal. A quota classification here would let
		// throttling poison zone_health and trigger failover.
		{
			"quota-phrased rate limit stays a rate limit",
			&googleapi.Error{Code: 429, Message: "Quota exceeded for quota metric 'Queries' and limit 'Queries per minute' of service 'compute.googleapis.com'"},
			ErrorTypeRateLimit,
		},
		{
			"bare 429 is a rate limit",
			&googleapi.Error{Code: 429},
			ErrorTypeRateLimit,
		},
		// Same guard at the operation level: quota wording without an actual
		// exceedance must fall through to the transient 503 path, not record
		// a regional quota failure against the (zone, family) cell.
		{
			"op-level quota mention without exceedance stays a transient",
			&googleapi.Error{Code: 503, Message: "Backend error: could not verify quota for project"},
			ErrorTypeResourceNotReady,
		},
		// resourceNotReady is a resource-scoped transient (a disk, subnet or
		// instance still settling), not a zone outage: ZoneDown burns the
		// zone's availability cell and can move the host.
		{
			"resourceNotReady is not a zone signal",
			&googleapi.Error{Code: 400, Errors: []googleapi.ErrorItem{{
				Reason:  "resourceNotReady",
				Message: "The resource 'projects/p/zones/z/disks/d' is not ready",
			}}},
			ErrorTypeResourceNotReady,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ClassifyError(tt.err).Type; got != tt.want {
				t.Errorf("ClassifyError(%v).Type = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// An organization policy refusal is classified by the constraint it names,
// however the rest of the error reads. The wording around the constraint
// here is illustrative; the constraint identifier is what the rule matches.
func TestClassifyOrgPolicyConstraints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code string
	}{
		{
			name: "trusted image projects as a 412 conditionNotMet",
			err: &googleapi.Error{
				Code:    http.StatusPreconditionFailed,
				Message: "Constraint constraints/compute.trustedImageProjects violated for project p.",
				Errors: []googleapi.ErrorItem{{
					Reason:  "conditionNotMet",
					Message: "Constraint constraints/compute.trustedImageProjects violated for project p.",
				}},
			},
			code: "constraints/compute.trustedImageProjects",
		},
		{
			name: "nested virtualization as a 403 with an image word in it",
			err: &googleapi.Error{
				Code:    http.StatusForbidden,
				Message: "Constraint constraints/compute.disableNestedVirtualization violated; image not found is unrelated",
				Errors:  []googleapi.ErrorItem{{Reason: "forbidden"}},
			},
			code: "constraints/compute.disableNestedVirtualization",
		},
		{
			name: "flattened into an operation error",
			err:  errors.New(`instance creation failed: errors:{code:"CONDITION_NOT_MET" message:"Constraint constraints/compute.trustedImageProjects violated"}`),
			code: "constraints/compute.trustedImageProjects",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ClassifyError(tc.err)
			if got.Code != tc.code || got.Type != ErrorTypePermissionDenied || got.IsRetryable {
				t.Errorf("ClassifyError = %+v, want non-retryable permission_denied coded %s", got, tc.code)
			}
		})
	}
}

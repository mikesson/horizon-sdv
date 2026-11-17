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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
)

// fakeOpsClient serves the zoneOperations surface: every POST .../wait
// answers with the next response in sequence (the last repeats), counting
// calls. Non-wait routes fail the test, because WaitZoneOperation must never
// fall back to get-polling.
func fakeOpsClient(t *testing.T, calls *atomic.Int64, responses ...string) *compute.ZoneOperationsClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/wait") {
			t.Errorf("unexpected request %s %s; only the wait RPC is allowed", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		n := calls.Add(1)
		resp := responses[min(int(n)-1, len(responses)-1)]
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, resp)
	}))
	t.Cleanup(srv.Close)
	client, err := compute.NewZoneOperationsRESTClient(t.Context(),
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("creating fake operations client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestWaitZoneOperationDone(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	client := fakeOpsClient(t, &calls, `{"name":"op-1","status":"DONE"}`)

	if err := WaitZoneOperation(t.Context(), client, "p", "z", "op-1"); err != nil {
		t.Fatalf("WaitZoneOperation = %v, want nil", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("wait calls = %d, want 1 (a DONE answer must end the wait)", got)
	}
}

// The wait RPC is best-effort and may return with the operation still
// running; only DONE ends the wait, so an early return must re-issue.
func TestWaitZoneOperationReissuesUntilDone(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	client := fakeOpsClient(t, &calls,
		`{"name":"op-1","status":"RUNNING"}`,
		`{"name":"op-1","status":"DONE"}`)

	if err := WaitZoneOperation(t.Context(), client, "p", "z", "op-1"); err != nil {
		t.Fatalf("WaitZoneOperation = %v, want nil", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("wait calls = %d, want 2 (a non-DONE answer must re-issue)", got)
	}
}

// A DONE operation's stored failure must come back in the SDK's flattened
// googleapi.Error shape so the classifier's message rules keep firing: a
// stockout surfaced by the operation wait must classify as
// ErrorTypeResourceExhausted, not degrade to a generic retryable 503 (which
// would suppress zone failover).
func TestWaitZoneOperationDoneWithErrorClassifies(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	client := fakeOpsClient(t, &calls, `{
		"name":"op-1","status":"DONE",
		"httpErrorStatusCode":503,"httpErrorMessage":"SERVICE UNAVAILABLE",
		"error":{"errors":[{"code":"ZONE_RESOURCE_POOL_EXHAUSTED",
			"message":"The zone does not have enough resources available"}]}}`)

	err := WaitZoneOperation(t.Context(), client, "p", "z", "op-1")
	if err == nil {
		t.Fatal("WaitZoneOperation = nil, want the operation's stored failure")
	}
	apiErr, ok := errors.AsType[*googleapi.Error](err)
	if !ok {
		t.Fatalf("error %v does not carry a googleapi.Error", err)
	}
	if apiErr.Code != http.StatusServiceUnavailable {
		t.Errorf("googleapi code = %d, want 503", apiErr.Code)
	}
	classified := ClassifyError(err)
	if classified.Type != ErrorTypeResourceExhausted {
		t.Errorf("classified as %s, want %s (message flattening broke the stockout rule)",
			classified.Type, ErrorTypeResourceExhausted)
	}
}

// A canceled context must end the wait promptly instead of re-issuing
// against an operation that keeps reporting RUNNING.
func TestWaitZoneOperationHonorsContext(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	client := fakeOpsClient(t, &calls, `{"name":"op-1","status":"RUNNING"}`)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := WaitZoneOperation(ctx, client, "p", "z", "op-1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitZoneOperation = %v, want the context's deadline error", err)
	}
}

// A stockout names the zones that currently have capacity in
// error_info.metadatas, and that answer must survive the flattening of the
// error proto into a message. Payload is the one europe-west4 returned for a
// g2-standard-4 on 2026-08-08, trimmed to the fields read.
func TestWaitZoneOperationCarriesZonesGCENamed(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	client := fakeOpsClient(t, &calls, `{
		"name":"op-1","status":"DONE",
		"httpErrorStatusCode":503,"httpErrorMessage":"SERVICE UNAVAILABLE",
		"error":{"errors":[{
			"code":"ZONE_RESOURCE_POOL_EXHAUSTED_WITH_DETAILS",
			"message":"The zone 'europe-west4-a' does not have enough resources available to fulfill the request.",
			"errorDetails":[{"errorInfo":{
				"domain":"compute.googleapis.com",
				"reason":"resource_availability",
				"metadatas":{
					"attachment":"nvidia-l4:1",
					"vmType":"g2-standard-4",
					"zone":"europe-west4-a",
					"zonesAvailable":"europe-west4-c"}}}]}]}}`)

	err := WaitZoneOperation(t.Context(), client, "p", "z", "op-1")
	if err == nil {
		t.Fatal("WaitZoneOperation = nil, want the operation's stored failure")
	}
	opErr, ok := errors.AsType[*OperationError](err)
	if !ok {
		t.Fatalf("error %v does not carry an *OperationError", err)
	}
	if opErr.Reason != "resource_availability" {
		t.Errorf("reason = %q, want %q", opErr.Reason, "resource_availability")
	}
	classified := ClassifyError(err)
	if classified.Type != ErrorTypeResourceExhausted {
		t.Errorf("classified as %s, want %s", classified.Type, ErrorTypeResourceExhausted)
	}
	want := []string{"europe-west4-c"}
	if !slices.Equal(classified.AlternativeZones, want) {
		t.Errorf("AlternativeZones = %v, want %v. GCE named the zone with capacity and it was dropped",
			classified.AlternativeZones, want)
	}
}

// A dimensioned quota names the exhausted pool only in the structured detail:
// the message beside it carries the generic metric with the dimension dropped,
// so nothing in the text says C4. Reading the detail is what tells this refusal
// from an exhaustion of the shared pool.
func TestWaitZoneOperationReadsTheExhaustedQuotaAxis(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	client := fakeOpsClient(t, &calls, `{
		"name":"op-1","status":"DONE",
		"httpErrorStatusCode":403,"httpErrorMessage":"QUOTA_EXCEEDED",
		"error":{"errors":[{
			"code":"QUOTA_EXCEEDED",
			"message":"Quota CPUS_PER_VM_FAMILY exceeded. Limit: 300.0 in region europe-west4.",
			"errorDetails":[{"quotaInfo":{
				"metricName":"compute.googleapis.com/cpus_per_vm_family",
				"limitName":"CPUS-PER-VM-FAMILY-per-project-region",
				"dimensions":{"region":"europe-west4","vm_family":"C4"},
				"limit":300.0,
				"futureLimit":600.0,
				"rolloutStatus":"IN_PROGRESS"}}]}]}}`)

	err := WaitZoneOperation(t.Context(), client, "p", "z", "op-1")
	if err == nil {
		t.Fatal("WaitZoneOperation = nil, want the operation's stored failure")
	}
	classified := ClassifyError(err)
	if classified.QuotaAxis != "CPUS_PER_VM_FAMILY:C4" {
		t.Errorf("QuotaAxis = %q, want CPUS_PER_VM_FAMILY:C4", classified.QuotaAxis)
	}
	if classified.Type != ErrorTypeQuotaCPU {
		t.Errorf("classified as %s, want %s", classified.Type, ErrorTypeQuotaCPU)
	}
}

// The same refusal as GCE words it for a caller reading the message: the
// generic metric, with the dimension dropped. Nothing in the text can name the
// pool, which is what makes the structured detail above the only source for it.
func TestWaitZoneOperationCannotNameTheFamilyFromTheMessage(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	client := fakeOpsClient(t, &calls, `{
		"name":"op-1","status":"DONE",
		"httpErrorStatusCode":403,"httpErrorMessage":"QUOTA_EXCEEDED",
		"error":{"errors":[{
			"code":"QUOTA_EXCEEDED",
			"message":"Quota CPUS_PER_VM_FAMILY exceeded. Limit: 300.0 in region europe-west4."}]}}`)

	err := WaitZoneOperation(t.Context(), client, "p", "z", "op-1")
	if strings.Contains(err.Error(), "C4") {
		t.Fatal("the message names a family; this refusal is supposed to be the anonymous form")
	}
	classified := ClassifyError(err)
	if classified.QuotaAxis != "" {
		t.Errorf("QuotaAxis = %q, want empty: no structured detail named a pool", classified.QuotaAxis)
	}
	if classified.Type != ErrorTypeQuotaCPU {
		t.Errorf("classified as %s, want %s from the message alone", classified.Type, ErrorTypeQuotaCPU)
	}
}

// A hyperdisk capacity refusal is its own axis. The message contains neither
// "ssd" nor "disk", so before the structured detail was read it fell past every
// quota rule to a generic exhaustion, and the zone-health cell for the pool
// that actually ran out was never written.
func TestWaitZoneOperationClassifiesHyperdiskQuota(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	client := fakeOpsClient(t, &calls, `{
		"name":"op-1","status":"DONE",
		"httpErrorStatusCode":403,"httpErrorMessage":"QUOTA_EXCEEDED",
		"error":{"errors":[{
			"code":"QUOTA_EXCEEDED",
			"message":"Quota HDB_TOTAL_GB exceeded. Limit: 40960.0 in region europe-west4.",
			"errorDetails":[{"quotaInfo":{
				"metricName":"compute.googleapis.com/hyperdisk_balanced_total_storage",
				"limitName":"HDB-TOTAL-GB-per-project-region",
				"dimensions":{"region":"europe-west4"},
				"limit":40960.0}}]}]}}`)

	classified := ClassifyError(WaitZoneOperation(t.Context(), client, "p", "z", "op-1"))
	if classified.QuotaAxis != gcecatalog.QuotaTypeHDBTotalGB {
		t.Errorf("QuotaAxis = %q, want %s", classified.QuotaAxis, gcecatalog.QuotaTypeHDBTotalGB)
	}
	if classified.Type != ErrorTypeQuotaHyperdisk {
		t.Errorf("classified as %s, want %s", classified.Type, ErrorTypeQuotaHyperdisk)
	}
}

// A refusal carrying no structured detail still classifies off the message.
// The text rules are only the fallback.
func TestWaitZoneOperationClassifiesHyperdiskQuotaFromMessageAlone(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	client := fakeOpsClient(t, &calls, `{
		"name":"op-1","status":"DONE",
		"httpErrorStatusCode":403,"httpErrorMessage":"QUOTA_EXCEEDED",
		"error":{"errors":[{
			"code":"QUOTA_EXCEEDED",
			"message":"Quota HDB_TOTAL_GB exceeded. Limit: 40960.0 in region europe-west4."}]}}`)

	classified := ClassifyError(WaitZoneOperation(t.Context(), client, "p", "z", "op-1"))
	if classified.Type != ErrorTypeQuotaHyperdisk {
		t.Errorf("classified as %s, want %s", classified.Type, ErrorTypeQuotaHyperdisk)
	}
	if classified.QuotaAxis != "" {
		t.Errorf("QuotaAxis = %q, want empty: nothing structured said which pool", classified.QuotaAxis)
	}
}

// GCE reports an empty zonesAvailable when no zone in the region can take the
// request. That empty list is an answer, which differs from a missing field,
// and must not become advice.
func TestWaitZoneOperationReportsNoZonesWhenRegionIsExhausted(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	client := fakeOpsClient(t, &calls, `{
		"name":"op-1","status":"DONE",
		"httpErrorStatusCode":503,"httpErrorMessage":"SERVICE UNAVAILABLE",
		"error":{"errors":[{
			"code":"ZONE_RESOURCE_POOL_EXHAUSTED_WITH_DETAILS",
			"message":"The zone 'europe-west4-c' does not have enough resources available to fulfill the request.",
			"errorDetails":[{"errorInfo":{
				"domain":"compute.googleapis.com",
				"reason":"resource_availability",
				"metadatas":{"zone":"europe-west4-c","zonesAvailable":""}}}]}]}}`)

	err := WaitZoneOperation(t.Context(), client, "p", "z", "op-1")
	classified := ClassifyError(err)
	if classified.Type != ErrorTypeResourceExhausted {
		t.Errorf("classified as %s, want %s", classified.Type, ErrorTypeResourceExhausted)
	}
	if len(classified.AlternativeZones) != 0 {
		t.Errorf("AlternativeZones = %v, want none", classified.AlternativeZones)
	}
}

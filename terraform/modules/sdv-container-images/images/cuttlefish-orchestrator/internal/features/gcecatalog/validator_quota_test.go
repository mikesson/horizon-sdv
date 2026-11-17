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

package gcecatalogfeature

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype/machinetypetest"
	"cloud-cuttlefish-orchestrator/internal/platform/gce"
)

// quotaFixture is a one-zone catalog for exercising Validate's regional-quota
// judgment: fresh region, n1 (attachable GPUs) and g2 (integrated GPU) shapes
// available in the zone.
type quotaFixture struct {
	catalog *mockCatalogRepo
	types   *machinetypetest.FakeRepo
}

func newQuotaFixture() *quotaFixture {
	f := &quotaFixture{catalog: newMockCatalogRepo(), types: machinetypetest.NewFakeRepo()}
	f.catalog.enableZone("us-central1-a", gcecatalog.ZoneStatusUp)
	f.types.Types["n1-standard-8"] = &machinetype.MachineType{
		ID: "n1-standard-8", Family: "n1", VCPUs: 8, IsEnabled: true,
		GPUMode: machinetype.GPUModeAttachable, MaxGPUs: 4,
	}
	f.types.Types["g2-standard-8"] = &machinetype.MachineType{
		ID: "g2-standard-8", Family: "g2", VCPUs: 8, IsEnabled: true,
		GPUMode: machinetype.GPUModeIntegrated, IntegratedGPUType: "nvidia-l4", IntegratedGPUCount: 1,
	}
	f.catalog.machineTypes["us-central1-a"] = map[string]bool{"n1-standard-8": true, "g2-standard-8": true}
	f.catalog.accelerators["us-central1-a"] = map[string]int64{"nvidia-tesla-t4": 4, "nvidia-l4": 1}
	return f
}

// setQuota records a fresh regional quota row.
func (f *quotaFixture) setQuota(quotaType string, limit, usage int64) {
	now := time.Now()
	f.catalog.quotas["us-central1:"+quotaType] = &gcecatalog.RegionalQuota{
		Region: "us-central1", QuotaType: quotaType,
		LimitValue: limit, UsageValue: usage, LastSyncAt: &now,
	}
}

func (f *quotaFixture) validator() *CatalogValidator {
	return NewCatalogValidator(f.catalog, f.types,
		ValidatorConfig{}, slog.Default(),
		WithShapeCatalog(f.catalog))
}

func n1Validation() gcecatalog.ValidationRequest {
	return gcecatalog.ValidationRequest{Zone: "us-central1-a", MachineType: "n1-standard-8"}
}

func hasErrorCode(result *gcecatalog.ValidationResult, code string) bool {
	for _, e := range result.Errors {
		if e.Code == code {
			return true
		}
	}
	return false
}

func hasWarningCode(result *gcecatalog.ValidationResult, code string) bool {
	for _, w := range result.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestValidateQuotaCPUExhausted(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypeCPUs, 100, 96) // 4 free < 8 needed
	v := f.validator()

	result, err := v.Validate(t.Context(), n1Validation())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("want invalid: the region lacks CPU headroom for the shape")
	}
	if !hasErrorCode(result, gcecatalog.ErrorCodeQuotaCPUExceeded) {
		t.Errorf("errors = %+v, want %s", result.Errors, gcecatalog.ErrorCodeQuotaCPUExceeded)
	}
	if result.Errors[0].Field != "machine_type" {
		t.Errorf("field = %q, want machine_type", result.Errors[0].Field)
	}
}

// A request that fits exactly must pass: Validate judges real capacity with
// no reserve margin. The operator's quota margin is an automatic-placement
// reserve (ranking), never a validation rule for an explicitly chosen zone.
func TestValidateQuotaExactFitPasses(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypeCPUs, 100, 92) // exactly 8 free
	v := f.validator()

	result, err := v.Validate(t.Context(), n1Validation())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.Valid {
		t.Fatalf("want valid at an exact fit, got errors: %+v", result.Errors)
	}
}

func TestValidateQuotaSpotJudgesPreemptiblePool(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypeCPUs, 1000, 0)           // standard: plentiful
	f.setQuota(gcecatalog.QuotaTypePreemptibleCPUs, 16, 16) // preemptible: exhausted
	v := f.validator()

	req := n1Validation()
	req.ProvisioningModel = "SPOT"
	result, err := v.Validate(t.Context(), req)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("want invalid: a Spot spec draws from the preemptible pool, which is exhausted")
	}
	if !hasErrorCode(result, gcecatalog.ErrorCodeSpotQuotaCPUExceeded) {
		t.Errorf("errors = %+v, want %s", result.Errors, gcecatalog.ErrorCodeSpotQuotaCPUExceeded)
	}
}

func TestValidateQuotaGPUExhausted(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypeCPUs, 1000, 0)
	f.setQuota(gcecatalog.QuotaTypeNvidiaT4GPUs, 4, 4)
	v := f.validator()

	req := n1Validation()
	req.GPUType = "nvidia-tesla-t4"
	req.GPUCount = 1
	result, err := v.Validate(t.Context(), req)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("want invalid: the T4 pool is exhausted")
	}
	if !hasErrorCode(result, gcecatalog.ErrorCodeQuotaGPUExceeded) {
		t.Errorf("errors = %+v, want %s", result.Errors, gcecatalog.ErrorCodeQuotaGPUExceeded)
	}
	if result.Errors[0].Field != "gpu_count" {
		t.Errorf("field = %q, want gpu_count", result.Errors[0].Field)
	}
}

// An integrated-GPU shape carries its GPU demand implicitly: the request
// names no GPU fields, yet the machine's integrated accelerator draws from
// its quota pool all the same.
func TestValidateQuotaIntegratedGPUDemand(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypeCPUs, 1000, 0)
	f.setQuota(gcecatalog.QuotaTypeNvidiaL4GPUs, 2, 2)
	v := f.validator()

	result, err := v.Validate(t.Context(), gcecatalog.ValidationRequest{
		Zone: "us-central1-a", MachineType: "g2-standard-8",
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("want invalid: the integrated L4's pool is exhausted")
	}
	if !hasErrorCode(result, gcecatalog.ErrorCodeQuotaGPUExceeded) {
		t.Errorf("errors = %+v, want %s", result.Errors, gcecatalog.ErrorCodeQuotaGPUExceeded)
	}
}

func TestValidateQuotaNearLimitWarns(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypeCPUs, 100, 88) // 88% used, 12 free: fits, near limit
	f.setQuota(gcecatalog.QuotaTypeNvidiaT4GPUs, 10, 9)
	v := f.validator()

	req := n1Validation()
	req.GPUType = "nvidia-tesla-t4"
	req.GPUCount = 1
	result, err := v.Validate(t.Context(), req)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.Valid {
		t.Fatalf("want valid (near the limit is not over it), got errors: %+v", result.Errors)
	}
	warnings := 0
	for _, w := range result.Warnings {
		if w.Code == gcecatalog.WarningCodeQuotaNearLimit {
			warnings++
		}
	}
	if warnings != 2 {
		t.Errorf("near-limit warnings = %d (%+v), want one for CPU and one for GPU",
			warnings, result.Warnings)
	}
}

// A project with no preemptible grant draws its Spot VMs from the standard
// pools, so a Spot validation with no synced PREEMPTIBLE_* rows judges the
// standard pool. An exhausted standard pool must fail the validation instead
// of passing as "unknown". Mirrors ranking's fallback (the two engines are one).
func TestValidateQuotaSpotFallsBackToStandardPool(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypeCPUs, 100, 96) // 4 free < 8 needed; no preemptible rows
	v := f.validator()

	req := n1Validation()
	req.ProvisioningModel = "SPOT"
	result, err := v.Validate(t.Context(), req)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("want invalid: with no preemptible grant, Spot draws from the exhausted standard pool")
	}
	if !hasErrorCode(result, gcecatalog.ErrorCodeSpotQuotaCPUExceeded) {
		t.Errorf("errors = %+v, want %s", result.Errors, gcecatalog.ErrorCodeSpotQuotaCPUExceeded)
	}
}

// A stale quota row must not hard-fail an explicitly chosen zone: the values
// may be long out of date (a dead sync ticker), so the row degrades to a
// warning and the create's own quota failure remains the accurate verdict.
func TestValidateQuotaStaleRowWarnsInsteadOfRejecting(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	old := time.Now().Add(-2 * time.Hour)
	f.catalog.quotas["us-central1:CPUS"] = &gcecatalog.RegionalQuota{
		Region: "us-central1", QuotaType: gcecatalog.QuotaTypeCPUs,
		LimitValue: 100, UsageValue: 100, LastSyncAt: &old, // exhausted, but stale
	}
	v := f.validator()

	result, err := v.Validate(t.Context(), n1Validation())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.Valid {
		t.Fatalf("want valid (stale data carries no verdict), got errors: %+v", result.Errors)
	}
	if !hasWarningCode(result, gcecatalog.WarningCodeCatalogStale) {
		t.Errorf("warnings = %+v, want a %s warning", result.Warnings, gcecatalog.WarningCodeCatalogStale)
	}
}

// An unconfigured validator falls back to a bound derived from the cadence
// that produces the rows, so a row many refreshes old is out of trust long
// before an hour of them has passed.
func TestValidateQuotaFallbackStalenessFollowsTheRefreshInterval(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	aged := time.Now().Add(-10 * gce.QuotaRefreshInterval)
	f.catalog.quotas["us-central1:CPUS"] = &gcecatalog.RegionalQuota{
		Region: "us-central1", QuotaType: gcecatalog.QuotaTypeCPUs,
		LimitValue: 100, UsageValue: 100, LastSyncAt: &aged, // exhausted, but aged out
	}
	// The fixture leaves QuotaStaleThreshold unset, so the compiled bound judges.
	v := f.validator()

	result, err := v.Validate(t.Context(), n1Validation())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.Valid {
		t.Fatalf("want valid (a row ten refreshes old carries no verdict), got errors: %+v", result.Errors)
	}
	if !hasWarningCode(result, gcecatalog.WarningCodeCatalogStale) {
		t.Errorf("warnings = %+v, want a %s warning", result.Warnings, gcecatalog.WarningCodeCatalogStale)
	}
}

// When device media takes external IPs, every host draws one IN_USE_ADDRESSES
// slot, and the validation judges that axis exactly like the create will.
func TestValidateQuotaJudgesInUseAddresses(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypeCPUs, 1000, 0)
	f.setQuota(gcecatalog.QuotaTypeInUseAddresses, 8, 8)
	v := NewCatalogValidator(f.catalog, f.types,
		ValidatorConfig{UseExternalIP: true}, slog.Default(),
		WithShapeCatalog(f.catalog))

	result, err := v.Validate(t.Context(), n1Validation())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("want invalid: the region has no in-use address headroom")
	}
	if !hasErrorCode(result, gcecatalog.ErrorCodeQuotaIPExceeded) {
		t.Errorf("errors = %+v, want %s", result.Errors, gcecatalog.ErrorCodeQuotaIPExceeded)
	}
}

// A per-family CPU pool with no reading admits the create. This is the one
// behavior per-family pools must get right: the time series behind such
// a pool does not exist until the project has run the family, so on the day it
// ships every c4/n4/c4a shape is in exactly this state. An absent reading read
// as a pool granted nothing would refuse every one of those creates. The
// difference between the two states shows here and nowhere else, because
// admission is the verdict that hard-fails a real zero.
func TestValidateUnreadPerFamilyQuotaAdmitsTheCreate(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.types.Types["c4-standard-8"] = &machinetype.MachineType{
		ID: "c4-standard-8", Family: "c4", VCPUs: 8, IsEnabled: true,
		BootDiskType: "hyperdisk-balanced", GPUMode: machinetype.GPUModeNone,
	}
	f.catalog.machineTypes["us-central1-a"]["c4-standard-8"] = true
	// Every flat pool is read and roomy; only the per-family and hyperdisk
	// pools (the ones no series exists for yet) are missing.
	f.setQuota(gcecatalog.QuotaTypeCPUs, 3000, 0)
	f.setQuota(gcecatalog.QuotaTypeSSDTotalGB, 40960, 0)
	v := f.validator()

	result, err := v.Validate(t.Context(), gcecatalog.ValidationRequest{
		Zone: "us-central1-a", MachineType: "c4-standard-8",
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.Valid {
		t.Fatalf("want valid: an unread pool is not a pool granted nothing; errors: %+v", result.Errors)
	}
}

// The same shape against a pool that was read and is exhausted fails, so the
// pass above is the absence and not a per-family pool that nothing judges.
func TestValidateExhaustedPerFamilyQuotaRefusesTheCreate(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.types.Types["c4-standard-8"] = &machinetype.MachineType{
		ID: "c4-standard-8", Family: "c4", VCPUs: 8, IsEnabled: true,
		BootDiskType: "hyperdisk-balanced", GPUMode: machinetype.GPUModeNone,
	}
	f.catalog.machineTypes["us-central1-a"]["c4-standard-8"] = true
	f.setQuota("CPUS_PER_VM_FAMILY:C4", 300, 296) // 4 free < 8 needed
	v := f.validator()

	result, err := v.Validate(t.Context(), gcecatalog.ValidationRequest{
		Zone: "us-central1-a", MachineType: "c4-standard-8",
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("want invalid: the region's C4 pool has no headroom for the shape")
	}
	if !hasErrorCode(result, gcecatalog.ErrorCodeQuotaCPUExceeded) {
		t.Errorf("errors = %+v, want %s", result.Errors, gcecatalog.ErrorCodeQuotaCPUExceeded)
	}
}

// A hyperdisk-balanced boot disk is charged the hyperdisk pool, which the
// pd-* shapes never touch, so an exhausted SSD pool says nothing about it and
// an exhausted hyperdisk pool refuses it.
func TestValidateHyperdiskBootDiskJudgesItsOwnPool(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.types.Types["c4-standard-8"] = &machinetype.MachineType{
		ID: "c4-standard-8", Family: "c4", VCPUs: 8, IsEnabled: true,
		BootDiskType: "hyperdisk-balanced", GPUMode: machinetype.GPUModeNone,
	}
	f.catalog.machineTypes["us-central1-a"]["c4-standard-8"] = true
	f.setQuota("CPUS_PER_VM_FAMILY:C4", 3000, 0)
	f.setQuota(gcecatalog.QuotaTypeSSDTotalGB, 40960, 40960) // exhausted, and irrelevant
	f.setQuota(gcecatalog.QuotaTypeHDBTotalGB, 40960, 0)
	v := f.validator()

	req := gcecatalog.ValidationRequest{Zone: "us-central1-a", MachineType: "c4-standard-8"}
	result, err := v.Validate(t.Context(), req)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.Valid {
		t.Fatalf("want valid: an exhausted SSD pool does not bind a hyperdisk boot disk; errors: %+v", result.Errors)
	}

	f.setQuota(gcecatalog.QuotaTypeHDBTotalGB, 40960, 40960)
	result, err = f.validator().Validate(t.Context(), req)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("want invalid: the hyperdisk pool the boot disk fills is exhausted")
	}
	if !hasErrorCode(result, gcecatalog.ErrorCodeQuotaDiskExceeded) {
		t.Errorf("errors = %+v, want %s", result.Errors, gcecatalog.ErrorCodeQuotaDiskExceeded)
	}
	if !strings.Contains(result.Errors[0].Message, gcecatalog.QuotaTypeHDBTotalGB) {
		t.Errorf("message = %q, want it to name the pool that ran out", result.Errors[0].Message)
	}
}

// A metric the catalog has never synced passes silently: an untracked pool is
// not evidence of missing capacity, and warning on every untracked metric
// would bury real signals.
func TestValidateQuotaUnknownRowPasses(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	v := f.validator()

	result, err := v.Validate(t.Context(), n1Validation())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.Valid {
		t.Fatalf("want valid with no synced quota rows, got errors: %+v", result.Errors)
	}
	if hasWarningCode(result, gcecatalog.WarningCodeQuotaNearLimit) {
		t.Errorf("warnings = %+v, want no quota warnings for untracked pools", result.Warnings)
	}
}

// A FRESH row granting 0 is a real no-grant, not sync lag: GCE would reject
// the create with QUOTA_EXCEEDED, so Validate fails it instead of
// mislabeling fresh data as stale and passing.
func TestValidateQuotaZeroLimitFails(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypeCPUs, 0, 0)
	v := f.validator()

	result, err := v.Validate(t.Context(), n1Validation())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("want invalid: a fresh zero-limit CPU row is an ungranted pool")
	}
	if !hasErrorCode(result, gcecatalog.ErrorCodeQuotaCPUExceeded) {
		t.Errorf("errors = %+v, want %s", result.Errors, gcecatalog.ErrorCodeQuotaCPUExceeded)
	}
	if !strings.Contains(result.Errors[0].Message, "available 0") {
		t.Errorf("message = %q, want it to render available 0", result.Errors[0].Message)
	}
	if hasWarningCode(result, gcecatalog.WarningCodeCatalogStale) {
		t.Errorf("warnings = %+v, want no staleness warning for a fresh row", result.Warnings)
	}
}

func TestValidateQuotaZeroLimitGPUFails(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypeCPUs, 1000, 0)
	f.setQuota(gcecatalog.QuotaTypeNvidiaT4GPUs, 0, 0)
	v := f.validator()

	req := n1Validation()
	req.GPUType = "nvidia-tesla-t4"
	req.GPUCount = 1
	result, err := v.Validate(t.Context(), req)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("want invalid: the T4 pool grants 0")
	}
	if !hasErrorCode(result, gcecatalog.ErrorCodeQuotaGPUExceeded) {
		t.Errorf("errors = %+v, want %s", result.Errors, gcecatalog.ErrorCodeQuotaGPUExceeded)
	}
}

func TestValidateQuotaZeroLimitIPFails(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypeCPUs, 1000, 0)
	f.setQuota(gcecatalog.QuotaTypeInUseAddresses, 0, 0)
	v := NewCatalogValidator(f.catalog, f.types,
		ValidatorConfig{UseExternalIP: true}, slog.Default(),
		WithShapeCatalog(f.catalog))

	result, err := v.Validate(t.Context(), n1Validation())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("want invalid: the region grants no in-use addresses")
	}
	if !hasErrorCode(result, gcecatalog.ErrorCodeQuotaIPExceeded) {
		t.Errorf("errors = %+v, want %s", result.Errors, gcecatalog.ErrorCodeQuotaIPExceeded)
	}
}

// A limit-0 PREEMPTIBLE_CPUS row is GCE's encoding of "no separate
// preemptible grant, Spot draws from the standard pool". It must resolve to
// the standard pool and pass when that pool has room, never hard-fail the
// Spot create on the zero-limit row itself.
func TestValidateQuotaSpotZeroLimitPreemptibleFallsBack(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypePreemptibleCPUs, 0, 0)
	f.setQuota(gcecatalog.QuotaTypeCPUs, 1000, 0)
	v := f.validator()

	req := n1Validation()
	req.ProvisioningModel = "SPOT"
	result, err := v.Validate(t.Context(), req)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.Valid {
		t.Fatalf("want valid: Spot draws from the roomy standard pool, got errors: %+v", result.Errors)
	}
}

// With the preemptible grant at 0 AND the standard pool granting 0, no pool
// can serve the Spot create: fail with the Spot code.
func TestValidateQuotaSpotBothPoolsZeroFails(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypePreemptibleCPUs, 0, 0)
	f.setQuota(gcecatalog.QuotaTypeCPUs, 0, 0)
	v := f.validator()

	req := n1Validation()
	req.ProvisioningModel = "SPOT"
	result, err := v.Validate(t.Context(), req)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("want invalid: neither pool grants any Spot CPU capacity")
	}
	if !hasErrorCode(result, gcecatalog.ErrorCodeSpotQuotaCPUExceeded) {
		t.Errorf("errors = %+v, want %s", result.Errors, gcecatalog.ErrorCodeSpotQuotaCPUExceeded)
	}
}

// A zero-limit preemptible grant whose standard pool is untracked leaves
// nothing judgeable: the pool Spot would actually draw from is unknown, so
// the validation passes advisorily instead of failing on a pool Spot does
// not draw from.
func TestValidateQuotaSpotZeroLimitPreemptibleUntrackedStandardPasses(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypePreemptibleCPUs, 0, 0)
	v := f.validator()

	req := n1Validation()
	req.ProvisioningModel = "SPOT"
	result, err := v.Validate(t.Context(), req)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.Valid {
		t.Fatalf("want valid: the authoritative (standard) pool is unknown, got errors: %+v", result.Errors)
	}
}

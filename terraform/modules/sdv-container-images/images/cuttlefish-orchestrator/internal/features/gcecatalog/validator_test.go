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
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype/machinetypetest"
)

// mockCatalogRepo stands in for both halves of the catalog: the operator's
// enabled zone set and the in-process GCE snapshot (ShapeCatalog).
//
// The availability maps carry the snapshot's three states in their key
// structure: a zone missing from machineTypes/accelerators was never reported
// by Compute Engine and reads as unknown, while a zone present with the shape
// absent reads as a positive "not offered". zoneStatuses is the snapshot's own
// zone list; empty means no snapshot has landed.
type mockCatalogRepo struct {
	zoneStatuses map[string]gcecatalog.ZoneStatus
	quotas       map[string]*gcecatalog.RegionalQuota
	machineTypes map[string]map[string]bool  // zone -> machineType -> offered
	accelerators map[string]map[string]int64 // zone -> accelerator -> max cards
	enabledZones []string
	// enabledZonesErr injects a Spanner failure into the zone listing.
	enabledZonesErr error
	builtAt         time.Time
	partial         []string
}

func newMockCatalogRepo() *mockCatalogRepo {
	return &mockCatalogRepo{
		zoneStatuses: make(map[string]gcecatalog.ZoneStatus),
		quotas:       make(map[string]*gcecatalog.RegionalQuota),
		machineTypes: make(map[string]map[string]bool),
		accelerators: make(map[string]map[string]int64),
	}
}

// enableZone puts a zone in the snapshot and in the operator's enabled set.
func (m *mockCatalogRepo) enableZone(zone string, status gcecatalog.ZoneStatus) {
	m.zoneStatuses[zone] = status
	m.enabledZones = append(m.enabledZones, zone)
}

func (m *mockCatalogRepo) ListEnabledZones(context.Context) ([]string, error) {
	return m.enabledZones, m.enabledZonesErr
}

func (m *mockCatalogRepo) SetEnabledZones(_ context.Context, zones []string) error {
	m.enabledZones = zones
	return nil
}

func (m *mockCatalogRepo) Zones() map[string]gcecatalog.ZoneStatus { return m.zoneStatuses }

func (m *mockCatalogRepo) BuiltAt() time.Time { return m.builtAt }

func (m *mockCatalogRepo) Partial() []string { return m.partial }

func (m *mockCatalogRepo) MachineTypeAvailability(zone, machineType string) gcecatalog.Availability {
	offered, reported := m.machineTypes[zone]
	switch {
	case !reported:
		return gcecatalog.AvailabilityUnknown
	case offered[machineType]:
		return gcecatalog.AvailabilityOffered
	default:
		return gcecatalog.AvailabilityNotOffered
	}
}

func (m *mockCatalogRepo) AcceleratorAvailability(zone, acceleratorType string) gcecatalog.Availability {
	offered, reported := m.accelerators[zone]
	if !reported {
		return gcecatalog.AvailabilityUnknown
	}
	if _, ok := offered[acceleratorType]; ok {
		return gcecatalog.AvailabilityOffered
	}
	return gcecatalog.AvailabilityNotOffered
}

func (m *mockCatalogRepo) ZoneAccelerators(zone string) []*gcecatalog.Accelerator {
	offered := m.accelerators[zone]
	out := make([]*gcecatalog.Accelerator, 0, len(offered))
	for _, name := range slices.Sorted(maps.Keys(offered)) {
		out = append(out, &gcecatalog.Accelerator{
			Region: gcecatalog.RegionFromZone(zone), Zone: zone,
			AcceleratorType: name, IsAvailable: true, MaxCount: offered[name],
		})
	}
	return out
}

func (m *mockCatalogRepo) RegionalQuota(region, quotaType string) *gcecatalog.RegionalQuota {
	return m.quotas[region+":"+quotaType]
}

func (m *mockCatalogRepo) RegionalQuotas(region string) []*gcecatalog.RegionalQuota {
	var result []*gcecatalog.RegionalQuota
	for key, q := range m.quotas {
		if strings.HasPrefix(key, region+":") {
			result = append(result, q)
		}
	}
	return result
}

// Verify mock implementations satisfy interfaces.
var (
	_ ZoneEnablement = (*mockCatalogRepo)(nil)
	_ ShapeCatalog   = (*mockCatalogRepo)(nil)
)

// Test functions

func TestValidateProvisioningModel(t *testing.T) {
	t.Parallel()
	catalogRepo := newMockCatalogRepo()
	machineTypeRepo := machinetypetest.NewFakeRepo()
	logger := slog.Default()
	config := ValidatorConfig{}

	validator := NewCatalogValidator(catalogRepo, machineTypeRepo, config, logger,
		WithShapeCatalog(catalogRepo))

	tests := []struct {
		name              string
		provisioningModel string
		expectValid       bool
		expectErrorCode   string
	}{
		{
			name:              "Empty provisioning model (default)",
			provisioningModel: "",
			expectValid:       true,
		},
		{
			name:              "Standard VM",
			provisioningModel: "STANDARD",
			expectValid:       true,
		},
		{
			name:              "Spot VM",
			provisioningModel: "SPOT",
			expectValid:       true,
		},
		{
			name:              "Invalid provisioning model",
			provisioningModel: "INVALID",
			expectValid:       false,
			expectErrorCode:   gcecatalog.ErrorCodeInvalidProvisioningModel,
		},
		{
			name:              "Lowercase spot (invalid)",
			provisioningModel: "spot",
			expectValid:       false,
			expectErrorCode:   gcecatalog.ErrorCodeInvalidProvisioningModel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &gcecatalog.ValidationResult{Valid: true}
			valid := validator.validateProvisioningModel(tt.provisioningModel, result)

			if valid != tt.expectValid {
				t.Errorf("validateProvisioningModel() = %v, want %v", valid, tt.expectValid)
			}

			if !tt.expectValid && len(result.Errors) > 0 {
				if result.Errors[0].Code != tt.expectErrorCode {
					t.Errorf("error code = %s, want %s", result.Errors[0].Code, tt.expectErrorCode)
				}
			}
		})
	}
}

func TestSpotVMQuotaValidation(t *testing.T) {
	t.Parallel()
	catalogRepo := newMockCatalogRepo()
	machineTypeRepo := machinetypetest.NewFakeRepo()
	logger := slog.Default()
	config := ValidatorConfig{}

	// Setup test data
	now := time.Now()

	// Add zone
	catalogRepo.enableZone("us-central1-a", gcecatalog.ZoneStatusUp)

	// Add machine type
	machineTypeRepo.Types["n1-standard-8"] = &machinetype.MachineType{
		ID:        "n1-standard-8",
		Family:    "n1",
		VCPUs:     8,
		MemoryGB:  30,
		IsEnabled: true,
		GPUMode:   GPUModeAttachable,
		MaxGPUs:   4,
	}
	catalogRepo.machineTypes["us-central1-a"] = map[string]bool{
		"n1-standard-8": true,
	}
	catalogRepo.accelerators["us-central1-a"] = map[string]int64{
		"nvidia-tesla-t4": 4,
	}

	// Standard quotas (plenty available); freshly synced like real rows.
	catalogRepo.quotas["us-central1:CPUS"] = &gcecatalog.RegionalQuota{
		Region:     "us-central1",
		QuotaType:  gcecatalog.QuotaTypeCPUs,
		LimitValue: 100,
		UsageValue: 20,
		LastSyncAt: &now,
	}
	catalogRepo.quotas["us-central1:NVIDIA_T4_GPUS"] = &gcecatalog.RegionalQuota{
		Region:     "us-central1",
		QuotaType:  gcecatalog.QuotaTypeNvidiaT4GPUs,
		LimitValue: 10,
		UsageValue: 2,
		LastSyncAt: &now,
	}

	// Preemptible quotas (limited)
	catalogRepo.quotas["us-central1:PREEMPTIBLE_CPUS"] = &gcecatalog.RegionalQuota{
		Region:     "us-central1",
		QuotaType:  gcecatalog.QuotaTypePreemptibleCPUs,
		LimitValue: 16,
		UsageValue: 8,
		LastSyncAt: &now,
	}
	catalogRepo.quotas["us-central1:PREEMPTIBLE_NVIDIA_T4_GPUS"] = &gcecatalog.RegionalQuota{
		Region:     "us-central1",
		QuotaType:  gcecatalog.QuotaTypePreemptibleNvidiaT4GPUs,
		LimitValue: 2,
		UsageValue: 2, // Exhausted!
		LastSyncAt: &now,
	}

	validator := NewCatalogValidator(catalogRepo, machineTypeRepo, config, logger,
		WithShapeCatalog(catalogRepo))
	ctx := t.Context()

	tests := []struct {
		name              string
		provisioningModel string
		gpuCount          int64
		expectValid       bool
		expectErrorCode   string
	}{
		{
			name:              "Standard VM with available quota",
			provisioningModel: "STANDARD",
			gpuCount:          1,
			expectValid:       true,
		},
		{
			name:              "Spot VM with available CPU quota",
			provisioningModel: "SPOT",
			gpuCount:          0,
			expectValid:       true,
		},
		{
			name:              "Spot VM with exhausted GPU quota",
			provisioningModel: "SPOT",
			gpuCount:          1,
			expectValid:       false,
			expectErrorCode:   gcecatalog.ErrorCodeSpotQuotaGPUExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := gcecatalog.ValidationRequest{
				Zone:              "us-central1-a",
				MachineType:       "n1-standard-8",
				GPUType:           "nvidia-tesla-t4",
				GPUCount:          tt.gpuCount,
				ProvisioningModel: tt.provisioningModel,
			}

			result, err := validator.Validate(ctx, req)
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}

			if result.Valid != tt.expectValid {
				t.Errorf("Valid = %v, want %v", result.Valid, tt.expectValid)
				if len(result.Errors) > 0 {
					t.Logf("Errors: %+v", result.Errors)
				}
			}

			if !tt.expectValid && tt.expectErrorCode != "" {
				found := false
				for _, e := range result.Errors {
					if e.Code == tt.expectErrorCode {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected error code %s not found in errors: %+v", tt.expectErrorCode, result.Errors)
				}
			}
		})
	}
}

// Admission follows the same fail-open rule as ranking, and the cold-start
// case is the one that matters: a create arriving before the first snapshot
// lands must be admitted with a warning, not refused for a zone nobody has
// asked Compute Engine about yet.
func TestValidateAdmitsUnknownAvailability(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture()
	f.setQuota(gcecatalog.QuotaTypeCPUs, 1000, 0)
	// No snapshot: no zone has been reported.
	clear(f.catalog.machineTypes)
	clear(f.catalog.accelerators)
	v := f.validator()

	req := n1Validation()
	req.GPUType = "nvidia-tesla-t4"
	req.GPUCount = 1
	result, err := v.Validate(t.Context(), req)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.Valid {
		t.Fatalf("a cold start refused a create it knows nothing against: %+v", result.Errors)
	}
	if !hasWarningCode(result, gcecatalog.WarningCodeCatalogStale) {
		t.Errorf("warnings = %+v, want the unverified-availability warning", result.Warnings)
	}

	// The converse, so this cannot pass by never checking: a zone Compute
	// Engine reported without the shape is still refused.
	f.catalog.machineTypes["us-central1-a"] = map[string]bool{}
	result, err = v.Validate(t.Context(), n1Validation())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("a zone that positively does not offer the shape was admitted")
	}
	if !hasErrorCode(result, gcecatalog.ErrorCodeMachineTypeNotInZone) {
		t.Errorf("errors = %+v, want %s", result.Errors, gcecatalog.ErrorCodeMachineTypeNotInZone)
	}
}

func TestQuotaTypeDisplayName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		quotaType string
		expected  string
	}{
		{gcecatalog.QuotaTypeCPUs, "standard"},
		{gcecatalog.QuotaTypeNvidiaT4GPUs, "standard"},
		{gcecatalog.QuotaTypePreemptibleCPUs, "preemptible"},
		{gcecatalog.QuotaTypePreemptibleNvidiaT4GPUs, "preemptible"},
	}

	for _, tt := range tests {
		t.Run(tt.quotaType, func(t *testing.T) {
			result := quotaTypeDisplayName(tt.quotaType)
			if result != tt.expected {
				t.Errorf("quotaTypeDisplayName(%s) = %s, want %s", tt.quotaType, result, tt.expected)
			}
		})
	}
}

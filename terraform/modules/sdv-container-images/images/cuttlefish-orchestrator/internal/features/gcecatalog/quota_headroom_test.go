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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// demandFor is the demand model's answer for one shape, keyed by quota type.
func demandFor(t *testing.T, mt *machinetype.MachineType, req gcecatalog.ValidationRequest) map[string]metricDemand {
	t.Helper()
	f := newRankingFixture()
	f.types.Types[mt.ID] = mt
	byType := make(map[string]metricDemand)
	for _, d := range f.validator(t).Ranker().bindingDemand(req, mt) {
		byType[d.quotaType] = d
	}
	return byType
}

func shape(id, family, bootDisk string, vcpus int64) *machinetype.MachineType {
	return &machinetype.MachineType{
		ID: id, Family: family, VCPUs: vcpus, BootDiskType: bootDisk,
		IsEnabled: true, GPUMode: GPUModeNone,
	}
}

// Each family is charged the CPU pool Compute Engine actually draws it from.
// Charging every shape the shared flat pool is right only for n1: it judges
// twice the real headroom for n2, ten times it for c3, and a pool n4/c4/c4a
// never touch at all.
func TestBindingDemandChargesEachFamilyItsOwnCPUPool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		machineType string
		family      string
		wantQuota   string
	}{
		{"n1-standard-8", "n1", gcecatalog.QuotaTypeCPUs},
		{"n2-standard-8", "n2", gcecatalog.QuotaTypeN2CPUs},
		{"c3-standard-8", "c3", gcecatalog.QuotaTypeC3CPUs},
		{"c4-standard-8", "c4", "CPUS_PER_VM_FAMILY:C4"},
		{"n4-standard-8", "n4", "CPUS_PER_VM_FAMILY:N4"},
		{"c4a-highmem-96-metal", "c4a", "CPUS_PER_VM_FAMILY:C4A"},
	}
	for _, tc := range tests {
		demand := demandFor(t, shape(tc.machineType, tc.family, "pd-balanced", 8),
			gcecatalog.ValidationRequest{MachineType: tc.machineType})
		cpu, ok := demand[tc.wantQuota]
		require.True(t, ok, "%s must charge %s; demand was %+v", tc.machineType, tc.wantQuota, demand)
		assert.Equal(t, int64(8), cpu.amount)
		assert.Equal(t, gcecatalog.ZoneMetricQuotaCPU, cpu.healthMetric,
			"every CPU pool cools the one CPU axis; the cell already carries the family")
	}
}

// G2 spends no CPU quota at all. Its L4 GPU quota is the whole admission
// control. Charging it vCPUs refuses placements Compute Engine would accept.
func TestBindingDemandChargesG2NoCPUQuota(t *testing.T) {
	t.Parallel()
	g2 := shape("g2-standard-8", "g2", "pd-balanced", 8)
	g2.GPUMode = GPUModeIntegrated
	g2.IntegratedGPUType = "nvidia-l4"
	g2.IntegratedGPUCount = 1

	demand := demandFor(t, g2, gcecatalog.ValidationRequest{MachineType: g2.ID})

	for quotaType, d := range demand {
		assert.NotEqual(t, gcecatalog.ZoneMetricQuotaCPU, d.healthMetric,
			"g2 charged %s against the CPU axis", quotaType)
	}
	gpu, ok := demand[gcecatalog.QuotaTypeNvidiaL4GPUs]
	require.True(t, ok, "the integrated L4 is the pool g2 does draw; demand was %+v", demand)
	assert.Equal(t, int64(1), gpu.amount)
}

// The boot disk is charged too, to the pool its type actually fills: pd-*
// disks to the SSD pool, hyperdisk-balanced ones to the hyperdisk pool, which
// counts against nothing else.
func TestBindingDemandChargesTheBootDisk(t *testing.T) {
	t.Parallel()
	tests := []struct {
		bootDisk     string
		wantQuota    string
		wantHealth   string
		unwantedPool string
	}{
		{"pd-balanced", gcecatalog.QuotaTypeSSDTotalGB, gcecatalog.ZoneMetricQuotaSSD, gcecatalog.QuotaTypeHDBTotalGB},
		{"hyperdisk-balanced", gcecatalog.QuotaTypeHDBTotalGB, gcecatalog.ZoneMetricQuotaHyperdisk, gcecatalog.QuotaTypeSSDTotalGB},
	}
	for _, tc := range tests {
		demand := demandFor(t, shape("c4-standard-8", "c4", tc.bootDisk, 8),
			gcecatalog.ValidationRequest{MachineType: "c4-standard-8"})
		disk, ok := demand[tc.wantQuota]
		require.True(t, ok, "a %s boot disk must charge %s; demand was %+v", tc.bootDisk, tc.wantQuota, demand)
		assert.Positive(t, disk.amount, "the boot disk charge is its size in GB")
		assert.Equal(t, tc.wantHealth, disk.healthMetric)
		assert.NotContains(t, demand, tc.unwantedPool)
	}
}

// A Spot spec judges the preemptible pools, and a pool with no preemptible
// twin (a boot disk is charged the same either way) is judged as itself.
func TestBindingDemandSpotSubstitutesThePreemptibleCPUPool(t *testing.T) {
	t.Parallel()
	demand := demandFor(t, shape("n2-standard-8", "n2", "pd-balanced", 8),
		gcecatalog.ValidationRequest{MachineType: "n2-standard-8", ProvisioningModel: provisioningModelSpot})

	cpu, ok := demand[gcecatalog.QuotaTypePreemptibleCPUs]
	require.True(t, ok, "a Spot n2 draws the preemptible pool; demand was %+v", demand)
	assert.Equal(t, gcecatalog.QuotaTypeN2CPUs, cpu.fallback,
		"with no preemptible grant in the region, the family's own pool is what is drawn")
	assert.Contains(t, demand, gcecatalog.QuotaTypeSSDTotalGB)
}

// A per-family pool with no reading is unknown, and unknown passes: the series
// behind it does not exist until the project runs the family, so on the day
// this ships every c4/n4/c4a zone is in exactly this state. Reading it as a
// pool granted nothing would make all of them unplaceable at once.
func TestRankZonesUnknownPerFamilyQuotaPassesAdvisorily(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	f.types.Types["c4-standard-8"] = shape("c4-standard-8", "c4", "hyperdisk-balanced", 8)
	for _, zone := range []string{"europe-west4-a", "europe-west4-b", "us-central1-a"} {
		f.catalog.machineTypes[zone]["c4-standard-8"] = true
	}
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(),
		gcecatalog.ValidationRequest{MachineType: "c4-standard-8"}, gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Len(t, candidates, 3, "no zone may be removed for a pool nothing has read")
}

// The same shape with a read pool is judged on it. An exhausted per-family
// pool removes the region.
func TestRankZonesExhaustedPerFamilyQuotaRemovesTheRegion(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	f.types.Types["c4-standard-8"] = shape("c4-standard-8", "c4", "hyperdisk-balanced", 8)
	for _, zone := range []string{"europe-west4-a", "europe-west4-b", "us-central1-a"} {
		f.catalog.machineTypes[zone]["c4-standard-8"] = true
	}
	now := time.Now()
	f.catalog.quotas["europe-west4:CPUS_PER_VM_FAMILY:C4"] = &gcecatalog.RegionalQuota{
		Region: "europe-west4", QuotaType: "CPUS_PER_VM_FAMILY:C4",
		LimitValue: 300, UsageValue: 300, LastSyncAt: &now,
	}
	v := f.validator(t)

	candidates, err := v.RankZones(t.Context(),
		gcecatalog.ValidationRequest{MachineType: "c4-standard-8"}, gcecatalog.RankOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"us-central1-a"}, zoneNames(candidates))
}

// The fallback to standard quota is a property of the region. Once any
// preemptible pool is granted there, every Spot VM in the region is on
// preemptible quota and cannot consume standard quota in its place, even on
// an axis whose own preemptible grant is zero.
func TestRankZonesSpotDoesNotFallBackWhereThePreemptibleGrantExists(t *testing.T) {
	t.Parallel()
	f := newRankingFixture()
	now := time.Now()
	// europe-west4 holds a preemptible CPU grant, so it is in preemptible mode;
	// its preemptible GPU pool is granted nothing.
	f.catalog.quotas["europe-west4:PREEMPTIBLE_CPUS"] = &gcecatalog.RegionalQuota{
		Region: "europe-west4", QuotaType: gcecatalog.QuotaTypePreemptibleCPUs,
		LimitValue: 5000, UsageValue: 0, LastSyncAt: &now,
	}
	f.catalog.quotas["europe-west4:PREEMPTIBLE_NVIDIA_T4_GPUS"] = &gcecatalog.RegionalQuota{
		Region: "europe-west4", QuotaType: gcecatalog.QuotaTypePreemptibleNvidiaT4GPUs,
		LimitValue: 0, UsageValue: 0, LastSyncAt: &now,
	}
	f.catalog.quotas["europe-west4:NVIDIA_T4_GPUS"] = &gcecatalog.RegionalQuota{
		Region: "europe-west4", QuotaType: gcecatalog.QuotaTypeNvidiaT4GPUs,
		LimitValue: 16, UsageValue: 0, LastSyncAt: &now,
	}
	v := f.validator(t)

	req := n1SpotRequest()
	req.GPUType = "nvidia-tesla-t4"
	req.GPUCount = 1
	demand := v.Ranker().bindingDemand(req, f.types.Types["n1-standard-8"])
	var gpu metricDemand
	for _, d := range demand {
		if d.healthMetric == gcecatalog.ZoneMetricQuotaGPU {
			gpu = d
		}
	}
	require.Equal(t, gcecatalog.QuotaTypePreemptibleNvidiaT4GPUs, gpu.quotaType)

	row, judged, _ := v.Ranker().resolveQuotaRow("europe-west4", gpu)
	assert.Equal(t, gcecatalog.QuotaTypePreemptibleNvidiaT4GPUs, judged,
		"the region has a preemptible grant, so the standard GPU pool is not what Spot draws")
	require.NotNil(t, row)
	assert.Equal(t, int64(0), row.LimitValue)
}

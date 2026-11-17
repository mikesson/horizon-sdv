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

package gcecatalog

import (
	"math"
	"strings"
	"time"
)

// QuotaType constants for GCE regional quotas.
const (
	// Standard VM quotas. CPUS is one pool shared by n1 and E2; every other
	// machine family has a pool of its own, flat for n2 and c3 and dimensioned
	// for everything newer.
	QuotaTypeCPUs            = "CPUS"
	QuotaTypeN2CPUs          = "N2_CPUS"
	QuotaTypeC3CPUs          = "C3_CPUS"
	QuotaTypeCPUsPerVMFamily = "CPUS_PER_VM_FAMILY"
	QuotaTypeNvidiaT4GPUs    = "NVIDIA_T4_GPUS"
	QuotaTypeNvidiaL4GPUs    = "NVIDIA_L4_GPUS"
	QuotaTypeNvidiaP4GPUs    = "NVIDIA_P4_GPUS"
	QuotaTypeNvidiaV100GPUs  = "NVIDIA_V100_GPUS"
	QuotaTypeNvidiaA100GPUs  = "NVIDIA_A100_GPUS"
	QuotaTypeNvidiaH100GPUs  = "NVIDIA_H100_GPUS"
	QuotaTypeInUseAddresses  = "IN_USE_ADDRESSES"
	QuotaTypeSSDTotalGB      = "SSD_TOTAL_GB"
	QuotaTypeHDBTotalGB      = "HDB_TOTAL_GB"
	QuotaTypeInstances       = "INSTANCES"

	// Preemptible/Spot VM quotas.
	// When a project has PREEMPTIBLE_* quotas, Spot VMs MUST use them.
	QuotaTypePreemptibleCPUs           = "PREEMPTIBLE_CPUS"
	QuotaTypePreemptibleNvidiaT4GPUs   = "PREEMPTIBLE_NVIDIA_T4_GPUS"
	QuotaTypePreemptibleNvidiaL4GPUs   = "PREEMPTIBLE_NVIDIA_L4_GPUS"
	QuotaTypePreemptibleNvidiaP4GPUs   = "PREEMPTIBLE_NVIDIA_P4_GPUS"
	QuotaTypePreemptibleNvidiaV100GPUs = "PREEMPTIBLE_NVIDIA_V100_GPUS"
	QuotaTypePreemptibleNvidiaA100GPUs = "PREEMPTIBLE_NVIDIA_A100_GPUS"
	QuotaTypePreemptibleNvidiaH100GPUs = "PREEMPTIBLE_NVIDIA_H100_GPUS"
)

// LimitUnlimited is the limit of a quota with no effective ceiling. Compute
// Engine reports one as 2^63-1, which does not survive the float64 the region
// list carries it in: the round-tripped value is above math.MaxInt64 and the
// conversion to int64 is undefined. Readers clamp to this instead, so an
// unlimited pool is a value the judgment and the wire can both state.
const LimitUnlimited int64 = math.MaxInt64

// RegionalQuota represents GCE quota limits and usage for a region.
type RegionalQuota struct {
	// Region is the GCE region (e.g., "us-central1").
	Region string `json:"region"`

	// QuotaType is the type of quota (e.g., "CPUS", "NVIDIA_T4_GPUS").
	QuotaType string `json:"quota_type"`

	// LimitValue is the maximum allowed value for this quota.
	LimitValue int64 `json:"limit_value"`

	// UsageValue is the current usage of this quota.
	UsageValue int64 `json:"usage_value"`

	// LastSyncAt is when this quota was last synced.
	LastSyncAt *time.Time `json:"last_sync_at,omitempty"`

	// CreatedAt is when this record was created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when this record was last updated.
	UpdatedAt time.Time `json:"updated_at"`
}

// Available returns the remaining capacity for this quota.
func (q *RegionalQuota) Available() int64 {
	if q.UsageValue >= q.LimitValue {
		return 0
	}
	return q.LimitValue - q.UsageValue
}

// Percentage returns the usage as a percentage (0-100).
func (q *RegionalQuota) Percentage() float64 {
	if q.LimitValue == 0 {
		return 0
	}
	return float64(q.UsageValue) / float64(q.LimitValue) * 100
}

// IsExhausted returns true if no capacity remains.
func (q *RegionalQuota) IsExhausted() bool {
	return q.UsageValue >= q.LimitValue
}

// CanProvision checks if the requested amount can be provisioned.
func (q *RegionalQuota) CanProvision(amount int64) bool {
	return q.UsageValue+amount <= q.LimitValue
}

// IsNearLimit returns true if usage is at or above the warning threshold (default 85%).
func (q *RegionalQuota) IsNearLimit(thresholdPercent float64) bool {
	if thresholdPercent == 0 {
		thresholdPercent = 85
	}
	return q.Percentage() >= thresholdPercent
}

// IsPreemptibleQuota returns true if this is a preemptible/Spot VM quota.
func (q *RegionalQuota) IsPreemptibleQuota() bool {
	return IsPreemptibleQuotaType(q.QuotaType)
}

// IsPreemptibleQuotaType returns true if the quota type is for preemptible/Spot VMs.
func IsPreemptibleQuotaType(quotaType string) bool {
	switch quotaType {
	case QuotaTypePreemptibleCPUs,
		QuotaTypePreemptibleNvidiaT4GPUs,
		QuotaTypePreemptibleNvidiaL4GPUs,
		QuotaTypePreemptibleNvidiaP4GPUs,
		QuotaTypePreemptibleNvidiaV100GPUs,
		QuotaTypePreemptibleNvidiaA100GPUs,
		QuotaTypePreemptibleNvidiaH100GPUs:
		return true
	}
	return false
}

// computeQuotaMetricPrefix is what a Compute Engine quota metric name is
// qualified by when it is reported structurally rather than as one of the
// region list's bare enum names.
const computeQuotaMetricPrefix = "compute.googleapis.com/"

// Quota metric names whose structured form does not simply uppercase into the
// region list's name for the same pool.
var quotaTypeByMetricName = map[string]string{
	"ssd_total_storage":                 QuotaTypeSSDTotalGB,
	"hyperdisk_balanced_total_storage":  QuotaTypeHDBTotalGB,
	"local_ssd_total_storage":           "LOCAL_SSD_TOTAL_GB",
	"disks_total_storage":               "DISKS_TOTAL_GB",
	"preemptible_local_ssd_total_bytes": "PREEMPTIBLE_LOCAL_SSD_GB",
}

// QuotaTypeForMetricName names the pool a structured quota refusal reports,
// in the identity the catalog holds its rows under.
//
// It exists because a refusal is the one place the exhausted pool is stated
// exactly. The message beside it names the generic metric and drops the
// dimension entirely (a C4 CPU refusal reads "Quota CPUS_PER_VM_FAMILY
// exceeded" with no C4 in it), so the dimensions map is the only place the
// pool that actually ran out appears.
func QuotaTypeForMetricName(metricName string, dimensions map[string]string) string {
	name, _ := strings.CutPrefix(metricName, computeQuotaMetricPrefix)
	if name == "" {
		return ""
	}
	quotaType, ok := quotaTypeByMetricName[name]
	if !ok {
		quotaType = strings.ToUpper(name)
	}
	// Compute Engine's dimensioned quotas select a pool by exactly one
	// dimension besides the region the whole refusal is already scoped to.
	for _, key := range []string{"vm_family", "gpu_family"} {
		if value := dimensions[key]; value != "" {
			return DimensionedQuotaType(quotaType, strings.ToUpper(value))
		}
	}
	return quotaType
}

// IsCPUQuotaType reports whether a quota type is one of the CPU pools, flat or
// dimensioned, on-demand or preemptible.
func IsCPUQuotaType(quotaType string) bool {
	metric, _ := QuotaTypeDimension(quotaType)
	switch metric {
	case QuotaTypeCPUs, QuotaTypeN2CPUs, QuotaTypeC3CPUs,
		QuotaTypeCPUsPerVMFamily, QuotaTypePreemptibleCPUs:
		return true
	}
	return false
}

// IsGPUQuotaType reports whether a quota type is one of the GPU pools.
func IsGPUQuotaType(quotaType string) bool {
	metric, _ := QuotaTypeDimension(quotaType)
	return strings.HasSuffix(metric, "GPUS") || metric == "GPUS_PER_GPU_FAMILY"
}

// HasPreemptibleGrant reports whether a region's quota rows show any
// preemptible grant at all.
//
// The switch is per region, not per pool. A project with no preemptible quota
// in a region draws its Spot VMs from the standard pools; once any preemptible
// quota is granted there, every Spot VM in that region counts against
// preemptible quota and cannot consume standard quota in its place, including
// on an axis whose own preemptible grant is zero.
func HasPreemptibleGrant(rows []*RegionalQuota) bool {
	for _, q := range rows {
		if q.LimitValue > 0 && IsPreemptibleQuotaType(q.QuotaType) {
			return true
		}
	}
	return false
}

// QuotaTypeForGPU returns the standard regional quota metric a GPU type
// draws from, or "" for GPU types without a tracked quota metric. Spot
// requests substitute the preemptible twin via ToPreemptibleQuotaType.
func QuotaTypeForGPU(gpuType string) string {
	switch gpuType {
	case "nvidia-tesla-t4":
		return QuotaTypeNvidiaT4GPUs
	case "nvidia-l4":
		return QuotaTypeNvidiaL4GPUs
	case "nvidia-tesla-p4":
		return QuotaTypeNvidiaP4GPUs
	case "nvidia-tesla-v100":
		return QuotaTypeNvidiaV100GPUs
	case "nvidia-tesla-a100", "nvidia-a100-80gb":
		return QuotaTypeNvidiaA100GPUs
	case "nvidia-h100-80gb":
		return QuotaTypeNvidiaH100GPUs
	default:
		return ""
	}
}

// quotaDimensionSep separates a dimensioned quota metric from the dimension
// value that selects one of its pools. Compute Engine's regional quota list
// carries only flat metric names, so a dimensioned pool has no name of its own
// there; joining the two into one identity keeps every (region, metric) lookup,
// every judgment and the operations wire working unchanged on both kinds.
const quotaDimensionSep = ":"

// DimensionedQuotaType names one pool of a dimensioned quota metric, e.g.
// CPUS_PER_VM_FAMILY at vm_family C4.
func DimensionedQuotaType(metric, dimension string) string {
	if dimension == "" {
		return metric
	}
	return metric + quotaDimensionSep + dimension
}

// QuotaTypeDimension splits a quota type into its metric and the dimension
// value selecting one of its pools; the dimension is empty for a flat metric.
func QuotaTypeDimension(quotaType string) (string, string) {
	metric, dimension, _ := strings.Cut(quotaType, quotaDimensionSep)
	return metric, dimension
}

// CPUQuotaTypeForFamily returns the CPU quota pool one machine family draws
// from, or "" for a family that spends no CPU quota at all.
//
// Compute Engine splits CPU quota three ways. n1 shares one flat CPUS pool
// with E2; n2 and c3 have flat pools of their own; everything newer draws the
// dimensioned CPUS_PER_VM_FAMILY pool selected by its own vm_family, which is
// why an unrecognized family resolves there rather than to the shared pool.
// The dimensioned form is where new families land, and being wrong that way
// yields no quota row and an advisory pass, where being wrong the other way
// would judge a pool the family never touches.
//
// G2 is the one family that spends none: its L4 GPU quota is the whole
// admission control, and charging it vCPUs refuses placements Compute Engine
// would have accepted.
func CPUQuotaTypeForFamily(family string) string {
	switch family {
	case "":
		return "" // a shape with no family charges nothing rather than a guessed pool
	case "n1", "e2":
		return QuotaTypeCPUs
	case "n2":
		return QuotaTypeN2CPUs
	case "c3":
		return QuotaTypeC3CPUs
	case "g2":
		return ""
	default:
		return DimensionedQuotaType(QuotaTypeCPUsPerVMFamily, strings.ToUpper(family))
	}
}

// BootDiskQuotaType returns the regional capacity pool a boot disk of this
// type draws from, or "" when the type draws from none this deployment tracks.
//
// Hyperdisk Balanced counts against HDB_TOTAL_GB and nothing else: Compute
// Engine documents dual-counting for Hyperdisk Extreme and Hyperdisk
// Throughput, and for no other type. A c4/n4/c4a boot disk is therefore
// invisible to SSD_TOTAL_GB, which is the pool the pd-* shapes fill.
func BootDiskQuotaType(diskType string) string {
	switch diskType {
	case "hyperdisk-balanced":
		return QuotaTypeHDBTotalGB
	case "pd-balanced", "pd-ssd":
		return QuotaTypeSSDTotalGB
	default:
		return ""
	}
}

// ToPreemptibleQuotaType converts a standard quota type to its preemptible equivalent.
// Returns the original type if no preemptible equivalent exists.
//
// Every CPU pool maps onto the one flat PREEMPTIBLE_CPUS grant: Compute Engine
// documents preemptible CPU quota as a single pool covering all machine types,
// with no per-family dimension. That claim predates the dimensioned pools and
// there is live counter-evidence: a preemptible_h4d_cpus consumer-quota metric
// exists, for a family that is itself on CPUS_PER_VM_FAMILY. If Spot on a
// dimensioned family turns out to draw a per-family preemptible pool, that pool
// is expressible here as a dimensioned type without changing anything else.
func ToPreemptibleQuotaType(quotaType string) string {
	if metric, _ := QuotaTypeDimension(quotaType); metric == QuotaTypeCPUsPerVMFamily {
		return QuotaTypePreemptibleCPUs
	}
	switch quotaType {
	case QuotaTypeCPUs, QuotaTypeN2CPUs, QuotaTypeC3CPUs:
		return QuotaTypePreemptibleCPUs
	case QuotaTypeNvidiaT4GPUs:
		return QuotaTypePreemptibleNvidiaT4GPUs
	case QuotaTypeNvidiaL4GPUs:
		return QuotaTypePreemptibleNvidiaL4GPUs
	case QuotaTypeNvidiaP4GPUs:
		return QuotaTypePreemptibleNvidiaP4GPUs
	case QuotaTypeNvidiaV100GPUs:
		return QuotaTypePreemptibleNvidiaV100GPUs
	case QuotaTypeNvidiaA100GPUs:
		return QuotaTypePreemptibleNvidiaA100GPUs
	case QuotaTypeNvidiaH100GPUs:
		return QuotaTypePreemptibleNvidiaH100GPUs
	default:
		return quotaType
	}
}

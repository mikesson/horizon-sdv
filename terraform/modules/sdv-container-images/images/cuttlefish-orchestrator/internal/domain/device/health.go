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

package device

import "time"

// HealthStatus represents the health state the device controller records for
// a device.
//
// Health status is determined by periodic health checks that verify the
// device is responsive and functioning correctly. Consecutive failures
// cause the status to degrade from healthy to unhealthy to zombie.
type HealthStatus string

// Health statuses, in order of degradation.
const (
	HealthStatusUnknown   HealthStatus = "unknown"   // Health not yet checked
	HealthStatusHealthy   HealthStatus = "healthy"   // Device is responsive
	HealthStatusUnhealthy HealthStatus = "unhealthy" // Device not responding
	HealthStatusZombie    HealthStatus = "zombie"    // Process exists but unresponsive
)

// HealthCheckConfig configures the behavior of device health monitoring.
//
// These settings control how frequently devices are checked and how failures
// are counted.
type HealthCheckConfig struct {
	// Interval is the time between consecutive health checks for each device.
	Interval time.Duration

	// FailureThreshold is the number of consecutive failures required
	// before marking a device as unhealthy.
	FailureThreshold int

	// ZombieThreshold is the number of consecutive failures required
	// before marking a device as a zombie (unrecoverable).
	ZombieThreshold int

	// SweepOrphanCVDs lets the CVD census delete groups no device row
	// claims. Default false: detect and alert only.
	SweepOrphanCVDs bool

	// ZombieRecreate enables health-driven CVD recovery: the zombie
	// suspector files device.reconcile for a running+zombie device, which
	// recreates the CVD from desired_spec (bounded budget) or fails it with a
	// reason. Default false: detect only (today's behavior).
	ZombieRecreate bool
}

// HealthUpdate is one device's health-check outcome for BatchUpdateHealth.
type HealthUpdate struct {
	DeviceID         string
	HostID           string // Devices are interleaved in hosts; both keys are required.
	Status           HealthStatus
	ConsecutiveFails int64
	FailReason       string
	// CVDStatus is the raw CVD observation recorded this cycle (one of the
	// CVDState* values, or "" when the CVD was absent from the fleet). For a
	// device with more than one instance it is FoldInstanceStates over
	// Instances below.
	CVDStatus string
	// Instances is the instance array observed this cycle. Nil means "not
	// observed this cycle" and leaves the stored array untouched, the same
	// contract as BootObs; a non-nil empty array is a real observation of a
	// group with nothing in it. Rides the same mutation as CVDStatus, because
	// CVDStatus is derived from it and the two must never be read as a
	// mismatched pair.
	Instances []Instance
	// BootObs is this cycle's boot-progress observation, when the cycle
	// enriched one. Nil means "not sampled this cycle" and leaves the stored
	// observation untouched. It never blanks the column. Non-nil rides the
	// same mutation as CVDStatus (one sample) and forces the full write path
	// past the TimestampOnly dedup.
	BootObs *BootObservation
	// TimestampOnly marks an unchanged healthy result whose CVD observation is
	// also unchanged: only last_health_check and last_healthy_at advance.
	TimestampOnly bool
}

// DefaultHealthCheckConfig returns the default health check configuration.
// These defaults are suitable for most production deployments.
func DefaultHealthCheckConfig() HealthCheckConfig {
	return HealthCheckConfig{
		Interval:         1 * time.Minute,
		FailureThreshold: 3,  // 3 failures = unhealthy
		ZombieThreshold:  10, // 10 failures = zombie
	}
}

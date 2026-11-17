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

// Executable form of the lease invariants. The prose that recorded them said
// breaking one strands or destroys real VMs instead of failing a test.
//
// These assert RELATIONS between the constants, never their values. The
// existing failsafe tests check that the built instance carries
// hostMaxRunDurationSeconds, which is true however that constant is defined:
// they follow a change rather than catching one. Host leases have no cap, so
// no constant bounds the longest lease. What keeps the failsafe from firing
// inside a granted lease is the horizon check every lease raise passes
// through, and these pin that relation.

package hostfeature

import (
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// gceMaxRunDurationSeconds is the largest max_run_duration Compute Engine
// accepts (120 days).
const gceMaxRunDurationSeconds int64 = 120 * 24 * 3600

// The failsafe starts as far out as GCE allows. GCE moves it only on a stopped
// VM, so every second below the maximum is a second of uptime a growing lease
// can never use.
func TestFailsafeIsTheGCEMaximum(t *testing.T) {
	t.Parallel()
	if hostMaxRunDurationSeconds != gceMaxRunDurationSeconds {
		t.Fatalf("max_run_duration is %ds, want the GCE maximum %ds: leases are uncapped "+
			"and the failsafe cannot be moved on a running VM", hostMaxRunDurationSeconds, gceMaxRunDurationSeconds)
	}
}

// A lease ending at the VM horizon minus the grace is the last one the check
// admits; one second more is refused. With the failsafe as the horizon this is
// the statement that the failsafe never fires inside a lease plus its teardown
// grace.
func TestHorizonAdmitsLeasesUpToTheFailsafeLessGrace(t *testing.T) {
	t.Parallel()
	now := time.Now()
	terminates := now.Add(time.Duration(hostMaxRunDurationSeconds) * time.Second)
	last := terminates.Add(-time.Duration(host.GracePeriodSeconds) * time.Second)

	if err := host.CheckVMHorizon("h", last, &terminates); err != nil {
		t.Fatalf("a lease ending exactly grace before the VM stops was refused: %v", err)
	}
	if err := host.CheckVMHorizon("h", last.Add(time.Second), &terminates); err == nil {
		t.Fatal("a lease whose grace passes the VM stop time was admitted: GCE would stop the VM under it")
	}
}

// The longest create-time lease, and the largest single host extend, must fit
// under the failsafe of a VM that has just started. Otherwise a fresh host
// could be refused its own create-time TTL.
func TestFreshHostTakesTheLongestSingleGrant(t *testing.T) {
	t.Parallel()
	now := time.Now()
	terminates := now.Add(time.Duration(hostMaxRunDurationSeconds) * time.Second)
	lease := now.Add(time.Duration(host.MaxHostTTLHours) * time.Hour)
	if err := host.CheckVMHorizon("h", lease, &terminates); err != nil {
		t.Fatalf("a fresh host cannot take a %dh lease under its own failsafe: %v", host.MaxHostTTLHours, err)
	}
}

// The default host TTL must be grantable. A default above the cap would make
// every host created without an explicit ttl_hours fail validation, and a
// default of zero would give hosts no lease at all.
func TestDefaultHostTTLIsWithinTheCap(t *testing.T) {
	t.Parallel()
	if host.DefaultHostTTLSeconds <= 0 {
		t.Fatalf("default host TTL is %ds; a host with no lease is never reaped",
			host.DefaultHostTTLSeconds)
	}
	if maxLease := host.MaxHostTTLHours * 3600; host.DefaultHostTTLSeconds > maxLease {
		t.Fatalf("default host TTL %ds exceeds the cap %ds the API enforces",
			host.DefaultHostTTLSeconds, maxLease)
	}
}

// A host must be older than a full host create before its emptiness may be read
// as "drained".
//
// This pins the relation between the two values. A host row exists from
// admission and holds no device until one commits onto it, which is a
// provisioning leg later at best.
// Set the guard at or below the leg and the fleet starts shortening the leases
// of hosts its own creates are still filling. The symptom is a create landing
// on a host already queued for the reaper, never a failed shorten.
func TestShortenGuardOutlastsAHostCreate(t *testing.T) {
	t.Parallel()
	if host.MinShortenAge <= host.ProvisioningLeg {
		t.Fatalf("MinShortenAge is %v but a host create takes %v: an empty host would be "+
			"judged drained while its own create is still filling it",
			host.MinShortenAge, host.ProvisioningLeg)
	}
	// And it must stay a guard rather than becoming a lifetime: a host that
	// cannot be reclaimed within its own lease is never reclaimed at all.
	if lease := time.Duration(host.DefaultHostTTLSeconds) * time.Second; host.MinShortenAge >= lease {
		t.Fatalf("MinShortenAge %v is not smaller than the default host lease %v", host.MinShortenAge, lease)
	}
}

// The grace margin must be positive and must not swallow the lease it
// protects. It exists so a device's final minutes are never spent on a host
// already being reaped; a zero grace removes that guarantee silently.
func TestGracePeriodIsAMarginNotALease(t *testing.T) {
	t.Parallel()
	if host.GracePeriodSeconds <= 0 {
		t.Fatalf("grace is %ds: a device's expiry would raise its host's to exactly "+
			"its own, leaving the host reapable in the device's final moments",
			host.GracePeriodSeconds)
	}
	if host.GracePeriodSeconds >= host.DefaultHostTTLSeconds {
		t.Fatalf("grace %ds is not smaller than the default host lease %ds",
			host.GracePeriodSeconds, host.DefaultHostTTLSeconds)
	}
}

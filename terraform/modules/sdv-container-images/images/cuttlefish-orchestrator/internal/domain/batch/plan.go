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

package batch

// HostSlot is one placeable host and its free device capacity as the planner
// sees it (max_devices - current_device_count). For PlacementPack the caller
// passes these most-loaded-first so nearly-full hosts fill before emptier ones.
type HostSlot struct {
	HostID string
	Free   int
}

// Assignment places Count devices onto an existing host.
type Assignment struct {
	HostID string
	Count  int
}

// Plan is the resolved placement for a batch: devices packed onto existing
// hosts plus the number of new hosts to provision (each holding up to
// PerNewHost devices; the last may hold fewer). Admitted is how many devices
// the plan actually places. It equals the requested count unless clamped by
// host quota headroom, in which case the caller decides between rejecting the
// request and admitting a smaller batch.
type Plan struct {
	OnExisting []Assignment
	NewHosts   int
	PerNewHost int
	Admitted   int
}

// Assign expands the plan into a concrete per-device host-id list (length
// Admitted): existing-host slots first, then the new hosts in order using the
// provisioned ids. The caller must provision exactly Plan.NewHosts hosts first
// and pass their ids in order; fewer ids panics (a programming error).
func (p Plan) Assign(newHostIDs []string) []string {
	out := make([]string, 0, p.Admitted)
	for _, a := range p.OnExisting {
		for range a.Count {
			out = append(out, a.HostID)
		}
	}
	for h := 0; h < p.NewHosts && len(out) < p.Admitted; h++ {
		for range min(p.PerNewHost, p.Admitted-len(out)) {
			out = append(out, newHostIDs[h])
		}
	}
	return out
}

// Compute plans how to place count devices. PlacementIsolated ignores existing
// capacity and provisions dedicated hosts; PlacementPack fills existing free
// slots first (most-loaded-first, as ordered by the caller) then provisions the
// remainder. maxPerNewHost is the per-host device cap of a provisioned host;
// hostHeadroom (MaxHosts - UsedHosts) bounds new-host provisioning. When the
// full count cannot fit within headroom, Admitted is clamped to the feasible
// maximum.
func Compute(count int, placement Placement, existing []HostSlot, maxPerNewHost, hostHeadroom int) Plan {
	if maxPerNewHost < 1 {
		maxPerNewHost = 1
	}
	if hostHeadroom < 0 {
		hostHeadroom = 0
	}
	p := Plan{PerNewHost: maxPerNewHost}

	remaining := count
	if placement == PlacementPack {
		for _, h := range existing {
			if remaining == 0 {
				break
			}
			take := min(remaining, h.Free)
			if take > 0 {
				p.OnExisting = append(p.OnExisting, Assignment{HostID: h.HostID, Count: take})
				remaining -= take
			}
		}
	}

	wantNew := ceilDiv(remaining, maxPerNewHost)
	if wantNew <= hostHeadroom {
		p.NewHosts = wantNew
		p.Admitted = count
		return p
	}

	// Not enough host quota for the remainder: clamp to what fits.
	p.NewHosts = hostHeadroom
	onExisting := count - remaining
	p.Admitted = min(onExisting+hostHeadroom*maxPerNewHost, count)
	return p
}

// ceilDiv returns ceil(a/b) for non-negative a and positive b; 0 when a <= 0.
func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

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

package hostconfig

// SweepPolicy is what the background loops are allowed to do on their own.
//
// It lives here rather than in the settings slice because two surfaces serve
// it: the settings endpoint an operator writes through, and the control-plane
// snapshot the operations view polls. One type, so the two answers cannot
// disagree.
//
// The three sweeps default off, so a loop that deletes unattended is granted
// that power explicitly. ReaperDryRun defaults off meaning the reaper does
// delete, so it is the one field where false means the working state and true
// the safe one.
type SweepPolicy struct {
	DeleteOrphanVMs bool `json:"delete_orphan_vms" doc:"Let the host reconciler delete managed GCE instances with no host row (off = detect only)"`
	OrphanCVDs      bool `json:"orphan_cvds" doc:"Let the device controller delete CVD groups with no device row (off = detect only)"`
	ZombieRecreate  bool `json:"zombie_recreate" doc:"Let the device controller recreate a CVD whose guest is gone (off = detect only)"`
	ReaperDryRun    bool `json:"reaper_dry_run" doc:"Suspend the reaper's and batch settlement's deletions, logging what they would have done"`
	// IsSet reports whether an operator stored any of the four, so a console
	// can say "compiled default" rather than implying a choice.
	IsSet bool `json:"is_set,omitempty" doc:"Whether an operator stored any of these; false means the compiled defaults alone apply"`
}

// SweepPolicyPatch names the switches to change; nil fields are left alone.
// One switch flip is one field, so it lands one audited config change instead
// of rewriting the other three.
type SweepPolicyPatch struct {
	DeleteOrphanVMs *bool
	OrphanCVDs      *bool
	ZombieRecreate  *bool
	ReaperDryRun    *bool
}

// IsEmpty reports whether the patch would change nothing.
func (p SweepPolicyPatch) IsEmpty() bool {
	return p.DeleteOrphanVMs == nil && p.OrphanCVDs == nil &&
		p.ZombieRecreate == nil && p.ReaperDryRun == nil
}

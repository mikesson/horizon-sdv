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

package hostconfigfeature_test

import (
	"context"
	"errors"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// rowRepo serves fixed rows; Get of an absent family is the NotFoundError the
// Spanner repository returns.
type rowRepo struct {
	failingRepo
	rows map[string]*hostconfig.HostImageConfig
}

func (r *rowRepo) Get(_ context.Context, family string) (*hostconfig.HostImageConfig, error) {
	row, ok := r.rows[family]
	if !ok {
		return nil, &hostconfig.NotFoundError{MachineFamily: family}
	}
	clone := *row
	return &clone, nil
}

func (r *rowRepo) List(_ context.Context, _ hostconfig.ListFilter) ([]*hostconfig.HostImageConfig, error) {
	out := make([]*hostconfig.HostImageConfig, 0, len(r.rows))
	for _, row := range r.rows {
		clone := *row
		out = append(out, &clone)
	}
	return out, nil
}

func enabledRows() *rowRepo {
	return &rowRepo{rows: map[string]*hostconfig.HostImageConfig{
		"n1":  {MachineFamily: "n1", ImageFamily: "cco-host", Enabled: true},
		"c4a": {MachineFamily: "c4a", ImageFamily: "cco-host", Enabled: true},
	}}
}

// A refusal is never cached, so an operator's fix is seen by the next create
// on every replica. A success is, so a burst of creates makes one lookup.
func TestRefusalsAreNeverCached(t *testing.T) {
	t.Parallel()
	images := &failingImages{err: true}
	svc := newService(enabledRows(), &failingSettings{}, images)

	_, err := svc.ResolveImage(t.Context(), "n1", machinetype.ArchX86)
	if unavailable, ok := errors.AsType[*hostconfig.ImageUnavailableError](err); !ok || unavailable.Reason != hostconfig.ImageNotFound {
		t.Fatalf("first resolve = %v, want not_found", err)
	}

	images.err = false // the operator publishes the image
	img, err := svc.ResolveImage(t.Context(), "n1", machinetype.ArchX86)
	if err != nil {
		t.Fatalf("resolve after the fix = %v, want the image", err)
	}
	if img.Project != deploymentProject {
		t.Errorf("resolved in %q, want %q", img.Project, deploymentProject)
	}

	calls := images.timesCalled
	if _, err := svc.ResolveImage(t.Context(), "n1", machinetype.ArchX86); err != nil {
		t.Fatalf("cached resolve: %v", err)
	}
	if images.timesCalled != calls {
		t.Errorf("a success was looked up again (%d calls, want %d)", images.timesCalled, calls)
	}
}

// An image that declares no architecture is x86 to GCE, so it cannot serve an
// arm64 family.
func TestArmFamilyAgainstAnX86ImageIsArchMismatch(t *testing.T) {
	t.Parallel()
	svc := newService(enabledRows(), &failingSettings{}, &failingImages{})

	_, err := svc.ResolveImage(t.Context(), "c4a", machinetype.ArchArm)
	unavailable, ok := errors.AsType[*hostconfig.ImageUnavailableError](err)
	if !ok || unavailable.Reason != hostconfig.ImageArchMismatch {
		t.Fatalf("resolve = %v, want arch_mismatch", err)
	}
	if unavailable.MachineFamily != "c4a" || unavailable.ImageFamily != "cco-host" {
		t.Errorf("refusal = %+v, want it to name c4a and cco-host", unavailable)
	}
}

// A family with no row is not provisionable. There is no fallback image.
func TestFamilyWithNoRowIsNoConfig(t *testing.T) {
	t.Parallel()
	images := &failingImages{}
	svc := newService(enabledRows(), &failingSettings{}, images)

	_, err := svc.ResolveImage(t.Context(), "g2", machinetype.ArchX86)
	if unavailable, ok := errors.AsType[*hostconfig.ImageUnavailableError](err); !ok || unavailable.Reason != hostconfig.ImageNoConfig {
		t.Fatalf("resolve = %v, want no_config", err)
	}
	if images.timesCalled != 0 {
		t.Errorf("a family with no row reached GCE %d times", images.timesCalled)
	}
}

// The admin list carries each row's status from the same resolution.
func TestListReportsEachRowsResolution(t *testing.T) {
	t.Parallel()
	repo := enabledRows()
	repo.rows["g2"] = &hostconfig.HostImageConfig{MachineFamily: "g2", ImageFamily: "cco-host-gpu", Enabled: false}
	svc := newService(repo, &failingSettings{}, &failingImages{})

	rows, _, err := svc.List(t.Context(), hostconfig.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, row := range rows {
		if row.Resolved == nil {
			t.Fatalf("%s has no resolved block", row.MachineFamily)
		}
		switch row.MachineFamily {
		case "g2":
			if row.Resolved.OK || row.Resolved.Reason != hostconfig.ImageNoConfig {
				t.Errorf("disabled g2 resolved = %+v, want no_config", row.Resolved)
			}
		default:
			if !row.Resolved.OK || row.Resolved.ImageName != "cco-host-1" {
				t.Errorf("%s resolved = %+v, want ok on cco-host-1", row.MachineFamily, row.Resolved)
			}
		}
	}
}

// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"testing"

	"github.com/acn-horizon-sdv/module-manager/internal/controller"
)

func TestAttachRevisionInfoPinnedAndFollowing(t *testing.T) {
	h := &Handler{targetRevision: "env/sbx"}

	pinned := &ModuleResponse{Name: "workloads-common"}
	statePinned := &controller.State{
		ModuleTargetRevisions: map[string]string{"workloads-common": "feature/test"},
	}
	h.attachRevisionInfo(pinned, statePinned)
	if !pinned.Pinned {
		t.Fatal("expected pinned module")
	}
	if pinned.TargetRevision != "feature/test" {
		t.Fatalf("targetRevision: got %q", pinned.TargetRevision)
	}
	if pinned.ClusterTargetRevision != "env/sbx" {
		t.Fatalf("clusterTargetRevision: got %q", pinned.ClusterTargetRevision)
	}

	following := &ModuleResponse{Name: "workloads-android"}
	stateFollowing := &controller.State{}
	h.attachRevisionInfo(following, stateFollowing)
	if following.Pinned {
		t.Fatal("expected following module")
	}
	if following.TargetRevision != "env/sbx" {
		t.Fatalf("targetRevision: got %q", following.TargetRevision)
	}
}

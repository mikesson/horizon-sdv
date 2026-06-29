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

package controller

import "testing"

func TestEffectiveTargetRevision(t *testing.T) {
	def := "HEAD"
	st := &State{
		ModuleTargetRevisions: map[string]string{"a": "main", "b": "  v1  "},
	}
	if got := EffectiveTargetRevision(st, "a", def); got != "main" {
		t.Fatalf("a: got %q", got)
	}
	if got := EffectiveTargetRevision(st, "b", def); got != "v1" {
		t.Fatalf("b: got %q", got)
	}
	if got := EffectiveTargetRevision(st, "missing", def); got != "HEAD" {
		t.Fatalf("missing: got %q", got)
	}
	if got := EffectiveTargetRevision(nil, "x", "  develop  "); got != "develop" {
		t.Fatalf("nil state: got %q", got)
	}
}

func TestIsModulePinned(t *testing.T) {
	st := &State{
		ModuleTargetRevisions: map[string]string{"pinned": "main", "empty": "  "},
	}
	if !IsModulePinned(st, "pinned") {
		t.Fatal("expected pinned")
	}
	if IsModulePinned(st, "missing") {
		t.Fatal("expected not pinned for missing key")
	}
	if IsModulePinned(st, "empty") {
		t.Fatal("expected whitespace-only ref to be unpinned")
	}
	if IsModulePinned(nil, "x") {
		t.Fatal("expected nil state to be unpinned")
	}
}

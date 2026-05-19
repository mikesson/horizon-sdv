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
//
package workflow

import (
	"errors"
	"testing"
)

func TestMatchOutputArtifacts(t *testing.T) {
	arts := []OutputArtifact{
		{NodeID: "n1", Name: "a", TemplateName: "t1", GcsURI: "gs://b/o1"},
		{NodeID: "n2", Name: "a", TemplateName: "t2", GcsURI: "gs://b/o2"},
		{NodeID: "n3", Name: "b", GcsURI: "gs://b/o3"},
	}
	_, err := MatchOutputArtifacts(arts, "a", "", "")
	var amb *AmbiguousArtifactsError
	if !errors.As(err, &amb) || len(amb.Candidates) != 2 {
		t.Fatalf("expected ambiguous, got %v", err)
	}
	got, err := MatchOutputArtifacts(arts, "a", "n2", "")
	if err != nil || len(got) != 1 || got[0].GcsURI != "gs://b/o2" {
		t.Fatalf("got %v %v", got, err)
	}
	got, err = MatchOutputArtifacts(arts, "a", "", "t2")
	if err != nil || len(got) != 1 || got[0].NodeID != "n2" {
		t.Fatalf("templateName filter: got %v %v", got, err)
	}
	_, err = MatchOutputArtifacts(arts, "a", "n1", "t2")
	if err == nil {
		t.Fatal("expected no match for conflicting nodeId+templateName")
	}
	_, err = MatchOutputArtifacts(arts, "missing", "", "")
	if err == nil {
		t.Fatal("expected no match")
	}
}

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
	"fmt"
	"strings"
)

// AmbiguousArtifactsError is returned when several output artifacts share the same name
// and the caller did not narrow with nodeId or templateName (or filters still match more than one).
type AmbiguousArtifactsError struct {
	ArtifactName string
	Candidates   []OutputArtifact
}

func (e *AmbiguousArtifactsError) Error() string {
	return fmt.Sprintf("ambiguous: %d artifacts named %q; pass nodeId or templateName", len(e.Candidates), e.ArtifactName)
}

// MatchOutputArtifacts returns artifacts whose Name equals artifactName.
// If nodeID is non-empty, only artifacts with that NodeID are considered.
// If templateName is non-empty, only artifacts with that TemplateName are considered.
// When both are set, both must match. Returns *AmbiguousArtifactsError when multiple matches
// remain after filters (or when none are set and multiple artifacts share the name).
func MatchOutputArtifacts(arts []OutputArtifact, artifactName, nodeID, templateName string) ([]OutputArtifact, error) {
	wantNode := strings.TrimSpace(nodeID)
	wantTpl := strings.TrimSpace(templateName)
	var matches []OutputArtifact
	for _, a := range arts {
		if a.Name != artifactName {
			continue
		}
		if wantNode != "" && a.NodeID != wantNode {
			continue
		}
		if wantTpl != "" && a.TemplateName != wantTpl {
			continue
		}
		matches = append(matches, a)
	}
	if len(matches) == 0 {
		if wantNode != "" || wantTpl != "" {
			return nil, fmt.Errorf("no artifact named %q matching filters", artifactName)
		}
		return nil, fmt.Errorf("no artifact named %q", artifactName)
	}
	if len(matches) > 1 {
		return nil, &AmbiguousArtifactsError{ArtifactName: artifactName, Candidates: matches}
	}
	return matches, nil
}

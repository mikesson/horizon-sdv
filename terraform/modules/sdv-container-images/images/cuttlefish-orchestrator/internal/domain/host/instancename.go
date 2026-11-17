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

package host

import (
	"fmt"
	"regexp"
	"strings"
)

// InstancePrefix namespaces every CCO-managed GCE instance name. It is
// reserved in the display-name namespace, because CreateRequest.Name is a
// human label, and a label carrying this prefix would compose into "cf-cf-…" here
// and be indistinguishable from a binding downstream. Reserving it is what
// keeps the two namespaces disjoint by construction.
const InstancePrefix = "cf-"

// DisplayNamePattern is the label namespace: what a caller may send as a
// host's name. The create request's `pattern` tag is what actually enforces it
// (a struct tag cannot reference a constant), so the two are bound by an
// agreement test in the host feature slice.
//
// It cannot express the InstancePrefix reservation (RE2 has no lookahead);
// ValidateDisplayName carries that half.
const DisplayNamePattern = "^[a-z][a-z0-9-]*$"

// InstanceName derives the GCE instance name for one incarnation of a host:
// "cf-{sanitized_name}-{uuid8}" ("cf-{uuid8}" without a name), plus
// "-i{incarnation}" for incarnation ≥ 1. Incarnation 0 keeps the legacy
// unsuffixed form so every pre-incarnation row/VM pair stays matched.
//
// The function is PURE and deterministic: given the host's stored name, ID,
// and an incarnation number, it reproduces the exact instance name that
// incarnation used, which is what lets the orphan GC compute the complete
// set of a live host's stale-incarnation husks without parsing heuristics.
//
// Length budgeting comes out of the sanitized NAME, never the uuid8 identity
// segment or the incarnation suffix: truncating from the end (the previous
// formula) cut into the uuid for maximum-length names and narrowed the
// uniqueness margin. GCE instance names must be lowercase, start with a
// letter, contain only letters, numbers, and hyphens, and be 1-63 characters.
func InstanceName(userInput, hostID string, incarnation int64) string {
	const maxLen = 63
	// Lowercased because only userInput passes through the sanitizer: the
	// identity segment was spliced in as stored, so an uppercase UUID produced
	// a name GCE rejects ("cf-0-000000A0"). Generated IDs are lowercase today,
	// which is the only reason this never fired, and lowercasing hex is
	// identity for every one of them. RFC 4122 requires readers to accept
	// uppercase, so nothing upstream guarantees it stays that way.
	uuidSuffix := strings.ToLower(strings.ReplaceAll(hostID, "-", ""))[:8]
	incSuffix := ""
	if incarnation > 0 {
		incSuffix = fmt.Sprintf("-i%d", incarnation)
	}

	sanitized := sanitizeForGCE(userInput)
	if sanitized != "" {
		// InstancePrefix + name + "-" + uuid8 + incSuffix
		budget := maxLen - len(InstancePrefix) - 1 - len(uuidSuffix) - len(incSuffix)
		if len(sanitized) > budget {
			sanitized = strings.TrimRight(sanitized[:budget], "-")
		}
	}
	if sanitized == "" {
		return InstancePrefix + uuidSuffix + incSuffix
	}
	return InstancePrefix + sanitized + "-" + uuidSuffix + incSuffix
}

// ValidateDisplayName rejects a display name that reaches into the binding
// namespace or into the ID namespace. The create request's pattern tag cannot
// express either (Go's RE2 has no lookahead), so the rule lives beside the
// prefix it protects.
func ValidateDisplayName(name string) error {
	if strings.HasPrefix(name, InstancePrefix) {
		return &ReservedNameError{Name: name}
	}
	if IDShaped(name) {
		return &IDShapedNameError{Name: name}
	}
	return nil
}

// idShape matches the two UUID spellings the name pattern admits: hyphenated,
// and the 32-digit form that format:"uuid" also accepts.
var idShape = regexp.MustCompile(`^(?i:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{32})$`)

// IDShaped reports whether a display name would parse as a UUID. Clients take
// a name or an ID in the same argument, so a name that is also an ID is
// ambiguous. Device names follow the same rule.
func IDShaped(name string) bool {
	return idShape.MatchString(name)
}

// sanitizeForGCE reduces a user-facing name to GCE's instance-name alphabet:
// lowercase, spaces/underscores to hyphens, everything outside [a-z0-9-]
// dropped, hyphen runs collapsed, no leading/trailing hyphens. Length is the
// caller's concern (InstanceName budgets it against the fixed segments).
func sanitizeForGCE(input string) string {
	if input == "" {
		return ""
	}

	s := strings.ToLower(input)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")

	var result strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}
	s = result.String()

	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}

	return strings.Trim(s, "-")
}

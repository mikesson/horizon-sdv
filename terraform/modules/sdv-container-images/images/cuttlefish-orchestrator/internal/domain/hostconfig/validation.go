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

import (
	"fmt"
	"regexp"
	"strings"
)

// Validation patterns for machine family and image family names.
var (
	// machineFamilyPattern matches valid GCE machine family prefixes.
	// Examples: "n1", "g2", "c4a", "n2d"
	// Rules: 1-10 lowercase alphanumeric characters, must start with letter.
	machineFamilyPattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,9}$`)

	// imageFamilyPattern matches valid GCE image family names.
	// Rules: 1-63 characters, lowercase letters, numbers, and hyphens.
	// Must start with a letter, cannot end with a hyphen, no consecutive hyphens.
	imageFamilyPattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

	// gcpProjectPattern matches valid GCP project IDs.
	// Rules: 6-30 characters, lowercase letters, numbers, and hyphens.
	gcpProjectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
)

// FieldImageFamily names the image family in a ValidationError, including the
// ones the host image config feature raises after the domain rules pass.
const FieldImageFamily = "image_family"

// ValidationError represents a validation failure.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation error for %s: %s", e.Field, e.Message)
}

// ValidateMachineFamily validates a machine family identifier.
func ValidateMachineFamily(family string) error {
	if family == "" {
		return &ValidationError{
			Field:   "machine_family",
			Message: "machine family is required",
		}
	}

	if !machineFamilyPattern.MatchString(family) {
		return &ValidationError{
			Field:   "machine_family",
			Message: "must be 1-10 lowercase alphanumeric characters, starting with a letter",
		}
	}

	return nil
}

// ValidateImageFamily validates a GCE image family name.
func ValidateImageFamily(family string) error {
	if family == "" {
		return &ValidationError{
			Field:   FieldImageFamily,
			Message: "image family is required",
		}
	}

	if len(family) > 63 {
		return &ValidationError{
			Field:   FieldImageFamily,
			Message: "must not exceed 63 characters",
		}
	}

	if strings.Contains(family, "--") {
		return &ValidationError{
			Field:   FieldImageFamily,
			Message: "must not contain consecutive hyphens",
		}
	}

	if !imageFamilyPattern.MatchString(family) {
		return &ValidationError{
			Field:   FieldImageFamily,
			Message: "must be lowercase letters, numbers, and hyphens; start with a letter; not end with a hyphen",
		}
	}

	return nil
}

// ValidateSourceProject validates an optional GCP project ID.
func ValidateSourceProject(project string) error {
	if project == "" {
		// Empty is valid - means use system default.
		return nil
	}

	if len(project) < 6 || len(project) > 30 {
		return &ValidationError{
			Field:   "source_project",
			Message: "must be 6-30 characters",
		}
	}

	if !gcpProjectPattern.MatchString(project) {
		return &ValidationError{
			Field:   "source_project",
			Message: "must be lowercase letters, numbers, and hyphens; start with a letter",
		}
	}

	return nil
}

// ValidateHostImageConfig validates all fields of a HostImageConfig.
func ValidateHostImageConfig(config *HostImageConfig) error {
	if err := ValidateMachineFamily(config.MachineFamily); err != nil {
		return err
	}

	if err := ValidateImageFamily(config.ImageFamily); err != nil {
		return err
	}

	if err := ValidateSourceProject(config.SourceProject); err != nil {
		return err
	}

	if len(config.Description) > 500 {
		return &ValidationError{
			Field:   "description",
			Message: "must not exceed 500 characters",
		}
	}

	return nil
}

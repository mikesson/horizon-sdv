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

package gcecatalog

import (
	"time"
)

// Accelerator represents GPU/accelerator availability in a specific zone.
type Accelerator struct {
	// Region is the parent region (e.g., "us-central1").
	Region string `json:"region"`

	// Zone is the zone identifier (e.g., "us-central1-a").
	Zone string `json:"zone"`

	// AcceleratorType is the accelerator type (e.g., "nvidia-tesla-t4").
	AcceleratorType string `json:"accelerator_type"`

	// IsAvailable indicates if this accelerator is available in the zone.
	IsAvailable bool `json:"is_available"`

	// MaxCount is the maximum number of accelerators that can be attached.
	MaxCount int64 `json:"max_count,omitempty"`

	// CreatedAt is when this record was created.
	CreatedAt time.Time `json:"created_at"`
}

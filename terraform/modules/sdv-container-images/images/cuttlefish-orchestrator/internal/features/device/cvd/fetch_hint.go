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

package cvd

import (
	"fmt"
	"slices"
	"strings"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// launcherMissingSignature is the text cvd prints when the host tools it
// fetched hold no launcher: "'<dir>/bin/' does not contain any of
// '[cvd_internal_start, launch_cvd]'.". A cvd that cannot fetch URL sources
// leaves that directory empty and fails here instead of at the fetch, so the
// fetch failure itself never reaches the error.
const launcherMissingSignature = "does not contain any of"

// withURLFetchHint puts the likely cause in front of a cvd load failure for a
// create that fetches a gs:// or https:// source, naming the image the host
// booted. The hint leads because the wrapped error carries cvd's full stderr
// and the device error_message column keeps only its first 1000 runes. Any
// other failure is returned unchanged.
func withURLFetchHint(err error, req map[string]any, h *host.Host) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !strings.Contains(msg, launcherMissingSignature) || !strings.Contains(msg, "launch_cvd") {
		return err
	}
	if !hasURLSource(req["common"]) && !hasURLSource(req["instances"]) {
		return err
	}
	image := "the host image"
	if h.BootImage != "" {
		image = "host image " + h.BootImage
	}
	return fmt.Errorf("%s may predate URL build sources "+
		"(cuttlefish 1.58, or nightly 1.58.0~git202610160155 or later): %w", image, err)
}

// hasURLSource reports whether any string in a JSON-shaped env_config value
// is a gs:// or https:// URL. The fetch section is left out by the caller,
// because its api_base_url names the Android Build endpoint and no build.
func hasURLSource(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.HasPrefix(t, "gs://") || strings.HasPrefix(t, "https://")
	case map[string]any:
		for _, elem := range t {
			if hasURLSource(elem) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(t, hasURLSource)
	}
	return false
}

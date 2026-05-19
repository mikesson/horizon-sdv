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
package gcs

const (
	defaultSignedURLSeconds = 600
	minSignedURLSeconds     = 60
	// maxSignedURLSeconds caps lifetime below 12h (security / org policy; IAM SignBlob still needs no exported keys).
	maxSignedURLSeconds = 12*3600 - 1
)

// ClampDurationSeconds applies signed-URL TTL rules: 0 or negative → default (600);
// otherwise clamp to [60, maxSignedURLSeconds] (< 12h).
func ClampDurationSeconds(seconds int) int {
	if seconds <= 0 {
		return defaultSignedURLSeconds
	}
	if seconds < minSignedURLSeconds {
		return minSignedURLSeconds
	}
	if seconds > maxSignedURLSeconds {
		return maxSignedURLSeconds
	}
	return seconds
}

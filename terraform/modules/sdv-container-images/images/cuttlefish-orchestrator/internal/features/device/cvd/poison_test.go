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
	"errors"
	"fmt"
	"testing"
)

func TestIsHostPoisonError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated failure", errors.New("connection refused"), false},
		{"empty", errors.New(""), false},
		{"leaked tap", errors.New("failed to validate tap devices for instance 1"), true},
		{"tap in use", errors.New("tap device in use: cvd-etap-01"), true},
		{"stale registration", errors.New("Device Id already registered"), true},
		{"pre-registered", errors.New("Device Id already pre-registered"), true},
		// The signature can sit anywhere in a wrapped, multi-line HO 500 body.
		{"wrapped", fmt.Errorf("creating CVD: %w",
			errors.New("HTTP 500: run_cvd: tap interface eth0 could not be brought up")), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isHostPoisonError(tc.err); got != tc.want {
				t.Errorf("isHostPoisonError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

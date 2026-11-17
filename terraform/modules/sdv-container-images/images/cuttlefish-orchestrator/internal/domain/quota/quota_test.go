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

package quota

import (
	"testing"
)

func TestQuota_Available(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		used     int64
		max      int64
		expected int64
	}{
		{"Full available", 0, 100, 100},
		{"Half available", 50, 100, 50},
		{"None available", 100, 100, 0},
		{"Over consumed", 150, 100, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &Quota{UsedHosts: tt.used, MaxHosts: tt.max}
			if got := q.Available(); got != tt.expected {
				t.Errorf("Available() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestQuota_CanProvision(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		used   int64
		max    int64
		amount int64
		can    bool
	}{
		{"Can provision within limit", 50, 100, 25, true},
		{"Can provision exactly to limit", 50, 100, 50, true},
		{"Cannot provision over limit", 50, 100, 51, false},
		{"Cannot provision when exhausted", 100, 100, 1, false},
		{"Can provision zero", 100, 100, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &Quota{UsedHosts: tt.used, MaxHosts: tt.max}
			if got := q.CanProvision(tt.amount); got != tt.can {
				t.Errorf("CanProvision(%d) = %v, want %v", tt.amount, got, tt.can)
			}
		})
	}
}

func TestQuota_Percentage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		used     int64
		max      int64
		expected float64
	}{
		{"Zero usage", 0, 100, 0},
		{"Half usage", 50, 100, 50},
		{"Full usage", 100, 100, 100},
		{"Over usage", 150, 100, 150},
		{"Zero max", 50, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &Quota{UsedHosts: tt.used, MaxHosts: tt.max}
			if got := q.Percentage(); got != tt.expected {
				t.Errorf("Percentage() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestQuota_IsExhausted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		used      int64
		max       int64
		exhausted bool
	}{
		{"Not exhausted", 50, 100, false},
		{"At limit", 100, 100, true},
		{"Over limit", 150, 100, true},
		{"Empty quota", 0, 100, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &Quota{UsedHosts: tt.used, MaxHosts: tt.max}
			if got := q.IsExhausted(); got != tt.exhausted {
				t.Errorf("IsExhausted() = %v, want %v", got, tt.exhausted)
			}
		})
	}
}

func TestQuota_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		quota   Quota
		wantErr bool
	}{
		{
			name: "Valid platform quota",
			quota: Quota{
				Scope:     ScopePlatform,
				ScopeID:   GlobalScopeID,
				MaxHosts:  100,
				UsedHosts: 50,
			},
			wantErr: false,
		},
		{
			name: "Missing scope",
			quota: Quota{
				ScopeID:  "scope-123",
				MaxHosts: 100,
			},
			wantErr: true,
		},
		{
			name: "Invalid scope",
			quota: Quota{
				Scope:    "invalid",
				ScopeID:  "scope-123",
				MaxHosts: 100,
			},
			wantErr: true,
		},
		{
			name: "Missing scope ID",
			quota: Quota{
				Scope:    ScopePlatform,
				MaxHosts: 100,
			},
			wantErr: true,
		},
		{
			name: "Negative max hosts",
			quota: Quota{
				Scope:    ScopePlatform,
				ScopeID:  GlobalScopeID,
				MaxHosts: -1,
			},
			wantErr: true,
		},
		{
			name: "Negative used hosts",
			quota: Quota{
				Scope:     ScopePlatform,
				ScopeID:   GlobalScopeID,
				MaxHosts:  100,
				UsedHosts: -1,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.quota.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExceededError(t *testing.T) {
	t.Parallel()
	err := &ExceededError{
		Scope:     ScopePlatform,
		ScopeID:   GlobalScopeID,
		Requested: 5,
		Available: 2,
		MaxHosts:  10,
	}

	expectedMsg := "host quota exceeded: requested 5, available 2/10"
	if got := err.Error(); got != expectedMsg {
		t.Errorf("Error() = %v, want %v", got, expectedMsg)
	}
}

func TestNotFoundError(t *testing.T) {
	t.Parallel()
	err := &NotFoundError{
		Scope:   ScopePlatform,
		ScopeID: GlobalScopeID,
	}

	expectedMsg := "quota not found: platform/global"
	if got := err.Error(); got != expectedMsg {
		t.Errorf("Error() = %v, want %v", got, expectedMsg)
	}
}

func TestScopeTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		scope    Scope
		expected string
	}{
		{ScopePlatform, "platform"},
	}

	for _, tt := range tests {
		t.Run(string(tt.scope), func(t *testing.T) {
			if string(tt.scope) != tt.expected {
				t.Errorf("Scope = %v, want %v", string(tt.scope), tt.expected)
			}
		})
	}
}

func TestGlobalScopeID(t *testing.T) {
	t.Parallel()
	if GlobalScopeID != "global" {
		t.Errorf("GlobalScopeID = %v, want global", GlobalScopeID)
	}
}

func TestDefaultQuotas(t *testing.T) {
	t.Parallel()
	if DefaultPlatformMaxHosts != 100 {
		t.Errorf("DefaultPlatformMaxHosts = %d, want 100", DefaultPlatformMaxHosts)
	}
}

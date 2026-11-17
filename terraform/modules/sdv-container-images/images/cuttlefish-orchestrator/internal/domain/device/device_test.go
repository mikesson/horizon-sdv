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

package device

import (
	"testing"
)

func TestStatusIsRetired(t *testing.T) {
	t.Parallel()
	retired := map[Status]bool{StatusDeleting: true, StatusDeleted: true}
	for _, s := range allStatuses {
		if got := s.IsRetired(); got != retired[s] {
			t.Errorf("%s.IsRetired() = %v, want %v", s, got, retired[s])
		}
	}
}

func TestStatusTransition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from     Status
		to       Status
		expected bool
	}{
		{StatusPending, StatusCreating, true},
		{StatusPending, StatusRunning, false},
		{StatusCreating, StatusStarting, true},
		{StatusStarting, StatusRunning, true},
		{StatusRunning, StatusStopping, true},
		{StatusRunning, StatusStarting, false},
		{StatusStopped, StatusStarting, true},
		{StatusStopped, StatusDeleting, true},
		{StatusDeleted, StatusRunning, false},
		{StatusError, StatusDeleting, true},
		{StatusError, StatusCreating, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.from)+"_to_"+string(tt.to), func(t *testing.T) {
			if got := tt.from.CanTransitionTo(tt.to); got != tt.expected {
				t.Errorf("CanTransitionTo(%s, %s) = %v, want %v", tt.from, tt.to, got, tt.expected)
			}
		})
	}
}

func TestStatusIsTerminal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status   Status
		terminal bool
	}{
		{StatusPending, false},
		{StatusRunning, false},
		{StatusDeleted, true},
		{StatusError, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsTerminal(); got != tt.terminal {
				t.Errorf("IsTerminal() = %v, want %v", got, tt.terminal)
			}
		})
	}
}

func TestStatusIsRunning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status  Status
		running bool
	}{
		{StatusRunning, true},
		{StatusStarting, false},
		{StatusStopped, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsRunning(); got != tt.running {
				t.Errorf("IsRunning() = %v, want %v", got, tt.running)
			}
		})
	}
}

func TestDeviceValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		device  Device
		wantErr bool
	}{
		{
			name: "Valid Device",
			device: Device{
				Name:   "valid-device",
				HostID: "host-123",
			},
			wantErr: false,
		},
		{
			name: "Missing Name",
			device: Device{
				Name:   "",
				HostID: "host-123",
			},
			wantErr: true,
		},
		{
			name: "Missing HostID",
			device: Device{
				Name:   "valid-device",
				HostID: "",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.device.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDeviceTransitionTo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		fromStatus Status
		toStatus   Status
		wantErr    bool
	}{
		{
			name:       "Valid transition pending to creating",
			fromStatus: StatusPending,
			toStatus:   StatusCreating,
			wantErr:    false,
		},
		{
			name:       "Invalid transition pending to running",
			fromStatus: StatusPending,
			toStatus:   StatusRunning,
			wantErr:    true,
		},
		{
			name:       "Valid transition running to stopping",
			fromStatus: StatusRunning,
			toStatus:   StatusStopping,
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Device{Status: tt.fromStatus}
			err := d.TransitionTo(tt.toStatus)
			if (err != nil) != tt.wantErr {
				t.Errorf("TransitionTo() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && d.Status != tt.toStatus {
				t.Errorf("TransitionTo() status = %v, want %v", d.Status, tt.toStatus)
			}
		})
	}
}

func TestDeviceSetError(t *testing.T) {
	t.Parallel()
	d := &Device{Status: StatusRunning}
	d.SetError("something went wrong")

	if d.Status != StatusError {
		t.Errorf("SetError() status = %v, want %v", d.Status, StatusError)
	}
	if d.ErrorMessage != "something went wrong" {
		t.Errorf("SetError() message = %v, want 'something went wrong'", d.ErrorMessage)
	}
}

func TestHealthStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status  HealthStatus
		healthy bool
	}{
		{HealthStatusHealthy, true},
		{HealthStatusUnhealthy, false},
		{HealthStatusZombie, false},
		{HealthStatusUnknown, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status == HealthStatusHealthy; got != tt.healthy {
				t.Errorf("status == HealthStatusHealthy = %v, want %v", got, tt.healthy)
			}
		})
	}
}

// The desired count is charged at admission and refunded at delete, so the two
// ends must read the same number through the same accessor. Zero is the case
// that matters: a row written before the spec carried a count restores it as
// zero, and charging zero slots lets a host accept devices without ever
// filling. An under-count is the same defect as oversubscription, reversed.
func TestDesiredInstanceCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		count int
		want  int
	}{
		{"an unset count is one device's worth", 0, 1},
		{"a simple-mode device costs one", 1, 1},
		{"a group costs one slot per instance", 8, 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Device{InstanceCount: tt.count}
			if got := d.DesiredInstanceCount(); got != tt.want {
				t.Errorf("DesiredInstanceCount() with InstanceCount=%d = %d, want %d",
					tt.count, got, tt.want)
			}
		})
	}
}

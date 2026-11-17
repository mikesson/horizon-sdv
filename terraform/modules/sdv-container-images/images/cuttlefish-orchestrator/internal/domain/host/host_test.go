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
	"errors"
	"testing"
	"time"
)

func TestStatusTransition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from     Status
		to       Status
		expected bool
	}{
		{StatusPending, StatusProvisioning, true},
		{StatusPending, StatusRunning, false},
		{StatusProvisioning, StatusStarting, true},
		{StatusRunning, StatusStopping, true},
		{StatusStopped, StatusStarting, true},
		{StatusDeleted, StatusRunning, false},
	}

	for _, tt := range tests {
		if got := tt.from.CanTransitionTo(tt.to); got != tt.expected {
			t.Errorf("CanTransitionTo(%s, %s) = %v, want %v", tt.from, tt.to, got, tt.expected)
		}
	}
}

func TestHostValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		host    Host
		wantErr bool
	}{
		{
			name: "Valid Host",
			host: Host{
				Name:       "valid-host",
				Zone:       "us-central1-a",
				MaxDevices: 4,
			},
			wantErr: false,
		},
		{
			name: "Missing Name",
			host: Host{
				Name:       "",
				Zone:       "us-central1-a",
				MaxDevices: 4,
			},
			wantErr: true,
		},
		{
			name: "Missing Zone",
			host: Host{
				Name:       "valid-host",
				Zone:       "",
				MaxDevices: 4,
			},
			wantErr: true,
		},
		{
			name: "Invalid MaxDevices",
			host: Host{
				Name:       "valid-host",
				Zone:       "us-central1-a",
				MaxDevices: 0,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.host.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestValidateBootDiskSizeGB pins the boot-disk rule: 0 is the default
// sentinel and always valid; non-zero values must land inside
// [MinBootDiskGB, MaxBootDiskGB]. Both edges are exercised because the /v2
// tag only enforces the ceiling, and this rule is the floor's sole enforcement.
func TestValidateBootDiskSizeGB(t *testing.T) {
	t.Parallel()
	tests := []struct {
		gb      int64
		wantErr bool
	}{
		{0, false},
		{MinBootDiskGB, false},
		{MaxBootDiskGB, false},
		{400, false},
		{MinBootDiskGB - 1, true},
		{MaxBootDiskGB + 1, true},
		{-1, true},
	}
	for _, tt := range tests {
		err := ValidateBootDiskSizeGB(tt.gb)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateBootDiskSizeGB(%d) error = %v, wantErr %v", tt.gb, err, tt.wantErr)
		}
		if err != nil {
			var bde *BootDiskSizeError
			if !errors.As(err, &bde) {
				t.Errorf("ValidateBootDiskSizeGB(%d) = %T, want *BootDiskSizeError (web.MapError matches it structurally)", tt.gb, err)
			}
		}
	}
}

func TestHostOrchestratorURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		host Host
		want string
	}{
		{
			name: "With Both IPs prefers Internal",
			host: Host{
				IPAddress:            "1.2.3.4",
				InternalIP:           "10.0.0.1",
				HostOrchestratorPort: 2080,
			},
			// Prefers InternalIP for VPC connectivity
			want: "http://10.0.0.1:2080",
		},
		{
			name: "With Only Internal IP",
			host: Host{
				IPAddress:            "",
				InternalIP:           "10.0.0.1",
				HostOrchestratorPort: 2080,
			},
			want: "http://10.0.0.1:2080",
		},
		{
			name: "With Only External IP",
			host: Host{
				IPAddress:            "1.2.3.4",
				InternalIP:           "",
				HostOrchestratorPort: 2080,
			},
			// Falls back to external IP when no internal IP
			want: "http://1.2.3.4:2080",
		},
		{
			name: "No IP",
			host: Host{
				IPAddress:            "",
				InternalIP:           "",
				HostOrchestratorPort: 2080,
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.host.HostOrchestratorURL(); got != tt.want {
				t.Errorf("HostOrchestratorURL() = %v, want %v", got, tt.want)
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
		{StatusProvisioning, false},
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

func TestStatusIsHealthy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status  Status
		healthy bool
	}{
		{StatusRunning, true},
		{StatusStarting, false},
		{StatusStopped, false},
		{StatusError, false},
		{StatusUnreachable, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsHealthy(); got != tt.healthy {
				t.Errorf("IsHealthy() = %v, want %v", got, tt.healthy)
			}
		})
	}
}

func TestHostHasCapacity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		host     Host
		expected bool
	}{
		{
			name: "Has capacity",
			host: Host{
				Status:             StatusRunning,
				MaxDevices:         4,
				CurrentDeviceCount: 2,
			},
			expected: true,
		},
		{
			name: "No capacity - full",
			host: Host{
				Status:             StatusRunning,
				MaxDevices:         4,
				CurrentDeviceCount: 4,
			},
			expected: false,
		},
		{
			name: "No capacity - not running",
			host: Host{
				Status:             StatusStopped,
				MaxDevices:         4,
				CurrentDeviceCount: 2,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.host.HasCapacity(); got != tt.expected {
				t.Errorf("HasCapacity() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestHostAvailableSlots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		host     Host
		expected int64
	}{
		{
			name: "All slots available",
			host: Host{
				MaxDevices:         4,
				CurrentDeviceCount: 0,
			},
			expected: 4,
		},
		{
			name: "Some slots available",
			host: Host{
				MaxDevices:         4,
				CurrentDeviceCount: 2,
			},
			expected: 2,
		},
		{
			name: "No slots available",
			host: Host{
				MaxDevices:         4,
				CurrentDeviceCount: 4,
			},
			expected: 0,
		},
		{
			name: "Over capacity",
			host: Host{
				MaxDevices:         4,
				CurrentDeviceCount: 5,
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.host.AvailableSlots(); got != tt.expected {
				t.Errorf("AvailableSlots() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestHostTransitionTo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		fromStatus Status
		toStatus   Status
		wantErr    bool
	}{
		{
			name:       "Valid transition pending to provisioning",
			fromStatus: StatusPending,
			toStatus:   StatusProvisioning,
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
			h := &Host{Status: tt.fromStatus}
			err := h.TransitionTo(tt.toStatus)
			if (err != nil) != tt.wantErr {
				t.Errorf("TransitionTo() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && h.Status != tt.toStatus {
				t.Errorf("TransitionTo() status = %v, want %v", h.Status, tt.toStatus)
			}
		})
	}
}

func TestStatusTransitionPreemption(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from     Status
		to       Status
		expected bool
	}{
		// Preemption can occur from running
		{StatusRunning, StatusPreempted, true},
		// A dying Spot VM is observed before it is understood: the drift
		// writer adopts stopping/stopped from the VM's STOPPING/TERMINATED
		// window, and the preemption detector must be able to reclassify.
		{StatusStopping, StatusPreempted, true},
		{StatusStopped, StatusPreempted, true},
		// Can retry from preempted to provisioning
		{StatusPreempted, StatusProvisioning, true},
		// Can delete from preempted
		{StatusPreempted, StatusDeleting, true},
		// Cannot go from preempted to running directly
		{StatusPreempted, StatusRunning, false},
	}

	for _, tt := range tests {
		name := string(tt.from) + "_to_" + string(tt.to)
		t.Run(name, func(t *testing.T) {
			if got := tt.from.CanTransitionTo(tt.to); got != tt.expected {
				t.Errorf("CanTransitionTo(%s, %s) = %v, want %v", tt.from, tt.to, got, tt.expected)
			}
		})
	}
}

func TestHostIsSpotVM(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		provisioningModel ProvisioningModel
		expected          bool
	}{
		{
			name:              "Standard VM",
			provisioningModel: ProvisioningModelStandard,
			expected:          false,
		},
		{
			name:              "Spot VM",
			provisioningModel: ProvisioningModelSpot,
			expected:          true,
		},
		{
			name:              "Empty defaults to Standard",
			provisioningModel: "",
			expected:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := Host{ProvisioningModel: tt.provisioningModel}
			if got := h.IsSpotVM(); got != tt.expected {
				t.Errorf("IsSpotVM() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestHostCanRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		host     Host
		expected bool
	}{
		{
			name: "Preempted host can retry",
			host: Host{
				Status:            StatusPreempted,
				ProvisioningModel: ProvisioningModelSpot,
			},
			expected: true,
		},
		{
			name: "Error host with retryable flag can retry",
			host: Host{
				Status:         StatusError,
				ErrorRetryable: true,
			},
			expected: true,
		},
		{
			name: "Error host without retryable flag cannot retry",
			host: Host{
				Status:         StatusError,
				ErrorRetryable: false,
			},
			expected: false,
		},
		{
			name: "Running host cannot retry",
			host: Host{
				Status: StatusRunning,
			},
			expected: false,
		},
		{
			name: "Stopped host cannot retry",
			host: Host{
				Status: StatusStopped,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.host.CanRetry(); got != tt.expected {
				t.Errorf("CanRetry() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestStatusIsRetryable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status    Status
		retryable bool
	}{
		{StatusPending, false},
		{StatusProvisioning, false},
		{StatusRunning, false},
		{StatusStopped, false},
		{StatusError, true},
		{StatusPreempted, true},
		{StatusDeleted, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsRetryable(); got != tt.retryable {
				t.Errorf("IsRetryable() = %v, want %v", got, tt.retryable)
			}
		})
	}
}

func TestProvisioningModelValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		model ProvisioningModel
		valid bool
	}{
		{"Standard is valid", ProvisioningModelStandard, true},
		{"Spot is valid", ProvisioningModelSpot, true},
		{"Empty defaults to standard", "", true},
		{"Invalid model", ProvisioningModel("INVALID"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.model.IsValid()
			if got != tt.valid {
				t.Errorf("IsValid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

// Preemptions accumulate toward the auto-recovery cap only within the count
// window: a host quiet for longer starts fresh, because chronic-thrash
// protection must not become a lifetime ban.
func TestSetPreemptedWindowedCount(t *testing.T) {
	t.Parallel()
	h := &Host{Status: StatusRunning}

	h.SetPreempted("op-1")
	if h.PreemptionCount != 1 {
		t.Fatalf("PreemptionCount = %d, want 1", h.PreemptionCount)
	}

	// Within the window: counts accumulate.
	h.Status = StatusRunning
	h.SetPreempted("op-2")
	if h.PreemptionCount != 2 {
		t.Fatalf("PreemptionCount = %d, want 2", h.PreemptionCount)
	}

	// Quiet past the window: the count restarts at 1.
	stale := time.Now().Add(-PreemptionCountWindow - time.Hour)
	h.PreemptedAt = &stale
	h.Status = StatusRunning
	h.SetPreempted("op-3")
	if h.PreemptionCount != 1 {
		t.Fatalf("PreemptionCount after window = %d, want 1 (fresh window)", h.PreemptionCount)
	}
	if h.LastPreemptionID != "op-3" {
		t.Errorf("LastPreemptionID = %q, want op-3", h.LastPreemptionID)
	}
}

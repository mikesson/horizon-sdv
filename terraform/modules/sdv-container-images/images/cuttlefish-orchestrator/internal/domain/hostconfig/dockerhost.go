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

// Compiled defaults for the docker-host policy below.
//
// n1-standard-8 holds one 4-cpu/8-GiB guest with headroom, and the n1 family
// sidesteps the AMX guest fault the current host image's 6.1 kernel hits on
// newer Intel families. 12 hours outlasts a working day of lab tests while
// still bounding a leak. Unlike every other host, a docker-shape host can
// never earn an extension (lease extension keys off device-row activity, and
// its CVDs have no rows), so this lease is the lifetime.
const (
	DefaultDockerMachineType       = "n1-standard-8"
	DefaultDockerTTLHours    int64 = 12
)

// DockerHost is the operator policy for the legacy /v1 docker host shape: the
// GCE shape a docker-only create request compiles to. Clients built for
// upstream's on-premises deployment (OmniLab ATS) send docker and never read
// it back, so the request carries no usable shape of its own. Domain
// vocabulary because two consumers must agree on it: the settings surface that
// stores it and the legacy facade that applies it at create time.
type DockerHost struct {
	// MachineType is the GCE machine type a docker-shape create provisions.
	MachineType string

	// TTLHours is the lease a docker-shape host starts with, and, uniquely,
	// its whole lifetime: its CVDs arrive through the HO proxy with no device
	// rows, so nothing can ever extend the lease.
	TTLHours int64

	// IsSet reports whether an operator stored the policy; false means at
	// least one field carries its compiled default.
	IsSet bool
}

// DefaultDockerHost is the compiled policy: the answer every consumer lands
// on when no operator stored one, or a stored value fails to parse.
func DefaultDockerHost() DockerHost {
	return DockerHost{
		MachineType: DefaultDockerMachineType,
		TTLHours:    DefaultDockerTTLHours,
	}
}

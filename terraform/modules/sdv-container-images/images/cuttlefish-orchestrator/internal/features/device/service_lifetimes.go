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

package devicefeature

import (
	"context"
	"errors"
	"fmt"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// WithLifetimePolicy wires the system-config reader the create and extend
// paths resolve operator lifetime defaults from. Nil reads as the compiled
// defaults.
func WithLifetimePolicy(policy PolicyReader) DeviceServiceOption {
	return func(s *DeviceService) {
		s.lifetimePolicy = policy
	}
}

// policyLifetimes resolves the operator lifetime policy for devices (session
// TTL, extension grant and allowance). A nil reader and missing,
// malformed or out-of-range keys read as the compiled defaults, under the same
// clamp the settings surface applies on read.
func (s *DeviceService) policyLifetimes(ctx context.Context) (hostconfig.Lifetimes, error) {
	policy := hostconfig.DefaultLifetimes()
	if s.lifetimePolicy == nil {
		return policy, nil
	}
	for _, field := range []struct {
		key    string
		lo, hi int64
		into   *int64
	}{
		{models.ConfigKeyDeviceTTLHours, 1, device.MaxTTLHours, &policy.DeviceTTLHours},
		{models.ConfigKeyExtendHours, 1, device.MaxExtendHours, &policy.ExtendHours},
		{models.ConfigKeyExtendCount, 0, int64(device.MaxExtendCount), &policy.ExtendCount},
	} {
		cfg, err := s.lifetimePolicy.Get(ctx, field.key)
		if err != nil {
			if errors.Is(err, cspanner.ErrKeyNotFound) {
				continue
			}
			return hostconfig.Lifetimes{}, fmt.Errorf("reading lifetime policy %q: %w", field.key, err)
		}
		*field.into, _ = hostconfig.PolicyInt64(cfg.AsString(), field.lo, field.hi, *field.into)
	}
	return policy, nil
}

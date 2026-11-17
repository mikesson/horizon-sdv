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

package cli

import (
	"context"
	"fmt"
	"strings"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
)

// InstanceFlag selects one Android VM in the device's CVD group. Every
// instance-scoped device command embeds it.
type InstanceFlag struct {
	Instance string `placeholder:"NAME" help:"Instance name (see 'device get'); defaults to the device's only instance"`
}

// resolve returns the instance the command addresses. Without --instance it
// reads the device and uses its only instance. A device with several instances
// is refused so a destructive verb cannot land on the wrong Android VM.
func (f InstanceFlag) resolve(ctx context.Context, client *cco.Client, id string) (string, error) {
	if f.Instance != "" {
		return f.Instance, nil
	}
	d, err := client.GetDevice(ctx, id)
	if err != nil {
		return "", fmt.Errorf("resolving the instance for device %s: %w", id, err)
	}
	return f.resolveOn(d)
}

// resolveOn is resolve against a device the caller already fetched.
func (f InstanceFlag) resolveOn(d *cco.Device) (string, error) {
	if f.Instance != "" {
		return f.Instance, nil
	}
	switch len(d.Instances) {
	case 0:
		return "", fmt.Errorf("device %s has no instances yet; it may still be creating", d.ID)
	case 1:
		return d.Instances[0].Name, nil
	default:
		names := make([]string, len(d.Instances))
		for i, inst := range d.Instances {
			names[i] = inst.Name
		}
		return "", fmt.Errorf("device %s has %d instances (%s); name one with --instance",
			d.ID, len(names), strings.Join(names, ", "))
	}
}

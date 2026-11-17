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
	"strings"
	"testing"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
)

func groupDevice() *cco.Device {
	return &cco.Device{
		ID: "dev-1", Name: "my-car-rig", HostID: "host-1", Status: "running",
		ADBPort: 6520, HostExternalIP: "34.90.1.2",
		Instances: []cco.Instance{
			{Name: "ivi", State: "Running", ADBPort: 6520, Displays: []string{"1920 x 1080 ( 160 )"}},
			{Name: "cluster", State: "Running", ADBPort: 6521, Displays: []string{"1280 x 720 ( 160 )"}},
			{Name: "watch", State: "Boot failed", ADBPort: 6522},
		},
	}
}

// The --instance help points at `device get`, so it must list every instance
// name of a group.
func TestPrintDeviceNamesEveryInstance(t *testing.T) {
	out, err := captureStdout(t, func() error {
		printDevice(groupDevice())
		return nil
	})
	if err != nil {
		t.Fatalf("printDevice: %v", err)
	}

	for _, name := range []string{"ivi", "cluster", "watch"} {
		if !strings.Contains(out, name) {
			t.Errorf("instance %q is missing from:\n%s", name, out)
		}
	}
	// Each instance shows its own state and port.
	if !strings.Contains(out, "Boot failed") {
		t.Errorf("the failed instance's state is missing from:\n%s", out)
	}
	if !strings.Contains(out, "adb 6522") {
		t.Errorf("the third instance's port is missing from:\n%s", out)
	}
}

// The flat ADB lines hold the first instance's values, so a group must not
// print them as the device's.
func TestPrintDeviceOmitsTheFlatADBLinesForAGroup(t *testing.T) {
	out, err := captureStdout(t, func() error {
		printDevice(groupDevice())
		return nil
	})
	if err != nil {
		t.Fatalf("printDevice: %v", err)
	}

	if strings.Contains(out, "ADB Serial:") {
		t.Errorf("a group must not render a single dialable serial:\n%s", out)
	}
	if strings.Contains(out, "ADB Port:") {
		t.Errorf("a group must not render one instance's port as the device's:\n%s", out)
	}
	// The firewall opens only the first instance's port, so its address must
	// not appear either.
	if strings.Contains(out, "34.90.1.2:6520") {
		t.Errorf("a group must not print instance 0's dial address:\n%s", out)
	}
}

// A single-instance device keeps the flat ADB lines, because its port is the
// one the firewall opens.
func TestPrintDeviceKeepsTheFlatLinesForOneInstance(t *testing.T) {
	d := groupDevice()
	d.Instances = d.Instances[:1]

	out, err := captureStdout(t, func() error {
		printDevice(d)
		return nil
	})
	if err != nil {
		t.Fatalf("printDevice: %v", err)
	}

	if !strings.Contains(out, "ADB Port:     6520") {
		t.Errorf("a single-instance device keeps its ADB port:\n%s", out)
	}
	if !strings.Contains(out, "ADB Serial:   34.90.1.2:6520") {
		t.Errorf("a single-instance device keeps its dialable serial:\n%s", out)
	}
	if strings.Contains(out, "Instances:") {
		t.Errorf("the instances block is a group affordance and must be absent at N=1:\n%s", out)
	}
}

// A device whose host has not answered yet has no instances. It must render
// without an empty instances block.
func TestPrintDeviceWithNoInstancesYet(t *testing.T) {
	d := groupDevice()
	d.Instances = nil
	d.ADBPort = 0
	d.HostExternalIP = ""

	out, err := captureStdout(t, func() error {
		printDevice(d)
		return nil
	})
	if err != nil {
		t.Fatalf("printDevice: %v", err)
	}
	if strings.Contains(out, "Instances:") {
		t.Errorf("no instances observed yet must render no block:\n%s", out)
	}
	if !strings.Contains(out, "my-car-rig") {
		t.Errorf("the device still renders:\n%s", out)
	}
}

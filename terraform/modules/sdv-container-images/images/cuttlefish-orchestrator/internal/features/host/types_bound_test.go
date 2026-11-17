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

package hostfeature

import (
	"reflect"
	"strconv"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// TestTTLHoursMaximumMatchesDomain binds the create-time TTL bound to the
// constant that documents it.
//
// The `maximum` tag is the only thing enforcing the bound, because the service
// takes req.TTLHours as given and the auto-provisioning path leaves it at zero
// to pick up the default. host.MaxHostTTLHours carries the rationale while the
// tag carries the enforcement, and this test is the link between them.
func TestTTLHoursMaximumMatchesDomain(t *testing.T) {
	t.Parallel()

	body, ok := reflect.TypeFor[CreateHostInput]().FieldByName("Body")
	if !ok {
		t.Fatal("CreateHostInput has no Body field")
	}
	f, ok := body.Type.FieldByName("TTLHours")
	if !ok {
		t.Fatal("CreateHostInput.Body has no TTLHours field")
	}
	tag, ok := f.Tag.Lookup("maximum")
	if !ok {
		t.Fatal("CreateHostInput.Body.TTLHours has no `maximum` tag: the TTL bound is unenforced")
	}

	got, err := strconv.ParseInt(tag, 10, 64)
	if err != nil {
		t.Fatalf("maximum tag %q is not an integer: %v", tag, err)
	}
	if got != host.MaxHostTTLHours {
		t.Errorf("ttl_hours maximum tag = %d, host.MaxHostTTLHours = %d\n"+
			"update the tag in types.go and the constant in domain/host together", got, host.MaxHostTTLHours)
	}
}

// TestExtendHoursMaximumMatchesDomain binds the per-request host extend bound
// to the constant that documents it. The tag is the only enforcement: the
// service adds whatever hours it is handed.
func TestExtendHoursMaximumMatchesDomain(t *testing.T) {
	t.Parallel()

	body, ok := reflect.TypeFor[ExtendHostInput]().FieldByName("Body")
	if !ok {
		t.Fatal("ExtendHostInput has no Body field")
	}
	f, ok := body.Type.FieldByName("Hours")
	if !ok {
		t.Fatal("ExtendHostInput.Body has no Hours field")
	}
	if tag := f.Tag.Get("maximum"); tag != strconv.FormatInt(host.MaxHostTTLHours, 10) {
		t.Errorf("extend hours maximum tag = %q, host.MaxHostTTLHours = %d\n"+
			"update the tag in types.go and the constant in domain/host together", tag, host.MaxHostTTLHours)
	}
	if tag := f.Tag.Get("minimum"); tag != "1" {
		t.Errorf("extend hours minimum tag = %q, want 1: a zero or negative extend would lower the lease", tag)
	}
}

// TestMaxDevicesBoundsMatchDomain binds the device-capacity tags to the
// domain constants: the maximum is what the API refuses beyond, the default
// is what the auto-provisioning density policy falls back to. Nothing but
// this test links either pair.
func TestMaxDevicesBoundsMatchDomain(t *testing.T) {
	t.Parallel()

	body, ok := reflect.TypeFor[CreateHostInput]().FieldByName("Body")
	if !ok {
		t.Fatal("CreateHostInput has no Body field")
	}
	f, ok := body.Type.FieldByName("MaxDevices")
	if !ok {
		t.Fatal("CreateHostInput.Body has no MaxDevices field")
	}
	if tag := f.Tag.Get("maximum"); tag != strconv.FormatInt(host.MaxDevicesPerHost, 10) {
		t.Errorf("max_devices maximum tag = %q, host.MaxDevicesPerHost = %d", tag, host.MaxDevicesPerHost)
	}
	if tag := f.Tag.Get("default"); tag != strconv.FormatInt(host.DefaultMaxDevices, 10) {
		t.Errorf("max_devices default tag = %q, host.DefaultMaxDevices = %d", tag, host.DefaultMaxDevices)
	}
}

// TestBootDiskMaximumMatchesDomain binds the boot-disk ceiling tag to
// host.MaxBootDiskGB. The tag's `minimum` stays 0 on purpose, since 0 means "the
// default", so the floor (host.MinBootDiskGB, non-zero values only) cannot be
// a range tag and is enforced by host.ValidateBootDiskSizeGB in the service;
// its own test lives with the domain rule.
func TestBootDiskMaximumMatchesDomain(t *testing.T) {
	t.Parallel()

	body, ok := reflect.TypeFor[CreateHostInput]().FieldByName("Body")
	if !ok {
		t.Fatal("CreateHostInput has no Body field")
	}
	f, ok := body.Type.FieldByName("BootDiskSizeGB")
	if !ok {
		t.Fatal("CreateHostInput.Body has no BootDiskSizeGB field")
	}
	tag, ok := f.Tag.Lookup("maximum")
	if !ok {
		t.Fatal("CreateHostInput.Body.BootDiskSizeGB has no `maximum` tag: the ceiling is unenforced")
	}

	got, err := strconv.ParseInt(tag, 10, 64)
	if err != nil {
		t.Fatalf("maximum tag %q is not an integer: %v", tag, err)
	}
	if got != host.MaxBootDiskGB {
		t.Errorf("boot_disk_size_gb maximum tag = %d, host.MaxBootDiskGB = %d\n"+
			"update the tag in types.go and the constant in domain/host together", got, host.MaxBootDiskGB)
	}
}

// TestNamePatternMatchesDomain binds the create-time name alphabet to the
// constant that documents it.
//
// The tag is what the API enforces and nothing links it to the domain, so on
// drift the tag wins silently. host.DisplayNamePattern carries the label
// namespace; this test is the link.
func TestNamePatternMatchesDomain(t *testing.T) {
	t.Parallel()

	body, ok := reflect.TypeFor[CreateHostInput]().FieldByName("Body")
	if !ok {
		t.Fatal("CreateHostInput has no Body field")
	}
	f, ok := body.Type.FieldByName("Name")
	if !ok {
		t.Fatal("CreateHostInput.Body has no Name field")
	}
	tag, ok := f.Tag.Lookup("pattern")
	if !ok {
		t.Fatal("CreateHostInput.Body.Name has no `pattern` tag: the name alphabet is unenforced")
	}

	if tag != host.DisplayNamePattern {
		t.Errorf("name pattern tag = %q, host.DisplayNamePattern = %q\n"+
			"update the tag in types.go and the constant in domain/host together", tag, host.DisplayNamePattern)
	}
}

// TestUpdateMaxDevicesBoundsMatchCreate binds the reshape bound to the
// create-time one. Two tags now carry the same rule and nothing links them:
// let them drift and a host could be created at a size the update endpoint
// refuses, or reshaped to one create would never grant.
func TestUpdateMaxDevicesBoundsMatchCreate(t *testing.T) {
	t.Parallel()

	create := maxDevicesField[CreateHostInput](t)
	update := maxDevicesField[UpdateHostInput](t)

	for _, tag := range []string{"minimum", "maximum"} {
		want, ok := create.Tag.Lookup(tag)
		if !ok {
			t.Fatalf("CreateHostInput.Body.MaxDevices has no `%s` tag", tag)
		}
		got, ok := update.Tag.Lookup(tag)
		if !ok {
			t.Fatalf("UpdateHostInput.Body.MaxDevices has no `%s` tag: the bound is unenforced", tag)
		}
		if got != want {
			t.Errorf("max_devices %s: update tag = %q, create tag = %q\nboth tags carry the same rule; change them together", tag, got, want)
		}
	}
}

func maxDevicesField[T any](t *testing.T) reflect.StructField {
	t.Helper()
	body, ok := reflect.TypeFor[T]().FieldByName("Body")
	if !ok {
		t.Fatalf("%s has no Body field", reflect.TypeFor[T]().Name())
	}
	f, ok := body.Type.FieldByName("MaxDevices")
	if !ok {
		t.Fatalf("%s.Body has no MaxDevices field", reflect.TypeFor[T]().Name())
	}
	return f
}

// TestGPUCountMaximumMatchesCatalogCeiling binds the create-time GPU count to
// the same constant the catalog's editable max_gpus is bound to, so an
// operator cannot raise a machine type's ceiling above what create accepts.
func TestGPUCountMaximumMatchesCatalogCeiling(t *testing.T) {
	t.Parallel()

	body, ok := reflect.TypeFor[CreateHostInput]().FieldByName("Body")
	if !ok {
		t.Fatal("CreateHostInput has no Body field")
	}
	f, ok := body.Type.FieldByName("GPUCount")
	if !ok {
		t.Fatal("CreateHostInput.Body has no GPUCount field")
	}
	tag, ok := f.Tag.Lookup("maximum")
	if !ok {
		t.Fatal("CreateHostInput.Body.GPUCount has no `maximum` tag: the GPU ceiling is unenforced")
	}
	got, err := strconv.ParseInt(tag, 10, 64)
	if err != nil {
		t.Fatalf("maximum tag %q is not an integer: %v", tag, err)
	}
	if got != machinetype.MaxAttachableGPUs {
		t.Errorf("gpu_count maximum tag = %d, machinetype.MaxAttachableGPUs = %d", got, machinetype.MaxAttachableGPUs)
	}
}

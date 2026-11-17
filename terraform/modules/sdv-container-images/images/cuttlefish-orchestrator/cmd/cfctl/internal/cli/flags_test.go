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
	"reflect"
	"strings"
	"testing"
)

// TestSharedFlagShapesAreTheOnlyDeclarations rejects a command struct that
// declares its own Wait, Output, NoDownload or IdempotencyKey field instead of
// embedding the shared flag struct. Listed exceptions must still exist.
func TestSharedFlagShapesAreTheOnlyDeclarations(t *testing.T) {
	// Maps Struct.Field to the reason the shared flag struct does not fit.
	bespoke := map[string]string{
		"BatchCreateCmd.Wait":        "prints the settled devices",
		"DeviceSnapshotTakeCmd.Wait": "prints the snapshot id",
		"DeviceScreenshotCmd.Output": "always downloads",
	}
	shapeFor := map[string]string{
		"Wait": "WaitFlags", "Output": "DownloadFlags", "NoDownload": "DownloadFlags",
		"IdempotencyKey": "IdempotencyFlags",
	}
	sharedShapes := map[string]bool{"WaitFlags": true, "DownloadFlags": true, "IdempotencyFlags": true}

	seen := map[string]bool{}
	var walk func(rt reflect.Type)
	walk = func(rt reflect.Type) {
		if rt.Kind() != reflect.Struct || seen[rt.Name()] {
			return
		}
		seen[rt.Name()] = true
		for f := range rt.Fields() {
			if f.Type.Kind() == reflect.Struct && f.Type.PkgPath() == rt.PkgPath() {
				walk(f.Type)
			}
			shape, guarded := shapeFor[f.Name]
			if !guarded || sharedShapes[rt.Name()] {
				continue
			}
			if bespoke[rt.Name()+"."+f.Name] == "" {
				t.Errorf("%s declares its own %s: embed %s, or record here why the behavior differs", rt.Name(), f.Name, shape)
			}
		}
	}
	walk(reflect.TypeFor[CLI]())

	for key := range bespoke {
		if name, _, _ := strings.Cut(key, "."); !seen[name] {
			t.Errorf("bespoke exception %q no longer exists: delete the entry", key)
		}
	}
	for name := range sharedShapes {
		if !seen[name] {
			t.Errorf("shared shape %q is never embedded. The walk or the shape is broken", name)
		}
	}
}

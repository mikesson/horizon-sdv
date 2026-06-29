// Copyright (c) 2026 Accenture, All Rights Reserved.

package controller

import "testing"

func TestModuleUsesPrefixedChildApplication(t *testing.T) {
	for _, name := range []string{"workloads-android", "workloads-common"} {
		if !ModuleUsesPrefixedChildApplication(name) {
			t.Fatalf("%q: expected true", name)
		}
	}
	for _, name := range []string{"", "sample", "workloads-openbsw"} {
		if ModuleUsesPrefixedChildApplication(name) {
			t.Fatalf("%q: expected false", name)
		}
	}
}

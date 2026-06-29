// Copyright (c) 2026 Accenture, All Rights Reserved.

package controller

import "testing"

func TestNamespacePrefixFromModuleConfig(t *testing.T) {
	t.Setenv("MODULE_CONFIG", "")
	if got := NamespacePrefixFromModuleConfig("namespacePrefix: ab-\n"); got != "ab-" {
		t.Fatalf("yaml: got %q", got)
	}
	if got := NamespacePrefixFromModuleConfig(`{"namespacePrefix":"xy-"}`); got != "xy-" {
		t.Fatalf("json: got %q", got)
	}
	t.Setenv("MODULE_CONFIG", "namespacePrefix: from-env-\n")
	if got := NamespacePrefixFromModuleConfig(""); got != "from-env-" {
		t.Fatalf("env fallback: got %q", got)
	}
}

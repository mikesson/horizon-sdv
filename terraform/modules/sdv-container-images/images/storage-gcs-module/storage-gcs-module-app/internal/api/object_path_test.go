// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package api

import "testing"

func TestInvalidObjectPath(t *testing.T) {
	allow := []string{
		"backup..2026.tar",
		"v1..2/notes.txt",
		"dir/..hidden",
		"path/to/object",
		"..hidden",
		"foo.",
	}
	for _, p := range allow {
		if invalidObjectPath(p) {
			t.Errorf("allowed path rejected: %q", p)
		}
	}
	deny := []string{
		"/absolute",
		"..",
		"foo/../bar",
		"../secret",
		"a/../b/c",
	}
	for _, p := range deny {
		if !invalidObjectPath(p) {
			t.Errorf("invalid path allowed: %q", p)
		}
	}
}

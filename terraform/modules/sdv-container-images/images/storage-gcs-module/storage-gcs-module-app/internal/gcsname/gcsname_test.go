// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package gcsname

import "testing"

func TestHasProjectPrefix(t *testing.T) {
	if !HasProjectPrefix("my-proj", "my-proj-aaos") {
		t.Fatal("expected prefix match")
	}
	if HasProjectPrefix("my-proj", "other-aaos") {
		t.Fatal("expected reject other project")
	}
	if HasProjectPrefix("my-proj", "my-proj") {
		t.Fatal("expected reject exact project id without hyphen suffix")
	}
	if HasProjectPrefix("", "my-proj-aaos") {
		t.Fatal("expected reject empty project")
	}
}

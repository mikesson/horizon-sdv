// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package api

import "strings"

// invalidObjectPath is true for absolute keys or keys with a path segment that is exactly "..".
// GCS object names may contain ".." as a substring (e.g. backup..2026.tar); those are allowed.
func invalidObjectPath(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

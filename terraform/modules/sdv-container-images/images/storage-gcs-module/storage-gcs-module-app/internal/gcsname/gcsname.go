// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package gcsname

import "strings"

// HasProjectPrefix is true when bucket is "{projectID}-..." (project-scoped names).
func HasProjectPrefix(projectID, bucket string) bool {
	projectID = strings.TrimSpace(projectID)
	bucket = strings.TrimSpace(bucket)
	if projectID == "" || bucket == "" {
		return false
	}
	return strings.HasPrefix(bucket, projectID+"-")
}

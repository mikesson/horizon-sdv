// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
package gcs

import (
	"fmt"
	"path"
	"strings"
)

// ParseGCSURI splits gs://bucket/object into bucket and object key (no leading slash on key).
func ParseGCSURI(raw string) (bucket, object string, err error) {
	s := strings.TrimSpace(raw)
	const p = "gs://"
	if !strings.HasPrefix(s, p) {
		return "", "", fmt.Errorf("not a gs:// URI")
	}
	rest := strings.TrimPrefix(s, p)
	if rest == "" {
		return "", "", fmt.Errorf("empty gs:// URI")
	}
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return "", "", fmt.Errorf("gs:// URI missing object path")
	}
	bucket, key := rest[:i], rest[i+1:]
	if bucket == "" || key == "" {
		return "", "", fmt.Errorf("invalid gs:// bucket or object")
	}
	return bucket, key, nil
}

// ObjectBaseName returns the last path segment of the object key in a gs:// URI, or empty if invalid.
func ObjectBaseName(raw string) string {
	_, object, err := ParseGCSURI(raw)
	if err != nil {
		return ""
	}
	b := path.Base(object)
	if b == "" || b == "." {
		return ""
	}
	return b
}

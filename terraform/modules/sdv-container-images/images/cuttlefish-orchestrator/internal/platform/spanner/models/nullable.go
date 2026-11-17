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

package models

import (
	"time"

	"cloud.google.com/go/spanner"
)

// ToNullString converts a string to a spanner.NullString.
func ToNullString(s string) spanner.NullString {
	if s == "" {
		return spanner.NullString{}
	}
	return spanner.NullString{StringVal: s, Valid: true}
}

// ToNullStringPtr converts a string pointer to a spanner.NullString.
func ToNullStringPtr(s *string) spanner.NullString {
	if s == nil || *s == "" {
		return spanner.NullString{}
	}
	return spanner.NullString{StringVal: *s, Valid: true}
}

// ToNullTime converts a time pointer to a spanner.NullTime.
func ToNullTime(t *time.Time) spanner.NullTime {
	if t == nil || t.IsZero() {
		return spanner.NullTime{}
	}
	return spanner.NullTime{Time: *t, Valid: true}
}

// ToNullInt64 converts an int64 to a spanner.NullInt64.
func ToNullInt64(i int64) spanner.NullInt64 {
	return spanner.NullInt64{Int64: i, Valid: true}
}

// ToNullInt64Ptr converts an *int64 to a spanner.NullInt64 (nil => null).
func ToNullInt64Ptr(i *int64) spanner.NullInt64 {
	if i == nil {
		return spanner.NullInt64{}
	}
	return spanner.NullInt64{Int64: *i, Valid: true}
}

// FromNullInt64Ptr returns a pointer to the int64 value, or nil when null.
func FromNullInt64Ptr(n spanner.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	i := n.Int64
	return &i
}

// FromNullString returns the string value, or "" when null.
func FromNullString(n spanner.NullString) string {
	if n.Valid {
		return n.StringVal
	}
	return ""
}

// FromNullStringPtr returns a pointer to the string value, or nil when null.
func FromNullStringPtr(n spanner.NullString) *string {
	if !n.Valid {
		return nil
	}
	s := n.StringVal
	return &s
}

// FromNullTimePtr returns a pointer to the time value, or nil when null.
func FromNullTimePtr(n spanner.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time
	return &t
}

// FromNullInt64 returns the int64 value, or 0 when null.
func FromNullInt64(n spanner.NullInt64) int64 {
	if n.Valid {
		return n.Int64
	}
	return 0
}

// FromNullBool returns the bool value, or false when null.
func FromNullBool(n spanner.NullBool) bool {
	if n.Valid {
		return n.Bool
	}
	return false
}

// FromNullJSONMap returns the JSON value as an object, nil when null, or an
// empty map when the stored JSON is not an object (array, string, number).
// An unchecked assertion here would panic every read of the row.
func FromNullJSONMap(n spanner.NullJSON) map[string]any {
	if !n.Valid {
		return nil
	}
	m, ok := n.Value.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return m
}

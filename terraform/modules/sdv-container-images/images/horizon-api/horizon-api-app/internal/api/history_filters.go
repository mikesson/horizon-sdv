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

package api

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/acn-horizon-sdv/horizon-api/internal/workflow"
)

const (
	historyListChunk = int64(100)
	historyMaxScan   = int64(5000)
	nameRegexMaxLen  = 256
)

type historyFilter struct {
	phases map[string]struct{}

	startedAfter  *time.Time
	startedBefore *time.Time

	finishedAfter  *time.Time
	finishedBefore *time.Time

	nameGlob   string
	nameRegex  *regexp.Regexp
	anyApplied bool
}

func parseHistoryFilters(q url.Values) (*historyFilter, error) {
	f := &historyFilter{}

	if v := strings.TrimSpace(q.Get("phase")); v != "" {
		f.phases = make(map[string]struct{})
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			f.phases[strings.ToLower(p)] = struct{}{}
		}
		if len(f.phases) > 0 {
			f.anyApplied = true
		}
	}

	var err error
	if f.startedAfter, err = parseQueryTime(q, "startedAfter"); err != nil {
		return nil, err
	}
	if f.startedAfter != nil {
		f.anyApplied = true
	}
	if f.startedBefore, err = parseQueryTime(q, "startedBefore"); err != nil {
		return nil, err
	}
	if f.startedBefore != nil {
		f.anyApplied = true
	}
	if f.finishedAfter, err = parseQueryTime(q, "finishedAfter"); err != nil {
		return nil, err
	}
	if f.finishedAfter != nil {
		f.anyApplied = true
	}
	if f.finishedBefore, err = parseQueryTime(q, "finishedBefore"); err != nil {
		return nil, err
	}
	if f.finishedBefore != nil {
		f.anyApplied = true
	}

	if g := strings.TrimSpace(q.Get("nameGlob")); g != "" {
		f.nameGlob = g
		f.anyApplied = true
	}
	if rx := strings.TrimSpace(q.Get("nameRegex")); rx != "" {
		if len(rx) > nameRegexMaxLen {
			return nil, fmt.Errorf("nameRegex exceeds max length %d", nameRegexMaxLen)
		}
		cr, err := regexp.Compile(rx)
		if err != nil {
			return nil, fmt.Errorf("nameRegex: %w", err)
		}
		f.nameRegex = cr
		f.anyApplied = true
	}

	if f.startedAfter != nil && f.startedBefore != nil && f.startedAfter.After(*f.startedBefore) {
		return nil, fmt.Errorf("startedAfter is after startedBefore")
	}
	if f.finishedAfter != nil && f.finishedBefore != nil && f.finishedAfter.After(*f.finishedBefore) {
		return nil, fmt.Errorf("finishedAfter is after finishedBefore")
	}

	return f, nil
}

func parseQueryTime(q url.Values, key string) (*time.Time, error) {
	s := strings.TrimSpace(q.Get(key))
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: use RFC3339 or RFC3339Nano (%w)", key, err)
	}
	return &t, nil
}

func parseStatusTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return t, true
	}
	t, err = time.Parse(time.RFC3339, s)
	if err == nil {
		return t, true
	}
	return time.Time{}, false
}

func (f *historyFilter) matchesTerminal(u *unstructured.Unstructured) bool {
	if f == nil || !f.anyApplied {
		return true
	}
	if len(f.phases) > 0 {
		disp := workflow.DisplayPhaseForAPI(u)
		if _, ok := f.phases[strings.ToLower(disp)]; !ok {
			return false
		}
	}
	name := u.GetName()
	if f.nameGlob != "" {
		ok, _ := filepath.Match(f.nameGlob, name)
		if !ok {
			return false
		}
	}
	if f.nameRegex != nil && !f.nameRegex.MatchString(name) {
		return false
	}
	startedStr, _, _ := unstructured.NestedString(u.Object, "status", "startedAt")
	finishedStr, _, _ := unstructured.NestedString(u.Object, "status", "finishedAt")
	if f.startedAfter != nil || f.startedBefore != nil {
		t, ok := parseStatusTime(startedStr)
		if !ok {
			return false
		}
		if f.startedAfter != nil && t.Before(*f.startedAfter) {
			return false
		}
		if f.startedBefore != nil && t.After(*f.startedBefore) {
			return false
		}
	}
	if f.finishedAfter != nil || f.finishedBefore != nil {
		t, ok := parseStatusTime(finishedStr)
		if !ok {
			return false
		}
		if f.finishedAfter != nil && t.Before(*f.finishedAfter) {
			return false
		}
		if f.finishedBefore != nil && t.After(*f.finishedBefore) {
			return false
		}
	}
	return true
}

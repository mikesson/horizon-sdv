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

package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// result is one check's outcome. Durations are reported for every check, and
// not only for the failures. A verb creeping toward its deadline is the early
// warning that a deadline is about to become a fault.
type result struct {
	name    string
	ok      bool
	skipped bool
	detail  string
	took    time.Duration
}

type findings struct {
	results []result
}

func (f *findings) pass(name string, took time.Duration, detail string) {
	f.results = append(f.results, result{name: name, ok: true, detail: detail, took: took})
}

func (f *findings) fail(name string, took time.Duration, detail string) {
	f.results = append(f.results, result{name: name, detail: detail, took: took})
}

func (f *findings) skip(name, why string) {
	f.results = append(f.results, result{name: name, ok: true, skipped: true, detail: why})
}

func (f *findings) failed() bool {
	for _, r := range f.results {
		if !r.ok {
			return true
		}
	}
	return false
}

func (d *driver) report() {
	var b strings.Builder
	fmt.Fprintf(&b, "\nlivesmoke %s on %s/%s (%s)\n",
		d.cfg.Scenario, d.cfg.Project, d.cfg.Service, d.serverURL)
	fmt.Fprintf(&b, "%-12s %-8s %10s  %s\n", "CHECK", "RESULT", "TOOK", "DETAIL")

	var passed, failed, skipped int
	for _, r := range d.results() {
		status := "FAIL"
		switch {
		case r.skipped:
			status, skipped = "SKIP", skipped+1
		case r.ok:
			status, passed = "ok", passed+1
		default:
			failed++
		}
		took := ""
		if !r.skipped {
			took = r.took.Round(100 * time.Millisecond).String()
		}
		fmt.Fprintf(&b, "%-12s %-8s %10s  %s\n", r.name, status, took, r.detail)
	}

	fmt.Fprintf(&b, "\n%d passed, %d failed, %d skipped in %s\n",
		passed, failed, skipped, time.Since(d.started).Round(time.Second))
	if d.cfg.Keep && d.created {
		fmt.Fprintf(&b, "kept: device %s on host %s. Delete them when done\n", d.dev.ID, d.dev.HostID)
	}
	fmt.Fprint(os.Stdout, b.String())
}

func (d *driver) results() []result { return d.f.results }

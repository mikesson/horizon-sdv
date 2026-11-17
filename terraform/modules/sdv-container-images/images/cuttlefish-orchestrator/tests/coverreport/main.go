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

// Command coverreport reports block coverage for internal/ alongside
// error-branch coverage.
//
// The second number is the one worth watching. Yuan et al. found that 92% of
// catastrophic failures in distributed systems came from incorrectly handled
// non-fatal errors, and that over half were reachable by simple tests of the
// error-handling code. A suite can carry a respectable total while leaving
// almost every `if err != nil` arm unexecuted, and the total will not say so.
//
// Usage: go run ./tests/coverreport [-fail-under-error N] cover.out
package main

import (
	"bufio"
	"cmp"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// file:startLine.startCol,endLine.endCol numStmts count
var blockRe = regexp.MustCompile(`^(.+?):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)$`)

// Go records an if-block starting at the `if` itself, so the block's first
// source line is the condition rather than the body.
var errBranchRe = regexp.MustCompile(`\bif\s+[^;]*\berr\b[^;]*(!=\s*nil|:=)`)

const module = "cloud-cuttlefish-orchestrator/"

type blockKey struct {
	file      string
	startLine int
	// Same class as relayhook's cacheKey: `unused` cannot see key identity.
	pos string //nolint:unused // written, never read; it keeps two blocks opening on one line distinct map keys
}

// merge parses a profile, keeping the highest count seen for each block.
//
// A -coverpkg=./... profile lists every block once per test binary, so the
// same block appears many times with different counts. Merging on position
// with max is what `go tool cover` does; summing or last-wins are both wrong.
func merge(path string) (map[blockKey]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening profile: %w", err)
	}
	defer f.Close()

	blocks := map[blockKey]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		m := blockRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		startLine, _ := strconv.Atoi(m[2])
		key := blockKey{
			file:      m[1],
			startLine: startLine,
			pos:       m[3] + "," + m[4] + "." + m[5],
		}
		count, _ := strconv.Atoi(m[7])
		if prev, ok := blocks[key]; !ok || count > prev {
			blocks[key] = count
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading profile: %w", err)
	}
	return blocks, nil
}

// sourceLines lazily loads and caches files named by the profile.
type sourceLines map[string][]string

func (s sourceLines) line(rel string, n int) string {
	lines, ok := s[rel]
	if !ok {
		data, err := os.ReadFile(rel)
		if err != nil {
			s[rel] = []string{}
			return ""
		}
		lines = strings.Split(string(data), "\n")
		s[rel] = lines
	}
	if n < 1 || n > len(lines) {
		return ""
	}
	return lines[n-1]
}

type stats struct {
	total, covered, errTotal, errCovered int
	uncovered, perPkgTotal               map[string]int
}

func tally(blocks map[blockKey]int) stats {
	sources := sourceLines{}
	s := stats{uncovered: map[string]int{}, perPkgTotal: map[string]int{}}
	for key, count := range blocks {
		rel := strings.TrimPrefix(key.file, module)
		if !strings.HasPrefix(rel, "internal/") || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		s.total++
		if count > 0 {
			s.covered++
		}
		if errBranchRe.MatchString(strings.TrimSpace(sources.line(rel, key.startLine))) {
			pkg := filepath.Dir(rel)
			s.errTotal++
			s.perPkgTotal[pkg]++
			if count > 0 {
				s.errCovered++
			} else {
				s.uncovered[pkg]++
			}
		}
	}
	return s
}

func printUncovered(s stats) {
	if len(s.uncovered) == 0 {
		return
	}
	fmt.Println("\nuncovered error branches by package:")
	type row struct {
		pkg string
		n   int
	}
	rows := make([]row, 0, len(s.uncovered))
	for pkg, n := range s.uncovered {
		rows = append(rows, row{pkg, n})
	}
	// Count descending, then name. The Python predecessor broke ties by
	// insertion order, so its output was not deterministic.
	slices.SortFunc(rows, func(a, b row) int {
		if c := cmp.Compare(b.n, a.n); c != 0 {
			return c
		}
		return cmp.Compare(a.pkg, b.pkg)
	})
	if len(rows) > 15 {
		rows = rows[:15]
	}
	for _, r := range rows {
		fmt.Printf("%6d / %-5d %s\n", r.n, s.perPkgTotal[r.pkg], r.pkg)
	}
}

func run() int {
	failUnder := flag.Float64("fail-under-error", -1,
		"exit non-zero if error-branch coverage falls below this percentage")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: coverreport [-fail-under-error N] cover.out")
		return 2
	}

	blocks, err := merge(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	s := tally(blocks)
	if s.total == 0 {
		fmt.Fprintln(os.Stderr, "no internal/ blocks found in profile")
		return 1
	}

	pct := 100 * float64(s.covered) / float64(s.total)
	errPct := 0.0
	if s.errTotal > 0 {
		errPct = 100 * float64(s.errCovered) / float64(s.errTotal)
	}
	fmt.Printf("internal/ blocks         %6d  covered %6d  = %.1f%%\n", s.total, s.covered, pct)
	fmt.Printf("error-handling branches  %6d  covered %6d  = %.1f%%\n",
		s.errTotal, s.errCovered, errPct)
	printUncovered(s)

	if *failUnder >= 0 && errPct < *failUnder {
		fmt.Fprintf(os.Stderr, "\nerror-branch coverage %.1f%% is below the floor of %.1f%%\n",
			errPct, *failUnder)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run())
}

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

package coapi

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// upstreamDir is the checkout these structs were copied from. The checkout is
// not tracked, so the test skips when it is absent; CCO_REQUIRE_UPSTREAM=1
// (set by scripts/ci.sh) turns that skip into a failure, because a silently
// skipped drift check is the emulator trap all over again.
const upstreamDir = "../../../../UPSTREAM/cloud-android-orchestration/api/v1"

// unmirrored records every upstream api/v1 type this package does not
// carry, with the reason. A type in neither this table nor the
// package is a drift failure, so upstream growing a new type turns the build
// red instead of going unnoticed.
var unmirrored = map[string]string{
	"OperationResult": "unused by /v1: our :wait returns the bare resolved resource",
	"NewConnMsg":      "signaling passes through the Host Orchestrator proxy untyped",
	"NewConnReply":    "signaling passes through the Host Orchestrator proxy untyped",
	"ForwardMsg":      "signaling passes through the Host Orchestrator proxy untyped",
	"SServerResponse": "upstream server-internal helper, never on the wire",
}

// wireField is the slice of a struct field that determines its JSON output:
// name, type expression and full tag. Declaration order matters too, because
// encoding/json emits fields in that order, so fields compare by position.
type wireField struct {
	Name, Type, Tag string
}

func TestCoapiMatchesUpstream(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat(upstreamDir); err != nil {
		if os.Getenv("CCO_REQUIRE_UPSTREAM") != "" {
			t.Fatalf("CCO_REQUIRE_UPSTREAM is set but the upstream checkout is unreadable: %v", err)
		}
		t.Skipf("upstream checkout not present: %v", err)
	}
	ours := parseStructs(t, ".")
	theirs := parseStructs(t, upstreamDir)

	for name, ourFields := range ours {
		upFields, ok := theirs[name]
		if !ok {
			t.Errorf("coapi.%s has no counterpart in upstream api/v1", name)
			continue
		}
		if diff := diffFields(ourFields, upFields); diff != "" {
			t.Errorf("coapi.%s drifted from upstream:\n%s", name, diff)
		}
	}
	for name := range theirs {
		if _, mirrored := ours[name]; mirrored {
			continue
		}
		if _, declined := unmirrored[name]; !declined {
			t.Errorf("upstream api/v1 declares %s, which is neither mirrored here nor recorded in the unmirrored table", name)
		}
	}
	for name := range unmirrored {
		if _, ok := theirs[name]; !ok {
			t.Errorf("unmirrored table lists %s, which upstream no longer declares; delete the entry", name)
		}
		if _, ok := ours[name]; ok {
			t.Errorf("%s is both mirrored and listed as unmirrored", name)
		}
	}
}

func parseStructs(t *testing.T, dir string) map[string][]wireField {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	structs := make(map[string][]wireField)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				structs[ts.Name.Name] = structFields(st)
			}
		}
	}
	return structs
}

func structFields(st *ast.StructType) []wireField {
	var fields []wireField
	for _, f := range st.Fields.List {
		var tag string
		if f.Tag != nil {
			tag = f.Tag.Value
		}
		typ := types.ExprString(f.Type)
		for _, name := range f.Names {
			fields = append(fields, wireField{Name: name.Name, Type: typ, Tag: tag})
		}
	}
	return fields
}

func diffFields(ours, theirs []wireField) string {
	var b strings.Builder
	for i := range max(len(ours), len(theirs)) {
		var o, u wireField
		if i < len(ours) {
			o = ours[i]
		}
		if i < len(theirs) {
			u = theirs[i]
		}
		if o != u {
			fmt.Fprintf(&b, "  field %d: ours %+v, upstream %+v\n", i, o, u)
		}
	}
	return b.String()
}

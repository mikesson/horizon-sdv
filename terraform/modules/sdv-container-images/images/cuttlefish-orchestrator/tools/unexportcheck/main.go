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

// Command unexportcheck reports exported package-level types, constants and
// variables that nothing in the module references.
//
// It covers the one blind spot the other two dead-code tools share.
// staticcheck's `unused` assumes every exported identifier is used, because
// whole-program mode was removed from staticcheck in 2020.2 and never
// returned. x/tools/cmd/deadcode does see exported identifiers, but only
// functions and methods. Nothing reports an exported type or constant that no
// longer has a reader.
//
// The analysis is sound for this repository specifically: every package lives
// under internal/, cmd/ or tests/, so no code outside the module can import
// them and a whole-module reference scan sees every possible use. Point it at
// a library and it will report the library's entire public API.
//
// Usage:
//
//	unexportcheck [flags] [packages]   # default: ./...
//
//	-allow FILE   skip identifiers listed one "pkg.Name" per line (# comments)
//	-generated    include declarations in generated files (off by default)
//	-over         also report identifiers used only inside their declaring
//	              package, which could be unexported. Noisy in a codebase whose
//	              transport types are exported by framework convention.
//
// Exits 1 when it reports something, 0 when clean, 2 on a load error.
package main

import (
	"bufio"
	"cmp"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// generatedRe matches the line that marks a generated file, as specified by
// https://go.dev/s/generatedcode.
var generatedRe = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

type decl struct {
	pos       token.Position
	kind      string
	name      string
	pkgPath   string
	iota      bool
	generated bool
	external  int
	internal  int
}

// ident is the allowlist key: the declaring package's base name and the
// identifier, e.g. "audit.ActionQuotaExceed".
func (d *decl) ident() string { return shortPkg(d.pkgPath) + "." + d.name }

func main() {
	os.Exit(run())
}

func run() int {
	allowPath := flag.String("allow", "", "file of pkg.Name identifiers to skip, one per line")
	includeGenerated := flag.Bool("generated", false, "include declarations in generated files")
	includeOver := flag.Bool("over", false, "also report identifiers usable unexported")
	flag.Parse()

	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	allow, err := loadAllow(*allowPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "unexportcheck:", err)
		return 2
	}

	dead, over, err := analyze("", patterns, options{
		allow:            allow,
		includeGenerated: *includeGenerated,
		includeOver:      *includeOver,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "unexportcheck:", err)
		return 2
	}

	n := report("unreferenced anywhere in the module", dead)
	n += report("used only inside the declaring package (could be unexported)", over)
	if n > 0 {
		return 1
	}
	return 0
}

type options struct {
	allow            map[string]bool
	includeGenerated bool
	includeOver      bool
}

// analyze loads patterns rooted at dir (the working directory when empty) and
// splits the exported declarations into the unreferenced and the
// package-local. over is nil unless opts.includeOver.
func analyze(dir string, patterns []string, opts options) ([]*decl, []*decl, error) {
	pkgs, err := packages.Load(&packages.Config{
		Dir: dir,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Tests: true,
	}, patterns...)
	if err != nil {
		return nil, nil, fmt.Errorf("load: %w", err)
	}
	// A package that failed to type-check contributes no Uses, so its
	// references would be invisible and every identifier it alone reads would
	// be reported as dead. Refuse rather than report a false positive.
	if n := packages.PrintErrors(pkgs); n > 0 {
		return nil, nil, fmt.Errorf("%d package(s) failed to load; refusing to report", n)
	}

	decls := collect(pkgs, opts.includeGenerated)
	countUses(pkgs, decls)

	var dead, over []*decl
	for _, d := range decls {
		if opts.allow[d.ident()] {
			continue
		}
		switch {
		case d.external == 0 && d.internal == 0:
			dead = append(dead, d)
		case d.external == 0 && opts.includeOver:
			over = append(over, d)
		}
	}
	return dead, over, nil
}

// collect indexes every exported package-level type, constant and variable.
// Functions and methods are left out because deadcode reasons about them
// through the call graph, which handles dynamic dispatch that this tool cannot.
func collect(pkgs []*packages.Package, includeGenerated bool) map[string]*decl {
	decls := map[string]*decl{}
	for _, p := range pkgs {
		if p.Types == nil || p.Fset == nil || p.Types.Name() == "main" {
			continue
		}
		gen := generatedFiles(p)
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if !obj.Exported() {
				continue
			}
			var kind string
			switch obj.(type) {
			case *types.TypeName:
				kind = "type"
			case *types.Const:
				kind = "const"
			case *types.Var:
				kind = "var"
			default:
				continue // funcs: deadcode's job
			}
			pos := p.Fset.Position(obj.Pos())
			if _, ok := decls[pos.String()]; ok {
				continue // already seen via another package variant (e.g. the test build)
			}
			if gen[pos.Filename] && !includeGenerated {
				continue
			}
			decls[pos.String()] = &decl{
				pos: pos, kind: kind, name: name, pkgPath: normPkg(p.PkgPath),
				iota:      kind == "const" && fromIota(p, obj),
				generated: gen[pos.Filename],
			}
		}
	}
	return decls
}

// countUses attributes every reference to a collected declaration, split by
// whether it came from the declaring package. Positions key the map because
// the same declaration is a distinct types.Object in each package variant that
// packages.Load produces for a package and its test build.
func countUses(pkgs []*packages.Package, decls map[string]*decl) {
	for _, p := range pkgs {
		if p.TypesInfo == nil || p.Fset == nil {
			continue
		}
		from := normPkg(p.PkgPath)
		for _, obj := range p.TypesInfo.Uses {
			if obj == nil || obj.Pkg() == nil {
				continue
			}
			d, ok := decls[p.Fset.Position(obj.Pos()).String()]
			if !ok {
				continue
			}
			if from == d.pkgPath {
				d.internal++
			} else {
				d.external++
			}
		}
	}
}

func report(title string, ds []*decl) int {
	if len(ds) == 0 {
		return 0
	}
	slices.SortFunc(ds, func(a, b *decl) int {
		return cmp.Or(
			strings.Compare(a.pos.Filename, b.pos.Filename),
			cmp.Compare(a.pos.Line, b.pos.Line),
		)
	})
	fmt.Printf("%d exported declarations %s:\n", len(ds), title)
	for _, d := range ds {
		var notes []string
		if d.iota {
			// Deleting one shifts every later value in the block, so an
			// iota constant is a rename-or-keep decision, never a plain delete.
			notes = append(notes, "iota: deleting renumbers the block")
		}
		if d.generated {
			notes = append(notes, "generated")
		}
		note := ""
		if len(notes) > 0 {
			note = "  [" + strings.Join(notes, "; ") + "]"
		}
		fmt.Printf("%s:%d:%d: %s %s%s\n", d.pos.Filename, d.pos.Line, d.pos.Column, d.kind, d.ident(), note)
	}
	fmt.Println()
	return len(ds)
}

// fromIota reports whether a constant draws its value from iota, either by
// naming it or by inheriting the previous spec's expression in a grouped
// declaration. Both cases make the constant positional.
func fromIota(p *packages.Package, obj types.Object) bool {
	file := fileFor(p, obj.Pos())
	if file == nil {
		return false
	}
	for _, d := range file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST || obj.Pos() < gd.Pos() || obj.Pos() > gd.End() {
			continue
		}
		var last []ast.Expr
		for _, s := range gd.Specs {
			vs, ok := s.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if len(vs.Values) > 0 {
				last = vs.Values
			}
			if vs.Pos() <= obj.Pos() && obj.Pos() <= vs.End() {
				// An empty spec repeats `last`, so both share its iota-ness.
				return mentionsIota(last)
			}
		}
	}
	return false
}

func mentionsIota(exprs []ast.Expr) bool {
	found := false
	for _, e := range exprs {
		ast.Inspect(e, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "iota" {
				found = true
			}
			return !found
		})
	}
	return found
}

func fileFor(p *packages.Package, pos token.Pos) *ast.File {
	for _, f := range p.Syntax {
		if f.Pos() <= pos && pos <= f.End() {
			return f
		}
	}
	return nil
}

// generatedFiles returns the set of files carrying the generated-code marker.
func generatedFiles(p *packages.Package) map[string]bool {
	gen := map[string]bool{}
	for _, f := range p.Syntax {
		name := p.Fset.Position(f.Pos()).Filename
		for _, cg := range f.Comments {
			// The marker must precede the package clause.
			if cg.Pos() > f.Package {
				break
			}
			for _, c := range cg.List {
				if generatedRe.MatchString(c.Text) {
					gen[name] = true
				}
			}
		}
	}
	return gen
}

func loadAllow(path string) (map[string]bool, error) {
	allow := map[string]bool{}
	if path == "" {
		return allow, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open allowlist: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line != "" {
			allow[line] = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read allowlist: %w", err)
	}
	return allow, nil
}

// normPkg folds a package's test variants onto the package itself, so a
// reference from foo_test counts as internal to foo.
func normPkg(p string) string {
	return strings.TrimSuffix(strings.TrimSuffix(p, ".test"), "_test")
}

func shortPkg(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

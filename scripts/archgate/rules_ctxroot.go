package main

// ctxroot holds the base contexts of this process in one register: "cancellable where cancellation
// helps, detached where leaving state is the point" cannot be checked any other way. Every
// context.Background() / context.TODO() in production code is one of three things, and the baseline
// states which for each site, as "<file>|<function>|<reason>|x<count>":
//
//	root     — a genuine entry point: main's signal context, the bridge agent's stdio loop, a goroutine
//	           that is itself a root. Nothing above it to cancel from.
//	detached — a write whose point is to leave state behind. It must not die with the request that
//	           triggered it, so a background root is correct and saying so is the record.
//	legacy   — a caller has a context and does not pass one. Shrink this line of the baseline.
//
// A new call site is a finding until it is classified; a site that disappears makes its entry stale —
// a rename is the same event as far as the register is concerned — and so does one more
// context.Background() inside a function that is already classified.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ctxReasons are the classifications a baseline entry may carry.
var ctxReasons = map[string]bool{"root": true, "detached": true, "legacy": true}

func scanContextRoots(root string) ([]Finding, int, error) {
	classified := map[string]string{} // "<file>|<fn>" -> reason
	if raw, err := os.ReadFile(filepath.Join(root, BaselinePath("ctxroot"))); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.Split(line, "|")
			if len(parts) != 4 || !strings.HasPrefix(parts[3], "x") {
				return nil, 0, fmt.Errorf("baseline entry %q is not file|function|reason|x<count", line)
			}
			if !ctxReasons[parts[2]] {
				return nil, 0, fmt.Errorf("baseline entry %q: reason %q is not root|detached|legacy", line, parts[2])
			}
			classified[parts[0]+"|"+parts[1]] = parts[2]
		}
	} else if !os.IsNotExist(err) {
		return nil, 0, err
	}

	files, err := goFiles(root)
	if err != nil {
		return nil, 0, err
	}
	// The roots goFiles walks are directories; main.go is the process entry point and the one root
	// context this repo has that is not inside a directory tree.
	if _, err := os.Stat(filepath.Join(root, "main.go")); err == nil {
		files = append(files, "main.go")
	}

	var findings []Finding
	for _, rel := range files {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
		if err != nil {
			continue
		}
		funcs := funcSpans(fset, f)
		line := func(pos token.Pos) int { return fset.Position(pos).Line }
		// One finding per (file, function), carrying how many base contexts live there. The count is in the
		// key on purpose: a register keyed by function alone would let a second, tenth or hundredth
		// context.Background() into a classified function without saying anything, which is the whole debt
		// this rule exists to watch.
		type site struct {
			first int
			count int
		}
		perFunc := map[string]*site{}
		var order []string
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "context" || (sel.Sel.Name != "Background" && sel.Sel.Name != "TODO") {
				return true
			}
			fn := funcs.at(line(n.Pos()))
			key := rel + "|" + fn
			at, ok := perFunc[key]
			if !ok {
				at = &site{first: line(n.Pos())}
				perFunc[key] = at
				order = append(order, key)
			}
			at.count++
			return true
		})
		for _, key := range order {
			at := perFunc[key]
			fn := key[strings.LastIndex(key, "|")+1:]
			reason, known := classified[key]
			detail, why := fn+"|unclassified|x"+itoa(at.count), fmt.Sprintf(
				"%d base context(s) in %s are not in the register — add a %s|root|detached|legacy|x%d line to %s",
				at.count, rel, key, at.count, BaselinePath("ctxroot"))
			if known {
				detail, why = fn+"|"+reason+"|x"+itoa(at.count), ""
			}
			findings = append(findings, Finding{File: rel, Line: at.first, Detail: detail, Why: why})
		}
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Key() < findings[j].Key() })
	return findings, len(files), nil
}

// funcRanges maps a line number to the innermost function declaration enclosing it, so the register names
// a function rather than a line that moves.
type funcRanges struct {
	names []string
	begin []int
	end   []int
}

func (r funcRanges) at(line int) string {
	best := -1
	bestLen := 0
	for i := range r.names {
		if line >= r.begin[i] && line <= r.end[i] {
			if l := r.end[i] - r.begin[i]; best == -1 || l < bestLen {
				best, bestLen = i, l
			}
		}
	}
	if best == -1 {
		return "<file>"
	}
	return r.names[best]
}

func funcSpans(fset *token.FileSet, f *ast.File) funcRanges {
	var out funcRanges
	add := func(name string, begin, end token.Pos) {
		out.names = append(out.names, name)
		out.begin = append(out.begin, fset.Position(begin).Line)
		out.end = append(out.end, fset.Position(end).Line)
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				name = recvName(d.Recv.List[0].Type) + "." + name
			}
			add(name, d.Pos(), d.End())
		case *ast.GenDecl:
			// A var/const initializer holding a closure is a real home for a base context.
			for _, spec := range d.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok && vs.Values != nil {
					add("<initializer>", d.Pos(), d.End())
				}
			}
		}
	}
	return out
}

func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvName(t.X)
	}
	return "?"
}

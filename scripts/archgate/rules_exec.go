package main

// The split protocol of backend/exec/TaskExecutor is written as prose at the top of executor.go, and
// prose is re-inflatable: a concern gets cut out of the type in one week and handed the type back in
// the next, and the method count climbs to where it started. These three rules make each of its
// sentences a build failure.
//
// execctor — TaskExecutor is built by NewTaskExecutor, so a dependency added to the struct arrives at
// every caller at once. A composite literal anywhere else builds a struct with a field the
// constructor would have set, and the nil only shows up when that path runs. Empty baseline: the tree
// holds exactly one literal (the constructor's), so a new one fails rather than joining a register.
//
// execseam — a collaborator is given the dependencies its own functions use, and never the executor's
// locks. Three shapes give them back, and each is reported:
//
//   - a field or parameter of type *TaskExecutor: every method, taskMu and sandboxMu included;
//   - a field, parameter or result of type *sync.Mutex / *sync.RWMutex: a lock passed by pointer can be
//     stored, copied and handed on, so the protocol asks for func() func() — or func(string) func()
//     for a per-task lock — which cannot be held past the scope that took it;
//   - the identifiers taskMu / sandboxMu outside a method on the type that owns them. taskMu belongs
//     to TaskExecutor and sandboxMu to that task's executionState; the owner's methods are spread over
//     fifteen files and taking its own lock there is the discipline the protocol describes, while a
//     lock reference that reaches any other receiver is the seam leaking.
//
// What it does not report, and why:
//
//   - a value sync.Mutex in any struct (CommitQueue.mu, mergeOrchestrator's commitDone.mu, and
//     executionState's own sandboxMu): the lock belongs to that struct, which is what the protocol wants;
//   - a release-func field (mergeOrchestrator.lockWorktree) or a func value whose body takes the lock
//     on the executor's side (summarizeOrchestrator.runningState): the prescribed shape;
//   - a *executionState field or argument (toolEnv.state): permitted when the concern owns the fields
//     it reads, which is a judgement about fields, not about types;
//   - *TaskExecutor as a result — NewTaskExecutor's own signature.
//
// Two entries are baselined rather than fixed, both helpers that run inside a TaskExecutor method and
// reach it directly: diagnosticsPass.te (one turn-boundary pass over the files that turn changed) and
// handleLoopDetected's te parameter (the loop's termination path). Clearing one means handing that
// helper the func value in its place.
//
// funclen — the size one function may reach. The baseline names the functions over the ceiling —
// TaskExecutor.finalizeTurn and TaskExecutor.executeTask, every other function in backend/, pkg/ and
// main.go sits under it — and it only shrinks: crossing the ceiling is a new violation, so a concern
// being split out cannot grow a new giant on the way.

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

// execDir is the package the split protocol governs. Non-recursive on purpose: the protocol is about
// types that share TaskExecutor's locks, and only this package can.
const execDir = "backend/exec"

// execCtorFile is the one file allowed to build a TaskExecutor — NewTaskExecutor's body.
const execCtorFile = "backend/exec/executor.go"

// execLockOwners names each of the executor's locks and the one type that owns it. taskMu guards the
// map of running tasks and the bridge-recovery latch, so it belongs to TaskExecutor; sandboxMu guards
// a task's sandbox callbacks, so it belongs to the task's own executionState, and sandbox()/setSandbox()
// are its accessors. A method on the owner reaching its own lock is the protocol; the same lock
// reaching any other receiver is the seam leaking.
var execLockOwners = map[string]string{"taskMu": "TaskExecutor", "sandboxMu": "executionState"}

// --- execctor ----------------------------------------------------------------

// scanExecutorConstruction reports every TaskExecutor composite literal outside the constructor,
// counting every literal in the tree as its population: a matcher that stopped recognising the
// literal would otherwise report the empty baseline as clean.
func scanExecutorConstruction(root string) ([]Finding, int, error) {
	files, err := goFilesIncluding(root, true)
	if err != nil {
		return nil, 0, err
	}
	var out []Finding
	literals := 0
	for _, rel := range files {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
		if err != nil {
			continue // a file this rule cannot read is not a violation; the count of literals is the guard
		}
		funcs := funcSpans(fset, f)
		type site struct {
			line int
			n    int
		}
		sites := map[string]*site{}
		var order []string
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isExecutorExpr(lit.Type) {
				return true
			}
			literals++
			if rel == execCtorFile {
				return true
			}
			line := fset.Position(n.Pos()).Line
			// Keyed by function with the count in it, as ctxroot does: a second literal in a
			// function that already has one is one more bypass, not the same bypass again.
			key := rel + "|" + funcs.at(line)
			at, ok := sites[key]
			if !ok {
				at = &site{line: line}
				sites[key] = at
				order = append(order, key)
			}
			at.n++
			return true
		})
		for _, key := range order {
			at := sites[key]
			fn := key[strings.LastIndex(key, "|")+1:]
			out = append(out, Finding{
				File: rel, Line: at.line, Detail: fn + "|x" + itoa(at.n),
				Why: "build it with exec.NewTaskExecutor(Deps{...}): a literal outside the constructor is a " +
					"TaskExecutor with whatever fields the constructor sets left unset, and the missing one " +
					"only shows up when the path that reads it runs",
			})
		}
	}
	if literals == 0 {
		return nil, 0, fmt.Errorf("no TaskExecutor composite literal under %s — the matcher is blind, not the tree", root)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, literals, nil
}

// isExecutorExpr reports that the expression *is* the executor's type — bare, behind a pointer, or
// through the package qualifier — which is what a composite literal's Type slot holds.
func isExecutorExpr(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.StarExpr:
		return isExecutorExpr(t.X)
	case *ast.Ident:
		return t.Name == "TaskExecutor"
	case *ast.SelectorExpr:
		return t.Sel != nil && t.Sel.Name == "TaskExecutor"
	}
	return false
}

// holdsExecutor reports a mention of the executor's type anywhere in a declared type: bare, behind a
// pointer, through the package qualifier, or inside a func value or map. Each of those hands the
// collaborator a live executor.
func holdsExecutor(e ast.Expr) bool {
	return mentions(e, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.Ident:
			return t.Name == "TaskExecutor"
		case *ast.SelectorExpr:
			return t.Sel != nil && t.Sel.Name == "TaskExecutor"
		}
		return false
	})
}

// mentions reports whether any node of an expression satisfies want.
func mentions(e ast.Expr, want func(ast.Node) bool) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if n != nil && want(n) {
			found = true
			return false
		}
		return true
	})
	return found
}

// --- execseam ----------------------------------------------------------------

func scanExecSeam(root string) ([]Finding, int, error) {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(execDir)))
	if err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", execDir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		files = append(files, execDir+"/"+e.Name())
	}
	if len(files) == 0 {
		return nil, 0, fmt.Errorf("no production Go files under %s — the matcher is blind, not the tree", execDir)
	}

	s := &seamScan{seen: map[string]bool{}}
	for _, rel := range files {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
		if err != nil {
			// This package has to compile for the server to build, so a file the rule cannot parse
			// is the rule going blind on the one package it exists for.
			return nil, 0, fmt.Errorf("%s: parse: %v", rel, err)
		}
		s.file = rel
		s.fset = fset
		s.seamFile(f)
	}
	if s.checked == 0 {
		return nil, 0, fmt.Errorf("%s declares no struct fields or function parameters — the matcher is blind, not the tree", execDir)
	}
	sort.Slice(s.out, func(i, j int) bool { return s.out[i].Key() < s.out[j].Key() })
	return s.out, s.checked, nil
}

// seamScan accumulates one package's findings. Detail is the baseline key, so one shape is reported
// once per file rather than once per line.
type seamScan struct {
	file    string
	fset    *token.FileSet
	checked int
	out     []Finding
	seen    map[string]bool
}

func (s *seamScan) add(pos token.Pos, detail, why string) {
	if s.seen[s.file+"|"+detail] {
		return
	}
	s.seen[s.file+"|"+detail] = true
	s.out = append(s.out, Finding{File: s.file, Line: s.fset.Position(pos).Line, Detail: detail, Why: why})
}

const whyExecutorHeld = "a collaborator holding *TaskExecutor can call any method and take taskMu or " +
	"sandboxMu; pass the func value whose body takes the lock on the executor's side of the seam"
const whyLockByPointer = "hand the lock as a release func — func() func(), or func(string) func() for a " +
	"per-task lock — because a *sync.Mutex can be stored, copied and handed on (see LockWorktree)"
const whyExecutorLockReached = "taskMu is TaskExecutor's and sandboxMu is executionState's; a " +
	"collaborator reaches the state they guard through a func value, never through the lock itself"

func (s *seamScan) seamFile(f *ast.File) {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok == token.TYPE {
				s.seamTypes(d)
			}
		case *ast.FuncDecl:
			s.seamFunc(d)
		}
	}
}

func (s *seamScan) seamTypes(d *ast.GenDecl) {
	for _, spec := range d.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		own := ts.Name.Name == "TaskExecutor"
		// Fields of nested struct literals too: mergeOrchestrator's commitDone.mu lives inside one, so
		// a check that read only the outer field list would miss the fields a split grows.
		ast.Inspect(ts.Type, func(n ast.Node) bool {
			fld, ok := n.(*ast.Field)
			if !ok {
				return true
			}
			s.checked++
			name := fieldName(fld)
			if isMutexPointer(fld.Type) {
				s.add(fld.Type.Pos(), ts.Name.Name+"."+name+" is a "+exprText(fld.Type)+" field", whyLockByPointer)
			}
			// A field of the executor's own type on TaskExecutor is a self-reference, not a seam; a
			// copy of someone else's lock is a copy whatever struct declares it.
			if !own && holdsExecutor(fld.Type) {
				s.add(fld.Type.Pos(), ts.Name.Name+"."+name+" holds "+exprText(fld.Type), whyExecutorHeld)
			}
			for _, n := range fld.Names {
				if owner, declared := execLockOwners[n.Name]; declared && ts.Name.Name != owner {
					s.add(n.Pos(), ts.Name.Name+"."+n.Name+" is a copy of the "+owner+" lock", whyExecutorLockReached)
				}
			}
			return true
		})
	}
}

func (s *seamScan) seamFunc(d *ast.FuncDecl) {
	if d.Type == nil {
		return
	}
	if d.Type.Params != nil {
		for _, p := range d.Type.Params.List {
			s.checked++
			if holdsExecutor(p.Type) {
				s.add(p.Pos(), d.Name.Name+" takes "+exprText(p.Type), whyExecutorHeld)
			}
			if isMutexPointer(p.Type) {
				s.add(p.Pos(), d.Name.Name+" takes "+exprText(p.Type), whyLockByPointer)
			}
		}
	}
	if d.Type.Results != nil {
		for _, r := range d.Type.Results.List {
			s.checked++
			if isMutexPointer(r.Type) {
				s.add(r.Pos(), d.Name.Name+" returns "+exprText(r.Type), whyLockByPointer)
			}
		}
	}
	// Params and results included: a constructor that receives a lock under the executor's own name
	// has taken it across the seam whatever its type says.
	recv := recvTypeName(d)
	ast.Inspect(d, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if owner, declared := execLockOwners[id.Name]; !declared || owner == recv {
			return true
		}
		s.add(id.Pos(), funcLabel(d)+" reaches "+id.Name, whyExecutorLockReached)
		return true
	})
}

// recvTypeName is the receiver type's name, or "" for a function that has none.
func recvTypeName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return ""
	}
	return recvName(d.Recv.List[0].Type)
}

// funcLabel names a function the way the ctxroot register does, receiver included, so the baseline
// points at a declaration rather than at a line.
func funcLabel(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	return recvName(d.Recv.List[0].Type) + "." + d.Name.Name
}

func fieldName(fld *ast.Field) string {
	if len(fld.Names) > 0 {
		return fld.Names[0].Name
	}
	return exprText(fld.Type) // an embedded field is its type
}

// isMutexPointer reports a lock handed over by pointer, at any nesting: a func value that answers
// *sync.Mutex leaves the caller free to store it, which is the thing a release func cannot do.
func isMutexPointer(e ast.Expr) bool {
	return mentions(e, func(n ast.Node) bool {
		star, ok := n.(*ast.StarExpr)
		if !ok {
			return false
		}
		sel, ok := star.X.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && pkg.Name == "sync" && (sel.Sel.Name == "Mutex" || sel.Sel.Name == "RWMutex")
	})
}

// exprText renders a field or parameter type for the baseline key. Only the shapes this rule matches
// get spelled out; anything else keys on "type" rather than on source text that moves with formatting.
func exprText(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprText(t.X)
	case *ast.SelectorExpr:
		if t.Sel == nil {
			return "type"
		}
		return exprText(t.X) + "." + t.Sel.Name
	case *ast.FuncType:
		// A func value is what the protocol prescribes, so it is the shape most likely to appear in a
		// baseline entry: name the types it moves, since those are the half that can carry a lock.
		text := "func(" + exprList(t.Params) + ")"
		if held := exprList(t.Results); held != "" {
			text += " -> " + held
		}
		return text
	}
	return "type"
}

func exprList(fields *ast.FieldList) string {
	if fields == nil {
		return ""
	}
	out := make([]string, 0, len(fields.List))
	for _, f := range fields.List {
		out = append(out, exprText(f.Type))
	}
	return strings.Join(out, ", ")
}

// --- funclen -----------------------------------------------------------------

// funcLineCeiling is the size one function may reach, counted from the func keyword to its closing
// brace. 400 sits above every function in the tree but the two the baseline names. A var so a fixture
// can plant a long function without writing 400 lines of it.
var funcLineCeiling = 400

func scanFunctionLength(root string) ([]Finding, int, error) {
	files, err := goFiles(root)
	if err != nil {
		return nil, 0, err
	}
	var out []Finding
	checked := 0
	for _, rel := range files {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
		if err != nil {
			continue
		}
		// The AST, not a brace count: a nested closure belongs to the function it is written in, and
		// a ceiling measured on a brace count would be a ceiling on whichever function opens last.
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.FuncDecl)
			if !ok || d.Body == nil {
				continue // a declaration without a body has no length to report
			}
			checked++
			lines := fset.Position(d.End()).Line - fset.Position(d.Pos()).Line + 1
			if lines <= funcLineCeiling {
				continue
			}
			out = append(out, Finding{
				File: rel, Line: fset.Position(d.Pos()).Line, Detail: funcLabel(d),
				Why: fmt.Sprintf("%d lines against a %d-line ceiling: split it along a seam, do not raise the "+
					"ceiling or add this name to the baseline", lines, funcLineCeiling),
			})
		}
	}
	if checked == 0 {
		return nil, 0, fmt.Errorf("no function declarations under %s — the matcher is blind, not the tree", root)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, checked, nil
}

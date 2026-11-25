package controllers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestJobDecisionsReadBypassCache(t *testing.T) {
	fset := token.NewFileSet()
	files := parsePackageSources(t, fset, ".")
	if len(files) == 0 {
		t.Fatal("no non-test source parsed: the guard checks nothing anymore")
	}

	liveForSites, jobDeciders := 0, 0
	for name, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			where := name + ":" + fn.Name.Name

			for _, call := range callsTo(fn.Body, "jobs", "LiveFor") {
				liveForSites++
				if len(call.Args) < 2 || !isReaderCall(call.Args[1]) {
					t.Errorf("%s: jobs.LiveFor reads through %s instead of r.reader().\n"+
						"The live-Job inventory is the ONLY barrier that "+
						"asks the world rather than the controller's memory. Read through "+
						"the informer cache, it returns an empty list for a Job created "+
						"a few milliseconds earlier, hence \"nothing is running\", hence "+
						"the second apply starts.", where, exprText(fset, call.Args[1]))
				}
			}

			if !declaresJobVar(fn.Body) || !hasCachedJobGet(fn.Body) {
				return true
			}
			jobDeciders++
			if !hasReaderGet(fn.Body) {
				t.Errorf("%s: decides on a batchv1.Job read by r.Get without confirming with "+
					"r.reader().Get.\nA Job missing from the CACHE is not a vanished "+
					"Job: the informer lags, especially on a Job just "+
					"created. Concluding on that read alone clears live reservations, "+
					"and, on a finalize path, it "+
					"removes the finalizer and makes the object vanish.", where)
			}
			return true
		})
	}

	if liveForSites == 0 {
		t.Error("no jobs.LiveFor call found: THE uniqueness guard is gone from the " +
			"package, or the test no longer sees it")
	}
	if jobDeciders == 0 {
		t.Error("no function reads a batchv1.Job with r.Get: the test no longer " +
			"watches the uncached confirmation")
	}
}

func parsePackageSources(t *testing.T, fset *token.FileSet, dir string) map[string]*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	out := map[string]*ast.File{}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", n, err)
		}
		out[n] = f
	}
	return out
}

func callsTo(body *ast.BlockStmt, pkg, fn string) []*ast.CallExpr {
	var out []*ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == fn {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg {
				out = append(out, call)
			}
		}
		return true
	})
	return out
}

func isReaderCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "reader"
}

func declaresJobVar(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		if sel, ok := vs.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Job" {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "batchv1" {
				found = true
			}
		}
		return true
	})
	return found
}

func hasCachedJobGet(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Get" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "r" {
			found = true
		}
		return true
	})
	return found
}

func hasReaderGet(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Get" {
			return true
		}
		if isReaderCall(sel.X) {
			found = true
		}
		return true
	})
	return found
}

func exprText(fset *token.FileSet, e ast.Expr) string {
	start := fset.Position(e.Pos())
	end := fset.Position(e.End())
	if start.Filename != end.Filename {
		return "<expression>"
	}
	data, err := os.ReadFile(start.Filename)
	if err != nil || end.Offset > len(data) {
		return "<expression>"
	}
	return string(data[start.Offset:end.Offset])
}

func TestNoLeaseReleaseIsSwallowed(t *testing.T) {
	fset := token.NewFileSet()
	files := parsePackageSources(t, fset, ".")
	seen := 0
	for name, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
				return true
			}
			blank, ok := assign.Lhs[0].(*ast.Ident)
			if !ok || blank.Name != "_" {
				return true
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Release" {
				return true
			}
			seen++
			t.Errorf("%s:%d: `_ = .Release()` discards a failed lease release. "+
				"Go through releaseLock(), which logs it: a lease that is not "+
				"released keeps the resource waiting until its TTL, and without a trace the "+
				"cause gets looked for elsewhere.", name, fset.Position(assign.Pos()).Line)
			return true
		})
	}
	if _, ok := files["controllers.go"]; !ok {
		t.Fatal("controllers.go not found: the guard checks nothing anymore")
	}
	if seen == 0 && !declaresFunc(files, "releaseLock") {
		t.Error("releaseLock is gone: nobody logs lease releases " +
			"anymore")
	}
}

func declaresFunc(files map[string]*ast.File, name string) bool {
	for _, file := range files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
				return true
			}
		}
	}
	return false
}

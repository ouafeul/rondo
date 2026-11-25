package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var controllersDir = filepath.Join("..", "..", "internal", "controllers")

func TestEveryReconcilerIsWired(t *testing.T) {
	declared := declaredReconcilers(t)
	if len(declared) == 0 {
		t.Fatalf("no SetupWithManager found in %s: the guard checks "+
			"nothing (package moved?)", controllersDir)
	}

	wired := map[string]bool{}
	for _, r := range reconcilers(reconcilerDeps{}) {
		wired[reconcilerName(r)] = true
	}

	for _, name := range declared {
		if !wired[name] {
			t.Errorf("%s declares SetupWithManager but no binary wires it: "+
				"it will never run.\nAdd it to reconcilers() in "+
				"cmd/rondo-controller/main.go.", name)
		}
	}

	declaredSet := map[string]bool{}
	for _, n := range declared {
		declaredSet[n] = true
	}
	for name := range wired {
		if !declaredSet[name] {
			t.Errorf("%s is in reconcilers() but does not declare SetupWithManager "+
				"in %s", name, controllersDir)
		}
	}
}

func TestReconcilersNeedsNoManager(t *testing.T) {
	got := reconcilers(reconcilerDeps{})
	if len(got) == 0 {
		t.Fatal("empty wiring table")
	}
	for i, r := range got {
		if r == nil {
			t.Errorf("entry %d is nil", i)
		}
	}
}

func declaredReconcilers(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, controllersDir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("reading %s: %v", controllersDir, err)
	}

	var out []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name.Name != "SetupWithManager" || fn.Recv == nil {
					continue
				}
				if len(fn.Recv.List) != 1 {
					continue
				}
				if name := receiverTypeName(fn.Recv.List[0].Type); name != "" {
					out = append(out, name)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

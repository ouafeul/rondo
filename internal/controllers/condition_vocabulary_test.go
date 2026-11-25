package controllers

import (
	"go/ast"
	"go/token"
	"sort"
	"strings"
	"testing"
)

func TestEveryDeclaredConditionIsWrittenBySomeone(t *testing.T) {
	declared := declaredConditions(t)
	if len(declared) < 4 {
		t.Fatalf("%d declared condition(s) found in api/v1alpha1: the "+
			"guard checks nothing anymore (file moved?)", len(declared))
	}

	written := conditionsWrittenBy(t, ".")
	if len(written) == 0 {
		t.Fatal("no setCondition call found: the guard compares nothing")
	}

	var orphans []string
	for name := range declared {
		if !written[name] {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("%d declared condition(s) that NOBODY writes: %s\n\n"+
			"A condition of the shared vocabulary is an API promise: documenting it "+
			"without ever setting it lets an operator rely on a guarantee that does "+
			"not exist, and makes all code that READS it structurally dead. "+
			"Either a reconciler sets it (setCondition), or it leaves "+
			"`api/v1alpha1/common.go`.",
			len(orphans), strings.Join(orphans, ", "))
	}
}

func declaredConditions(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	files := parsePackageSources(t, fset, "../../api/v1alpha1")
	out := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for _, name := range spec.Names {
				if strings.HasPrefix(name.Name, "Condition") && name.Name != "Condition" {
					out[name.Name] = true
				}
			}
			return true
		})
	}
	return out
}

func conditionsWrittenBy(t *testing.T, dir string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	files := parsePackageSources(t, fset, dir)
	out := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "setCondition" {
				return true
			}
			for _, arg := range call.Args {
				if name, ok := conditionName(arg); ok {
					out[name] = true
				}
			}
			return true
		})
	}
	return out
}

func conditionName(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		if strings.HasPrefix(v.Sel.Name, "Condition") {
			return v.Sel.Name, true
		}
	case *ast.Ident:
		if strings.HasPrefix(v.Name, "Condition") {
			return v.Name, true
		}
	}
	return "", false
}

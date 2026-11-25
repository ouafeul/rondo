package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const commandsPkg = "."

const rootBuilder = "run"

var commandCtor = regexp.MustCompile(`^cmd[A-Z]`)

func TestEveryCommandIsReachableFromRoot(t *testing.T) {
	declared, calls := parseCommandPackage(t)
	if len(declared) == 0 {
		t.Fatalf("no cmd*() constructor found in %s: the guard checks "+
			"nothing", commandsPkg)
	}
	if _, ok := calls[rootBuilder]; !ok {
		t.Fatalf("%s() not found: the guard does not know where to start", rootBuilder)
	}

	reached := map[string]bool{}
	queue := []string{rootBuilder}
	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]
		for _, callee := range calls[fn] {
			if reached[callee] {
				continue
			}
			reached[callee] = true
			queue = append(queue, callee)
		}
	}

	for _, name := range declared {
		if !reached[name] {
			t.Errorf("%s() builds a command that %s() does not reach: it will "+
				"never be runnable.\nAdd it to root.AddCommand in cmd/rondo/main.go, "+
				"or to a parent command's AddCommand.", name, rootBuilder)
		}
	}
}

func parseCommandPackage(t *testing.T) (declared []string, calls map[string][]string) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, commandsPkg, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("reading %s: %v", commandsPkg, err)
	}

	calls = map[string][]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Body == nil {
					continue
				}
				if commandCtor.MatchString(fn.Name.Name) && returnsCobraCommand(fn) {
					declared = append(declared, fn.Name.Name)
				}
				calls[fn.Name.Name] = calledConstructors(fn)
			}
		}
	}
	sort.Strings(declared)
	return declared, calls
}

func returnsCobraCommand(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		return false
	}
	star, ok := fn.Type.Results.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Command" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "cobra"
}

func calledConstructors(fn *ast.FuncDecl) []string {
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && commandCtor.MatchString(id.Name) {
			out = append(out, id.Name)
		}
		return true
	})
	return out
}

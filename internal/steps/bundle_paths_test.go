package steps

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var runtimeProduced = map[string]string{
	"kubeconfig.yaml":        "exported from the cluster by MgmtStage.InstallKubeconfig",
	"rondo.state.json":       "runtime state (generated tokens) written by internal/state",
	".tofurc":                "network_mirror written by TofuStage.ConfigureRegistry",
	".rondo-bootstrap":       "bootstrap manifests rendered by `rondo sync`",
	"kubeconfig-downstream-": "an RKE2 kubeconfig, exported by a tofu output",
}

var archiveBlocks = map[string]string{
	"iac.tar.gz":          "artifacts",
	"rondo-${ver}.tar.gz": "artifacts",
}

func TestReleaseArchivesCarryEverythingRondoReads(t *testing.T) {
	root := repoRoot(t)
	required := projectDirReads(t, root)
	if len(required) < 4 {
		t.Fatalf("implausible source scan (%v): the "+
			"filepath.Join(ProjectDir, ) detector finds nothing", required)
	}

	for _, p := range required {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("the code reads ProjectDir/%s, missing from the repository: %v", p, err)
		}
	}

	release := readFile(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	for archive, job := range archiveBlocks {
		tokens := tarTokens(t, release, archive)
		for _, p := range required {
			if !containsPath(tokens, p) {
				t.Errorf(".github/workflows/release.yml (job %s, archive %s): %q missing from the tar "+
					"list although rondo reads it under ProjectDir, so the extracted archive "+
					"cannot deploy", job, archive, p)
			}
		}
	}
}

func projectDirReads(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "docs", "vendor", ".cache":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isFilepathJoin(call.Fun) || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Args[0].(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ProjectDir" {
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, uerr := strconv.Unquote(lit.Value)
			if uerr == nil && v != "" && !excluded(v) {
				seen[v] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the sources: %v", err)
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func excluded(p string) bool {
	for prefix := range runtimeProduced {
		if p == prefix || strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func isFilepathJoin(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Join" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "filepath"
}

func tarTokens(t *testing.T, content, archive string) []string {
	t.Helper()
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "tar czf") || !strings.Contains(line, archive) {
			continue
		}
		var tokens []string
		for j := i; j < len(lines); j++ {
			cur := strings.TrimSpace(lines[j])
			cont := strings.HasSuffix(cur, `\`)
			tokens = append(tokens, strings.Fields(strings.TrimSuffix(cur, `\`))...)
			if !cont {
				break
			}
		}
		return tokens
	}
	t.Fatalf("no `tar czf ... %s` in .github/workflows/release.yml", archive)
	return nil
}

func containsPath(tokens []string, want string) bool {
	for _, tok := range tokens {
		if strings.TrimSuffix(tok, "/") == want {
			return true
		}
	}
	return false
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repository root: %v", err)
	}
	return root
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(raw)
}

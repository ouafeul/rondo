package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rondo/internal/platform"
)

func TestComponentLintAcceptsAPublishedTreeWithoutASite(t *testing.T) {
	root := repoRoot(t)
	tree := filepath.Join(root, "cmd", "rondo", "testdata", "vm-tree")
	crds := filepath.Join(root, "manifests", "crds")
	if err := runLint(t, tree, crds); err != nil {
		t.Fatalf("`component lint` refuses the test tree: %v", err)
	}
	findings, err := platform.ValidateDir(tree, crds, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("no warning on this tree: the test does not prove that a " +
			"warning alone exits 0")
	}
}

func TestComponentLintRejectsAnUnknownFieldByItsYAMLPath(t *testing.T) {
	root := repoRoot(t)
	crds := filepath.Join(root, "manifests", "crds")
	tree := copyPublishedTree(t, filepath.Join(root, "cmd", "rondo", "testdata", "vm-tree"))
	comp := filepath.Join(tree, "components", "vm.yaml")
	raw, err := os.ReadFile(comp)
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(raw), "cpu: 4", "cpus: 4", 1)
	if patched == string(raw) {
		t.Fatal("`cpu: 4` missing from the example tree, the test mutates nothing")
	}
	if err := os.WriteFile(comp, []byte(patched), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runLint(t, tree, crds); err == nil {
		t.Fatal("`component lint` exits 0 on an unknown field, which the apply would drop")
	}

	findings, err := platform.ValidateDir(tree, crds, "")
	if err != nil {
		t.Fatal(err)
	}
	const want = "spec.vm.sizing.cpus"
	for _, f := range findings {
		if f.Level == platform.LevelError && strings.Contains(f.Message, want) {
			return
		}
	}
	t.Errorf("no error names %q; findings: %v", want, findings)
}

func TestComponentLintIgnoresTheRONDOManifests(t *testing.T) {
	lint := componentLint()
	for _, name := range []string{"deps"} {
		f := lint.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("flag --%s missing from `component lint`", name)
		}
		if f.DefValue != "" {
			t.Errorf("--%s defaults to %q: a third party would see its tree judged against "+
				"RONDO's manifests", name, f.DefValue)
		}
	}
}

func TestComponentLintTakesItsCRDsFromTheToolingImage(t *testing.T) {
	t.Setenv("RONDO_CRDS_DIR", "/opt/rondo/crds")
	if got := defaultCRDDir(); got != "/opt/rondo/crds" {
		t.Errorf("--crds default = %q, RONDO_CRDS_DIR ignored: in the image, the third party "+
			"would have to pass --crds", got)
	}
	t.Setenv("RONDO_CRDS_DIR", "")
	if got := defaultCRDDir(); got != "manifests/crds" {
		t.Errorf("--crds default = %q outside the image, want the repository's tree", got)
	}
}

func TestToolingImageShipsTheCRDsWhereTheBinaryLooksForThem(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "build", "component-toolbox", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	var env, copied string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "ENV RONDO_CRDS_DIR="); ok {
			env = after
		}
		if after, ok := strings.CutPrefix(line, "COPY manifests/crds "); ok {
			copied = after
		}
	}
	if env == "" || copied == "" {
		t.Fatalf("component-toolbox Dockerfile: ENV RONDO_CRDS_DIR=%q, COPY "+
			"manifests/crds %q, the image does not carry the schema", env, copied)
	}
	if env != copied {
		t.Errorf("the image copies the CRDs to %q and announces %q: `component lint` would "+
			"find no schema there", copied, env)
	}
	t.Setenv("RONDO_CRDS_DIR", env)
	if got := defaultCRDDir(); got != env {
		t.Errorf("--crds default = %q in the image, want %q", got, env)
	}
}

func runLint(t *testing.T, tree, crds string) error {
	t.Helper()
	lint := componentLint()
	lint.SetOut(io.Discard)
	lint.SetErr(io.Discard)
	lint.SetArgs([]string{tree, "--crds", crds})
	return lint.Execute()
}

func copyPublishedTree(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "tree")
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("repository root not found (go.mod)")
	return ""
}

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rondo/internal/version"
)

// published makes this test binary a released rondo-sandbox.
func published(t *testing.T, v string) {
	t.Helper()
	old := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = old })
	flagRondoFrom, flagRondoVersion = "", ""
	t.Setenv("RONDO_SRC_DIR", "")
	t.Setenv("RONDO_SANDBOX_HOME", t.TempDir())
}

func TestRondoFromAClone(t *testing.T) {
	published(t, "dev")
	flagRondoFrom = "../.."
	src, err := resolveRondo()
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs("../..")
	if !src.clone || src.dir != abs {
		t.Errorf("source: %+v", src)
	}
	if env := src.env(); !slices.Contains(env, "RONDO_SANDBOX_RONDO_SOURCE=clone") {
		t.Errorf("script environment: %q", env)
	}
	flagRondoFrom = ""
	t.Setenv("RONDO_SRC_DIR", t.TempDir())
	if _, err := resolveRondo(); err == nil || !strings.Contains(err.Error(), "not an RONDO clone") {
		t.Errorf("RONDO_SRC_DIR outside a clone: %v", err)
	}
}

func TestDevelopmentBuildNeedsAnRondo(t *testing.T) {
	published(t, "dev")
	if _, err := resolveRondo(); err == nil || !strings.Contains(err.Error(), "--rondo-from") {
		t.Errorf("development binary: %v", err)
	}
	flagRondoVersion = "v1.2.3"
	t.Setenv("RONDO_SANDBOX_RONDO_REGISTRY", "")
	if _, err := resolveRondo(); err == nil || !strings.Contains(err.Error(), "RONDO_SANDBOX_RONDO_REGISTRY") {
		t.Errorf("without a registry: %v", err)
	}
}

func TestPublishedBuildDeploysItsOwnRondo(t *testing.T) {
	published(t, "v1.2.3")
	t.Setenv("RONDO_SANDBOX_RONDO_REGISTRY", "registry.example.test/rondo")
	src, err := resolveRondo()
	if err != nil {
		t.Fatal(err)
	}
	if src.clone || src.version != "v1.2.3" || src.registry != "registry.example.test/rondo" {
		t.Errorf("source: %+v", src)
	}
	for _, f := range []string{"manifests/dependencies.yaml", "platform/components", "apps"} {
		if _, err := os.Stat(filepath.Join(src.dir, f)); err != nil {
			t.Errorf("extracted tree without %s: %v", f, err)
		}
	}
	crds, _ := filepath.Glob(filepath.Join(src.dir, "manifests", "crds", "*.yaml"))
	if len(crds) == 0 {
		t.Error("extracted tree without CRDs")
	}
	want := []string{"RONDO_SANDBOX_RONDO_DIR=" + src.dir, "RONDO_SANDBOX_RONDO_SOURCE=registry",
		"RONDO_SANDBOX_RONDO_REGISTRY=registry.example.test/rondo", "RONDO_SANDBOX_RONDO_VERSION=v1.2.3"}
	if got := src.env(); !slices.Equal(got, want) {
		t.Errorf("script environment: %q", got)
	}

	// A published version is laid once: what is there stays.
	kept := filepath.Join(src.dir, "kept")
	if err := os.WriteFile(kept, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveRondo(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Error("tree extracted again for the same version")
	}

	// --rondo-version takes another version's images, with this binary's tree.
	flagRondoVersion = "v1.2.4"
	if src, err := resolveRondo(); err != nil || src.version != "v1.2.4" {
		t.Errorf("--rondo-version: %+v %v", src, err)
	}

	// The build's registry when the environment names none.
	t.Setenv("RONDO_SANDBOX_RONDO_REGISTRY", "")
	old := rondoRegistry
	rondoRegistry = "registry.example.test/rondo"
	t.Cleanup(func() { rondoRegistry = old })
	if src, err := resolveRondo(); err != nil || src.registry != rondoRegistry {
		t.Errorf("binary's registry: %+v %v", src, err)
	}
}

func TestCheckWithThePublishedRondo(t *testing.T) {
	published(t, "v1.2.3")
	t.Setenv("RONDO_SANDBOX_RONDO_REGISTRY", "registry.example.test/rondo")
	if rc := execute([]string{"check", "../../internal/sandbox/testdata/component"}); rc != 0 {
		t.Errorf("check with the embedded RONDO: code %d", rc)
	}
}

func TestDeployHandsTheScriptItsRondo(t *testing.T) {
	published(t, "dev")
	if rc := execute([]string{"deploy"}); rc != 1 {
		t.Errorf("deploy without RONDO: code %d", rc)
	}
	if rc := execute([]string{"deploy", "-c", "absent.yaml", "--rondo-from", "../.."}); rc != 1 {
		t.Errorf("deploy -c to a missing file: code %d", rc)
	}
	install, _, err := newRoot().Find([]string{"component", "install"})
	if err != nil {
		t.Fatal(err)
	}
	if err := install.ParseFlags([]string{"--rondo-from", "../..", "-c", "x.yaml", "--fresh", "x"}); err != nil {
		t.Fatal(err)
	}
	if got := scriptArgs(install, install.Flags().Args()); !slices.Equal(got, []string{"component", "install", "--fresh", "x"}) {
		t.Errorf("the script gets the global options: %q", got)
	}
}

func TestRondoVersionDefaultsToTheBinarysVersion(t *testing.T) {
	published(t, "v1.2.3")
	if f := newRoot().PersistentFlags().Lookup("rondo-version"); f == nil || f.DefValue != "v1.2.3" {
		t.Errorf("--rondo-version default: %+v, want rondo-sandbox's version", f)
	}
	if flagRondoVersion != "v1.2.3" {
		t.Errorf("--rondo-version not passed: %q", flagRondoVersion)
	}
}

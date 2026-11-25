package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rondo/internal/paths"
)

func TestEnvConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RONDO_HOME", home)
	t.Chdir(t.TempDir())

	if got, want := envConfigPath("lab"), filepath.Join(paths.EnvDir("lab"), "rondo.yaml"); got != want {
		t.Errorf("envConfigPath(lab) = %q, want %q", got, want)
	}
	if got := envConfigPath(""); got != "rondo.yaml" {
		t.Errorf("without a manifest: %q", got)
	}
	alt := filepath.Join(home, "rondo.yaml")
	if err := os.WriteFile(alt, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := envConfigPath(""); got != alt {
		t.Errorf("RONDO_HOME's: %q", got)
	}
	if err := os.WriteFile("rondo.yaml", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := envConfigPath(""); got != "rondo.yaml" {
		t.Errorf("the local one first: %q", got)
	}
}

func TestForwardedFlagsNeverSayYes(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)
	if got := forwardedFlags(); len(got) != 0 {
		t.Errorf("by default: %v", got)
	}
	flagLogLevel, flagIacVersion, flagIacChannel, flagYes = "debug", "v1.1.0", "edge-main", true
	got := strings.Join(forwardedFlags(), " ")
	if got != "--log-level debug --iac-version v1.1.0 --iac-channel edge-main" {
		t.Errorf("forwarded: %q", got)
	}
}

func TestTUIIsACommand(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)
	orig := os.Args
	os.Args = []string{"rondo", "tui", "--help"}
	t.Cleanup(func() { os.Args = orig })
	if err := run(); err != nil {
		t.Fatal(err)
	}
}

func TestKubectlPathPrefersTheDownloadedOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RONDO_HOME", home)
	t.Chdir(t.TempDir())
	onPath := t.TempDir()
	t.Setenv("PATH", onPath)
	if _, err := kubectlPath(); err == nil || !strings.Contains(err.Error(), "kubectl not found") {
		t.Errorf("without kubectl: %v", err)
	}
	put := func(dir string) string {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "kubectl")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		abs, _ := filepath.Abs(p)
		return abs
	}
	// Each one put shadows the ones before: PATH, then the repository's, then $RONDO_HOME's.
	for _, dir := range []string{onPath, filepath.Join(".local", "bin"), paths.BinDir()} {
		want := put(dir)
		if got, err := kubectlPath(); err != nil || got != want {
			t.Errorf("kubectl = %q, %v; want %q", got, err, want)
		}
	}
}

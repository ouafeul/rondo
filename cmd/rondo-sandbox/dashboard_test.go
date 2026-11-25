package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeKubectl puts on the PATH a kubectl that records how it was called and answers a
// port-forward like kubectl.
func fakeKubectl(t *testing.T) func() string {
	t.Helper()
	bin, record := t.TempDir(), filepath.Join(t.TempDir(), "calls")
	script := `#!/bin/sh
echo "kubectl $*" >> ` + record + `
case "$*" in
  *port-forward*) echo "Forwarding from 127.0.0.1:8080 -> 80" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0o755); err != nil { //nolint:gosec // a test executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() string {
		raw, _ := os.ReadFile(record)
		return string(raw)
	}
}

func TestDashboardForwardsToTheManagement(t *testing.T) {
	calls := fakeKubectl(t)
	state := t.TempDir()
	t.Setenv("RONDO_SANDBOX_HOME", state)
	kc := filepath.Join(state, "clusters", "rondo-sbx", "kubeconfig.yaml")
	if err := os.MkdirAll(filepath.Dir(kc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kc, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"dashboard", "--no-open"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "RONDO dashboard: http://localhost:8080") {
		t.Errorf("output: %q", out.String())
	}
	if want := "kubectl --kubeconfig " + kc + " -n rondo-system port-forward svc/rondo-dashboard 8080:80"; !strings.Contains(calls(), want) {
		t.Errorf("call:\n%s\nwant %q", calls(), want)
	}
	t.Setenv("RONDO_SANDBOX_HOME", t.TempDir())
	if rc := execute([]string{"dashboard", "--no-open"}); rc != 1 {
		t.Errorf("without a sandbox: code %d", rc)
	}
}

func TestTUIRefusesWithoutASandbox(t *testing.T) {
	t.Setenv("RONDO_SANDBOX_HOME", t.TempDir())
	if rc := execute([]string{"tui"}); rc != 1 {
		t.Errorf("tui without a sandbox: code %d", rc)
	}
	if got := complete(t, ""); !strings.Contains(got, "tui\t") || !strings.Contains(got, "dashboard\t") {
		t.Errorf("commandes :\n%s", got)
	}
}

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runScriptFunctions sources the embedded script without running main, with stubs (name →
// shell body) ahead of PATH, then runs snippet: the script's functions on a workstation
// whose tools the test chooses. It returns the output and the exit status.
func runScriptFunctions(t *testing.T, stubs map[string]string, snippet string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	lib := bytes.TrimSuffix(bytes.TrimRight(script, "\n"), []byte(`main "$@"`))
	if len(lib) == len(bytes.TrimRight(script, "\n")) {
		t.Fatal("the script no longer ends with main \"$@\"")
	}
	if err := os.WriteFile(filepath.Join(dir, "lib.sh"), lib, 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range stubs {
		//nolint:gosec // a test executable
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-c", `source "$1"; `+snippet, "bash", filepath.Join(dir, "lib.sh"))
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RONDO_SANDBOX_BIN=/bin/true", "RONDO_SANDBOX_HOME="+filepath.Join(dir, "home"), "STUB_LOG="+filepath.Join(dir, "calls"))
	out, err := cmd.CombinedOutput()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// BSD df (macOS) has no --output: the disk check is skipped there, not the whole command.
func TestCheckDiskWithoutGNUDf(t *testing.T) {
	docker := `[ "$1" = info ] && echo /var/lib/docker`
	out, code := runScriptFunctions(t, map[string]string{"docker": docker,
		"df": `echo "df: unrecognized option --output" >&2; exit 64`}, "check_disk; echo checked")
	if code != 0 || !strings.Contains(out, "checked") {
		t.Errorf("check_disk with BSD df: exit %d, output %q", code, out)
	}
	out, code = runScriptFunctions(t, map[string]string{"docker": docker,
		"df": `printf 'Use%%\n 91%%\n'`}, "check_disk; echo checked")
	if code == 0 || !strings.Contains(out, "91% full") {
		t.Errorf("check_disk at 91%%: exit %d, output %q", code, out)
	}
}

// A machine gets at most the CPUs of Docker's engine, fewer than the workstation's in a Docker
// VM: docker run refuses more.
func TestMachineCPUsBoundedByDocker(t *testing.T) {
	docker := `echo "$*" >> "$STUB_LOG"
case "$1" in
  info) echo 2 ;;
  inspect) exit 1 ;;
esac`
	snippet := `mkdir -p "$STATE"; echo "rondo-sbx-machine-vm-app-1 vm-app-1 172.28.0.10 4 512 vm app" > "$STATE/machines.txt"
reconcile_machines; cat "$STUB_LOG"`
	out, code := runScriptFunctions(t, map[string]string{"docker": docker}, snippet)
	if code != 0 || !strings.Contains(out, "--cpus 2 --memory 512m") {
		t.Errorf("machine of 4 CPUs on a 2-CPU engine: exit %d, calls:\n%s", code, out)
	}
}

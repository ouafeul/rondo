package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	engine := "/bin/true"
	if _, ok := stubs["engine"]; ok {
		engine = filepath.Join(bin, "engine")
	}
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RONDO_SANDBOX_BIN="+engine, "RONDO_SANDBOX_HOME="+filepath.Join(dir, "home"),
		"RONDO_SANDBOX_RONDO_DIR=../..", "STUB_LOG="+filepath.Join(dir, "calls"))
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

// component install without options hands the engine no empty argument, and runs under the
// bash of macOS (3.2), where an empty array is unbound under set -u.
func TestComponentInstallWithoutOptions(t *testing.T) {
	stubs := map[string]string{
		"engine": `echo "engine $*" >> "$STUB_LOG"
[ "$6" = plan ] && echo acme > "$3/installing"
exit 0`,
		"docker": `case "$1" in info) echo 2 ;; inspect) exit 1 ;; esac`,
	}
	for _, tool := range []string{"k3d", "kubectl", "helm", "openssl", "curl"} {
		stubs[tool] = "exit 0"
	}
	snippet := `mkdir -p "$STATE/pki" component && : > "$STATE/site.json" && : > "$STATE/machines.txt"
cmd_component_install component; cat "$STUB_LOG"`
	out, code := runScriptFunctions(t, stubs, snippet)
	if code != 0 {
		t.Fatalf("component install: exit %d\n%s", code, out)
	}
	for _, want := range []string{"publish acme --registry 127.0.0.1:5100\n",
		"install acme --timeout 30m --max-failures 2\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("engine call %q missing:\n%s", want, out)
		}
	}
}

// Every array the script starts empty is expanded with ${a[@]+"${a[@]}"}: bash before 4.4
// (macOS) takes "${a[@]}" of an empty array for an unbound variable under set -u.
func TestScriptExpandsEmptyArraysSafely(t *testing.T) {
	empty := regexp.MustCompile(`\b([a-z_]+)=\(\)`)
	for _, m := range empty.FindAllSubmatch(script, -1) {
		bare := `"${` + string(m[1]) + `[@]}"`
		guarded := `${` + string(m[1]) + `[@]+` + bare + `}`
		if n := bytes.Count(script, []byte(bare)) - bytes.Count(script, []byte(guarded)); n > 0 {
			t.Errorf("array %s, empty at first, expanded %d time(s) as %s: use %s", m[1], n, bare, guarded)
		}
	}
}

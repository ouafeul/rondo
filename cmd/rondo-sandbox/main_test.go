package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestEnginePlansTheTestComponent(t *testing.T) {
	state := t.TempDir()
	engine := func(args ...string) error {
		return runEngine(append(args, "--state", state, "--rondo-dir", "../.."))
	}
	component := "../../internal/sandbox/testdata/component"
	if err := engine("plan", component, "--ip-prefix", "10.0.0"); err == nil {
		t.Error("plan without a sandbox accepted")
	}
	for _, args := range [][]string{
		{"site", "--config", component + "/rondo-sandbox.yaml", "--ip-prefix", "10.0.0"},
		{"site", "--config", component + "/rondo-sandbox.yaml", "--ip-prefix", "10.0.0", "--downstreams"},
		{"plan", component, "--ip-prefix", "10.0.0"},
	} {
		if err := engine(args...); err != nil {
			t.Fatalf("engine %v: %v", args, err)
		}
	}
	for _, f := range []string{"site.json", "components/acme.json", "clusters.txt", "machines.txt"} {
		if _, err := os.Stat(filepath.Join(state, f)); err != nil {
			t.Errorf("%s missing: %v", f, err)
		}
	}
}

// sandboxState writes the state a deploy, a deploy downstreams and a component install leave.
func sandboxState(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("RONDO_SANDBOX_HOME", state)
	for f, content := range map[string]string{
		"site.json": `{"downstreams":[{"name":"obs","roles":["monitoring"]},{"name":"later","roles":["x"]}],
		  "clusters":[{"name":"rondo-sbx","target":"management","roles":["management"],"management":true},
		  {"name":"rondo-sbx-obs","target":"obs","roles":["monitoring"]}]}`,
		"components/acme.json": `{"component":"acme","enrollments":["web","agent"],"machines":[{"name":"vm-app-1"}]}`,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(state, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, f), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func TestScriptRunsWithTheUsersArguments(t *testing.T) {
	t.Setenv("RONDO_SANDBOX_HOME", t.TempDir())
	if rc := runScript([]string{"help"}); rc != 0 {
		t.Errorf("help: code %d", rc)
	}
	if rc := runScript([]string{"unknown"}); rc != 1 {
		t.Errorf("unknown command: code %d, want 1", rc)
	}
}

func TestExecuteKeepsTheScriptsExitCode(t *testing.T) {
	t.Setenv("RONDO_SANDBOX_HOME", t.TempDir())
	if rc := execute([]string{"version"}); rc != 0 {
		t.Errorf("version: code %d", rc)
	}
	if rc := execute([]string{"status"}); rc != 1 {
		t.Errorf("status without a sandbox: code %d, want 1", rc)
	}
	if rc := execute([]string{"deploy", "downstreams"}); rc != 1 {
		t.Errorf("deploy downstreams without a sandbox: code %d, want the script's (1)", rc)
	}
	if rc := execute([]string{"unknown"}); rc != 1 {
		t.Errorf("unknown command: code %d", rc)
	}
}

func TestScriptArgsRebuildsTheCommandLine(t *testing.T) {
	for _, c := range []struct {
		line, want []string
	}{
		{[]string{"component", "install", "--fresh", "--timeout", "1h", "../acme"},
			[]string{"component", "install", "--fresh", "--timeout", "1h", "../acme"}},
		{[]string{"deploy", "downstreams", "obs"}, []string{"deploy", "downstreams", "obs"}},
		{[]string{"destroy", "--purge"}, []string{"destroy", "--purge"}},
	} {
		cmd, rest, err := newRoot().Find(c.line)
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.ParseFlags(rest); err != nil {
			t.Fatal(err)
		}
		if got := scriptArgs(cmd, cmd.Flags().Args()); !slices.Equal(got, c.want) {
			t.Errorf("script gets %q, want %q", got, c.want)
		}
	}
}

// complete is what the shell gets for a command line ending at toComplete.
func complete(t *testing.T, args ...string) string {
	t.Helper()
	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs(append([]string{"__complete"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestCompletion(t *testing.T) {
	sandboxState(t)
	for name, c := range map[string]struct {
		args []string
		want []string
	}{
		"commands": {[]string{""}, []string{"init\t", "deploy\t", "component\t", "destroy\t", "status\t",
			"kubeconfig\t", "completion\t"}},
		"component":            {[]string{"component", "install", ""}, []string{":16"}},
		"flags":                {[]string{"component", "install", "--"}, []string{"--fresh", "--no-build", "--timeout"}},
		"uninstall":            {[]string{"component", "uninstall", ""}, []string{"acme\n", ":4"}},
		"subcommands":          {[]string{"component", ""}, []string{"new\t", "install\t", "uninstall\t"}},
		"new component":        {[]string{"component", "new", ""}, []string{":16"}},
		"archetypes":           {[]string{"component", "new", "--archetype", ""}, []string{"a,b\n", "a\n", "b\n", ":4"}},
		"declared downstreams": {[]string{"deploy", "downstreams", ""}, []string{"obs\n", "later\n", ":4"}},
		"deployed downstreams": {[]string{"destroy", "downstreams", ""}, []string{"obs\n:4"}},
		"enrollments":          {[]string{"logs", ""}, []string{"agent", "web", ":4"}},
		"destroy":              {[]string{"destroy", "--"}, []string{"--purge"}},
		"no files":             {[]string{"status", ""}, []string{":4"}},
		"kubeconfig": {[]string{"kubeconfig", ""}, []string{"management\tcluster rondo-sbx", "obs\tcluster rondo-sbx-obs",
			"monitoring\trole, cluster rondo-sbx-obs", ":4"}},
		"config": {[]string{"-c", ""}, []string{"yaml", ":8"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := complete(t, c.args...)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("completion of %q without %q:\n%s", c.args, w, got)
				}
			}
		})
	}
}

func TestCompletionScripts(t *testing.T) {
	for shell, want := range map[string]string{"bash": "__start_rondo-sandbox", "zsh": "#compdef rondo-sandbox",
		"fish": "complete -c rondo-sandbox"} {
		root := newRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs([]string{"completion", shell})
		if err := root.Execute(); err != nil || !strings.Contains(out.String(), want) {
			t.Errorf("completion %s: %v, without %q", shell, err, want)
		}
	}
}

func TestKubeconfigExportsTheClusterOfATarget(t *testing.T) {
	state := t.TempDir()
	t.Setenv("RONDO_SANDBOX_HOME", state)
	run := func(args ...string) (string, error) {
		root := newRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(append([]string{"kubeconfig"}, args...))
		err := root.Execute()
		return out.String(), err
	}
	if out, err := run(); err != nil || out != "export KUBECONFIG="+filepath.Join(state, "clusters", "rondo-sbx", "kubeconfig.yaml")+"\n" {
		t.Errorf("kubeconfig without a sandbox: %q (%v), want the management cluster", out, err)
	}
	sandboxState(t)
	for _, ref := range []string{"monitoring", "obs", "rondo-sbx-obs"} {
		out, err := run(ref)
		if err != nil || !strings.HasSuffix(out, filepath.Join("rondo-sbx-obs", "kubeconfig.yaml")+"\n") {
			t.Errorf("kubeconfig %s: %q (%v)", ref, out, err)
		}
	}
	if _, err := run("dmz"); err == nil || !strings.Contains(err.Error(), "management, obs") {
		t.Errorf("unknown role: %v, want the known Targets", err)
	}
}

func TestDebugCommands(t *testing.T) {
	t.Setenv("RONDO_SANDBOX_HOME", t.TempDir())
	t.Setenv("RONDO_SRC_DIR", "")
	if rc := execute([]string{"check", "--rondo-from", "../..", "../../internal/sandbox/testdata/component"}); rc != 0 {
		t.Errorf("check of the test component: code %d", rc)
	}
	if rc := execute([]string{"check", "../../internal/sandbox/testdata/component"}); rc != 1 {
		t.Errorf("check without RONDO (development binary): code %d", rc)
	}
	if rc := execute([]string{"check", "--rondo-from", "../..", "-c", "absent.yaml",
		"../../internal/sandbox/testdata/component"}); rc != 1 {
		t.Errorf("check against a site without downstreams: code %d, the monitoring role has no Target", rc)
	}
	sandboxState(t)
	if got := complete(t, "ssh", ""); !strings.Contains(got, "vm-app-1\n:4") {
		t.Errorf("completion of ssh:\n%s", got)
	}
	if rc := execute([]string{"ssh", "missing"}); rc != 1 {
		t.Errorf("ssh to an unknown machine: code %d", rc)
	}
	for _, cmd := range []string{"why", "values", "template", "pods", "tail", "restart"} {
		if got := complete(t, cmd, ""); !strings.Contains(got, "agent\nweb\n:4") {
			t.Errorf("completion of %s:\n%s", cmd, got)
		}
	}
	if got := complete(t, "check", ""); !strings.Contains(got, ":16") {
		t.Errorf("check completes a directory:\n%s", got)
	}
}

func TestVersionFlagSaysWhatVersionSays(t *testing.T) {
	out := func(args ...string) string {
		root := newRoot()
		var b bytes.Buffer
		root.SetOut(&b)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return b.String()
	}
	want := versionLine()
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		if got := out(args...); got != want {
			t.Errorf("%v: %q, want %q", args, got, want)
		}
	}
}

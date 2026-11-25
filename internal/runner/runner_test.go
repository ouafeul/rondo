package runner

import (
	"context"
	"strings"
	"testing"
)

func TestEnvPrecedence(t *testing.T) {
	t.Setenv("RONDO_TEST_KEY", "ambient")
	e := NewExec(map[string]string{"RONDO_TEST_KEY": "base", "ONLY_BASE": "b"})
	out, err := e.Output(context.Background(), Cmd{
		Name: "sh", Args: []string{"-c", "echo $RONDO_TEST_KEY $ONLY_BASE $ONLY_CALL"},
		Env: map[string]string{"RONDO_TEST_KEY": "call", "ONLY_CALL": "c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "call b c" {
		t.Errorf("env precedence: %q", out)
	}
}

func TestOutputCapturesStderrInError(t *testing.T) {
	e := NewExec(nil)
	_, err := e.Output(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo oops >&2; exit 3"}})
	if err == nil || !strings.Contains(err.Error(), "oops") {
		t.Fatalf("stderr missing from the error: %v", err)
	}
}

func TestRunDir(t *testing.T) {
	e := NewExec(nil)
	dir := t.TempDir()
	out, err := e.Output(context.Background(), Cmd{Name: "pwd", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.TrimSpace(out), dir) {
		t.Errorf("Dir not honored: %q != %q", out, dir)
	}
}

func TestRunStdin(t *testing.T) {
	e := NewExec(nil)
	out, err := e.Output(context.Background(), Cmd{Name: "cat", Stdin: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "secret" {
		t.Errorf("stdin not passed: %q", out)
	}
}

func TestDryRunSkipsRunButDelegatesOutput(t *testing.T) {
	fake := &Fake{Outputs: map[string]string{"kubectl get nodes": "node1"}}
	d := DryRun{Real: fake}
	if err := d.Run(context.Background(), Cmd{Name: "tofu", Args: []string{"apply"}}); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("DryRun.Run delegated: %v", fake.Calls)
	}
	out, err := d.Output(context.Background(), Cmd{Name: "kubectl", Args: []string{"get", "nodes"}})
	if err != nil || out != "node1" {
		t.Errorf("Output not delegated: %q %v", out, err)
	}
}

func TestFakeRecordsAndErrs(t *testing.T) {
	f := &Fake{Errs: map[string]error{"tofu init": context.Canceled}}
	if err := f.Run(context.Background(), Cmd{Name: "tofu", Args: []string{"init"}}); err != context.Canceled {
		t.Errorf("programmed error not returned: %v", err)
	}
	if _, err := f.Output(context.Background(), Cmd{Name: "unknown"}); err == nil {
		t.Error("unprogrammed output accepted")
	}
	if len(f.Calls) != 2 {
		t.Errorf("recorded calls: %d", len(f.Calls))
	}
}

package runner

import (
	"context"
	"strings"
	"testing"
	"time"
)

const testBound = 300 * time.Millisecond

const testMargin = 10 * time.Second

func TestDryRunOutputIsBounded(t *testing.T) {
	d := DryRun{Real: NewExec(nil), ReadTimeout: testBound}
	start := time.Now()
	out, err := d.Output(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "sleep 30"}})
	elapsed := time.Since(start)

	if elapsed > testMargin {
		t.Fatalf("unbounded read: %s for a bound of %s, a preview must return "+
			"even when the remote service never answers", elapsed, testBound)
	}
	if err == nil {
		t.Fatalf("timed-out read returned as SUCCESS (output %q): the caller would believe "+
			"it read the cluster state", out)
	}
	if out != "" {
		t.Errorf("partial output returned after timeout: %q", out)
	}
	for _, want := range []string{"read abandoned", testBound.String(), "preview"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not explain the abandon (want %q): %v", want, err)
		}
	}
}

func TestDryRunOutputDoesNotWaitForAGrandchild(t *testing.T) {
	d := DryRun{Real: NewExec(nil), ReadTimeout: testBound}
	start := time.Now()
	_, err := d.Output(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "sleep 30 & wait"}})
	elapsed := time.Since(start)

	if elapsed > testMargin {
		t.Fatalf("unbounded read with a surviving descendant: %s, killing the "+
			"process is not enough, the read must also stop waiting on its pipes", elapsed)
	}
	if err == nil {
		t.Fatal("timed-out read returned as SUCCESS")
	}
}

func TestOutputOutsideDryRunIsNotBounded(t *testing.T) {
	slow := Cmd{Name: "sh", Args: []string{"-c", "sleep 1; echo late"}}

	out, err := NewExec(nil).Output(context.Background(), slow)
	if err != nil {
		t.Fatalf("the real runner bounded a long read: %v", err)
	}
	if strings.TrimSpace(out) != "late" {
		t.Fatalf("output truncated outside dry-run: %q", out)
	}

	if _, err := (DryRun{Real: NewExec(nil), ReadTimeout: testBound}).Output(
		context.Background(), slow); err == nil {
		t.Error("the same read is not bounded under the dry-run decorator: " +
			"the preview would wait as long as a real deployment")
	}
}

func TestDryRunReadTimeoutDefault(t *testing.T) {
	if got := (DryRun{Real: NewExec(nil)}).readTimeout(); got != DryRunReadTimeout {
		t.Errorf("zero ReadTimeout not replaced by the default: %s", got)
	}
	if DryRunReadTimeout <= 0 || DryRunReadTimeout > 15*time.Second {
		t.Errorf("DryRunReadTimeout = %s: outside the window \"enough for a cluster that "+
			"answers, too little to wait for a TCP timeout\"", DryRunReadTimeout)
	}
}

func TestDryRunOutputDistinguishesExternalCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (DryRun{Real: NewExec(nil), ReadTimeout: time.Minute}).Output(
		ctx, Cmd{Name: "sh", Args: []string{"-c", "echo ok"}})
	if err == nil {
		t.Fatal("context already canceled: the read should have failed")
	}
	if strings.Contains(err.Error(), "read abandoned") {
		t.Errorf("an external cancellation is reported as the bound expiring: %v", err)
	}
}

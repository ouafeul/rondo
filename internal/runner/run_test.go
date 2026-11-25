package runner

import (
	"context"
	"testing"
)

func TestRunStreamsAndSucceeds(t *testing.T) {
	e := NewExec(nil)
	if err := e.Run(context.Background(), Cmd{Name: "true"}); err != nil {
		t.Fatalf("Run true: %v", err)
	}
}

func TestRunWithStdin(t *testing.T) {
	e := NewExec(nil)
	if err := e.Run(context.Background(), Cmd{Name: "cat", Stdin: "data"}); err != nil {
		t.Fatalf("Run cat (stdin): %v", err)
	}
}

func TestRunFailureWraps(t *testing.T) {
	e := NewExec(nil)
	if err := e.Run(context.Background(), Cmd{Name: "false"}); err == nil {
		t.Fatal("a failing command should return an error")
	}
	if err := e.Run(context.Background(), Cmd{Name: "false", Stdin: "x"}); err == nil {
		t.Fatal("a failing command (stdin) should return an error")
	}
}

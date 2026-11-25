package tui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteStartsTheForwardAgainWhenItDied(t *testing.T) {
	dir := t.TempDir()
	kubectl := newDashboard(t, dir)
	r, err := ConnectRemote(kubectl, "rondo-prod")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	if _, err := r.Targets(ctx); err != nil {
		t.Fatal(err)
	}
	first := r.pf.Process.Pid
	_ = r.pf.Process.Kill()
	<-r.exited
	got, err := r.Components(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("after the port-forward died: %+v, %v", got, err)
	}
	if r.pf.Process.Pid == first {
		t.Error("the same dead port-forward was reused")
	}
	trees, err := r.Trees(ctx)
	if err != nil || len(trees) != 3 || trees[0].Name != "acme" || len(trees[0].Objects) != 2 ||
		trees[1].Name != "base-management" || trees[2].Name != "rondo" {
		t.Errorf("trees = %+v, %v", trees, err)
	}
	if err := r.Close(); err != nil || r.pf != nil {
		t.Errorf("close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

func TestRemoteSaysWhyKubectlFailed(t *testing.T) {
	dir := t.TempDir()
	kubectl := writeKubectl(t, dir, `case "$*" in
"config get-contexts -o name") echo rondo-lab ;;
*port-forward*) echo 'error: services "rondo-dashboard" not found' >&2; exit 1 ;;
*) echo 'error: the server has asked for the client to provide credentials' >&2; exit 1 ;;
esac`)
	r, err := ConnectRemote(kubectl, "rondo-lab")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Overview(context.Background()); err == nil || !strings.Contains(err.Error(), `services "rondo-dashboard" not found`) {
		t.Errorf("port-forward: %v", err)
	}
	if _, err := r.Trees(context.Background()); err == nil || !strings.Contains(err.Error(), "provide credentials") {
		t.Errorf("get configmaps: %v", err)
	}
	if _, err := ConnectRemote(kubectl, "rondo-prod"); err == nil || !strings.Contains(err.Error(), "rondo-prod not found") {
		t.Errorf("missing context: %v", err)
	}
	if _, err := ConnectRemote(filepath.Join(dir, "missing"), "rondo-lab"); err == nil {
		t.Error("missing kubectl accepted")
	}
}

func TestRemoteGivesUpOnAForwardWithoutPort(t *testing.T) {
	dir := t.TempDir()
	kubectl := writeKubectl(t, dir, `case "$*" in
"config get-contexts -o name") echo rondo-lab ;;
*) exec sleep 600 ;;
esac`)
	r, err := ConnectRemote(kubectl, "rondo-lab")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := r.JobLogs(ctx, "j", 10); err == nil || !strings.Contains(err.Error(), "no port opened") {
		t.Errorf("without a port: %v", err)
	}
	if r.pf != nil {
		t.Error("a port-forward without a port was kept")
	}
}

func TestRemoteReadsLogs(t *testing.T) {
	dir := t.TempDir()
	r, err := ConnectRemote(newDashboard(t, dir), "rondo-default")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	for _, open := range []func() (string, error){
		func() (string, error) { return readAll(r.JobLogs(context.Background(), "helm-traefik", 5)) },
		func() (string, error) { return readAll(r.ServiceLogs(context.Background(), "rondo-controller-abc", 5)) },
	} {
		if got, err := open(); err != nil || got != "fake logs" {
			t.Errorf("logs = %q, %v", got, err)
		}
	}
	if _, err := r.JobLogs(context.Background(), "foreign", 5); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("Job unknown to the dashboard: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "configmaps.json"))
	if _, err := r.Trees(context.Background()); err == nil {
		t.Error("empty kubectl output accepted")
	}
}

func readAll(rc io.ReadCloser, err error) (string, error) {
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	return string(b), err
}

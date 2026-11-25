package main

import (
	"context"
	"os"
	"path/filepath"
	"rondo"
	"strconv"
	"strings"
	"testing"
	"time"

	"rondo/internal/config"
	"rondo/internal/steps"
)

const fakeKubectlSleep = 60 * time.Second

const planCeiling = 30 * time.Second

func TestDryRunPlanRunsToTheEndWithoutACluster(t *testing.T) {
	c := dryRunWithoutReachableCluster(t)

	start := time.Now()
	d, err := newDeps(context.Background(), c)
	if err == nil {
		err = steps.Execute(context.Background(), d.deployPlan())
	}
	elapsed := time.Since(start)

	if elapsed > planCeiling {
		t.Fatalf("the dry-run plan waited %s for an unreachable cluster: a `--dry-run` "+
			"must run from a workstation without a cluster, so never wait for the network",
			elapsed)
	}
	if err != nil {
		t.Fatalf("dry-run plan INTERRUPTED for lack of a cluster: %v\n"+
			"a dry run's reads are checks, not prerequisites", err)
	}
}

func dryRunWithoutReachableCluster(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"manifests", filepath.Join("platform", "components"),
		filepath.Join(".local", "bin")} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, body string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join("manifests", "dependencies.yaml"), "binaries: []\n", 0o644)
	write(filepath.Join("platform", "components", "probe.yaml"),
		"apiVersion: rondo.ouafeul.github.io/v1alpha1\nkind: Component\nmetadata:\n  name: probe\n", 0o600)
	write("ca.pem", "PEM\n", 0o600)
	write("rondo.yaml", strings.Replace(string(rondo.ExampleConfig), "./certs/mirror-ca.pem", "./ca.pem", 1), 0o600)
	write(filepath.Join(".local", "bin", "kubectl"),
		"#!/bin/sh\nexec sleep "+strconv.Itoa(int(fakeKubectlSleep.Seconds()))+"\n", 0o755)

	t.Chdir(dir)
	setFlagForTest(t, &flagConfig, "rondo.yaml")
	setFlagForTest(t, &flagDryRun, true)
	setFlagForTest(t, &flagSkipDownload, true)

	cfg, err := config.Load("rondo.yaml")
	if err != nil {
		t.Fatalf("invalid fixture manifest: %v", err)
	}
	return cfg
}

func setFlagForTest[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

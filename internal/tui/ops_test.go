package tui

import (
	"strings"
	"testing"
)

func TestFormArgsAndGlobalFlags(t *testing.T) {
	m := New(Options{Env: "prod", Forward: []string{"--iac-channel", "edge-main"}})
	f := newForm(m, opInstall, "acme")
	f.fields[1].input.SetValue("1.4.0")
	f.fields[3].input.SetValue(" /srv/acme ")
	args, err := f.args(false)
	if err != nil {
		t.Fatal(err)
	}
	m.dryRun = true
	got := strings.Join(m.commandArgs(opInstall, args), " ")
	want := "component install acme --version 1.4.0 --from /srv/acme --env prod --dry-run --iac-channel edge-main"
	if got != want {
		t.Errorf("args = %q\nwant %q", got, want)
	}

	m.config = "/etc/rondo.yaml"
	init := newForm(m, opInit, "")
	init.fields[1].input.SetValue("artifactory")
	init.fields[2].on = true
	args, _ = init.args(false)
	if got := strings.Join(m.commandArgs(opInit, args), " "); got != "init --env default --preset artifactory --force --config /etc/rondo.yaml --iac-channel edge-main" {
		t.Errorf("init = %q: neither --dry-run nor the current environment", got)
	}
}

func TestPickFieldSelectsTargets(t *testing.T) {
	f := &form{op: opDestroyDownstreams, fields: []field{pickField("rke2 Targets", "", []string{"alpha", "beta"}, "beta")}}
	args, _ := f.args(false)
	if strings.Join(args, " ") != "destroy downstreams beta" {
		t.Errorf("args = %v", args)
	}
	f.update(keyPress("a"))
	args, _ = f.args(false)
	if strings.Join(args, " ") != "destroy downstreams alpha beta" {
		t.Errorf("check all: %v", args)
	}
	f.update(keyPress("up"))
	f.update(keyPress("space"))
	args, _ = f.args(false)
	if strings.Join(args, " ") != "destroy downstreams beta" {
		t.Errorf("uncheck alpha: %v", args)
	}
}

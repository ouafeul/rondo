package steps

import (
	"context"
	"errors"
	"strings"
	"testing"

	"rondo/internal/runner"
)

func serverURLWS(t *testing.T) Workspace {
	t.Helper()
	dir := t.TempDir()
	return Workspace{ProjectDir: dir, StateDir: dir, BinDir: "/bin", CACert: "/ca"}
}

const readSettingCmd = "/bin/kubectl get settings.management.cattle.io server-url -o jsonpath={.value}"

func patchCallFor(calls []runner.Cmd) *runner.Cmd {
	for i := range calls {
		if len(calls[i].Args) > 0 && calls[i].Args[0] == "patch" {
			return &calls[i]
		}
	}
	return nil
}

func TestServerURLIsSetWhenEmpty(t *testing.T) {
	ws := serverURLWS(t)
	fake := &runner.Fake{Outputs: map[string]string{readSettingCmd: ""}}
	b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, fake), nil, false)

	if err := b.EnsureServerURL(context.Background()); err != nil {
		t.Fatalf("EnsureServerURL: %v", err)
	}
	patch := patchCallFor(fake.Calls)
	if patch == nil {
		t.Fatal("no `kubectl patch`: the setting stays empty and downstream nodes " +
			"will start without CATTLE_SERVER")
	}
	joined := strings.Join(patch.Args, " ")
	if !strings.Contains(joined, `{"value":"https://rancher.example.com"}`) {
		t.Errorf("the patch does not carry the URL derived from the site hostname: %q", joined)
	}
	if !strings.Contains(joined, RancherSettingsResource) {
		t.Errorf("the patch does not target %s (fully qualified name): %q",
			RancherSettingsResource, joined)
	}
}

func TestServerURLDoesNotOverwriteAnExistingValue(t *testing.T) {
	ws := serverURLWS(t)
	fake := &runner.Fake{Outputs: map[string]string{
		readSettingCmd: "https://rancher-vip.example.com",
	}}
	b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, fake), nil, false)

	if err := b.EnsureServerURL(context.Background()); err != nil {
		t.Fatalf("EnsureServerURL: %v", err)
	}
	if patch := patchCallFor(fake.Calls); patch != nil {
		t.Fatalf("a value set by hand was OVERWRITTEN (%v): already enrolled agents "+
			"would reach another address", patch.Args)
	}
}

func TestServerURLDoesNotRepatchAMatchingValue(t *testing.T) {
	ws := serverURLWS(t)
	fake := &runner.Fake{Outputs: map[string]string{
		readSettingCmd: "https://rancher.example.com\n",
	}}
	b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, fake), nil, false)

	if err := b.EnsureServerURL(context.Background()); err != nil {
		t.Fatalf("EnsureServerURL: %v", err)
	}
	if patch := patchCallFor(fake.Calls); patch != nil {
		t.Fatalf("setting already correct was rewritten: %v", patch.Args)
	}
}

func TestServerURLNeverFailsTheDeployment(t *testing.T) {
	ctx := context.Background()

	t.Run("unreadable setting", func(t *testing.T) {
		ws := serverURLWS(t)
		fake := &runner.Fake{}
		b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, fake), nil, false)
		if err := b.EnsureServerURL(ctx); err != nil {
			t.Fatalf("failed read turned into a deployment failure: %v", err)
		}
	})

	t.Run("patch refused", func(t *testing.T) {
		ws := serverURLWS(t)
		fake := &runner.Fake{
			Outputs: map[string]string{readSettingCmd: ""},
			Errs: map[string]error{
				"/bin/kubectl patch settings.management.cattle.io server-url --type=merge " +
					`-p {"value":"https://rancher.example.com"}`: errors.New("forbidden"),
			},
		}
		b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, fake), nil, false)
		if err := b.EnsureServerURL(ctx); err != nil {
			t.Fatalf("refused patch turned into a deployment failure: %v", err)
		}
		if patchCallFor(fake.Calls) == nil {
			t.Fatal("no patch attempted: the test does not exercise the path it claims to")
		}
	})

}

func TestServerURLDryRunReadsButDoesNotPatch(t *testing.T) {
	ws := serverURLWS(t)
	fake := &runner.Fake{Outputs: map[string]string{readSettingCmd: ""}}
	b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, fake), nil, true)

	if err := b.EnsureServerURL(context.Background()); err != nil {
		t.Fatalf("EnsureServerURL in dry-run: %v", err)
	}
	if patch := patchCallFor(fake.Calls); patch != nil {
		t.Fatalf("`--dry-run` MUTATED the cluster: %v", patch.Args)
	}
	if len(fake.Calls) == 0 {
		t.Fatal("no read in dry-run: the preview would say nothing about the missing setting")
	}
}

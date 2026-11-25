package steps

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func missingLookPath(present ...string) func(string) (string, error) {
	ok := map[string]bool{}
	for _, p := range present {
		ok[p] = true
	}
	return func(name string) (string, error) {
		if ok[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("executable file not found in $PATH")
	}
}

func TestPreflightNamesTheMissingPrerequisites(t *testing.T) {
	p := NewPreflightStage(false, false)
	p.lookPath = missingLookPath("ssh-keygen", "bash", "ssh", "scp")

	err := p.AssertHostTools(context.Background())
	if err == nil {
		t.Fatal("workstation without sshpass accepted: the failure will happen in the middle of an scp")
	}
	if !strings.Contains(err.Error(), "sshpass") {
		t.Errorf("the message does not name the missing binary: %v", err)
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("the message does not say what the binary is for: %v", err)
	}
}

func TestPreflightListsEveryMissingTool(t *testing.T) {
	p := NewPreflightStage(false, false)
	p.lookPath = missingLookPath("bash")

	err := p.AssertHostTools(context.Background())
	if err == nil {
		t.Fatal("bare workstation accepted")
	}
	for _, want := range []string{"ssh-keygen", "ssh", "scp", "sshpass"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s missing from the message: %v", want, err)
		}
	}
}

func TestPreflightWithoutManagementInfraDoesNotRequireSSH(t *testing.T) {
	p := NewPreflightStage(true, false)
	p.lookPath = missingLookPath("ssh-keygen")

	if err := p.AssertHostTools(context.Background()); err != nil {
		t.Fatalf("deploy downstreams refused over SSH tools it does not need: %v", err)
	}
}

func TestRPMDeclaresTheWorkstationPrerequisites(t *testing.T) {
	spec := readFile(t, filepath.Join(repoRoot(t), "packaging", "nfpm.yaml"))
	for _, tool := range append(append([]hostTool{}, hostToolsAlways...), hostToolsMgmtInfra...) {
		if !strings.Contains(spec, "- "+tool.pkg) {
			t.Errorf("packaging/nfpm.yaml does not declare %q (provides %s, needed for: %s): "+
				"`dnf install rondo` would ship a binary unable to deploy",
				tool.pkg, tool.name, tool.why)
		}
	}
	if !strings.Contains(spec, "depends:") {
		t.Error("packaging/nfpm.yaml has no depends section")
	}
}

func TestPreflightInDryRunWarnsWithoutRefusing(t *testing.T) {
	p := NewPreflightStage(false, true)
	p.lookPath = func(name string) (string, error) {
		if name == "sshpass" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}
	if err := p.AssertHostTools(context.Background()); err != nil {
		t.Fatalf("dry-run: the preflight must WARN, not refuse, yet it returns %v", err)
	}

	strict := NewPreflightStage(false, false)
	strict.lookPath = p.lookPath
	err := strict.AssertHostTools(context.Background())
	if err == nil {
		t.Fatal("outside dry-run: a workstation without sshpass must be refused")
	}
	if !strings.Contains(err.Error(), "sshpass") {
		t.Fatalf("the refusal must name sshpass, got: %v", err)
	}
}

package steps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rondo/internal/runner"
)

func wsWithCA(t *testing.T) Workspace {
	t.Helper()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, []byte("ARTI-CA"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Workspace{ProjectDir: dir, StateDir: dir, BinDir: "/bin", CACert: ca}
}

func planFake(showJSON string) *runner.Fake {
	return &runner.Fake{Outputs: map[string]string{"/bin/tofu show -json tfplan-management": showJSON}}
}

func tofuApplied(fake *runner.Fake) bool {
	for _, c := range fake.Calls {
		if strings.HasPrefix(c.String(), "/bin/tofu apply") {
			return true
		}
	}
	return false
}

func TestPlanWithoutChangesIsNotApplied(t *testing.T) {
	fake := planFake(`{"resource_changes":[{"address":"a","change":{"actions":["no-op"]}}]}`)
	st := NewTofuStage(testCfg(), wsWithCA(t), fake, fixedToken("t"), false)
	if err := st.ApplyManagement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tofuApplied(fake) {
		t.Error("apply run on a plan without changes")
	}
}

func TestDestructivePlanAsksForConfirmation(t *testing.T) {
	fake := planFake(`{"resource_changes":[{"address":"vsphere_virtual_machine.k3s[0]","change":{"actions":["delete","create"]}}]}`)
	st := NewTofuStage(testCfg(), wsWithCA(t), fake, fixedToken("t"), false)
	var asked string
	st.Confirm = func(q string) error { asked = q; return errors.New("cancelled") }
	if err := st.ApplyManagement(context.Background()); err == nil {
		t.Fatal("refused confirmation ignored")
	}
	if !strings.Contains(asked, "vsphere_virtual_machine.k3s[0]") {
		t.Errorf("question = %q, must name what would be destroyed", asked)
	}
	if tofuApplied(fake) {
		t.Error("destructive plan applied without confirmation")
	}
}

func TestNonDestructivePlanAppliesWithoutAsking(t *testing.T) {
	fake := planFake(`{"resource_changes":[{"address":"x","change":{"actions":["update"]}}]}`)
	st := NewTofuStage(testCfg(), wsWithCA(t), fake, fixedToken("t"), false)
	st.Confirm = func(string) error { t.Error("confirmation asked without any destruction"); return nil }
	if err := st.ApplyManagement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !tofuApplied(fake) {
		t.Error("plan not applied")
	}
}

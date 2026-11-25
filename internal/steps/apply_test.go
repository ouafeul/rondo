package steps

import (
	"context"
	"errors"
	"strings"
	"testing"

	"rondo/internal/kobj"
)

func recordCmd(p *PlatformStage, tree string) string {
	return p.ws.Tool("kubectl") + " -n " + kobj.PlatformNamespace + " get configmap " +
		kobj.RecordName(tree) + " --ignore-not-found -o " + dataPath("objects")
}

// index of the first call containing each fragment, -1 when absent.
func callIndex(calls []string, fragment string) int {
	for i, c := range calls {
		if strings.Contains(c, fragment) {
			return i
		}
	}
	return -1
}

func TestUninstallTreeRemovesEnrollmentsBeforeComponents(t *testing.T) {
	p, _, fk := capture(t, siteCfg())
	fk.Outputs[recordCmd(p, "hello")] = "Component/hello-vm\nEnrollment/hello-vm\n"

	if err := p.UninstallTree(context.Background(), "hello"); err != nil {
		t.Fatalf("UninstallTree: %v", err)
	}

	var calls []string
	for _, c := range fk.Calls {
		calls = append(calls, c.String())
	}
	delEnr := callIndex(calls, "delete enrollments."+kobj.APIGroup+" hello-vm")
	wait := callIndex(calls, "wait --for=delete --timeout="+EnrollmentRemovalTimeout.String()+
		" enrollments."+kobj.APIGroup+"/hello-vm")
	delComp := callIndex(calls, "delete components."+kobj.APIGroup+" hello-vm")
	delRecord := callIndex(calls, "delete configmap "+kobj.RecordName("hello"))
	if delEnr < 0 || wait < 0 || delComp < 0 || delRecord < 0 {
		t.Fatalf("missing call (Enrollment %d, wait %d, Component %d, record %d):\n%s",
			delEnr, wait, delComp, delRecord, strings.Join(calls, "\n"))
	}
	if !(delEnr < wait && wait < delComp && delComp < delRecord) {
		t.Errorf("want order Enrollment → wait → Component → record: the Enrollment's finalizer "+
			"builds its uninstall Job from the Component.\n%s",
			strings.Join(calls, "\n"))
	}
}

func TestUninstallTreeKeepsComponentsWhileEnrollmentsRemain(t *testing.T) {
	p, _, fk := capture(t, siteCfg())
	fk.Outputs[recordCmd(p, "hello")] = "Component/hello-vm\nEnrollment/hello-vm\n"
	waitCmd := p.ws.Tool("kubectl") + " -n " + kobj.PlatformNamespace + " wait --for=delete --timeout=" +
		EnrollmentRemovalTimeout.String() + " enrollments." + kobj.APIGroup + "/hello-vm"
	fk.Errs[waitCmd] = errors.New("timed out waiting for the condition")

	err := p.UninstallTree(context.Background(), "hello")
	if err == nil {
		t.Fatal("want an error when the Enrollments do not go away")
	}
	for _, c := range fk.Calls {
		s := c.String()
		if strings.Contains(s, "delete components.") || strings.Contains(s, "delete configmap") {
			t.Errorf("%q after a failed wait: without its Component the finalizer never finishes, "+
				"without the record a rerun cannot find the tree", s)
		}
	}
}

// Only Components (prune), or only Enrollments (the base tree that `rondo destroy downstreams`
// removes before its tofu destroy): no finalizer depends on what follows, nothing to wait for.
func TestDeleteObjectsWaitsOnlyWhenComponentsFollow(t *testing.T) {
	for _, objs := range [][]treeObject{
		{{Kind: "Component", Name: "old"}},
		{{Kind: "Enrollment", Name: "traefik-wld"}, {Kind: "Enrollment", Name: "metallb-wld"}},
	} {
		p, _, fk := capture(t, siteCfg())
		if err := p.deleteObjects(context.Background(), objs); err != nil {
			t.Fatalf("deleteObjects(%v): %v", objs, err)
		}
		for _, c := range fk.Calls {
			if strings.Contains(c.String(), " wait ") {
				t.Errorf("deleteObjects(%v) waits: %s", objs, c.String())
			}
		}
	}
}

package sandbox

import (
	"context"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"rondo/internal/runner"
)

// twoComponents is a sandbox with a downstream, a vSphere Target and two installed components,
// one of them with a machine.
func twoComponents(t *testing.T) (*sandbox, *runner.Fake) {
	t.Helper()
	st := &site{
		VSphere: []targetSpec{{Name: "dc1", Roles: []string{"dmz"}, ComponentValues: map[string]string{"zone": "a"}}},
		Secrets: map[string]map[string]string{"shared": {"k": "site"}},
		Clusters: []cluster{
			{Name: "rondo-sbx", Target: managementRole, Roles: []string{managementRole}, IngressIP: "172.28.0.100", Management: true},
			{Name: "rondo-sbx-obs", Target: "obs", Roles: []string{"monitoring"}, IngressIP: "172.28.0.101"},
		},
	}
	web := &plan{Component: "web", Enrollments: []string{"web", "agent"}, Targets: map[string]string{"web": "obs", "agent": "obs"},
		Secrets: map[string]map[string]string{"web": {"password": "p"}, "shared": {"k": "web"}}}
	vm := &plan{Component: "vm", Enrollments: []string{"vm"}, Targets: map[string]string{"vm": "dc1"},
		Machines: []machine{{Name: "vm-app-1", Enrollment: "vm", Target: "dc1", IP: "172.28.0.10"}},
		VMValues: map[string]map[string]string{"dc1": {"vmIPs.vm.app": `["172.28.0.10"]`}},
		Secrets:  map[string]map[string]string{"vm-secrets": {"TOKEN": "t"}}}
	sb, fake := fakeSandbox(t, st, nil)
	sb.plans = []*plan{web, vm}
	sb.cfg = siteConfig(st, sb.plans)
	return sb, fake
}

func TestSandboxSpansItsComponents(t *testing.T) {
	sb, _ := twoComponents(t)
	if got := sb.enrollmentNames(); !slices.Equal(got, []string{"agent", "vm", "web"}) {
		t.Errorf("Enrollments = %v", got)
	}
	if p := sb.planOf("vm"); p == nil || p.Component != "vm" {
		t.Errorf("component of Enrollment vm = %+v", p)
	}
	if sb.planOf("absent") != nil {
		t.Error("unknown Enrollment attached to a component")
	}
	if err := sb.component("absent"); err == nil || !strings.Contains(err.Error(), "web, vm") {
		t.Errorf("unknown component: %v, want the installed ones listed", err)
	}
	if err := sb.component("vm"); err != nil || sb.p.Component != "vm" {
		t.Errorf("component vm: %v", err)
	}
	if got := sb.machinesOf("vm"); !slices.Equal(got, []string{"vm-app-1"}) {
		t.Errorf("machines of vm = %v", got)
	}
	secrets := sb.secrets()
	if secrets["shared"]["k"] != "web" || secrets["web"]["password"] != "p" || secrets["vm-secrets"]["TOKEN"] != "t" {
		t.Errorf("Secrets = %v: want the site's, then the components'", secrets)
	}
	if got := sb.cfg.Targets.VSphere[0].ComponentValues; got["zone"] != "a" || got["vmIPs.vm.app"] == "" {
		t.Errorf("values of dc1 = %v: want componentValues and machine IPs", got)
	}
}

func TestApplyComponentSitePutsItsSecretsAndMachineIPs(t *testing.T) {
	sb, fake := twoComponents(t)
	if err := writeFile(sb.s.caPath(), "CA"); err != nil {
		t.Fatal(err)
	}
	if err := sb.component("vm"); err != nil {
		t.Fatal(err)
	}
	if err := sb.applyComponentSite(context.Background()); err != nil {
		t.Fatal(err)
	}
	var applied []string
	for _, c := range fake.Calls {
		if !strings.HasSuffix(c.String(), "apply -f -") {
			continue
		}
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(c.Stdin), &obj); err != nil {
			t.Fatal(err)
		}
		applied = append(applied, obj.Kind+"/"+obj.Metadata.Name)
		// The machine Jobs install the providers from the sandbox's OCI mirror, as a generic site's.
		if obj.Metadata.Name == "rondo-tofurc" && (!strings.Contains(c.Stdin, "oci_mirror") ||
			!strings.Contains(c.Stdin, `mirror.sandbox.test/rondo-tofu-local/${namespace}/${type}`)) {
			t.Errorf("tofurc without the OCI provider mirror:\n%s", c.Stdin)
		}
		if obj.Kind == "Target" && !strings.Contains(c.Stdin, "172.28.0.10") {
			t.Errorf("Target dc1 without the machine IP:\n%s", c.Stdin)
		}
	}
	slices.Sort(applied)
	want := []string{"ConfigMap/rondo-vsphere-dc1", "Secret/rondo-tofurc", "Secret/rondo-vm-ssh",
		"Secret/rondo-vsphere-dc1", "Secret/vm-secrets", "Target/dc1"}
	if !slices.Equal(applied, want) {
		t.Errorf("applied objects = %v, want %v", applied, want)
	}

	fake.Calls = nil
	if err := sb.component("web"); err != nil {
		t.Fatal(err)
	}
	if err := sb.applyComponentSite(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 2 {
		t.Errorf("component without machines: %d objects applied, want only its two Secrets", len(fake.Calls))
	}
}

func TestSelectDeployedNamesTheDownstreams(t *testing.T) {
	sb, _ := twoComponents(t)
	all, err := selectDeployed(sb.site, nil)
	if err != nil || len(all) != 1 || all[0].Target != "obs" {
		t.Errorf("all downstreams = %+v (%v)", all, err)
	}
	if _, err := selectDeployed(sb.site, []string{"missing"}); err == nil || !strings.Contains(err.Error(), "deployed: obs") {
		t.Errorf("unknown downstream: %v", err)
	}
	if _, err := selectDeployed(sb.site, []string{managementRole}); err == nil {
		t.Error("the management cluster is not a downstream")
	}
	if got := sb.site.vsphereOf("dmz"); got == nil || got.Name != "dc1" {
		t.Errorf("vSphere Target of role dmz = %+v", got)
	}
}

package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rondo/api/v1alpha1"
	"rondo/internal/platform"
	"rondo/internal/values"
)

const vmComponent = `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: vm
spec:
  vm:
    groups:
      - name: app
        replicas: 2
      - name: db
        sizing: {cpu: 2, memoryMb: 2048, diskGb: 20}
    rpmRepos:
      - {name: baseos, repo: "${ARTIFACTORY_RPM_REPO}", path: el9/baseos, gpgKey: file:///k}
      - {name: again, repo: rondo-rpms-local, path: /el9/baseos/, gpgKey: file:///k}
    artifacts:
      - {repo: "${ARTIFACTORY_GENERIC_REPO}", provenance: component, path: components/vm/1.0.0/payload.tar.gz}
      - {repo: rondo-generic-local, path: tools/jq/1.7/jq}
    ansible: {repo: rondo-generic-local, path: components/vm/ansible.tar.gz, version: "2.0.0"}
    installer:
      version: "1.0.0"
      script: deploy/install.sh
`

func vmTree(t *testing.T) *platform.Tree {
	t.Helper()
	dir := t.TempDir()
	if err := writeFile(filepath.Join(dir, "components", "vm.yaml"), vmComponent); err != nil {
		t.Fatal(err)
	}
	tr, err := platform.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// vmSite is a site with two vSphere Targets, dc1 giving a componentValue.
func vmSite() *site {
	return &site{VSphere: []targetSpec{{Name: "dc1", Roles: []string{"dmz"}, ComponentValues: map[string]string{"zone": "a"}},
		{Name: "dc2", Roles: []string{"dmz2"}}}}
}

func vmPlan(enrolls ...enrollmentInfo) *plan {
	p := &plan{Targets: map[string]string{}}
	for _, e := range enrolls {
		p.Targets[e.name] = vmSite().vsphereOf(e.role).Name
	}
	return p
}

func TestPlanMachinesNamesAndAddressesThemAsTerraformVM(t *testing.T) {
	tr := vmTree(t)
	vms := []enrollmentInfo{{name: "vm", component: "vm", role: "dmz"}}
	p := vmPlan(vms...)
	if err := planMachines(tr, vmSite(), p, vms, "10.0.0", nil, nil); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range p.Machines {
		got = append(got, m.Name+"@"+m.IP)
	}
	if strings.Join(got, " ") != "vm-app-1@10.0.0.10 vm-app-2@10.0.0.11 vm-db-1@10.0.0.12" {
		t.Errorf("machines = %v", got)
	}
	if p.Machines[0].CPU != 2 || p.Machines[0].MemoryMB != 4096 || p.Machines[2].MemoryMB != 2048 {
		t.Errorf("sizes = %+v: want the terraform/vm default (2/4096), else the group's", p.Machines)
	}
	if p.VMValues["dc1"]["vmIPs.vm.db"] != `["10.0.0.12"]` || p.Machines[0].Target != "dc1" {
		t.Errorf("vSphere Target values = %v, machines %+v", p.VMValues, p.Machines)
	}
	twice := append(vms, enrollmentInfo{name: "vm2", component: "vm", role: "dmz2"})
	if err := planMachines(tr, vmSite(), vmPlan(twice...), twice, "10.0.0", nil, nil); err == nil ||
		!strings.Contains(err.Error(), "same names") {
		t.Errorf("one Component twice: %v", err)
	}
}

func TestPlanMachinesKeepsTheirIPs(t *testing.T) {
	tr := vmTree(t)
	vms := []enrollmentInfo{{name: "vm", component: "vm", role: "dmz"}}
	other := &plan{Machines: []machine{{Name: "x-app-1", IP: "10.0.0.11"}}}
	previous := &plan{Machines: []machine{{Name: "vm-db-1", IP: "10.0.0.20"}, {Name: "vm-app-1", IP: "10.0.0.11"}}}
	p := vmPlan(vms...)
	if err := planMachines(tr, vmSite(), p, vms, "10.0.0", []*plan{other}, previous); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range p.Machines {
		got = append(got, m.Name+"@"+m.IP)
	}
	if strings.Join(got, " ") != "vm-app-1@10.0.0.10 vm-app-2@10.0.0.12 vm-db-1@10.0.0.20" {
		t.Errorf("machines = %v: want vm-db-1 to keep its IP, vm-app-1 to give its own to the other component", got)
	}
}

func TestPlanMachinesStopsBeyondItsAddresses(t *testing.T) {
	// three components of an app group and a db machine: 3 × (29 + 1) machines fill .10 to .99
	plan90 := func(lastApp int) error {
		dir := t.TempDir()
		for n, app := range map[string]int{"a": 29, "b": 29, "c": lastApp} {
			c := strings.Replace(vmComponent, "replicas: 2", fmt.Sprintf("replicas: %d", app), 1)
			if err := writeFile(filepath.Join(dir, "components", n+".yaml"),
				strings.Replace(c, "name: vm\n", "name: "+n+"\n", 1)); err != nil {
				t.Fatal(err)
			}
		}
		tr, err := platform.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		vms := []enrollmentInfo{{name: "a", component: "a", role: "r1"}, {name: "b", component: "b", role: "r2"},
			{name: "c", component: "c", role: "r3"}}
		st := &site{VSphere: []targetSpec{{Name: "dc", Roles: []string{"r1", "r2", "r3"}}}}
		p := &plan{Targets: map[string]string{"a": "dc", "b": "dc", "c": "dc"}}
		err = planMachines(tr, st, p, vms, "10.0.0", nil, nil)
		if err == nil && (len(p.Machines) != 90 || p.Machines[89].IP != "10.0.0.99") {
			t.Errorf("%d machines, the last at %s: want 90, up to .99", len(p.Machines), p.Machines[len(p.Machines)-1].IP)
		}
		return err
	}
	if err := plan90(29); err != nil {
		t.Errorf("90 machines rejected: %v", err)
	}
	if err := plan90(30); err == nil || !strings.Contains(err.Error(), "more than 90 machines") {
		t.Errorf("91 machines: %v", err)
	}
}

func TestVMSourcesNeedABuildOrAnUpstream(t *testing.T) {
	tr := vmTree(t)
	deps := &depsFile{Binaries: []genericDep{{Name: "jq", Version: "1.7", TargetRepo: "rondo-generic-local",
		TargetPath: "tools/jq/{version}/jq"}}}
	builds := []artifactBuild{{Path: "components/vm/1.0.0/payload.tar.gz", Dir: "payload"},
		{Path: "/components/vm/ansible.tar.gz/", Dir: "ansible"}}
	srcs, err := vmSources(tr, builds, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 3 || srcs[0].repo != genericRepo || srcs[1].build != nil || !srcs[2].ansible {
		t.Errorf("sources = %+v", srcs)
	}
	if _, err := vmSources(tr, builds[:1], &depsFile{}); err == nil ||
		!strings.Contains(err.Error(), "artifact tools/jq/1.7/jq") || !strings.Contains(err.Error(), "Ansible content") {
		t.Errorf("no source: %v", err)
	}
	if _, err := vmSources(tr, []artifactBuild{{Path: "x", Dir: "a", File: "b"}}, deps); err == nil ||
		!strings.Contains(err.Error(), "exactly one of the two") {
		t.Errorf("dir and file: %v", err)
	}
}

func TestRPMProxiesNeedAnUpstream(t *testing.T) {
	tr := vmTree(t)
	proxies, err := rpmProxies(tr, map[string]string{"${ARTIFACTORY_RPM_REPO}/el9/baseos/": "https://mirror.example/9/BaseOS"})
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 1 || proxies[0].Prefix != "/artifactory/rondo-rpms-local/el9/baseos/" ||
		proxies[0].Upstream != "https://mirror.example/9/BaseOS/" {
		t.Errorf("proxies = %+v", proxies)
	}
	conf := rpmConf(proxies)
	for _, want := range []string{`location ~ ^/artifactory/rondo-rpms-local/el9/baseos/(.*)$`,
		"proxy_pass https://mirror.example/9/BaseOS/$1;", "resolver 127.0.0.11"} {
		if !strings.Contains(conf, want) {
			t.Errorf("configuration without %q:\n%s", want, conf)
		}
	}
	if regexpQuote("a.b+c") != `a\.b\+c` {
		t.Errorf("quote = %s", regexpQuote("a.b+c"))
	}
	if _, err := rpmProxies(tr, nil); err == nil || !strings.Contains(err.Error(), "rondo-rpms-local/el9/baseos") {
		t.Errorf("no upstream: %v", err)
	}
}

func TestMachineFilesPublishWhatTheJobsDownload(t *testing.T) {
	tr := vmTree(t)
	s := Settings{StateDir: t.TempDir(), RondoDir: "../.."}
	dir := t.TempDir()
	for _, f := range []string{"payload/deploy/install.sh", "ansible/site.yml", "dist/jq"} {
		if err := writeFile(filepath.Join(dir, f), f); err != nil {
			t.Fatal(err)
		}
	}
	p := &plan{Dir: dir, Machines: []machine{{Name: "vm-app-1"}}, Artifacts: []artifactBuild{
		{Path: "components/vm/1.0.0/payload.tar.gz", Dir: "payload"},
		{Path: "components/vm/ansible.tar.gz", Dir: "ansible"},
		{Path: "tools/jq/1.7/jq", File: "dist/jq", Build: "true"}}}
	pub, fake := testPublisher(t, "127.0.0.1:5100")
	rw := treeRewrites{charts: map[string]string{}, digests: map[string]string{}, ansible: map[string]string{}}
	if err := rondoMachineFiles(s, ""); err == nil {
		t.Error("published without an RONDO version")
	}
	if err := rondoMachineFiles(s, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if err := pub.machineFiles(context.Background(), s, p, tr, &depsFile{}, &rw); err != nil {
		t.Fatal(err)
	}
	generic := filepath.Join(s.StateDir, "generic", genericRepo)
	for _, f := range []string{"tofu-modules/vm/v1.2.3/vm.tar.gz", "ansible/rondo/v1.2.3/rondo-ansible.tar.gz",
		"components/vm/1.0.0/payload.tar.gz", "components/vm/ansible.tar.gz"} {
		if _, err := os.Stat(filepath.Join(generic, f)); err != nil {
			t.Errorf("%s not published: %v", f, err)
		}
	}
	jq, err := os.ReadFile(filepath.Join(generic, "tools/jq/1.7/jq"))
	if err != nil || string(jq) != "dist/jq" {
		t.Errorf("built file = %q (%v)", jq, err)
	}
	if len(fake.Calls) != 1 || fake.Calls[0].Dir != dir || !strings.Contains(fake.Calls[0].String(), "bash -c true") {
		t.Errorf("build = %v", fake.Calls)
	}
	sum := sha256.Sum256(jq)
	if rw.digests["vm/tools/jq/1.7/jq"] != "sha256:"+hex.EncodeToString(sum[:]) || len(rw.ansible["vm"]) != 8 {
		t.Errorf("rewrites = %+v", rw)
	}
	spec := map[string]any{
		"artifacts": []any{map[string]any{"path": "/tools/jq/1.7/jq"}, map[string]any{"path": "other"}},
		"ansible":   map[string]any{"version": "2.0.0"},
	}
	rewriteVM("vm", spec, rw)
	arts := spec["artifacts"].([]any)
	if arts[0].(map[string]any)["digest"] == nil || arts[1].(map[string]any)["digest"] != nil ||
		!strings.HasPrefix(spec["ansible"].(map[string]any)["version"].(string), "2.0.0-sbx") {
		t.Errorf("rewritten component = %v", spec)
	}
}

func TestPortsReachAMachine(t *testing.T) {
	p := &plan{Machines: []machine{{Name: "vm-app-1", IP: "10.0.0.10"}}}
	st := &site{}
	fw, err := resolvePorts(st, p, []portSpec{{HostPort: 8088, Machine: "vm-app-1", Port: 8080}})
	if err != nil {
		t.Fatal(err)
	}
	if fw[0].IP != "10.0.0.10" || fw[0].Machine != "vm-app-1" || fw[0].String() != "tcp://localhost:8088 → 10.0.0.10:8080" {
		t.Errorf("forward = %+v", fw[0])
	}
	if _, err := resolvePorts(st, p, []portSpec{{HostPort: 8088, Machine: "missing", Port: 8080}}); err == nil ||
		!strings.Contains(err.Error(), "vm-app-1") {
		t.Errorf("unknown machine: %v", err)
	}
	if _, err := resolvePorts(st, p, []portSpec{{HostPort: 8088, Machine: "vm-app-1", Role: "x", Port: 8080}}); err == nil {
		t.Error("machine and role together accepted")
	}
}

// A machine Enrollment converges as the controller decides it: the provisioning and the
// installation applied are the ones its Component and Target now give.
func TestMachinesConvergedFollowsTheController(t *testing.T) {
	comp := &v1alpha1.Component{Spec: v1alpha1.ComponentSpec{VM: &v1alpha1.VMSpec{
		Groups: []v1alpha1.VMGroup{{Name: "app", Replicas: 1}},
		Installer: &v1alpha1.VMInstaller{Version: "1.0.0", Script: "deploy/install.sh",
			SecretsFrom: []v1alpha1.ValuesSource{{SecretRef: &v1alpha1.LocalObjectReference{Name: "vm-secrets"},
				Keys: []string{"TOKEN"}}}},
	}}}
	comp.Name = "vm"
	target := &v1alpha1.Target{Spec: v1alpha1.TargetSpec{Values: map[string]string{"vmIPs.vm.app": `["10.0.0.10"]`}}}
	target.Name = vsphereStub
	w := &world{components: map[string]*v1alpha1.Component{"vm": comp},
		targets: map[string]*v1alpha1.Target{vsphereStub: target},
		secrets: map[string]map[string]string{"vm-secrets": {"TOKEN": "t"}}}
	e := &v1alpha1.Enrollment{Spec: v1alpha1.EnrollmentSpec{Component: "vm"}}
	e.Status.ResolvedTarget = vsphereStub
	if w.converged(e) {
		t.Error("converged before any installation")
	}
	e.Status.AppliedValuesHash = values.VMInfraHash(comp.Spec.VM, "vm", target)
	e.Status.AppliedInstallerVersion = "1.0.0"
	e.Status.AppliedChartVersion = values.VMInstallHash(comp.Spec.VM, map[string]string{"TOKEN": "t"})
	if !w.converged(e) {
		t.Error("not converged after the wanted installation")
	}
	w.secrets["vm-secrets"]["TOKEN"] = "other"
	if w.converged(e) {
		t.Error("converged although the installer secret changed: the controller replays vm-install")
	}
}

func TestMachinesAndSSHNeedAPlan(t *testing.T) {
	s := testSettings(t)
	if Machines(s) != nil {
		t.Error("machines without a plan")
	}
	if _, err := RunSSH(context.Background(), s, "vm-app-1", nil, false); err == nil {
		t.Error("ssh without a plan accepted")
	}
	if err := savePlan(s, &plan{Component: "vm", Machines: []machine{{Name: "vm-app-1"}}}); err != nil {
		t.Fatal(err)
	}
	if got := Machines(s); len(got) != 1 || got[0] != "vm-app-1" {
		t.Errorf("machines = %v", got)
	}
	if _, err := RunSSH(context.Background(), s, "missing", nil, false); err == nil || !strings.Contains(err.Error(), "vm-app-1") {
		t.Errorf("unknown machine: %v", err)
	}
}

package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rondo/internal/platform"
)

func testSettings(t *testing.T) Settings {
	t.Helper()
	return Settings{StateDir: t.TempDir(), RondoDir: "../.."}
}

// deployed is the site of a component's rondo-sandbox.yaml as `deploy` then `deploy downstreams`
// leave it.
func deployed(t *testing.T, dir string) Settings {
	t.Helper()
	s := testSettings(t)
	config := filepath.Join(dir, sandboxFileName)
	if err := RunSite(s, config, "172.28.0", false, nil); err != nil {
		t.Fatalf("RunSite: %v", err)
	}
	if st, _ := loadSite(s); len(st.Downstreams) > 0 {
		if err := RunSite(s, config, "172.28.0", true, nil); err != nil {
			t.Fatalf("RunSite --downstreams: %v", err)
		}
	}
	return s
}

func planned(t *testing.T) (Settings, *plan) {
	t.Helper()
	s := deployed(t, "testdata/component")
	if err := RunPlan(s, "testdata/component", "172.28.0"); err != nil {
		t.Fatalf("RunPlan: %v", err)
	}
	p, err := loadPlan(s, "acme")
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

func readState(t *testing.T, s Settings, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.StateDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRunSiteDeploysTheManagementThenTheDownstreams(t *testing.T) {
	s := testSettings(t)
	config := "testdata/component/rondo-sandbox.yaml"
	if err := RunSite(s, config, "172.28.0", true, nil); err == nil || !strings.Contains(err.Error(), "deploy") {
		t.Errorf("downstreams before the management cluster: %v", err)
	}
	if err := RunSite(s, config, "172.28.0", false, nil); err != nil {
		t.Fatal(err)
	}
	if got := readState(t, s, "clusters.txt"); got != "rondo-sbx\n" {
		t.Errorf("clusters.txt = %q, want the management cluster only", got)
	}
	if got := readState(t, s, "rondo-images.txt"); got != "controller\ntoolbox\nsandbox-machine\n" {
		t.Errorf("RONDO images = %q: a vSphere Target needs the sandbox machine", got)
	}
	st, _ := loadSite(s)
	if len(st.Clusters) != 1 || !slices.Equal(st.Clusters[0].Roles, []string{managementRole}) ||
		st.Clusters[0].IngressIP != "172.28.0.100" {
		t.Errorf("clusters = %+v", st.Clusters)
	}
	if err := RunSite(s, config, "172.28.0", true, []string{"missing"}); err == nil || !strings.Contains(err.Error(), "obs") {
		t.Errorf("unknown downstream: %v", err)
	}
	for range 2 {
		if err := RunSite(s, config, "172.28.0", true, nil); err != nil {
			t.Fatal(err)
		}
		if got := readState(t, s, "clusters.txt"); got != "rondo-sbx-obs\n" {
			t.Errorf("clusters.txt = %q", got)
		}
		st, _ = loadSite(s)
		if c := st.cluster("obs"); c == nil || c.IngressIP != "172.28.0.101" || !slices.Equal(c.Roles, []string{"monitoring"}) {
			t.Errorf("downstream = %+v: its ingress IP must stay the same", c)
		}
	}
	if err := RunSite(s, config, "172.28.0", false, nil); err != nil {
		t.Fatal(err)
	}
	if st, _ = loadSite(s); len(st.Clusters) != 2 {
		t.Errorf("deploy forgets the deployed downstreams: %+v", st.Clusters)
	}
}

func TestCheckTargetsRefusesAnAmbiguousSite(t *testing.T) {
	for name, c := range map[string]struct{ file, want string }{
		"role held twice": {"targets:\n  management: {roles: [a]}\n  downstreams: [{name: x, roles: [a]}]\n", "both hold"},
		"name taken":      {"targets:\n  downstreams: [{name: x, roles: [a]}]\n  vsphere: [{name: x, roles: [b]}]\n", "already taken"},
		"management":      {"targets:\n  downstreams: [{name: management, roles: [a]}]\n", "already taken"},
		"management role": {"targets:\n  downstreams: [{name: x, roles: [management]}]\n", "management cluster only"},
		"no role":         {"targets:\n  downstreams: [{name: x}]\n", "empty roles"},
		"name too long":   {"targets:\n  downstreams: [{name: abcdefghijklmnopqrstuvwxyz, roles: [a]}]\n", "24 characters"},
		"rondo.yaml key":  {"targets:\n  downstreams: [{name: x, roles: [a], rke2: {}}]\n", "targets.downstreams"},
	} {
		t.Run(name, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), sandboxFileName)
			if err := os.WriteFile(config, []byte(c.file), 0o600); err != nil {
				t.Fatal(err)
			}
			err := RunSite(testSettings(t), config, "172.28.0", false, nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want %q", err, c.want)
			}
		})
	}
}

func TestRunPlanPlacesEnrollmentsOnTheirTargets(t *testing.T) {
	_, p := planned(t)
	if p.Component != "acme" {
		t.Errorf("component %q, want the name from rondo-sandbox.yaml", p.Component)
	}
	if !slices.Equal(p.Enrollments, []string{"agent", "agent-forward", "vm", "web"}) {
		t.Errorf("Enrollments = %v", p.Enrollments)
	}
	want := map[string]string{"agent": "obs", "agent-forward": managementRole, "vm": "dc1", "web": "obs"}
	for e, target := range want {
		if p.Targets[e] != target {
			t.Errorf("Enrollment %s on %q, want %q", e, p.Targets[e], target)
		}
	}
	if !slices.Equal(p.Namespaces, []string{"logging", "web"}) {
		t.Errorf("namespaces = %v", p.Namespaces)
	}
}

func TestRunPlanPutsMachinesOnTheVSphereTarget(t *testing.T) {
	s, p := planned(t)
	want := machine{Name: "vm-app-1", Enrollment: "vm", Component: "vm", Target: "dc1", Group: "app",
		IP: "172.28.0.10", CPU: 4, MemoryMB: 1024}
	if len(p.Machines) != 1 || p.Machines[0] != want {
		t.Errorf("machines = %+v, want %+v", p.Machines, want)
	}
	if p.VMValues["dc1"]["vmIPs.vm.app"] != `["172.28.0.10"]` {
		t.Errorf("vSphere Target values = %+v", p.VMValues)
	}
	if got := readState(t, s, "machines.txt"); got != "rondo-sbx-machine-vm-app-1 vm-app-1 172.28.0.10 4 1024 vm app\n" {
		t.Errorf("machines.txt = %q", got)
	}
	key := readState(t, s, filepath.Join("machines", "authorized_key"))
	if !strings.HasPrefix(key, "ssh-ed25519 ") {
		t.Errorf("machine key = %q", key)
	}
	if _, pub, err := machineKey(s); err != nil || pub+"\n" != key {
		t.Errorf("the machine key changed from one plan to the next (%v)", err)
	}
	if p.Secrets["vm-secrets"]["HELLO_SECRET"] != sandboxSecret {
		t.Errorf("secret from the installer's secretsFrom = %v", p.Secrets["vm-secrets"])
	}
	if st, _ := loadSite(s); st.subnetIP(1) != "172.28.0.1" {
		t.Errorf("gateway = %s", st.subnetIP(1))
	}
	if got := readState(t, s, "installing"); got != "acme\n" {
		t.Errorf("component being installed = %q", got)
	}
}

func TestRunPlanFillsSecretsAndPorts(t *testing.T) {
	s, p := planned(t)
	if got := p.Secrets["acme"]["password"]; got != "s3cret" {
		t.Errorf("secret acme.password = %q, want the value from secrets.components", got)
	}
	if len(p.Ports) != 2 || p.Ports[1].Host != "web.monitoring.sandbox.test" || p.Ports[1].IP != "172.28.0.101" ||
		p.Ports[1].Cluster != "rondo-sbx-obs" {
		t.Errorf("ports = %+v", p.Ports)
	}
	if got := readState(t, s, filepath.Join("ports", "ports.txt")); got != "5514/udp\n8080/tcp" {
		t.Errorf("ports.txt = %q", got)
	}
}

// tree copies the test component and lets a test rewrite one of its files.
func tree(t *testing.T, rel, content string) string {
	t.Helper()
	dst := t.TempDir()
	if err := copyDir("testdata/component", dst); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), raw, 0o644)
	})
}

func TestRunPlanRefusesWhatASiteWouldRefuse(t *testing.T) {
	enrollment := func(target string) string {
		return "apiVersion: rondo.ouafeul.github.io/v1alpha1\nkind: Enrollment\nmetadata: {name: web}\n" +
			"spec: {component: web, target: " + target + "}\n"
	}
	for name, c := range map[string]struct{ rel, content, want string }{
		"unknown field": {"platform/components/agent.yaml", `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: agent}
spec:
  chart: {oci: oci://h/r/fluent-bit, version: 0.57.2}
  namespace: logging
  bogus: true
`, "the site would reject"},
		"site value": {"platform/components/agent.yaml", `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: agent}
spec:
  chart: {oci: "oci://${REGISTRY_HOST}/${ARTIFACTORY_HELM_REPO}/fluent-bit", version: 0.57.2}
  namespace: logging
  releaseName: fluent-bit
  values: {zone: "${target.values.zone}"}
`, "${target.values.zone} on Target obs"},
		"unknown rondo-sandbox.yaml": {"rondo-sandbox.yaml", "imagez: []\n", "rondo-sandbox.yaml"},
		"role without Target":        {"platform/enrollments/web.yaml", enrollment("elsewhere"), "which no Target holds"},
		"chart on vSphere":           {"platform/enrollments/web.yaml", enrollment("dmz"), "a chart goes on a cluster"},
		"same release": {"platform/enrollments/agent-forward.yaml", "apiVersion: rondo.ouafeul.github.io/v1alpha1\n" +
			"kind: Enrollment\nmetadata: {name: agent-forward}\nspec: {component: agent-forward, target: obs}\n",
			"same release"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := tree(t, c.rel, c.content)
			err := RunPlan(deployed(t, "testdata/component"), dir, "172.28.0")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want %q", err, c.want)
			}
		})
	}
}

func TestRunPlanNamesTheDownstreamToDeploy(t *testing.T) {
	s := testSettings(t)
	if err := RunSite(s, "testdata/component/rondo-sandbox.yaml", "172.28.0", false, nil); err != nil {
		t.Fatal(err)
	}
	err := RunPlan(s, "testdata/component", "172.28.0")
	if err == nil || !strings.Contains(err.Error(), "rondo-sandbox deploy downstreams obs") {
		t.Errorf("downstream declared but not deployed: %v", err)
	}
	if err := RunPlan(testSettings(t), "testdata/component", "172.28.0"); err == nil || !strings.Contains(err.Error(), "deploy") {
		t.Errorf("plan without a sandbox: %v", err)
	}
}

func TestManagementRolesSpareACluster(t *testing.T) {
	dir := tree(t, "rondo-sandbox.yaml", `name: acme
targets:
  management: {roles: [monitoring]}
  vsphere: [{name: dc1, roles: [dmz], componentValues: {helloMessage: hi}}]
ports: [{hostPort: 8080, host: "web.monitoring.${INGRESS_BASE_DOMAIN}"}]
artifacts: [{path: components/vm/1.0.0/payload.tar.gz, dir: vm/payload}]
`)
	s := deployed(t, dir)
	err := RunPlan(s, dir, "172.28.0")
	if err == nil || !strings.Contains(err.Error(), "same release") {
		t.Errorf("fluent-bit twice on the management cluster: %v, want the collision", err)
	}
	if err := os.Remove(filepath.Join(dir, "platform", "enrollments", "agent-forward.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := RunPlan(s, dir, "172.28.0"); err != nil {
		t.Fatal(err)
	}
	p, _ := loadPlan(s, "acme")
	if p.Targets["web"] != managementRole || len(p.Ports) != 1 || p.Ports[0].Cluster != clusterPrefix {
		t.Errorf("web on %q, ports %+v: the monitoring role is on the management cluster", p.Targets["web"], p.Ports)
	}
	if p.Secrets["acme"]["password"] != sandboxSecret {
		t.Errorf("unset key = %q, want %q", p.Secrets["acme"]["password"], sandboxSecret)
	}
	if _, ok := p.Secrets["rondo-platform-values"]; ok {
		t.Error("rondo-platform-values belongs to RONDO, not to the component")
	}
	sf := &sandboxFile{}
	sf.Secrets.Components = map[string]map[string]string{"other": {"k": "v"}}
	tr, err := platform.Load(filepath.Join(dir, "platform"))
	if err != nil {
		t.Fatal(err)
	}
	got := secretsFor(tr, map[string]map[string]string{"other": {"k": "v"}, "unread": {"k": "v"}}, sf.Secrets.Components)
	if _, ok := got["unread"]; ok || got["other"]["k"] != "v" {
		t.Errorf("Secrets = %v: want the component's, not the site's that the tree does not read", got)
	}
}

func TestComponentsShareTheSandbox(t *testing.T) {
	s, first := planned(t)
	clash := t.TempDir()
	for f, content := range map[string]string{
		"platform/components/agent2.yaml": "apiVersion: rondo.ouafeul.github.io/v1alpha1\nkind: Component\n" +
			"metadata: {name: agent2}\nspec:\n  chart: {oci: \"oci://${REGISTRY_HOST}/${ARTIFACTORY_HELM_REPO}/fluent-bit\", " +
			"version: 0.57.2}\n  namespace: logging\n  releaseName: fluent-bit\n",
		"platform/enrollments/agent2.yaml": "apiVersion: rondo.ouafeul.github.io/v1alpha1\nkind: Enrollment\n" +
			"metadata: {name: agent2}\nspec: {component: agent2, target: monitoring}\n",
	} {
		if err := writeFile(filepath.Join(clash, f), content); err != nil {
			t.Fatal(err)
		}
	}
	err := RunPlan(s, clash, "172.28.0")
	if err == nil || !strings.Contains(err.Error(), "same release") || !strings.Contains(err.Error(), "Target obs") {
		t.Errorf("two components shipping the same release on one Target: %v", err)
	}

	other := machineComponent(t, "acme2", "vm2", 8081)
	if err := RunPlan(s, other, "172.28.0"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(Components(s), []string{"acme", "acme2"}) {
		t.Fatalf("components = %v", Components(s))
	}
	second, _ := loadPlan(s, "acme2")
	if len(second.Machines) != 1 || second.Machines[0].IP == first.Machines[0].IP {
		t.Errorf("machines of the second component = %+v: want an IP other than %s", second.Machines, first.Machines[0].IP)
	}
	if got := readState(t, s, "machines.txt"); strings.Count(got, "\n") != 2 {
		t.Errorf("machines.txt = %q, want both components' machines", got)
	}
	if got := readState(t, s, filepath.Join("ports", "ports.txt")); got != "5514/udp\n8080/tcp\n8081/tcp" {
		t.Errorf("ports.txt = %q, want both components' ports", got)
	}
	if !slices.Equal(Enrollments(s), []string{"agent", "agent-forward", "vm", "vm2", "web"}) {
		t.Errorf("Enrollments = %v", Enrollments(s))
	}
	if err := RunPlan(s, "testdata/component", "172.28.0"); err != nil {
		t.Fatal(err)
	}
	again, _ := loadPlan(s, "acme")
	if again.Machines[0].IP != first.Machines[0].IP {
		t.Errorf("machine reinstalled at %s, want its IP %s", again.Machines[0].IP, first.Machines[0].IP)
	}
	if err := RunPlan(s, machineComponent(t, "acme3", "vm3", 8081), "172.28.0"); err == nil ||
		!strings.Contains(err.Error(), "already forwarded by component acme2") {
		t.Errorf("workstation port taken by another component: %v", err)
	}
	if err := RunPlan(s, machineComponent(t, "acme3", "vm2", 8082), "172.28.0"); err == nil ||
		!strings.Contains(err.Error(), "Component/vm2 already installed by component acme2") {
		t.Errorf("Component of another component: %v", err)
	}
}

// machineComponent is a component of one machine Component on the dmz role, forwarding a
// workstation port to its machine.
func machineComponent(t *testing.T, name, vm string, port int) string {
	t.Helper()
	dir := t.TempDir()
	for f, content := range map[string]string{
		"platform/components/" + vm + ".yaml": "apiVersion: rondo.ouafeul.github.io/v1alpha1\nkind: Component\n" +
			"metadata: {name: " + vm + "}\nspec:\n  vm:\n    groups: [{name: app, replicas: 1}]\n" +
			"    installer: {version: \"1.0.0\", script: deploy/install.sh}\n",
		"platform/enrollments/" + vm + ".yaml": "apiVersion: rondo.ouafeul.github.io/v1alpha1\nkind: Enrollment\n" +
			"metadata: {name: " + vm + "}\nspec: {component: " + vm + ", target: dmz}\n",
		sandboxFileName: fmt.Sprintf("name: %s\nports: [{hostPort: %d, machine: %s-app-1, port: 80}]\n", name, port, vm),
	} {
		if err := writeFile(filepath.Join(dir, f), content); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestKubeconfigFindsTheClusterOfATarget(t *testing.T) {
	s, _ := planned(t)
	for ref, cluster := range map[string]string{"": "rondo-sbx", "management": "rondo-sbx",
		"monitoring": "rondo-sbx-obs", "obs": "rondo-sbx-obs", "rondo-sbx-obs": "rondo-sbx-obs"} {
		got, err := Kubeconfig(s, ref)
		if want := filepath.Join(s.StateDir, "clusters", cluster, "kubeconfig.yaml"); err != nil || got != want {
			t.Errorf("Kubeconfig(%q) = %q (%v), want %q", ref, got, err, want)
		}
	}
	if _, err := Kubeconfig(s, "dmz"); err == nil {
		t.Error("vSphere Target role accepted: it has no cluster")
	}
	if _, err := Kubeconfig(testSettings(t), "monitoring"); err == nil || !strings.Contains(err.Error(), "no sandbox") {
		t.Errorf("role without a sandbox: %v", err)
	}
}

func TestRunInitDescribesTheSiteTheTreeNeeds(t *testing.T) {
	dir := t.TempDir()
	if err := copyDir("testdata/component", dir); err != nil {
		t.Fatal(err)
	}
	if err := RunInit(dir, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("existing file overwritten without --force: %v", err)
	}
	if err := RunInit(dir, true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, sandboxFileName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{"  downstreams:\n    - name: monitoring", "      roles: [monitoring]",
		"  vsphere:\n    - name: dmz", "        helloMessage: CHANGEME", "    acme:\n      password: sandbox",
		"name: acme", "tagFromChart: apps/web"} {
		if !strings.Contains(got, want) {
			t.Errorf("rondo-sandbox.yaml without %q:\n%s", want, got)
		}
	}
	sf, err := readSandboxFile(filepath.Join(dir, sandboxFileName))
	if err != nil {
		t.Fatalf("the written file does not read back: %v", err)
	}
	if err := checkTargets(sf); err != nil {
		t.Errorf("the written site is rejected: %v", err)
	}
}

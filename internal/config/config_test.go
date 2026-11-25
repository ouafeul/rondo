package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, mutate func(string) string) (*Config, error) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "rondo.yaml.example"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if mutate != nil {
		content = mutate(content)
	}
	p := filepath.Join(t.TempDir(), "rondo.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestExampleLoads(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatalf("invalid rondo.yaml.example: %v", err)
	}
	if ref := c.Targets.Management.VSphereTarget; c.VSphereNamed(ref) == nil {
		t.Fatalf("management points at %q, which is not a vsphere target", ref)
	}
	if got := len(c.Targets.Downstreams); got != 1 {
		t.Errorf("%d downstreams, want 1", got)
	}
	if got := c.RancherHostname(); got != "rancher.cluster.example.local" {
		t.Errorf("rancher = %q", got)
	}
}

func TestValidationNamesTheProblem(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(string) string
		want   string
	}{
		"unknown vsphereTarget (management)": {func(s string) string {
			return strings.Replace(s, "    vsphereTarget: dc1\n    k3sVersion", "    vsphereTarget: dc9\n    k3sVersion", 1)
		}, "dc9"},
		"unknown vsphereTarget (downstream)": {func(s string) string {
			return strings.Replace(s, "        vsphereTarget: dc1\n        rke2Version", "        vsphereTarget: dc9\n        rke2Version", 1)
		}, "dc9"},
		"reserved name": {func(s string) string {
			return strings.Replace(s, "- name: rke2-a", "- name: management", 1)
		}, "reserved"},
		"duplicate name": {func(s string) string {
			return strings.Replace(s, "- name: rke2-a", "- name: dc1", 1)
		}, "duplicate"},
		"vCenter password for a downstream": {func(s string) string {
			return strings.Replace(s, "    dc1: CHANGEME\n", "    dc1: CHANGEME\n    rke2-a: x\n", 1)
		}, "secrets.vcenterPasswords.rke2-a"},
		"vCenter password for an unknown target": {func(s string) string {
			return strings.Replace(s, "    dc1: CHANGEME\n", "    dc1: CHANGEME\n    dc9: x\n", 1)
		}, "secrets.vcenterPasswords.dc9"},
		"missing vCenter password": {func(s string) string {
			return strings.Replace(s, "    dc1: CHANGEME\n", "    dc1: \"\"\n", 1)
		}, "secrets.vcenterPasswords.dc1 is required"},
		"management role on a vsphere target": {func(s string) string {
			return strings.Replace(s, "    - name: dc1\n", "    - name: dc1\n      roles: [management]\n", 1)
		}, "reserved"},
		"even control plane": {func(s string) string {
			return strings.Replace(s, "[10.0.0.31, 10.0.0.32, 10.0.0.33]", "[10.0.0.31, 10.0.0.32]", 1)
		}, "odd"},
		"unknown field": {func(s string) string {
			return strings.Replace(s, "ingressBaseDomain:", "exposure: {}\ningressBaseDomain:", 1)
		}, "exposure"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, tc.mutate)
			if err == nil {
				t.Fatal("manifest accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestEnvDrivesTheManagementModule(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := c.Env()
	for k, want := range map[string]string{
		"TF_VAR_vsphere_server":   "vcenter.example.local",
		"TF_VAR_vsphere_password": "CHANGEME",
		"TF_VAR_vms":              `[{"ip":"10.0.0.11","name":"k3s-server-1"},{"ip":"10.0.0.12","name":"k3s-server-2"},{"ip":"10.0.0.13","name":"k3s-server-3"}]`,
		"TF_VAR_vm_specs":         `{"cpu":4,"disk_gb":50,"memory_mb":8192}`,
		"TF_VAR_network_dns":      `["10.0.0.2"]`,
		"TF_VAR_vsphere_folder":   "rondo",
		"TF_VAR_environment":      "prod",
		"INGRESS_IP":              "10.0.0.100",
		"INGRESS_BASE_DOMAIN":     "cluster.example.local",
	} {
		if e[k] != want {
			t.Errorf("%s = %q, want %q", k, e[k], want)
		}
	}
}

func TestRKE2VarsDescribeTheCluster(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &c.Targets.Downstreams[0]
	v := c.RKE2Vars(r)
	for k, want := range map[string]string{
		"rke2_cluster_name":  "rke2-a",
		"rke2_master_ips":    `["10.0.0.31","10.0.0.32","10.0.0.33"]`,
		"rke2_worker_cpu":    "12",
		"rke2_registry_host": "registry.example.local",
		"rancher_ingress_ip": "10.0.0.100",
		"vsphere_datastore":  "datastore1",
		"vsphere_folder":     "rondo/prod",
	} {
		if v[k] != want {
			t.Errorf("%s = %q, want %q", k, v[k], want)
		}
	}
	if got := c.Mirror().StateURL(r.Name); got != "https://files.example.local/tofu-state/prod/rke2-a/terraform.tfstate" {
		t.Errorf("state = %q", got)
	}
}

func TestManagementRoleIsReserved(t *testing.T) {
	_, err := load(t, func(s string) string { return strings.Replace(s, "roles: [monitoring]", "roles: [management]", 1) })
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("management role on an rke2 target accepted: %v", err)
	}
}

func TestRegistryIPAloneKeepsTheDefaultAuthority(t *testing.T) {
	c, err := load(t, func(s string) string {
		return strings.Replace(s, "        workerNodeSize:", "        registryIp: 10.0.0.60\n        workerNodeSize:", 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	v := c.RKE2Vars(&c.Targets.Downstreams[0])
	if v["rke2_registry_ip"] != "10.0.0.60" {
		t.Errorf("rke2_registry_ip = %q, want the declared IP", v["rke2_registry_ip"])
	}
	if v["rke2_registry_host"] != "registry.example.local" {
		t.Errorf("rke2_registry_host = %q, want the default authority", v["rke2_registry_host"])
	}
}

func TestParseChecksAsLoadDoes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "rondo.yaml.example"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(raw); err != nil {
		t.Fatalf("example rejected: %v", err)
	}
	_, err = Parse([]byte(strings.Replace(string(raw), "ingressBaseDomain:", "ingresBaseDomain:", 1)))
	if err == nil || !strings.Contains(err.Error(), "parsing rondo.yaml") || !strings.Contains(err.Error(), "ingresBaseDomain") {
		t.Errorf("unknown field: %v", err)
	}
	_, err = Parse([]byte(strings.Replace(string(raw), "url: https://files.example.local\n    repository: rondo", "url: not-a-url\n    repository: rondo", 1)))
	if err == nil || !strings.Contains(err.Error(), "invalid manifest") {
		t.Errorf("invalid url: %v", err)
	}
}

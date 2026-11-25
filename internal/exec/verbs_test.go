package exec

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"rondo/api/v1alpha1"
	"rondo/internal/runner"
)

func archiveServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u == "" || p == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("fake-archive"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setArtifactoryEnv(t *testing.T) {
	t.Helper()
	t.Setenv(EnvArtifactoryUser, "svc")
	t.Setenv(EnvArtifactoryPassword, "pw")
	t.Setenv(EnvStateBaseURL, "https://arti/state")
}

func vmFixtures(t *testing.T, moduleURL string) (*Runtime, *runner.Fake) {
	t.Helper()
	setArtifactoryEnv(t)
	t.Setenv(EnvVMModuleURL, moduleURL)
	t.Setenv(EnvSSHKeySecret, "rondo-vm-ssh")
	t.Setenv(EnvArtifactoryURL, "https://arti.local/artifactory")

	target := vsphereTargetObj(map[string]string{
		"vmIPs.mft.app": `["10.0.2.10"]`,
		"vmTemplate":    "rhel9-cloudinit",
		"sshUser":       "rondo",
	})
	return newRuntime(t, "Enrollment", "mft-dmz",
		vmReleaseObj(nil), vmComponentObj(), target,
		secret("rondo-vm-ssh", map[string]string{
			"ssh-publickey": "ssh-ed25519 AAAA", "ssh-privatekey": "-----BEGIN-----",
		}),
		secret("mft-secrets", map[string]string{"DB_PASSWORD": "s3cr3t"}),
		secret("vsphere-creds", map[string]string{"vsphere_user": "u", "vsphere_password": "p"}),
		configMap("rondo-site", map[string]string{
			"vsphere_server": "vc.local", "vsphere_datacenter": "dc",
			"vsphere_cluster": "cl", "vsphere_datastore": "ds",
			"vsphere_network": "net", "network_gateway": "10.0.2.1",
			"network_netmask": "24", "network_dns": `["10.0.0.1"]`,
			"network_domain": "local", "artifactory_ca_pem": "CA",
		}))
}

func TestVMApplyPublishesInventory(t *testing.T) {
	srv := archiveServer(t)
	rt, fk := vmFixtures(t, srv.URL+"/vm-module.tar.gz")
	fk.Outputs["tofu output -json"] = `{
	  "ansible_inventory": {"value": "[app]\nmft-app-1 ansible_host=10.0.2.10\n"},
	  "machine_count": {"value": 1},
	  "ips_by_group": {"value": {"app": ["10.0.2.10"]}}
	}`

	if err := VMApply(t.Context(), rt); err != nil {
		t.Fatalf("VMApply: %v", err)
	}
	sec := mustSecret(t, rt, "mft-dmz-machines")
	if !strings.Contains(string(sec["ansible_inventory"]), "[app]") {
		t.Errorf("inventory = %q", sec["ansible_inventory"])
	}
	var got v1alpha1.Enrollment
	if err := rt.Client.Get(t.Context(),
		types.NamespacedName{Namespace: "rondo-system", Name: "mft-dmz"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.MachinesSecretRef == nil {
		t.Fatal("machinesSecretRef not published: the install phase would find nothing")
	}
}

func TestVMApplyRefusesWithoutInventory(t *testing.T) {
	srv := archiveServer(t)
	rt, fk := vmFixtures(t, srv.URL+"/vm-module.tar.gz")
	fk.Outputs["tofu output -json"] = `{"machine_count": {"value": 1}}`
	err := VMApply(t.Context(), rt)
	if err == nil {
		t.Fatal("missing inventory accepted")
	}
	if !strings.Contains(err.Error(), "ansible_inventory") {
		t.Errorf("message = %q", err)
	}
}

func TestVMPlanDoesNotMutate(t *testing.T) {
	srv := archiveServer(t)
	rt, fk := vmFixtures(t, srv.URL+"/vm-module.tar.gz")
	fk.Outputs["tofu plan -input=false -no-color"] = "Plan: 2 to add, 0 to change, 0 to destroy."
	if err := VMPlan(t.Context(), rt); err != nil {
		t.Fatalf("VMPlan: %v", err)
	}
	for _, c := range commandsNamed(fk.Calls, "tofu") {
		if c.Args[0] == "apply" || strings.Contains(c.String(), "-auto-approve") {
			t.Errorf("the plan mutated: %s", c.String())
		}
	}
}

func TestVMDestroyIsAutoApproved(t *testing.T) {
	srv := archiveServer(t)
	rt, fk := vmFixtures(t, srv.URL+"/vm-module.tar.gz")
	if err := VMDestroy(t.Context(), rt); err != nil {
		t.Fatalf("VMDestroy: %v", err)
	}
	if d := findTofu(t, fk.Calls, "destroy"); !strings.Contains(d.String(), "-auto-approve") {
		t.Errorf("destroy = %s", d.String())
	}
}

func TestVMInstallRunsOncePerGroup(t *testing.T) {
	srv := archiveServer(t)
	t.Setenv(EnvAnsibleContentURL, srv.URL+"/ansible.tar.gz")
	rt, fk := vmFixtures(t, srv.URL+"/vm-module.tar.gz")

	var comp v1alpha1.Component
	if err := rt.Client.Get(t.Context(),
		types.NamespacedName{Namespace: "rondo-system", Name: "mft"}, &comp); err != nil {
		t.Fatal(err)
	}
	comp.Spec.VM.Groups = []v1alpha1.VMGroup{
		{Name: "production", Replicas: 1, Config: map[string]v1alpha1.VMConfigValue{
			"NC_ROLE": {Value: strPtr("production")},
		}},
		{Name: "administration", Replicas: 1, Config: map[string]v1alpha1.VMConfigValue{
			"NC_ROLE": {Value: strPtr("administration")},
		}},
		{Name: "disabled", Replicas: 0},
	}
	if err := rt.Client.Update(t.Context(), &comp); err != nil {
		t.Fatal(err)
	}
	var rel v1alpha1.Enrollment
	if err := rt.Client.Get(t.Context(),
		types.NamespacedName{Namespace: "rondo-system", Name: "mft-dmz"}, &rel); err != nil {
		t.Fatal(err)
	}
	rel.Status.MachinesSecretRef = &v1alpha1.LocalObjectReference{Name: "mft-dmz-machines"}
	if err := rt.Client.Status().Update(t.Context(), &rel); err != nil {
		t.Fatal(err)
	}
	if err := rt.Client.Create(t.Context(),
		secret("mft-dmz-machines", map[string]string{
			"ansible_inventory": "[production]\nmft-production-1 ansible_host=10.0.2.10\n",
		})); err != nil {
		t.Fatal(err)
	}

	if err := VMInstall(t.Context(), rt); err != nil {
		t.Fatalf("VMInstall: %v", err)
	}

	plays := commandsNamed(fk.Calls, "ansible-playbook")
	if len(plays) != 2 {
		t.Fatalf("%d runs, want 2 (one per active group, the 0-replica group skipped)", len(plays))
	}
	if !strings.Contains(plays[0].String(), "--limit production") {
		t.Errorf("1st run = %s", plays[0].String())
	}
	if !strings.Contains(plays[1].String(), "--limit administration") {
		t.Errorf("2nd run = %s", plays[1].String())
	}
	for _, p := range plays {
		if !strings.Contains(p.String(), "--limit ") {
			t.Errorf("run without --limit: %s", p.String())
		}
		if !strings.Contains(p.String(), "-e @") {
			t.Errorf("variables outside a file: %s", p.String())
		}
		if strings.Contains(p.String(), "s3cr3t") {
			t.Errorf("secret in argv: %s", p.String())
		}
	}
	if plays[0].Env["ANSIBLE_REMOTE_USER"] != "rondo" {
		t.Errorf("ANSIBLE_REMOTE_USER = %q", plays[0].Env["ANSIBLE_REMOTE_USER"])
	}
}

func TestVMInstallWithoutInstallerIsANoop(t *testing.T) {
	srv := archiveServer(t)
	rt, fk := vmFixtures(t, srv.URL+"/vm-module.tar.gz")
	var comp v1alpha1.Component
	if err := rt.Client.Get(t.Context(),
		types.NamespacedName{Namespace: "rondo-system", Name: "mft"}, &comp); err != nil {
		t.Fatal(err)
	}
	comp.Spec.VM.Installer = nil
	if err := rt.Client.Update(t.Context(), &comp); err != nil {
		t.Fatal(err)
	}
	if err := VMInstall(t.Context(), rt); err != nil {
		t.Fatalf("VMInstall without installer: %v", err)
	}
	if len(commandsNamed(fk.Calls, "ansible-playbook")) != 0 {
		t.Error("a playbook ran although there is nothing to install")
	}
}

func strPtr(s string) *string { return &s }

func commandsNamed(calls []runner.Cmd, name string) []runner.Cmd {
	var out []runner.Cmd
	for _, c := range calls {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func callStrings(calls []runner.Cmd) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.String())
	}
	return out
}

func findTofu(t *testing.T, calls []runner.Cmd, sub string) runner.Cmd {
	t.Helper()
	return findCallNamed(t, calls, "tofu", sub)
}

func findCallNamed(t *testing.T, calls []runner.Cmd, name, sub string) runner.Cmd {
	t.Helper()
	for _, c := range calls {
		if c.Name == name && len(c.Args) > 0 && c.Args[0] == sub {
			return c
		}
	}
	t.Fatalf("no `%s %s` call; calls = %v", name, sub, callStrings(calls))
	return runner.Cmd{}
}

func mustSecret(t *testing.T, rt *Runtime, name string) map[string][]byte {
	t.Helper()
	var s corev1.Secret
	if err := rt.Client.Get(t.Context(),
		types.NamespacedName{Namespace: "rondo-system", Name: name}, &s); err != nil {
		t.Fatalf("Secret %s: %v", name, err)
	}
	return s.Data
}

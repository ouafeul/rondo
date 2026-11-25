package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"rondo/api/v1alpha1"
	"rondo/internal/runner"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func newRuntime(t *testing.T, kind, name string, objs ...client.Object) (*Runtime, *runner.Fake) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Enrollment{}).
		Build()
	fakeRun := &runner.Fake{
		Outputs: map[string]string{},
		Errs:    map[string]error{},
	}
	return &Runtime{
		Client:    c,
		Run:       fakeRun,
		Namespace: "rondo-system",
		Ref:       types.NamespacedName{Namespace: "rondo-system", Name: name},
		Kind:      kind,
		WorkDir:   t.TempDir(),
	}, fakeRun
}

func secret(name string, data map[string]string) *corev1.Secret {
	d := map[string][]byte{}
	for k, v := range data {
		d[k] = []byte(v)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "rondo-system"},
		Data:       d,
	}
}

func configMap(name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "rondo-system"},
		Data:       data,
	}
}

func TestMaterializeTofurcWritesFile(t *testing.T) {
	t.Setenv(EnvMirrorSecret, "tofurc")
	rt, _ := newRuntime(t, "Enrollment", "s",
		secret("tofurc", map[string]string{".tofurc": "provider_installation {}"}))
	path, err := rt.materializeTofurc(t.Context(), "rondo-system")
	if err != nil {
		t.Fatalf("materializeTofurc: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "provider_installation") {
		t.Errorf("content = %q", body)
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions %o, want 600 (the file carries a token)", perm)
	}
}

func TestMaterializeTofurcAbsentIsTolerated(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "s")
	path, err := rt.materializeTofurc(t.Context(), "rondo-system")
	if err != nil || path != "" {
		t.Fatalf("materializeTofurc without secret = (%q, %v)", path, err)
	}
}

func TestMaterializeTofurcMissingKey(t *testing.T) {
	t.Setenv(EnvMirrorSecret, "tofurc")
	rt, _ := newRuntime(t, "Enrollment", "s", secret("tofurc", map[string]string{"other": "x"}))
	if _, err := rt.materializeTofurc(t.Context(), "rondo-system"); err == nil {
		t.Fatal("Secret without the .tofurc key accepted")
	}
}

func vmComponentObj() *v1alpha1.Component {
	return &v1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: "mft", Namespace: "rondo-system"},
		Spec: v1alpha1.ComponentSpec{VM: &v1alpha1.VMSpec{
			Groups: []v1alpha1.VMGroup{{Name: "app", Replicas: 1}},
			Installer: &v1alpha1.VMInstaller{
				Script: "install.sh", Version: "1.0", SecretsFile: "secrets.env",
				SecretsFrom: []v1alpha1.ValuesSource{{
					SecretRef: &v1alpha1.LocalObjectReference{Name: "mft-secrets"},
					Keys:      []string{"DB_PASSWORD"},
				}},
			},
		}},
	}
}

func vmReleaseObj(mutate func(*v1alpha1.Enrollment)) *v1alpha1.Enrollment {
	rel := &v1alpha1.Enrollment{
		ObjectMeta: metav1.ObjectMeta{Name: "mft-dmz", Namespace: "rondo-system"},
		Spec: v1alpha1.EnrollmentSpec{
			Component: "mft",
			Target:    "dmz",
		},
		Status: v1alpha1.EnrollmentStatus{ResolvedTarget: "dmz"},
	}
	if mutate != nil {
		mutate(rel)
	}
	return rel
}

func vsphereTargetObj(values map[string]string) *v1alpha1.Target {
	return &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "dmz", Namespace: "rondo-system"},
		Spec: v1alpha1.TargetSpec{
			VSphere: &v1alpha1.VSpherePlacement{
				CredentialsSecretRef: &v1alpha1.LocalObjectReference{Name: "vsphere-creds"},
				SettingsConfigMapRef: &v1alpha1.LocalObjectReference{Name: "rondo-site"},
			},
			Values: values,
		},
	}
}

func TestLoadVMRefusesKubernetesTarget(t *testing.T) {
	k8sTarget := &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "dmz", Namespace: "rondo-system"},
		Spec:       v1alpha1.TargetSpec{Kubernetes: &v1alpha1.KubernetesCluster{InCluster: true}},
	}
	rt, _ := newRuntime(t, "Enrollment", "mft-dmz", vmReleaseObj(nil), vmComponentObj(), k8sTarget)
	_, err := rt.loadVM(t.Context())
	if err == nil {
		t.Fatal("Kubernetes Target accepted for a machine component")
	}
	if !strings.Contains(err.Error(), "vsphere") {
		t.Errorf("message = %q", err)
	}
}

func TestLoadVMRefusesChartComponent(t *testing.T) {
	chartComp := &v1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: "mft", Namespace: "rondo-system"},
		Spec: v1alpha1.ComponentSpec{
			Chart: &v1alpha1.ChartSource{OCI: "oci://x/y", Version: "1.0.0"}, Namespace: "n",
		},
	}
	rt, _ := newRuntime(t, "Enrollment", "mft-dmz", vmReleaseObj(nil), chartComp,
		vsphereTargetObj(nil))
	if _, err := rt.loadVM(t.Context()); err == nil {
		t.Fatal("chart component accepted by a VM verb")
	}
}

func TestVMTofuEnvTranslatesEverything(t *testing.T) {
	t.Setenv(EnvArtifactoryUser, "svc")
	t.Setenv(EnvArtifactoryPassword, "pw")
	t.Setenv(EnvStateBaseURL, "https://arti/state")
	t.Setenv(EnvSSHKeySecret, "rondo-vm-ssh")

	target := vsphereTargetObj(map[string]string{
		"vmIPs.mft.app": `["10.0.2.10"]`,
		"vmTemplate":    "rhel9-cloudinit",
		"sshUser":       "rondo",
	})
	rt, _ := newRuntime(t, "Enrollment", "mft-dmz",
		vmReleaseObj(nil), vmComponentObj(), target,
		secret("rondo-vm-ssh", map[string]string{
			"ssh-publickey":  "ssh-ed25519 AAAA...\n",
			"ssh-privatekey": "-----BEGIN-----",
		}),
		secret("vsphere-creds", map[string]string{"vsphere_user": "u", "vsphere_password": "p"}),
		configMap("rondo-site", map[string]string{
			"vsphere_server": "vc.local", "vsphere_datacenter": "dc",
			"vsphere_cluster": "cl", "vsphere_datastore": "ds",
			"vsphere_network": "net", "network_gateway": "10.0.2.1",
			"network_netmask": "24", "network_dns": `["10.0.0.1"]`,
			"network_domain": "local", "mirror_ca_pem": "CA",
			"vsphere_allow_unverified_ssl": "false",
			"vsphere_folder":               "", "vsphere_resource_pool": "",
		}))

	rv, err := rt.loadVM(t.Context())
	if err != nil {
		t.Fatalf("loadVM: %v", err)
	}
	env, err := rt.vmTofuEnv(t.Context(), rv)
	if err != nil {
		t.Fatalf("vmTofuEnv: %v", err)
	}
	if env["TF_VAR_component"] != "mft" {
		t.Errorf("TF_VAR_component = %q", env["TF_VAR_component"])
	}
	if !strings.Contains(env["TF_VAR_groups"], `"ips":["10.0.2.10"]`) {
		t.Errorf("TF_VAR_groups = %q", env["TF_VAR_groups"])
	}
	if env["TF_VAR_vsphere_template"] != "rhel9-cloudinit" {
		t.Errorf("TF_VAR_vsphere_template = %q", env["TF_VAR_vsphere_template"])
	}
	if env["TF_VAR_vsphere_server"] != "vc.local" || env["TF_VAR_vsphere_password"] != "p" {
		t.Error("placement or vCenter credentials are not passed to the module")
	}
	if !strings.Contains(env["TF_VAR_ssh_authorized_key"], "ssh-ed25519") {
		t.Errorf("TF_VAR_ssh_authorized_key = %q", env["TF_VAR_ssh_authorized_key"])
	}
	if strings.Contains(env["TF_VAR_ssh_authorized_key"], "BEGIN") {
		t.Error("the PRIVATE key leaked into the module variables")
	}
	if want := "https://arti/state/vm/mft-dmz/terraform.tfstate"; env["TF_HTTP_ADDRESS"] != want {
		t.Errorf("TF_HTTP_ADDRESS = %q, want %q", env["TF_HTTP_ADDRESS"], want)
	}
}

func TestVMTofuEnvRefusesWithoutTemplate(t *testing.T) {
	t.Setenv(EnvArtifactoryUser, "u")
	t.Setenv(EnvArtifactoryPassword, "p")
	t.Setenv(EnvStateBaseURL, "https://arti/state")
	t.Setenv(EnvSSHKeySecret, "rondo-vm-ssh")
	rt, _ := newRuntime(t, "Enrollment", "mft-dmz",
		vmReleaseObj(nil), vmComponentObj(),
		vsphereTargetObj(map[string]string{"vmIPs.mft.app": `["10.0.2.10"]`}),
		secret("rondo-vm-ssh", map[string]string{"ssh-publickey": "k", "ssh-privatekey": "p"}))
	rv, err := rt.loadVM(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.vmTofuEnv(t.Context(), rv); err == nil {
		t.Fatal("missing template accepted")
	} else if !strings.Contains(err.Error(), "spec.vm.template") {
		t.Errorf("message = %q, want it to say where to set the template", err)
	}
}

func TestSSHKeysRequireBothHalves(t *testing.T) {
	t.Setenv(EnvSSHKeySecret, "rondo-vm-ssh")
	for _, data := range []map[string]string{
		{"ssh-publickey": "k"},
		{"ssh-privatekey": "p"},
	} {
		rt, _ := newRuntime(t, "Enrollment", "r", secret("rondo-vm-ssh", data))
		if _, _, err := rt.sshKeys(t.Context()); err == nil {
			t.Errorf("incomplete pair accepted: %v", data)
		}
	}
}

func TestSSHKeysRequireSecretName(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "r")
	if _, _, err := rt.sshKeys(t.Context()); err == nil {
		t.Fatal("missing RONDO_VM_SSH_SECRET accepted")
	}
}

func TestInstallerSecretsAreEnumerated(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "mft-dmz",
		vmReleaseObj(nil), vmComponentObj(), vsphereTargetObj(nil),
		secret("mft-secrets", map[string]string{"DB_PASSWORD": "s3cr3t", "OTHER": "ignored"}))
	rv, err := rt.loadVM(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err := rt.installerSecrets(t.Context(), rv)
	if err != nil {
		t.Fatalf("installerSecrets: %v", err)
	}
	if got["DB_PASSWORD"] != "s3cr3t" {
		t.Errorf("secrets read = %v", got)
	}
	if _, leaked := got["OTHER"]; leaked {
		t.Error("a key not enumerated was read")
	}
}

func TestMachineInventoryRequiresProvisioning(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "mft-dmz",
		vmReleaseObj(nil), vmComponentObj(), vsphereTargetObj(nil))
	rv, err := rt.loadVM(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.machineInventory(t.Context(), rv); err == nil {
		t.Fatal("install accepted without provisioning")
	}
}

func TestMachineInventoryRefusesEmpty(t *testing.T) {
	rel := vmReleaseObj(func(r *v1alpha1.Enrollment) {
		r.Status.MachinesSecretRef = &v1alpha1.LocalObjectReference{Name: "m"}
	})
	rt, _ := newRuntime(t, "Enrollment", "mft-dmz", rel, vmComponentObj(),
		vsphereTargetObj(nil), secret("m", map[string]string{"ansible_inventory": "  \n"}))
	rv, err := rt.loadVM(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.machineInventory(t.Context(), rv); err == nil {
		t.Fatal("empty inventory accepted")
	}
}

func TestMachineInventoryWritesFile(t *testing.T) {
	rel := vmReleaseObj(func(r *v1alpha1.Enrollment) {
		r.Status.MachinesSecretRef = &v1alpha1.LocalObjectReference{Name: "m"}
	})
	rt, _ := newRuntime(t, "Enrollment", "mft-dmz", rel, vmComponentObj(),
		vsphereTargetObj(nil),
		secret("m", map[string]string{"ansible_inventory": "[app]\nmft-app-1 ansible_host=10.0.2.10\n"}))
	rv, err := rt.loadVM(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	path, err := rt.machineInventory(t.Context(), rv)
	if err != nil {
		t.Fatalf("machineInventory: %v", err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "[app]") {
		t.Errorf("inventory = %q", body)
	}
}

func TestWriteVarsFileIsPrivate(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "r")
	path, err := rt.writeVarsFile("app", &AnsibleVars{
		Component: "mft", ArtifactoryPassword: "s3cr3t",
		Secrets: map[string]string{"DB": "x"},
	})
	if err != nil {
		t.Fatalf("writeVarsFile: %v", err)
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions %o, want 600", perm)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), `"rondo_component":"mft"`) {
		t.Errorf("content = %q", body)
	}
}

func TestAnsibleRunEnvIsNonInteractive(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "r")
	env := rt.ansibleRunEnv("/work/key")
	if env["ANSIBLE_PRIVATE_KEY_FILE"] != "/work/key" {
		t.Errorf("private key = %q", env["ANSIBLE_PRIVATE_KEY_FILE"])
	}
	if env["ANSIBLE_NOCOLOR"] != "1" {
		t.Error("ANSIBLE_NOCOLOR missing: the Job logs would be a wall of escape codes")
	}
	if !strings.HasPrefix(env["ANSIBLE_LOCAL_TEMP"], rt.dir()) {
		t.Errorf("ANSIBLE_LOCAL_TEMP = %q, want it under %q", env["ANSIBLE_LOCAL_TEMP"], rt.dir())
	}
}

func TestPlaybookOfPrefersComponentContent(t *testing.T) {
	rv := &releaseVM{vm: &v1alpha1.VMSpec{}}
	if got := playbookOf(rv); got != "site.yml" {
		t.Errorf("default playbook = %q", got)
	}
	rv.vm.Ansible = &v1alpha1.VMAnsible{Playbook: "deploy.yml"}
	if got := playbookOf(rv); got != "deploy.yml" {
		t.Errorf("component playbook = %q", got)
	}
	rv.vm.Ansible.Playbook = ""
	if got := playbookOf(rv); got != "site.yml" {
		t.Errorf("component playbook without a name = %q", got)
	}
}

func TestFetchVMModuleRequiresURL(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "r")
	if _, err := rt.fetchVMModule(t.Context()); err == nil {
		t.Fatal("missing RONDO_VM_MODULE_URL accepted")
	}
}

func TestReadEnumeratedToleratesOptionalSettings(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "r",
		configMap("rondo-site", map[string]string{"vsphere_server": "vc"}))
	got, err := rt.readEnumerated(t.Context(), "rondo-site", false,
		[]string{"vsphere_server", "vsphere_folder"})
	if err != nil {
		t.Fatalf("readEnumerated: %v", err)
	}
	if got["vsphere_server"] != "vc" {
		t.Errorf("values = %v", got)
	}
	if _, present := got["vsphere_folder"]; present {
		t.Error("a missing key was invented")
	}
}

func TestReadEnumeratedSecretIsStrict(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "r",
		secret("creds", map[string]string{"vsphere_user": "u"}))
	if _, err := rt.readEnumerated(t.Context(), "creds", true,
		[]string{"vsphere_user", "vsphere_password"}); err == nil {
		t.Fatal("incomplete Secret accepted")
	}
}

func TestResolveSecretsEscapesJSON(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "r",
		secret("vals", map[string]string{"pw": `a"b\c`}))
	raw := []byte(`{"password":"${secrets.pw}"}`)
	got, err := rt.resolveSecrets(t.Context(), raw, []v1alpha1.ValuesSource{{
		SecretRef: &v1alpha1.LocalObjectReference{Name: "vals"}, Keys: []string{"pw"},
	}})
	if err != nil {
		t.Fatalf("resolveSecrets: %v", err)
	}
	if want := `{"password":"a\"b\\c"}`; string(got) != want {
		t.Errorf("values = %s, want %s", got, want)
	}
}

func TestResolveSecretsFailsClosed(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "r", secret("vals", map[string]string{"other": "s3cr3t-VALUE-9f2c"}))
	_, err := rt.resolveSecrets(t.Context(), []byte(`{"p":"${secrets.pw}"}`),
		[]v1alpha1.ValuesSource{{
			SecretRef: &v1alpha1.LocalObjectReference{Name: "vals"}, Keys: []string{"other"},
		}})
	if err == nil {
		t.Fatal("unresolved token accepted")
	}
	if !strings.Contains(err.Error(), "pw") {
		t.Errorf("message = %q, want it to name the missing key", err)
	}
	if strings.Contains(err.Error(), "s3cr3t-VALUE-9f2c") {
		t.Errorf("message = %q: a secret value leaked", err)
	}
}

func TestResolveSecretsUnterminatedToken(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "r", secret("vals", map[string]string{"pw": "x"}))
	_, err := rt.resolveSecrets(t.Context(), []byte(`{"p":"${secrets.pw`),
		[]v1alpha1.ValuesSource{{
			SecretRef: &v1alpha1.LocalObjectReference{Name: "vals"}, Keys: []string{"pw"},
		}})
	if err == nil {
		t.Fatal("unterminated token accepted")
	}
}

func TestResolveSecretsMultipleOccurrences(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "r", secret("vals", map[string]string{"pw": "s3cr3t"}))
	got, err := rt.resolveSecrets(t.Context(),
		[]byte(`{"a":"${secrets.pw}","b":"${secrets.pw}"}`),
		[]v1alpha1.ValuesSource{{
			SecretRef: &v1alpha1.LocalObjectReference{Name: "vals"}, Keys: []string{"pw"},
		}})
	if err != nil {
		t.Fatalf("resolveSecrets: %v", err)
	}
	if n := strings.Count(string(got), "s3cr3t"); n != 2 {
		t.Errorf("%d substitutions, want 2: %s", n, got)
	}
}

func TestReadValuesSourceFromConfigMap(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "r", configMap("vals", map[string]string{"k": "v"}))
	got, err := rt.readValuesSource(t.Context(), v1alpha1.ValuesSource{
		ConfigMapRef: &v1alpha1.LocalObjectReference{Name: "vals"}, Keys: []string{"k"},
	})
	if err != nil {
		t.Fatalf("readValuesSource: %v", err)
	}
	if got["k"] != "v" {
		t.Errorf("values = %v", got)
	}
}

func TestReadValuesSourceRejectsNoRef(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "r")
	if _, err := rt.readValuesSource(t.Context(),
		v1alpha1.ValuesSource{Keys: []string{"k"}}); err == nil {
		t.Fatal("source without a reference accepted")
	}
}

func TestRefFromEnv(t *testing.T) {
	t.Setenv(EnvTargetRef, "rondo-system/mft-dmz")
	t.Setenv(EnvTargetKind, "Enrollment")
	ref, kind, ns, err := RefFromEnv()
	if err != nil {
		t.Fatalf("RefFromEnv: %v", err)
	}
	if ref.Namespace != "rondo-system" || ref.Name != "mft-dmz" || kind != "Enrollment" || ns != "rondo-system" {
		t.Errorf("ref=%v kind=%q ns=%q", ref, kind, ns)
	}
}

func TestRefFromEnvRejectsMalformed(t *testing.T) {
	cases := [][2]string{
		{"", "Enrollment"},
		{"no-slash", "Enrollment"},
		{"rondo-system/mft", ""},
		{"/mft", "Enrollment"},
	}
	for _, c := range cases {
		t.Setenv(EnvTargetRef, c[0])
		t.Setenv(EnvTargetKind, c[1])
		if _, _, _, err := RefFromEnv(); err == nil {
			t.Errorf("malformed reference accepted: %q / %q", c[0], c[1])
		}
	}
}

func TestWorkDirDefault(t *testing.T) {
	rt := &Runtime{}
	if got := rt.dir(); got != workDir {
		t.Errorf("default directory = %q, want %q", got, workDir)
	}
}

func TestWriteFileCreatesParents(t *testing.T) {
	rt := &Runtime{WorkDir: t.TempDir()}
	path, err := rt.writeFile(filepath.Join("sub", "dir", "f.txt"), []byte("x"))
	if err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestVerbsAreRegistered(t *testing.T) {
	for _, verb := range []string{
		"helm-apply", "helm-uninstall",
		"vm-plan", "vm-apply", "vm-destroy", "vm-install",
	} {
		if _, ok := Verbs[verb]; !ok {
			t.Errorf("verb %q not registered", verb)
		}
	}
}

func TestLoadVMResolvesTargetValues(t *testing.T) {
	comp := vmComponentObj()
	comp.Spec.VM.Template = "${target.values.vmTemplate}"
	comp.Spec.VM.Groups[0].Config = map[string]v1alpha1.VMConfigValue{
		"TENANT": {Value: strPtr("${target.values.tenant}")},
	}
	target := vsphereTargetObj(map[string]string{
		"vmTemplate": "rhel9-cloudinit", "tenant": "acme",
	})
	rt, _ := newRuntime(t, "Enrollment", "mft-dmz", vmReleaseObj(nil), comp, target)

	rv, err := rt.loadVM(t.Context())
	if err != nil {
		t.Fatalf("loadVM: %v", err)
	}
	if rv.vm.Template != "rhel9-cloudinit" {
		t.Errorf("template = %q, the token is not resolved", rv.vm.Template)
	}
	if rv.component.Spec.VM.Template != "rhel9-cloudinit" {
		t.Errorf("Component template = %q", rv.component.Spec.VM.Template)
	}
	cfg, err := GroupConfig(rv.vm, "app")
	if err != nil {
		t.Fatalf("GroupConfig: %v", err)
	}
	if cfg["TENANT"].Value != "acme" {
		t.Errorf("config TENANT = %v, the token is not resolved", cfg["TENANT"].Value)
	}
}

func TestLoadVMRefusesUnknownTargetValue(t *testing.T) {
	comp := vmComponentObj()
	comp.Spec.VM.Config = map[string]v1alpha1.VMConfigValue{
		"TENANT": {Value: strPtr("${target.values.tenant}")},
	}
	rt, _ := newRuntime(t, "Enrollment", "mft-dmz", vmReleaseObj(nil), comp, vsphereTargetObj(nil))
	_, err := rt.loadVM(t.Context())
	if err == nil {
		t.Fatal("unknown token accepted")
	}
	if !strings.Contains(err.Error(), "target.values.tenant") {
		t.Errorf("message = %q, want it to name the token", err)
	}
}

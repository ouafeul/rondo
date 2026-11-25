package exec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"rondo/api/v1alpha1"
)

func str(s string) *string { return &s }
func i64(i int64) *int64   { return &i }
func bl(b bool) *bool      { return &b }

func vmTarget(values map[string]string) *v1alpha1.Target {
	return &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "dmz"},
		Spec: v1alpha1.TargetSpec{
			VSphere: &v1alpha1.VSpherePlacement{},
			Values:  values,
		},
	}
}

func TestVMGroupVarsMultiGroup(t *testing.T) {
	vm := &v1alpha1.VMSpec{
		Sizing: &v1alpha1.VMSizing{CPU: 4, MemoryMB: 8192, DiskGB: 100},
		Groups: []v1alpha1.VMGroup{
			{Name: "production", Replicas: 2},
			{Name: "administration", Replicas: 1,
				Sizing: &v1alpha1.VMSizing{CPU: 2, MemoryMB: 4096, DiskGB: 50}},
		},
	}
	target := vmTarget(map[string]string{
		"vmIPs.mft.production":     `["10.0.0.10","10.0.0.11"]`,
		"vmIPs.mft.administration": `["10.0.0.20"]`,
	})

	got, err := VMGroupVars(vm, "mft", target)
	if err != nil {
		t.Fatalf("VMGroupVars: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d groups, want 2", len(got))
	}
	if got[0].Name != "production" || len(got[0].IPs) != 2 {
		t.Errorf("group 0 = %+v", got[0])
	}
	if got[0].CPU != 4 || got[0].MemoryMB != 8192 {
		t.Errorf("the production group does not inherit the component sizing: %+v", got[0])
	}
	if got[1].CPU != 2 || got[1].DiskGB != 50 {
		t.Errorf("the administration group override is lost: %+v", got[1])
	}
}

func TestVMGroupVarsTakesExactlyReplicas(t *testing.T) {
	vm := &v1alpha1.VMSpec{Groups: []v1alpha1.VMGroup{{Name: "app", Replicas: 2}}}
	target := vmTarget(map[string]string{
		"vmIPs.demo.app": `["10.0.0.1","10.0.0.2","10.0.0.3"]`,
	})
	got, err := VMGroupVars(vm, "demo", target)
	if err != nil {
		t.Fatalf("VMGroupVars: %v", err)
	}
	if len(got[0].IPs) != 2 {
		t.Errorf("%d IPs kept for 2 replicas: %v", len(got[0].IPs), got[0].IPs)
	}
}

func TestVMGroupVarsRefusesTooFewIPs(t *testing.T) {
	vm := &v1alpha1.VMSpec{Groups: []v1alpha1.VMGroup{{Name: "app", Replicas: 3}}}
	target := vmTarget(map[string]string{"vmIPs.demo.app": `["10.0.0.1"]`})
	_, err := VMGroupVars(vm, "demo", target)
	if err == nil {
		t.Fatal("3 replicas for 1 IP accepted")
	}
	for _, want := range []string{"3 replicas", "1 IP", "vmIPs.demo.app"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message = %q, want it to contain %q", err, want)
		}
	}
}

func TestVMGroupVarsMissingTargetValue(t *testing.T) {
	vm := &v1alpha1.VMSpec{Groups: []v1alpha1.VMGroup{{Name: "app", Replicas: 1}}}
	_, err := VMGroupVars(vm, "demo", vmTarget(nil))
	if err == nil {
		t.Fatal("group without IPs accepted")
	}
	if !strings.Contains(err.Error(), "SITE values") {
		t.Errorf("message = %q, want it to explain where the IPs come from", err)
	}
}

func TestVMGroupVarsAcceptsCommaList(t *testing.T) {
	vm := &v1alpha1.VMSpec{Groups: []v1alpha1.VMGroup{{Name: "app", Replicas: 2}}}
	target := vmTarget(map[string]string{"vmIPs.demo.app": " 10.0.0.1 , 10.0.0.2 "})
	got, err := VMGroupVars(vm, "demo", target)
	if err != nil {
		t.Fatalf("VMGroupVars: %v", err)
	}
	if len(got[0].IPs) != 2 || got[0].IPs[0] != "10.0.0.1" {
		t.Errorf("IPs = %v", got[0].IPs)
	}
}

func TestVMGroupVarsZeroReplicasDisablesRole(t *testing.T) {
	vm := &v1alpha1.VMSpec{Groups: []v1alpha1.VMGroup{
		{Name: "app", Replicas: 1},
		{Name: "spare", Replicas: 0},
	}}
	target := vmTarget(map[string]string{"vmIPs.demo.app": `["10.0.0.1"]`})
	got, err := VMGroupVars(vm, "demo", target)
	if err != nil {
		t.Fatalf("VMGroupVars: %v", err)
	}
	if len(got) != 1 || got[0].Name != "app" {
		t.Errorf("groups = %+v, the zero-replica group should have been omitted", got)
	}
}

func TestVMGroupVarsAllZeroIsAnError(t *testing.T) {
	vm := &v1alpha1.VMSpec{Groups: []v1alpha1.VMGroup{{Name: "app", Replicas: 0}}}
	if _, err := VMGroupVars(vm, "demo", vmTarget(nil)); err == nil {
		t.Fatal("all groups at zero accepted")
	}
}

func TestVMGroupVarsCustomIPsKey(t *testing.T) {
	vm := &v1alpha1.VMSpec{Groups: []v1alpha1.VMGroup{
		{Name: "app", Replicas: 1, IPsFromTargetValue: "customIPs"},
	}}
	target := vmTarget(map[string]string{"customIPs": `["10.9.9.9"]`})
	got, err := VMGroupVars(vm, "demo", target)
	if err != nil {
		t.Fatalf("VMGroupVars: %v", err)
	}
	if got[0].IPs[0] != "10.9.9.9" {
		t.Errorf("IPs = %v", got[0].IPs)
	}
}

func TestVMGroupVarsDefaultSizing(t *testing.T) {
	vm := &v1alpha1.VMSpec{Groups: []v1alpha1.VMGroup{{Name: "app", Replicas: 1}}}
	target := vmTarget(map[string]string{"vmIPs.demo.app": `["10.0.0.1"]`})
	got, _ := VMGroupVars(vm, "demo", target)
	if got[0].CPU != vmDefaultSizing.CPU || got[0].MemoryMB != vmDefaultSizing.MemoryMB {
		t.Errorf("default sizing = %+v, want %+v", got[0], vmDefaultSizing)
	}
}

func TestVMGroupVarsSerialisesForTofu(t *testing.T) {
	vm := &v1alpha1.VMSpec{
		Sizing: &v1alpha1.VMSizing{CPU: 4, MemoryMB: 8192, DiskGB: 100},
		Groups: []v1alpha1.VMGroup{{Name: "app", Replicas: 1}},
	}
	target := vmTarget(map[string]string{"vmIPs.demo.app": `["10.0.0.1"]`})
	got, _ := VMGroupVars(vm, "demo", target)
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"name":"app"`, `"ips":["10.0.0.1"]`, `"memory_mb":8192`, `"disk_gb":100`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("JSON = %s, want it to contain %s", raw, want)
		}
	}
}

func mftComponent() *v1alpha1.Component {
	return &v1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: "mft"},
		Spec: v1alpha1.ComponentSpec{
			VM: &v1alpha1.VMSpec{
				Groups: []v1alpha1.VMGroup{
					{Name: "production", Replicas: 1, Config: map[string]v1alpha1.VMConfigValue{
						"NC_ROLE":            {Value: str("production")},
						"NC_TRUSTED_DOMAINS": {List: []string{"nc.example.local", "10.0.0.10"}},
					}},
					{Name: "administration", Replicas: 1, Config: map[string]v1alpha1.VMConfigValue{
						"NC_ROLE": {Value: str("administration")},
					}},
				},
				RPMRepos: []v1alpha1.RPMRepo{{
					Name: "rondo-rpms", Repo: "rondo-rpms-local", Path: "el9",
					GPGCheck: bl(false), ModuleHotfixes: true,
				}},
				Artifacts: []v1alpha1.VMArtifact{{
					Repo: "rondo-generic-local", Path: "mft/nextcloud-33.0.0.tar.bz2",
					Digest: "sha256:" + strings.Repeat("a", 64), Dest: "nextcloud",
				}},
				Config: map[string]v1alpha1.VMConfigValue{
					"NC_VERSION":        {Value: str("33.0.0")},
					"NC_OPEN_FIREWALL":  {Bool: bl(true)},
					"NC_PWD_MIN_LENGTH": {Number: i64(12)},
				},
				Installer: &v1alpha1.VMInstaller{
					Version:    "1.4.0",
					ConfigFile: "install.conf", SecretsFile: "secrets.env",
					ConfigFlag: "-c", SecretsFlag: "-e",
					Phases: []v1alpha1.VMInstallPhase{
						{Name: "install", Script: "deploy/install.sh",
							Args: []string{"--yes"}, SkipSteps: []string{"repo"}},
						{Name: "harden", Script: "deploy/harden.sh"},
						{Name: "custom", Script: "deploy/custom.sh"},
						{Name: "groups", Script: "deploy/groups.sh", Args: []string{"--yes"}},
					},
				},
			},
		},
	}
}

func TestBuildAnsibleVarsTypesAreKept(t *testing.T) {
	comp := mftComponent()
	vars, err := BuildAnsibleVars(comp, vmTarget(nil),
		"https://files.example.local", "svc", "pw",
		map[string]string{"NC_DB_PASSWORD": "x", "NC_ADMIN_PASSWORD": "y"}, 0)
	if err != nil {
		t.Fatalf("BuildAnsibleVars: %v", err)
	}
	if got := vars.Config["NC_VERSION"]; got.Kind != "string" || got.Value != "33.0.0" {
		t.Errorf("NC_VERSION = %+v, want a string", got)
	}
	if got := vars.Config["NC_OPEN_FIREWALL"]; got.Kind != "bool" || got.Value != true {
		t.Errorf("NC_OPEN_FIREWALL = %+v, want a bool", got)
	}
	if got := vars.Config["NC_PWD_MIN_LENGTH"]; got.Kind != "number" {
		t.Errorf("NC_PWD_MIN_LENGTH = %+v, want a number", got)
	}
}

func TestBuildAnsibleVarsConfigFormatDefaultsToShell(t *testing.T) {
	vars, err := BuildAnsibleVars(mftComponent(), vmTarget(nil),
		"https://files.example.local", "u", "p", map[string]string{"NC_DB_PASSWORD": "x"}, 0)
	if err != nil {
		t.Fatalf("BuildAnsibleVars: %v", err)
	}
	if got := vars.Installer["config_format"]; got != "shell" {
		t.Errorf("config_format = %v, want the shell default", got)
	}
}

func TestBuildAnsibleVarsPropagatesConfigFormat(t *testing.T) {
	for _, format := range []v1alpha1.VMConfigFormat{
		v1alpha1.ConfigFormatShell, v1alpha1.ConfigFormatEnv,
		v1alpha1.ConfigFormatYAML, v1alpha1.ConfigFormatJSON,
	} {
		comp := mftComponent()
		comp.Spec.VM.Installer.ConfigFormat = format
		vars, err := BuildAnsibleVars(comp, vmTarget(nil),
			"https://files.example.local", "u", "p", map[string]string{"NC_DB_PASSWORD": "x"}, 0)
		if err != nil {
			t.Fatalf("BuildAnsibleVars(%s): %v", format, err)
		}
		if got := vars.Installer["config_format"]; got != string(format) {
			t.Errorf("config_format = %v, want %q", got, format)
		}
	}
}

func TestGroupConfigOverlaysPerRole(t *testing.T) {
	vm := mftComponent().Spec.VM

	prod, err := GroupConfig(vm, "production")
	if err != nil {
		t.Fatalf("GroupConfig(production): %v", err)
	}
	if prod["NC_ROLE"].Value != "production" {
		t.Errorf("NC_ROLE production = %+v", prod["NC_ROLE"])
	}
	if prod["NC_VERSION"].Value != "33.0.0" {
		t.Errorf("the shared config is not inherited: %+v", prod["NC_VERSION"])
	}
	if got := prod["NC_TRUSTED_DOMAINS"]; got.Kind != "list" {
		t.Errorf("NC_TRUSTED_DOMAINS = %+v, want a list", got)
	}

	admin, err := GroupConfig(vm, "administration")
	if err != nil {
		t.Fatalf("GroupConfig(administration): %v", err)
	}
	if admin["NC_ROLE"].Value != "administration" {
		t.Errorf("NC_ROLE administration = %+v", admin["NC_ROLE"])
	}
	if _, leaked := admin["NC_TRUSTED_DOMAINS"]; leaked {
		t.Error("the production group config leaked into administration")
	}
}

func TestGroupConfigUnknownGroup(t *testing.T) {
	if _, err := GroupConfig(mftComponent().Spec.VM, "unknown"); err == nil {
		t.Fatal("unknown group accepted")
	}
}

func TestBuildAnsibleVarsBuildsURLs(t *testing.T) {
	vars, err := BuildAnsibleVars(mftComponent(), vmTarget(nil),
		"https://files.example.local/", "svc", "pw", map[string]string{"NC_DB_PASSWORD": "x"}, 0)
	if err != nil {
		t.Fatalf("BuildAnsibleVars: %v", err)
	}
	if got := vars.RPMRepos[0].BaseURL; got != "https://files.example.local/rondo-rpms-local/el9" {
		t.Errorf("RPM repository baseurl = %q", got)
	}
	if got := vars.Artifacts[0].URL; got != "https://files.example.local/rondo-generic-local/mft/nextcloud-33.0.0.tar.bz2" {
		t.Errorf("artifact URL = %q", got)
	}
	if !vars.RPMRepos[0].ModuleHotfixes {
		t.Error("module_hotfixes lost")
	}
}

func TestBuildAnsibleVarsRefusesGPGCheckWithoutKey(t *testing.T) {
	comp := mftComponent()
	comp.Spec.VM.RPMRepos[0].GPGCheck = bl(true)
	comp.Spec.VM.RPMRepos[0].GPGKey = ""
	_, err := BuildAnsibleVars(comp, vmTarget(nil), "https://files.example.local", "u", "p",
		map[string]string{"NC_DB_PASSWORD": "x"}, 0)
	if err == nil {
		t.Fatal("gpgCheck without gpgKey accepted")
	}
	if !strings.Contains(err.Error(), "gpgKey") {
		t.Errorf("message = %q", err)
	}
}

func TestBuildAnsibleVarsRefusesSecretsFileWithoutSecrets(t *testing.T) {
	_, err := BuildAnsibleVars(mftComponent(), vmTarget(nil),
		"https://files.example.local", "u", "p", nil, 0)
	if err == nil {
		t.Fatal("secrets file without secrets accepted")
	}
	if !strings.Contains(err.Error(), "secretsFrom") {
		t.Errorf("message = %q, want it to point at secretsFrom", err)
	}
}

func TestBuildAnsibleVarsInstallerContract(t *testing.T) {
	vars, err := BuildAnsibleVars(mftComponent(), vmTarget(nil),
		"https://files.example.local", "u", "p", map[string]string{"NC_DB_PASSWORD": "x"}, 0)
	if err != nil {
		t.Fatalf("BuildAnsibleVars: %v", err)
	}
	for _, key := range []string{
		"phases", "config_file", "config_format", "secrets_file",
		"steps_flag", "skip_flag", "config_flag", "secrets_flag", "become",
	} {
		if _, ok := vars.Installer[key]; !ok {
			t.Errorf("key %q missing from the installer contract", key)
		}
	}
	if vars.Installer["skip_flag"] != "--skip" {
		t.Errorf("skip_flag = %v, want the --skip default", vars.Installer["skip_flag"])
	}
	for _, key := range []string{"name", "script", "args", "steps", "skip_steps", "timeout"} {
		if _, ok := installPhase(t, vars, 0)[key]; !ok {
			t.Errorf("key %q missing from a phase contract", key)
		}
	}
	raw, _ := json.Marshal(vars.Installer)
	if strings.Contains(string(raw), "null") {
		t.Errorf("the contract carries a null (breaks Jinja filters): %s", raw)
	}
}

func installPhase(t *testing.T, vars *AnsibleVars, n int) map[string]any {
	t.Helper()
	phases, ok := vars.Installer["phases"].([]map[string]any)
	if !ok {
		t.Fatalf("phases missing from the contract: %#v", vars.Installer["phases"])
	}
	if n >= len(phases) {
		t.Fatalf("phase %d requested, %d phases in the contract", n, len(phases))
	}
	return phases[n]
}

func TestBuildAnsibleVarsKeepsPhaseOrderAndArgs(t *testing.T) {
	vars, err := BuildAnsibleVars(mftComponent(), vmTarget(nil),
		"https://files.example.local", "u", "p", map[string]string{"NC_DB_PASSWORD": "x"}, 45*time.Minute)
	if err != nil {
		t.Fatalf("BuildAnsibleVars: %v", err)
	}
	phases := vars.Installer["phases"].([]map[string]any)
	want := []string{"install", "harden", "custom", "groups"}
	if len(phases) != len(want) {
		t.Fatalf("%d phases in the contract, want %d", len(phases), len(want))
	}
	for i, name := range want {
		if got := phases[i]["name"]; got != name {
			t.Errorf("phase %d = %v, want %q: the declared order is significant", i, got, name)
		}
	}
	if got := phases[0]["args"]; !reflect.DeepEqual(got, []string{"--yes"}) {
		t.Errorf("install args = %#v", got)
	}
	if got := phases[0]["skip_steps"]; !reflect.DeepEqual(got, []string{"repo"}) {
		t.Errorf("install skip_steps = %#v", got)
	}
	if got := phases[1]["args"]; !reflect.DeepEqual(got, []string{}) {
		t.Errorf("harden args = %#v, want empty", got)
	}
	if got := phases[1]["skip_steps"]; !reflect.DeepEqual(got, []string{}) {
		t.Errorf("harden skip_steps = %#v, want empty", got)
	}
}

func TestBuildAnsibleVarsDesugarsSingleScript(t *testing.T) {
	comp := mftComponent()
	comp.Spec.VM.Installer = &v1alpha1.VMInstaller{
		Script: "deploy/install.sh", Version: "1.4.0",
		ConfigFile: "install.conf", SecretsFile: "secrets.env",
		Args: []string{"--yes"}, SkipSteps: []string{"repo"},
	}
	vars, err := BuildAnsibleVars(comp, vmTarget(nil),
		"https://files.example.local", "u", "p", map[string]string{"NC_DB_PASSWORD": "x"}, 0)
	if err != nil {
		t.Fatalf("BuildAnsibleVars: %v", err)
	}
	phases := vars.Installer["phases"].([]map[string]any)
	if len(phases) != 1 {
		t.Fatalf("%d phases for a single script, want 1", len(phases))
	}
	if phases[0]["name"] != v1alpha1.DefaultInstallPhaseName {
		t.Errorf("implicit phase name = %v", phases[0]["name"])
	}
	if phases[0]["script"] != "deploy/install.sh" {
		t.Errorf("script = %v", phases[0]["script"])
	}
	if got := phases[0]["args"]; !reflect.DeepEqual(got, []string{"--yes"}) {
		t.Errorf("the short-form args were not carried over: %#v", got)
	}
}

func TestBuildAnsibleVarsPropagatesTimeout(t *testing.T) {
	comp := mftComponent()
	comp.Spec.VM.Installer.Timeout = str("20m")
	comp.Spec.VM.Installer.Phases[0].Timeout = str("35m")

	vars, err := BuildAnsibleVars(comp, vmTarget(nil),
		"https://files.example.local", "u", "p", map[string]string{"NC_DB_PASSWORD": "x"}, 45*time.Minute)
	if err != nil {
		t.Fatalf("BuildAnsibleVars: %v", err)
	}
	phases := vars.Installer["phases"].([]map[string]any)
	if got := phases[0]["timeout"]; got != int((35 * time.Minute).Seconds()) {
		t.Errorf("install timeout = %v, want 2100 s (the phase bound)", got)
	}
	for _, i := range []int{1, 2, 3} {
		if got := phases[i]["timeout"]; got != int((20 * time.Minute).Seconds()) {
			t.Errorf("phase %d timeout = %v, want 1200 s (inherited)", i, got)
		}
	}
}

func TestBuildAnsibleVarsDerivesTimeoutFromJobBudget(t *testing.T) {
	budget := 45 * time.Minute
	vars, err := BuildAnsibleVars(mftComponent(), vmTarget(nil),
		"https://files.example.local", "u", "p", map[string]string{"NC_DB_PASSWORD": "x"}, budget)
	if err != nil {
		t.Fatalf("BuildAnsibleVars: %v", err)
	}
	want := int((budget - InstallPhaseTimeoutMargin).Seconds())
	for i, p := range vars.Installer["phases"].([]map[string]any) {
		got, ok := p["timeout"].(int)
		if !ok {
			t.Fatalf("phase %d timeout is not an integer number of seconds: %#v", i, p["timeout"])
		}
		if got != want {
			t.Errorf("derived timeout of phase %d = %d s, want %d s", i, got, want)
		}
		if time.Duration(got)*time.Second >= budget {
			t.Errorf("phase %d timeout (%d s) reaches the Job budget: the pod "+
				"would be killed before the secrets are shredded", i, got)
		}
	}
}

func TestBuildAnsibleVarsRefusesPhasesInDifferentDirs(t *testing.T) {
	comp := mftComponent()
	comp.Spec.VM.Installer.Phases[1].Script = "deploy/extra/harden.sh"
	comp.Spec.VM.Installer.ConfigFlag = ""
	comp.Spec.VM.Installer.SecretsFlag = ""
	_, err := BuildAnsibleVars(comp, vmTarget(nil),
		"https://files.example.local", "u", "p", map[string]string{"NC_DB_PASSWORD": "x"}, 0)
	if err == nil {
		t.Fatal("phases not co-located accepted although the secrets file is " +
			"delivered by convention")
	}
	if !strings.Contains(err.Error(), "harden") || !strings.Contains(err.Error(), "secretsFlag") {
		t.Errorf("message = %q: want it to name the failing phase and the way out", err)
	}

	comp.Spec.VM.Installer.ConfigFlag = "-c"
	comp.Spec.VM.Installer.SecretsFlag = "-e"
	if _, err := BuildAnsibleVars(comp, vmTarget(nil),
		"https://files.example.local", "u", "p", map[string]string{"NC_DB_PASSWORD": "x"}, 0); err != nil {
		t.Fatalf("phases not co-located rejected despite configFlag/secretsFlag: %v", err)
	}
}

func TestBuildAnsibleVarsRefusesDuplicatePhaseNames(t *testing.T) {
	comp := mftComponent()
	comp.Spec.VM.Installer.Phases[1].Name = "install"
	_, err := BuildAnsibleVars(comp, vmTarget(nil),
		"https://files.example.local", "u", "p", map[string]string{"NC_DB_PASSWORD": "x"}, 0)
	if err == nil {
		t.Fatal("duplicate phase names accepted")
	}
	if !strings.Contains(err.Error(), "declared twice") {
		t.Errorf("message = %q", err)
	}
}

func TestBuildAnsibleVarsRefusesInstallerWithoutEntryPoint(t *testing.T) {
	comp := mftComponent()
	comp.Spec.VM.Installer = &v1alpha1.VMInstaller{Version: "1.4.0"}
	_, err := BuildAnsibleVars(comp, vmTarget(nil), "https://files.example.local", "u", "p", nil, 0)
	if err == nil {
		t.Fatal("installer without script or phases accepted")
	}
	if !strings.Contains(err.Error(), "entry point") {
		t.Errorf("message = %q", err)
	}
}

func TestBuildAnsibleVarsRefusesBadTimeout(t *testing.T) {
	comp := mftComponent()
	comp.Spec.VM.Installer.Phases[0].Timeout = str("45min")
	_, err := BuildAnsibleVars(comp, vmTarget(nil),
		"https://files.example.local", "u", "p", map[string]string{"NC_DB_PASSWORD": "x"}, 0)
	if err == nil {
		t.Fatal("unreadable time bound accepted")
	}
	if !strings.Contains(err.Error(), "install") {
		t.Errorf("message = %q: want it to name the phase", err)
	}
}

func TestBuildAnsibleVarsRunInstallerFlag(t *testing.T) {
	comp := mftComponent()
	comp.Spec.VM.Installer = nil
	vars, err := BuildAnsibleVars(comp, vmTarget(nil), "https://files.example.local", "u", "p", nil, 0)
	if err != nil {
		t.Fatalf("BuildAnsibleVars: %v", err)
	}
	if vars.RunInstaller {
		t.Error("RunInstaller true without a declared installer")
	}
}

func TestConfigValueRejectsEmpty(t *testing.T) {
	if _, err := configValue("EMPTY", v1alpha1.VMConfigValue{}); err == nil {
		t.Fatal("empty configuration value accepted")
	}
}

package values

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"rondo/api/v1alpha1"
)

func vmSpecFixture() *v1alpha1.VMSpec {
	return &v1alpha1.VMSpec{
		Template: "rhel9",
		Sizing:   &v1alpha1.VMSizing{CPU: 4, MemoryMB: 8192, DiskGB: 100},
		Groups: []v1alpha1.VMGroup{
			{Name: "app", Replicas: 2},
		},
		Config: map[string]v1alpha1.VMConfigValue{
			"K": {Value: ptrStr("v")},
		},
		Installer: &v1alpha1.VMInstaller{Script: "install.sh", Version: "1.0.0"},
	}
}

func ptrStr(s string) *string { return &s }

func fixtureTarget(ips string) *v1alpha1.Target {
	return &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "dmz"},
		Spec:       v1alpha1.TargetSpec{Values: map[string]string{"vmIPs.demo.app": ips}},
	}
}

func TestInfraHashFollowsIPs(t *testing.T) {
	vm := vmSpecFixture()
	h1 := VMInfraHash(vm, "demo", fixtureTarget(`["10.0.0.1","10.0.0.2"]`))
	h2 := VMInfraHash(vm, "demo", fixtureTarget(`["10.0.0.1","10.0.0.2","10.0.0.3"]`))
	if h1 == h2 {
		t.Error("the infrastructure hash ignores the Target IPs")
	}
}

func TestInfraHashIgnoresInstallerVersion(t *testing.T) {
	target := fixtureTarget(`["10.0.0.1","10.0.0.2"]`)
	vm := vmSpecFixture()
	h1 := VMInfraHash(vm, "demo", target)
	vm.Installer.Version = "2.0.0"
	if h2 := VMInfraHash(vm, "demo", target); h1 != h2 {
		t.Error("the infrastructure hash follows the installer version: the machines would be recreated")
	}
}

func TestInfraHashIgnoresConfigFormat(t *testing.T) {
	target := fixtureTarget(`["10.0.0.1","10.0.0.2"]`)
	vm := vmSpecFixture()
	h1 := VMInfraHash(vm, "demo", target)
	vm.Installer.ConfigFormat = v1alpha1.ConfigFormatJSON
	if h2 := VMInfraHash(vm, "demo", target); h1 != h2 {
		t.Error("the infrastructure hash follows the config format: the machines would be recreated")
	}
}

func TestInstallHashIgnoresSizing(t *testing.T) {
	vm := vmSpecFixture()
	h1 := VMInstallHash(vm, nil)
	vm.Sizing = &v1alpha1.VMSizing{CPU: 8, MemoryMB: 16384, DiskGB: 200}
	if h2 := VMInstallHash(vm, nil); h1 != h2 {
		t.Error("the install hash follows sizing: the installation would be replayed for nothing")
	}
}

func TestInstallHashFollowsConfig(t *testing.T) {
	vm := vmSpecFixture()
	h1 := VMInstallHash(vm, nil)
	vm.Config["K"] = v1alpha1.VMConfigValue{Value: ptrStr("other")}
	if h2 := VMInstallHash(vm, nil); h1 == h2 {
		t.Error("the install hash ignores the configuration")
	}
}

func TestInstallHashFollowsGroupConfig(t *testing.T) {
	vm := vmSpecFixture()
	h1 := VMInstallHash(vm, nil)
	vm.Groups[0].Config = map[string]v1alpha1.VMConfigValue{"ROLE": {Value: ptrStr("app")}}
	if h2 := VMInstallHash(vm, nil); h1 == h2 {
		t.Error("the install hash ignores the group configuration")
	}
}

func TestInstallHashFollowsConfigFormat(t *testing.T) {
	vm := vmSpecFixture()
	asShell := VMInstallHash(vm, nil)
	vm.Installer.ConfigFormat = v1alpha1.ConfigFormatJSON
	asJSON := VMInstallHash(vm, nil)
	if asJSON == asShell {
		t.Error("the install hash ignores the config format")
	}
	vm.Installer.ConfigFormat = v1alpha1.ConfigFormatYAML
	if VMInstallHash(vm, nil) == asJSON {
		t.Error("the install hash confuses two formats")
	}
}

func TestInstallHashIsStable(t *testing.T) {
	vm := vmSpecFixture()
	vm.Config["A"] = v1alpha1.VMConfigValue{Value: ptrStr("1")}
	vm.Config["Z"] = v1alpha1.VMConfigValue{Value: ptrStr("2")}
	first := VMInstallHash(vm, nil)
	for i := 0; i < 20; i++ {
		if got := VMInstallHash(vm, nil); got != first {
			t.Fatalf("unstable hash: %q then %q", first, got)
		}
	}
}

func phasedInstaller() *v1alpha1.VMInstaller {
	return &v1alpha1.VMInstaller{
		Version: "1.0.0", ConfigFile: "install.conf", SecretsFile: "secrets.env",
		Phases: []v1alpha1.VMInstallPhase{
			{Name: "install", Script: "deploy/install.sh",
				Args: []string{"--yes"}, SkipSteps: []string{"repo"}},
			{Name: "harden", Script: "deploy/harden.sh"},
			{Name: "custom", Script: "deploy/custom.sh"},
			{Name: "groups", Script: "deploy/groups.sh"},
		},
	}
}

func TestInstallHashFollowsRemovedPhase(t *testing.T) {
	vm := vmSpecFixture()
	vm.Installer = phasedInstaller()
	full := VMInstallHash(vm, nil)

	vm.Installer.Phases = vm.Installer.Phases[:3]
	if trimmed := VMInstallHash(vm, nil); trimmed == full {
		t.Fatal("the install hash ignores the sequence: a removed (or " +
			"added) phase would not replay the installation")
	}
}

func TestInstallHashFollowsPhaseOrder(t *testing.T) {
	vm := vmSpecFixture()
	vm.Installer = phasedInstaller()
	ordered := VMInstallHash(vm, nil)

	p := vm.Installer.Phases
	p[1], p[2] = p[2], p[1]
	if swapped := VMInstallHash(vm, nil); swapped == ordered {
		t.Fatal("the install hash ignores the ORDER of phases")
	}
}

func TestInstallHashFollowsPhaseArgsAndSteps(t *testing.T) {
	vm := vmSpecFixture()
	vm.Installer = phasedInstaller()
	base := VMInstallHash(vm, nil)

	vm.Installer.Phases[0].Args = []string{"--yes", "--force"}
	withArgs := VMInstallHash(vm, nil)
	if withArgs == base {
		t.Error("the install hash ignores a phase's args")
	}
	vm.Installer.Phases[0].SkipSteps = nil
	if VMInstallHash(vm, nil) == withArgs {
		t.Error("the install hash ignores a phase's step selection")
	}
}

func TestInstallHashIsFormAgnostic(t *testing.T) {
	short := vmSpecFixture()
	short.Installer = &v1alpha1.VMInstaller{
		Script: "install.sh", Version: "1.0.0", Args: []string{"--yes"},
	}
	long := vmSpecFixture()
	long.Installer = &v1alpha1.VMInstaller{
		Version: "1.0.0",
		Phases: []v1alpha1.VMInstallPhase{{
			Name: v1alpha1.DefaultInstallPhaseName, Script: "install.sh",
			Args: []string{"--yes"},
		}},
	}
	if VMInstallHash(short, nil) != VMInstallHash(long, nil) {
		t.Error("the short form and the equivalent sequence do not have the same hash")
	}
}

func TestVMInstallerVersionFallsBackToAnsible(t *testing.T) {
	vm := &v1alpha1.VMSpec{Ansible: &v1alpha1.VMAnsible{
		Repo: "r", Path: "p", Version: "3.1.4",
	}}
	if got := VMInstallerVersion(vm); got != "3.1.4" {
		t.Errorf("version = %q, want the Ansible content's", got)
	}
	if got := VMInstallerVersion(&v1alpha1.VMSpec{}); got != "" {
		t.Errorf("version = %q, want empty without an installer", got)
	}
}

func TestInstallHashFollowsSecretContent(t *testing.T) {
	vm := vmSpecFixture()
	if VMInstallHash(vm, map[string]string{"DB_PASSWORD": "v1"}) ==
		VMInstallHash(vm, map[string]string{"DB_PASSWORD": "v2"}) {
		t.Error("the install hash ignores secret content: a rotation would replay nothing")
	}
}

package values

import (
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"rondo/api/v1alpha1"
)

func vmTargetFixture() *v1alpha1.Target {
	return &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "dmz"},
		Spec: v1alpha1.TargetSpec{Values: map[string]string{
			"vmTemplate": "rhel9-cloudinit",
			"tenant":     "acme",
			"dnsServer":  "10.0.0.1",
		}},
	}
}

func vmSpecWithTokens() *v1alpha1.VMSpec {
	interval := "${__interval}"
	tenant := "${target.values.tenant}"
	return &v1alpha1.VMSpec{
		Template: "${target.values.vmTemplate}",
		Groups: []v1alpha1.VMGroup{{
			Name: "app", Replicas: 1,
			Config: map[string]v1alpha1.VMConfigValue{
				"DNS": {List: []string{"${target.values.dnsServer}", "8.8.8.8"}},
			},
		}},
		Config: map[string]v1alpha1.VMConfigValue{
			"TENANT":   {Value: &tenant},
			"INTERVAL": {Value: &interval},
			"DEBUG":    {Bool: new(bool)},
		},
	}
}

func TestResolveVMSubstitutesTemplateAndConfig(t *testing.T) {
	out, err := ResolveVM(vmSpecWithTokens(), TargetVars(vmTargetFixture()))
	if err != nil {
		t.Fatalf("ResolveVM: %v", err)
	}
	if out.Template != "rhel9-cloudinit" {
		t.Errorf("template = %q, token not resolved", out.Template)
	}
	if got := *out.Config["TENANT"].Value; got != "acme" {
		t.Errorf("config TENANT = %q, token not resolved", got)
	}
	if got := out.Groups[0].Config["DNS"].List[0]; got != "10.0.0.1" {
		t.Errorf("group config DNS[0] = %q, token not resolved", got)
	}
	if got := *out.Config["INTERVAL"].Value; got != "${__interval}" {
		t.Errorf("config INTERVAL = %q, a foreign token must stay intact", got)
	}
}

func TestResolveVMLeavesInputAlone(t *testing.T) {
	in := vmSpecWithTokens()
	if _, err := ResolveVM(in, TargetVars(vmTargetFixture())); err != nil {
		t.Fatalf("ResolveVM: %v", err)
	}
	if in.Template != "${target.values.vmTemplate}" {
		t.Errorf("input template = %q, the cached spec was mutated", in.Template)
	}
	if got := *in.Config["TENANT"].Value; got != "${target.values.tenant}" {
		t.Errorf("input config TENANT = %q, the cached spec was mutated", got)
	}
	if got := in.Groups[0].Config["DNS"].List[0]; got != "${target.values.dnsServer}" {
		t.Errorf("input group config = %q, the cached spec was mutated", got)
	}
}

func TestResolveVMNamesTheUnknownToken(t *testing.T) {
	vm := vmSpecWithTokens()
	target := vmTargetFixture()
	delete(target.Spec.Values, "tenant")

	_, err := ResolveVM(vm, TargetVars(target))
	if err == nil {
		t.Fatal("missing key accepted: the machines would be created with a literal token")
	}
	var mv *MissingVarsError
	if !errors.As(err, &mv) {
		t.Fatalf("error of type %T, want *MissingVarsError", err)
	}
	if !strings.Contains(err.Error(), "target.values.tenant") {
		t.Errorf("message = %q, must name the token", err)
	}
	if !strings.Contains(err.Error(), "target.values.dnsServer") {
		t.Errorf("message = %q, must list the available keys", err)
	}
}

func TestResolveVMRefusesASecretInConfig(t *testing.T) {
	vm := vmSpecWithTokens()
	pwd := "${secrets.dbPassword}"
	vm.Config["DB_PASSWORD"] = v1alpha1.VMConfigValue{Value: &pwd}

	_, err := ResolveVM(vm, TargetVars(vmTargetFixture()))
	if err == nil {
		t.Fatal("secret accepted in spec.vm.config: the password would outlive the installation")
	}
	var se *SecretsInVMError
	if !errors.As(err, &se) {
		t.Fatalf("error of type %T, want *SecretsInVMError", err)
	}
	if !strings.Contains(err.Error(), "secrets.dbPassword") {
		t.Errorf("message = %q, must name the token", err)
	}
	for _, want := range []string{"installer.secretsFrom", "installer.secretsFile"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message = %q, must name %s", err, want)
		}
	}
}

func TestResolveVMRefusesASecretInGroupConfig(t *testing.T) {
	vm := vmSpecWithTokens()
	vm.Groups[0].Config["PEERS"] = v1alpha1.VMConfigValue{
		List: []string{"${target.values.dnsServer}", "${secrets.joinToken}"},
	}

	_, err := ResolveVM(vm, TargetVars(vmTargetFixture()))
	if err == nil {
		t.Fatal("secret accepted in groups[].config")
	}
	var se *SecretsInVMError
	if !errors.As(err, &se) {
		t.Fatalf("error of type %T, want *SecretsInVMError", err)
	}
	if !strings.Contains(err.Error(), "secrets.joinToken") {
		t.Errorf("message = %q, must name the token", err)
	}
}

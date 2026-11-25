package versions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dependencies.yaml")
	manifest := `
binaries:
  - {name: opentofu, version: "1.11.5"}
  - {name: helm, version: "v3.17.3"}
  - {name: rke2, version: "v1.34.5+rke2r1"}
helm_charts:
  - {name: cert-manager, version: "v1.17.1"}
  - {name: argo-cd, version: "7.8.2"}
`
	if err := os.WriteFile(p, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := Env(p)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"TOFU_VERSION":         "1.11.5",
		"HELM_VERSION":         "v3.17.3",
		"RKE2_VERSION":         "v1.34.5+rke2r1",
		"CERT_MANAGER_VERSION": "v1.17.1",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if _, ok := env["KUBECTL_VERSION"]; ok {
		t.Error("KUBECTL_VERSION set although kubectl is not in the manifest")
	}
}

func TestEnvErrors(t *testing.T) {
	if _, err := Env(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("missing file accepted")
	}
	p := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(p, []byte("binaries: {not: a list}"), 0o600)
	if _, err := Env(p); err == nil {
		t.Error("invalid YAML accepted")
	}
}

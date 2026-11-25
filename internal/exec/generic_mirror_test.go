package exec

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rondo/internal/tofustate"
)

// vmEnvOn is vmTofuEnv of a machine component, the Job's environment set by env.
func vmEnvOn(t *testing.T, env map[string]string) map[string]string {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	t.Setenv(EnvSSHKeySecret, "rondo-vm-ssh")
	rt, _ := newRuntime(t, "Enrollment", "mft-dmz",
		vmReleaseObj(nil), vmComponentObj(),
		vsphereTargetObj(map[string]string{"vmIPs.mft.app": `["10.0.2.10"]`, "vmTemplate": "rhel9"}),
		secret("rondo-vm-ssh", map[string]string{"ssh-publickey": "k", "ssh-privatekey": "p"}),
		secret("vsphere-creds", map[string]string{"vsphere_user": "u", "vsphere_password": "p"}),
		configMap("rondo-site", map[string]string{"vsphere_server": "vc.local", "mirror_ca_pem": ""}))
	rv, err := rt.loadVM(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out, err := rt.vmTofuEnv(t.Context(), rv)
	if err != nil {
		t.Fatalf("vmTofuEnv: %v", err)
	}
	return out
}

func TestVMStateInS3(t *testing.T) {
	bucket := tofustate.S3Bucket{Bucket: "states", Endpoint: "https://s3.example.local",
		Region: "eu-west-3", KeyPrefix: "sites/prod", UsePathStyle: true}
	env := vmEnvOn(t, map[string]string{
		EnvArtifactoryUser: "", EnvArtifactoryPassword: "",
		EnvStateBaseURL: "", EnvStateS3: bucket.Encode(),
	})
	args := env["TF_CLI_ARGS_init"]
	path, ok := strings.CutPrefix(args, "-backend-config=")
	if !ok {
		t.Fatalf("TF_CLI_ARGS_init = %q, want a -backend-config file", args)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`key = "sites/prod/vm/mft-dmz/terraform.tfstate"`,
		`region = "eu-west-3"`, "use_lockfile = true"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("backend config without %q:\n%s", want, raw)
		}
	}
	for k := range env {
		if strings.HasPrefix(k, "TF_HTTP_") {
			t.Errorf("%s set with an s3 state", k)
		}
	}
	if stateBackend() != tofustate.S3 {
		t.Error("the Job does not select the s3 backend")
	}
}

func TestVMHTTPStateOfAnAnonymousMirror(t *testing.T) {
	env := vmEnvOn(t, map[string]string{
		EnvArtifactoryUser: "", EnvArtifactoryPassword: "",
		EnvStateBaseURL: "https://files/state", EnvStateS3: "",
	})
	if env["TF_HTTP_ADDRESS"] != "https://files/state/vm/mft-dmz/terraform.tfstate" {
		t.Errorf("TF_HTTP_ADDRESS = %q", env["TF_HTTP_ADDRESS"])
	}
	if _, set := env["TF_HTTP_USERNAME"]; set {
		t.Error("basic auth sent to an anonymous state backend")
	}
	if stateBackend() != tofustate.HTTP {
		t.Error("the Job does not select the http backend")
	}
}

func TestDropEmptyCA(t *testing.T) {
	dir := t.TempDir()
	empty, full := filepath.Join(dir, "empty.pem"), filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("-----BEGIN CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCAFile, full)
	dropEmptyCA()
	if os.Getenv(EnvCAFile) != full {
		t.Error("a CA bundle was dropped")
	}
	t.Setenv(EnvCAFile, empty)
	dropEmptyCA()
	if _, set := os.LookupEnv(EnvCAFile); set {
		t.Error("an empty CA bundle stays in SSL_CERT_FILE: TLS would trust nothing")
	}
}

func TestMachineCAPathFollowsTheJob(t *testing.T) {
	t.Setenv(EnvCAFile, "/etc/rondo/ca/ca.pem")
	if machineCAPath() != nil {
		t.Error("the role's CA path is overridden although the Job has a CA")
	}
	t.Setenv(EnvCAFile, "")
	raw, err := json.Marshal(AnsibleVars{ArtifactoryCAPath: machineCAPath()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"rondo_artifactory_ca_path":""`) {
		t.Errorf("vars = %s, want an empty CA path so dnf is not pointed at a missing file", raw)
	}
}

func TestParseTofurcOCI(t *testing.T) {
	rc := "provider_installation {\n  oci_mirror {\n" +
		"    repository_template = \"registry.example.local:5000/providers/${namespace}/${type}\"\n" +
		"  }\n}\n"
	if got := parseTofurcOCI(rc); got != "registry.example.local:5000" {
		t.Errorf("registry = %q", got)
	}
	if got := parseTofurcOCI("provider_installation {\n  network_mirror {\n    url = \"https://a/\"\n  }\n}\n"); got != "" {
		t.Errorf("registry of a network mirror = %q", got)
	}
}

// The OCI mirror probe asks the registry's /v2/ and never fails the Job, whatever it answers.
func TestOCIMirrorProbeNeverFails(t *testing.T) {
	var asked string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE",
		Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCAFile, ca)
	registry := strings.TrimPrefix(srv.URL, "https://")
	for name, host := range map[string]string{"401": registry, "unreachable": "127.0.0.1:1"} {
		t.Run(name, func(t *testing.T) {
			rc := filepath.Join(dir, name+".tofurc")
			body := "provider_installation {\n  oci_mirror {\n    repository_template = \"" + host +
				"/p/${namespace}/${type}\"\n  }\n}\n"
			if err := os.WriteFile(rc, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			sc := &tofuWorkdir{env: map[string]string{"TF_CLI_CONFIG_FILE": rc}}
			if err := sc.checkMirror(context.Background()); err != nil {
				t.Errorf("probe failed the Job: %v", err)
			}
		})
	}
	if asked != "/v2/" {
		t.Errorf("probe asked %q, want /v2/", asked)
	}
}

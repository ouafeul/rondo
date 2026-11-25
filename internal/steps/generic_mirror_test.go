package steps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rondo/internal/config"
	"rondo/internal/runner"
	"rondo/internal/tofustate"
)

// anonymousCfg is a generic mirror read without credentials nor CA, its providers in an OCI
// mirror and its states in an S3 bucket.
func anonymousCfg() *config.Config {
	return &config.Config{
		EnvironmentName: "prod",
		Artifacts: config.Artifacts{
			OCI:   config.OCIEndpoint{Registry: "registry.example.local"},
			Files: config.FilesEndpoint{URL: "https://files.example.local"},
			Tofu: config.TofuEndpoints{ProviderOCI: "registry.example.local/providers",
				State: config.TofuState{S3: &config.S3State{Bucket: "states",
					Endpoint: "https://s3.example.local", UsePathStyle: true}}},
		},
		Secrets: config.Secrets{MirrorStateAccessKey: "AK", MirrorStateSecretKey: "SK"},
	}
}

func TestConfigureRegistryOnAnAnonymousOCIMirror(t *testing.T) {
	dir := t.TempDir()
	st := NewTofuStage(anonymousCfg(), Workspace{ProjectDir: dir, StateDir: dir}, &runner.Fake{},
		fixedToken("never-asked"), false)
	if err := st.ConfigureRegistry(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".tofurc"))
	if err != nil {
		t.Fatal(err)
	}
	rc := string(raw)
	if !strings.Contains(rc, `repository_template = "registry.example.local/providers/${namespace}/${type}"`) ||
		strings.Contains(rc, "oci_credentials") {
		t.Errorf(".tofurc:\n%s", rc)
	}
	for k := range st.regEnv {
		if strings.HasPrefix(k, "TF_TOKEN_") || k == "SSL_CERT_FILE" || strings.HasPrefix(k, "TF_HTTP_") {
			t.Errorf("%s set for an anonymous mirror without CA: %v", k, st.regEnv)
		}
	}
	if st.regEnv["AWS_ACCESS_KEY_ID"] != "AK" || st.regEnv["AWS_SECRET_ACCESS_KEY"] != "SK" {
		t.Errorf("s3 keys missing from the env: %v", st.regEnv)
	}
}

func TestApplyManagementOnAnS3State(t *testing.T) {
	dir := t.TempDir()
	stack := filepath.Join(dir, "terraform", "k3s")
	if err := os.MkdirAll(stack, 0o700); err != nil {
		t.Fatal(err)
	}
	fake := planFake(`{"resource_changes":[]}`)
	st := NewTofuStage(anonymousCfg(), Workspace{ProjectDir: dir, StateDir: dir, BinDir: "/bin"},
		fake, fixedToken(""), false)
	if err := st.ApplyManagement(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(dir, "tofu", "k3s.s3.tfbackend")
	if got, want := fake.Calls[0].String(), "/bin/tofu init -input=false -reconfigure -backend-config="+backend; got != want {
		t.Errorf("init = %q, want %q", got, want)
	}
	if c := fake.Calls[0].Env["TF_VAR_mirror_ca"]; c != "" {
		t.Errorf("TF_VAR_mirror_ca = %q without a CA", c)
	}
	override, err := os.ReadFile(filepath.Join(stack, tofustate.OverrideFile))
	if err != nil || !strings.Contains(string(override), `backend "s3" {}`) {
		t.Errorf("override = %q, %v", override, err)
	}
	conf, err := os.ReadFile(backend)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`bucket = "states"`, `key = "prod/k3s/terraform.tfstate"`,
		`endpoints = { s3 = "https://s3.example.local" }`, "use_path_style = true", "use_lockfile = true"} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("backend config without %q:\n%s", want, conf)
		}
	}
}

func TestHTTPStateRemovesAStaleS3Override(t *testing.T) {
	dir := t.TempDir()
	stack := filepath.Join(dir, "terraform", "k3s")
	if err := os.MkdirAll(stack, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := tofustate.Select(stack, tofustate.S3); err != nil {
		t.Fatal(err)
	}
	st := NewTofuStage(testCfg(), Workspace{ProjectDir: dir, StateDir: dir, BinDir: "/bin"},
		planFake(`{"resource_changes":[]}`), fixedToken("t"), false)
	if err := st.ApplyManagement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stack, tofustate.OverrideFile)); !os.IsNotExist(err) {
		t.Errorf("s3 override left in place with an http state: %v", err)
	}
}

func TestHelmDeployOnAnAnonymousMirror(t *testing.T) {
	fake := &runner.Fake{}
	h := NewHelmStage(anonymousCfg(), Workspace{ProjectDir: "/proj", StateDir: "/proj", BinDir: "/bin"}, fake)
	if err := h.Deploy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 1 || fake.Calls[0].String() != "/bin/helmfile --file /proj/helmfile.yaml sync" {
		t.Fatalf("calls = %v, want helmfile sync alone", fake.Calls)
	}
	for _, k := range []string{"SSL_CERT_FILE", "ARTIFACTORY_CA_CERT_PATH"} {
		if v, set := fake.Calls[0].Env[k]; set {
			t.Errorf("%s = %q without a CA", k, v)
		}
	}
}

func TestEnsureRegcredOnAnAnonymousMirror(t *testing.T) {
	fake := &runner.Fake{}
	ws := Workspace{BinDir: "/bin"}
	b := NewRancherBootstrap(anonymousCfg(), ws, NewKubeClient(ws, fake), nil, false)
	if err := b.EnsureRegcred(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("regcred applied for an anonymous mirror: %v", fake.Calls)
	}
}

func TestApplySiteWithoutCA(t *testing.T) {
	c := genericCfg()
	c.Artifacts.CACertificateFile, c.Artifacts.Username, c.Secrets.MirrorPassword = "", "", ""
	p, got, fk := capture(t, c)
	p.ws.CACert = ""
	fk.Effect = chain(fk.Effect, func(c runner.Cmd) error {
		if c.Name != "ssh-keygen" {
			return nil
		}
		keyPath := c.Args[len(c.Args)-1]
		if err := os.WriteFile(keyPath, []byte("PRIVATE"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(keyPath+".pub", []byte("ssh-ed25519 AAAA rondo-vm\n"), 0o600)
	})
	if err := p.ApplyCredentials(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := p.ApplySite(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ca := nested(t, got.find(t, "ConfigMap", "artifactory-ca"), "data")["ca.pem"]; ca != "" {
		t.Errorf("CA ConfigMap = %q, want empty: the Jobs then use the system trust store", ca)
	}
	values := nested(t, got.find(t, "Target", "management"), "spec", "values")
	if values["artifactoryCA"] != "" {
		t.Errorf("artifactoryCA = %q, want empty", values["artifactoryCA"])
	}
	pv := nested(t, got.find(t, "Secret", "rondo-platform-values"), "stringData")
	if pv["artifactoryAuthURL"] != "https://files.example.local" {
		t.Errorf("artifactoryAuthURL = %q, want no userinfo", pv["artifactoryAuthURL"])
	}
}

func TestApplyCredentialsCarriesTheS3Keys(t *testing.T) {
	p, got, _ := capture(t, anonymousCfg())
	if err := p.ApplyCredentials(t.Context()); err != nil {
		t.Fatal(err)
	}
	sd := nested(t, got.find(t, "Secret", "rondo-credentials"), "stringData")
	if sd["AWS_ACCESS_KEY_ID"] != "AK" || sd["AWS_SECRET_ACCESS_KEY"] != "SK" {
		t.Errorf("credentials = %v, want the s3 keys", sd)
	}
	if sd["ARTIFACTORY_USER"] != "" || sd["ARTIFACTORY_PASSWORD"] != "" {
		t.Errorf("credentials of an anonymous mirror = %v", sd)
	}
}

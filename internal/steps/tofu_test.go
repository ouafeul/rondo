package steps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rondo/internal/config"
	"rondo/internal/runner"
)

func testCfg() *config.Config {
	return &config.Config{
		Artifacts: config.Artifacts{Preset: config.PresetArtifactory, Username: "u",
			Artifactory: config.ArtifactoryMirror{URL: "https://arti.exa-mple.com/artifactory",
				Repositories: config.ArtifactoryRepositories{TofuState: "rondo-tofu-state-local"}}},
		Secrets: config.Secrets{MirrorPassword: "p"},
	}
}

type fixedToken string

func (f fixedToken) Token(context.Context) (string, error) { return string(f), nil }

func TestConfigureRegistryOnAGenericMirror(t *testing.T) {
	for name, token := range map[string]string{"anonymous": "", "declared token": "tok-site"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := &config.Config{Artifacts: config.Artifacts{Username: "u",
				OCI:   config.OCIEndpoint{Registry: "registry.example.local"},
				Files: config.FilesEndpoint{URL: "https://files.example.local"},
				Tofu: config.TofuEndpoints{ProviderMirror: "https://tofu-mirror.example.local/providers/",
					State: config.TofuState{HTTP: &config.HTTPState{URL: "https://files.example.local/state"}}}},
				Secrets: config.Secrets{MirrorPassword: "p", MirrorToken: token}}
			st := NewTofuStage(cfg, Workspace{ProjectDir: dir, StateDir: dir, CACert: "/ca.pem"},
				&runner.Fake{}, StaticToken(cfg.Mirror().MirrorToken), false)
			if err := st.ConfigureRegistry(context.Background()); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, ".tofurc"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `url = "https://tofu-mirror.example.local/providers/"`) {
				t.Errorf(".tofurc does not point at artifacts.tofu.providerMirror: %s", raw)
			}
			if got := st.regEnv["TF_TOKEN_tofu__mirror_example_local"]; got != token {
				t.Errorf("TF_TOKEN = %q, want %q (env %v)", got, token, st.regEnv)
			}
			if _, set := st.regEnv["TF_TOKEN_tofu__mirror_example_local"]; set && token == "" {
				t.Error("an empty token is presented to the mirror")
			}
			if st.regEnv["TF_HTTP_USERNAME"] != "u" || st.regEnv["TF_HTTP_PASSWORD"] != "p" {
				t.Errorf("state backend credentials: %v", st.regEnv)
			}
		})
	}
}

func TestConfigureRegistryWritesTofuRCAndEnv(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{ProjectDir: dir, StateDir: dir, BinDir: "/bin", CACert: "/ca.pem"}
	fake := &runner.Fake{}
	st := NewTofuStage(testCfg(), ws, fake, fixedToken("tok123"), false)

	if err := st.ConfigureRegistry(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".tofurc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "network_mirror") ||
		!strings.Contains(string(raw), "https://arti.exa-mple.com/artifactory/api/terraform/rondo-tofu-local/providers/") {
		t.Errorf("unexpected .tofurc: %s", raw)
	}
	if st.regEnv["TF_TOKEN_arti_exa__mple_com"] != "tok123" {
		t.Errorf("TF_TOKEN missing or badly munged: %v", st.regEnv)
	}
	if st.regEnv["SSL_CERT_FILE"] != "/ca.pem" || st.regEnv["TF_CLI_CONFIG_FILE"] != filepath.Join(dir, ".tofurc") {
		t.Errorf("regEnv: %v", st.regEnv)
	}
}

func TestConfigureRegistryDryRunNoTokenNoWrite(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{ProjectDir: dir, StateDir: dir, CACert: "/ca.pem"}
	st := NewTofuStage(testCfg(), ws, &runner.Fake{}, nil, true)

	if err := st.ConfigureRegistry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tofurc")); err == nil {
		t.Error(".tofurc written in dry-run")
	}
	if st.regEnv["TF_CLI_CONFIG_FILE"] == "" {
		t.Error("TF_CLI_CONFIG_FILE missing in dry-run")
	}
}

func TestConfigureRegistryCachesToken(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	}))
	defer srv.Close()
	arti := &ArtifactoryClient{URL: srv.URL, User: "u", Password: "p", HTTP: srv.Client()}
	st := NewTofuStage(testCfg(), Workspace{ProjectDir: t.TempDir(), StateDir: t.TempDir(), CACert: "/ca"}, &runner.Fake{}, arti, false)

	if err := st.ConfigureRegistry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfigureRegistry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("token requested %d times, want 1 (cache)", calls)
	}
}

func TestApplyManagementSequence(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, []byte("PEMPEM"), 0o600)
	ws := Workspace{ProjectDir: dir, StateDir: dir, BinDir: "/bin", CACert: ca}
	fake := planFake(`{"resource_changes":[{"address":"x","change":{"actions":["create"]}}]}`)
	st := NewTofuStage(testCfg(), ws, fake, fixedToken("t"), false)

	if err := st.ApplyManagement(context.Background()); err != nil {
		t.Fatal(err)
	}
	addr := "-backend-config=address=https://arti.exa-mple.com/artifactory/rondo-tofu-state-local/k3s/terraform.tfstate"
	want := []string{
		"/bin/tofu init -input=false -reconfigure " + addr + " -backend-config=update_method=PUT",
		"/bin/tofu plan -input=false -out=tfplan-management",
		"/bin/tofu show -json tfplan-management",
		"/bin/tofu apply -input=false tfplan-management",
	}
	if len(fake.Calls) != len(want) {
		t.Fatalf("calls: %d, want %d", len(fake.Calls), len(want))
	}
	stack := filepath.Join(dir, "terraform", "k3s")
	for i, c := range fake.Calls {
		if c.String() != want[i] {
			t.Errorf("call %d = %q, want %q", i, c.String(), want[i])
		}
		if c.Dir != stack {
			t.Errorf("call %d dir = %q, want %q", i, c.Dir, stack)
		}
		if c.Env["TF_VAR_mirror_ca"] != "PEMPEM" {
			t.Errorf("call %d: CA not exported", i)
		}
	}
}

func TestBackendCredsInEnvNotArgv(t *testing.T) {
	fake := planFake(`{"resource_changes":[]}`)
	cfg := testCfg()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, []byte("PEM"), 0o600)
	st := NewTofuStage(cfg, Workspace{ProjectDir: dir, StateDir: dir, BinDir: "/bin", CACert: ca}, fake, fixedToken("t"), false)
	if err := st.ConfigureRegistry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.regEnv["TF_HTTP_USERNAME"] != "u" || st.regEnv["TF_HTTP_PASSWORD"] != "p" {
		t.Errorf("backend creds missing from env: %v", st.regEnv)
	}
	if err := st.ApplyManagement(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range fake.Calls {
		if strings.Contains(c.String(), "TF_HTTP") || strings.Contains(c.String(), "password") || strings.Contains(c.String(), "=p ") {
			t.Errorf("backend secret leaks into argv: %q", c.String())
		}
		if c.Env["TF_HTTP_PASSWORD"] != "p" {
			t.Errorf("TF_HTTP_PASSWORD missing from the env of call %q", c.String())
		}
	}
}

func TestArtifactoryClientToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "u" || p != "p" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.ParseForm()
		if r.PostForm.Get("scope") != "member-of-groups:*" {
			t.Errorf("scope: %q", r.PostForm.Get("scope"))
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "abc"})
	}))
	defer srv.Close()

	a := &ArtifactoryClient{URL: srv.URL, User: "u", Password: "p", HTTP: srv.Client()}
	tok, err := a.Token(context.Background())
	if err != nil || tok != "abc" {
		t.Fatalf("token = %q, err = %v", tok, err)
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{})
	}))
	defer empty.Close()
	a2 := &ArtifactoryClient{URL: empty.URL, User: "u", Password: "p", HTTP: empty.Client()}
	if _, err := a2.Token(context.Background()); err == nil {
		t.Fatal("empty token accepted")
	}
}

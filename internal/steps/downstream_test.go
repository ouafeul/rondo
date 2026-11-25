package steps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rondo/internal/config"
	"rondo/internal/runner"
	"rondo/internal/state"
)

func rancherSrv(t *testing.T, validToken string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v3/users":
			if validToken != "" && r.Header.Get("Authorization") == "Bearer "+validToken {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		case strings.HasPrefix(r.URL.Path, "/v3-public/localProviders/local"):
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			if in["username"] != "admin" || in["password"] != "boot" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"token": "session-tok"})
		case r.URL.Path == "/v3/token":
			if r.Header.Get("Authorization") != "Bearer session-tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"name": "token-abc", "token": "token-abc:secretpart"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func dsCfg() *config.Config {
	return &config.Config{
		IngressBaseDomain: "example.com",
		Artifacts: config.Artifacts{Preset: config.PresetArtifactory, Username: "au",
			Artifactory: config.ArtifactoryMirror{URL: "https://arti.example.com",
				Repositories: config.ArtifactoryRepositories{Docker: "docker"}}},
		Secrets: config.Secrets{MirrorPassword: "ap", RancherBootstrapPassword: "boot"},
	}
}

func TestRancherClientLoginAndToken(t *testing.T) {
	srv := rancherSrv(t, "")
	defer srv.Close()
	rc := &RancherClient{BaseURL: srv.URL, HTTP: srv.Client()}

	session, err := rc.Login(context.Background(), "admin", "boot")
	if err != nil || session != "session-tok" {
		t.Fatalf("login: %q %v", session, err)
	}
	access, secret, err := rc.CreateToken(context.Background(), session, "rondo-downstream")
	if err != nil {
		t.Fatal(err)
	}
	if access != "token-abc" || secret != "secretpart" {
		t.Errorf("access=%q secret=%q", access, secret)
	}
}

func TestBootstrapTokenCreatesAndPersists(t *testing.T) {
	srv := rancherSrv(t, "")
	defer srv.Close()
	dir := t.TempDir()
	ws := Workspace{ProjectDir: dir, StateDir: dir, BinDir: "/bin", CACert: "/ca"}
	fake := &runner.Fake{Outputs: map[string]string{
		"/bin/kubectl -n cert-manager get secret cluster-ca-keypair -o jsonpath={.data.ca\\.crt}": base64.StdEncoding.EncodeToString([]byte("CAPEM")),
	}}
	b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, fake),
		&RancherClient{BaseURL: srv.URL, HTTP: srv.Client()}, false)

	if err := b.BootstrapToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "certs", "rancher-ca.pem"))
	if err != nil || string(raw) != "CAPEM" {
		t.Errorf("exported CA: %q %v", raw, err)
	}
	if b.rancherEnv["TF_VAR_rancher_access_key"] != "token-abc" || b.rancherEnv["TF_VAR_rancher_secret_key"] != "secretpart" {
		t.Errorf("rancherEnv: %v", b.rancherEnv)
	}
	st, err := state.Load(filepath.Join(dir, "rondo.state.json"))
	if err != nil || st.RancherAccessKey != "token-abc" {
		t.Errorf("state: %+v %v", st, err)
	}
}

func TestBootstrapTokenReusesValidState(t *testing.T) {
	srv := rancherSrv(t, "saved-ak:saved-sk")
	defer srv.Close()
	dir := t.TempDir()
	ws := Workspace{ProjectDir: dir, StateDir: dir, BinDir: "/bin", CACert: "/ca"}
	st := &state.State{RancherAccessKey: "saved-ak", RancherSecretKey: "saved-sk"}
	if err := st.Save(filepath.Join(dir, "rondo.state.json")); err != nil {
		t.Fatal(err)
	}
	fake := &runner.Fake{Outputs: map[string]string{
		"/bin/kubectl -n cert-manager get secret cluster-ca-keypair -o jsonpath={.data.ca\\.crt}": base64.StdEncoding.EncodeToString([]byte("CAPEM")),
	}}
	b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, fake),
		&RancherClient{BaseURL: srv.URL, HTTP: srv.Client()}, false)

	if err := b.BootstrapToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.rancherEnv["TF_VAR_rancher_access_key"] != "saved-ak" {
		t.Errorf("valid token not reused: %v", b.rancherEnv)
	}
}

func TestBootstrapTokenRefreshesStaleState(t *testing.T) {
	srv := rancherSrv(t, "")
	defer srv.Close()
	dir := t.TempDir()
	ws := Workspace{ProjectDir: dir, StateDir: dir, BinDir: "/bin", CACert: "/ca"}
	st := &state.State{RancherAccessKey: "stale-ak", RancherSecretKey: "stale-sk"}
	if err := st.Save(filepath.Join(dir, "rondo.state.json")); err != nil {
		t.Fatal(err)
	}
	fake := &runner.Fake{Outputs: map[string]string{
		"/bin/kubectl -n cert-manager get secret cluster-ca-keypair -o jsonpath={.data.ca\\.crt}": base64.StdEncoding.EncodeToString([]byte("CAPEM")),
	}}
	b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, fake),
		&RancherClient{BaseURL: srv.URL, HTTP: srv.Client()}, false)

	if err := b.BootstrapToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.rancherEnv["TF_VAR_rancher_access_key"] != "token-abc" {
		t.Errorf("stale token not regenerated: %v", b.rancherEnv)
	}
	got, err := state.Load(filepath.Join(dir, "rondo.state.json"))
	if err != nil || got.RancherAccessKey != "token-abc" {
		t.Errorf("state not rewritten: %+v %v", got, err)
	}
}

func TestEnsureRegcredGeneratesThenApplies(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{ProjectDir: dir, StateDir: dir, BinDir: "/bin", CACert: "/ca"}
	fake := &runner.Fake{}
	b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, fake), nil, false)

	if err := b.EnsureRegcred(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.Calls[0].String() != "/bin/kubectl apply -f -" {
		t.Errorf("call 0: %q", fake.Calls[0].String())
	}
	if !strings.Contains(fake.Calls[0].Stdin, "password: ap") ||
		!strings.Contains(fake.Calls[0].Stdin, "kubernetes.io/basic-auth") {
		t.Errorf("unexpected regcred manifest: %q", fake.Calls[0].Stdin)
	}
	for _, c := range fake.Calls {
		if strings.Contains(c.String(), "ap") && strings.Contains(c.String(), "password") {
			t.Errorf("password in argv: %q", c.String())
		}
	}
}

func TestEnsureRegcredDryRunNoExec(t *testing.T) {
	b := NewRancherBootstrap(dsCfg(), Workspace{}, nil, nil, true)
	if err := b.EnsureRegcred(context.Background()); err != nil {
		t.Fatalf("dry-run regcred: %v", err)
	}
}

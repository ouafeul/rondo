package steps

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rondo/internal/config"
	"rondo/internal/runner"
)

func TestNewRancherClientBuildsBaseURL(t *testing.T) {
	rc := NewRancherClient("rancher.example.com")
	if rc.BaseURL != "https://rancher.example.com" {
		t.Errorf("BaseURL = %q", rc.BaseURL)
	}
	if rc.HTTP == nil {
		t.Error("HTTP client not built")
	}
}

func TestRancherLoginError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	rc := &RancherClient{BaseURL: srv.URL, HTTP: srv.Client()}
	if _, err := rc.Login(context.Background(), "admin", "bad"); err == nil {
		t.Fatal("a failed login should return an error")
	}
}

func TestRancherCreateTokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	rc := &RancherClient{BaseURL: srv.URL, HTTP: srv.Client()}
	if _, _, err := rc.CreateToken(context.Background(), "sess", "desc"); err == nil {
		t.Fatal("a failed token creation should return an error")
	}
}

func TestRancherCreateTokenInvalidPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"name":"","token":""}`))
	}))
	defer srv.Close()
	rc := &RancherClient{BaseURL: srv.URL, HTTP: srv.Client()}
	if _, _, err := rc.CreateToken(context.Background(), "sess", "desc"); err == nil {
		t.Fatal("empty token accepted")
	}
}

func TestArtifactoryTokenLazyClientBadCA(t *testing.T) {
	a := &ArtifactoryClient{URL: "https://x", User: "u", Password: "p", CACertPath: filepath.Join(t.TempDir(), "absent.pem")}
	if _, err := a.Token(context.Background()); err == nil {
		t.Fatal("a missing CA should make Token fail")
	}
}

func TestArtifactoryTokenLazyClientSuccess(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"access_token":"lazytok"}`))
	}))
	defer srv.Close()
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	a := &ArtifactoryClient{URL: srv.URL, User: "u", Password: "p", CACertPath: caPath}
	tok, err := a.Token(context.Background())
	if err != nil || tok != "lazytok" {
		t.Fatalf("token = %q, err = %v", tok, err)
	}
	if a.HTTP == nil {
		t.Error("HTTP client not kept after lazy construction")
	}
}

func TestArtifactoryTokenHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	a := &ArtifactoryClient{URL: srv.URL, User: "u", Password: "p", HTTP: srv.Client()}
	if _, err := a.Token(context.Background()); err == nil {
		t.Fatal("an HTTP error should be returned")
	}
}

func TestInstallKubeconfig(t *testing.T) {
	proj := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := Workspace{ProjectDir: proj, StateDir: proj}
	kc := "clusters: [{name: default, cluster: {server: https://10.0.0.11:6443}}]\n" +
		"users: [{name: default, user: {token: t}}]\n"
	if err := os.WriteFile(ws.Kubeconfig(), []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	other := "contexts: [{name: other, context: {cluster: other, user: other}}]\ncurrent-context: other\n"
	if err := os.MkdirAll(filepath.Join(home, ".kube"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".kube", "config"), []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewMgmtStage(&config.Config{EnvironmentName: "dev"}, ws, &runner.Fake{}, false)
	m.UseContext = true
	if err := m.InstallKubeconfig(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(home, ".kube", "config"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: other", "name: rondo-dev", "server: https://10.0.0.11:6443", "current-context: rondo-dev"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("%q missing from ~/.kube/config:\n%s", want, got)
		}
	}
}

func TestInstallKubeconfigDryRun(t *testing.T) {
	m := NewMgmtStage(&config.Config{}, Workspace{}, &runner.Fake{}, true)
	if err := m.InstallKubeconfig(context.Background()); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
}

func TestInstallKubeconfigMissingSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	m := NewMgmtStage(&config.Config{}, Workspace{ProjectDir: t.TempDir(), StateDir: t.TempDir()}, &runner.Fake{}, false)
	if err := m.InstallKubeconfig(context.Background()); err == nil {
		t.Fatal("a missing source kubeconfig should fail")
	}
}

func TestSshConfigUsesCredentials(t *testing.T) {
	cfg := &config.Config{Targets: config.Targets{Management: config.K3sTarget{NodeSSHUsername: "cloud", NodeSSHPassword: "pw"}}}
	m := NewMgmtStage(cfg, Workspace{}, &runner.Fake{}, false)
	sc := m.sshConfig()
	if sc.User != "cloud" || len(sc.Auth) != 1 {
		t.Errorf("sshConfig: user=%q auth=%d", sc.User, len(sc.Auth))
	}
}

func TestCloudInitDoneUnreachable(t *testing.T) {
	cfg := &config.Config{Targets: config.Targets{Management: config.K3sTarget{NodeSSHUsername: "u", NodeSSHPassword: "p"}}}
	m := NewMgmtStage(cfg, Workspace{}, &runner.Fake{}, false)
	if cloudInitDone("127.0.0.1:0", m.sshConfig()) {
		t.Error("an unreachable host should return false")
	}
}

func TestExportClusterCAInvalidBase64(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{ProjectDir: dir, StateDir: dir, CACert: "/ca"}
	fake := &runner.Fake{Outputs: map[string]string{
		"/kubectl -n cert-manager get secret cluster-ca-keypair -o jsonpath={.data.ca\\.crt}": "!!!notbase64!!!",
	}}
	ws.BinDir = ""
	b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, fake), nil, false)
	if err := b.exportClusterCA(context.Background()); err == nil {
		t.Fatal("invalid base64 should fail")
	}
}

func TestExportClusterCADryRun(t *testing.T) {
	b := NewRancherBootstrap(dsCfg(), Workspace{}, nil, nil, true)
	if err := b.exportClusterCA(context.Background()); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
}

func TestBootstrapTokenDryRun(t *testing.T) {
	b := NewRancherBootstrap(dsCfg(), Workspace{}, nil, nil, true)
	if err := b.BootstrapToken(context.Background()); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
}

func TestDestroyManagement(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, []byte("PEM"), 0o600)
	ws := Workspace{ProjectDir: dir, StateDir: dir, BinDir: "/bin", CACert: ca}
	os.WriteFile(ws.Kubeconfig(), []byte("kc"), 0o600)
	fake := &runner.Fake{}
	st := NewTofuStage(testCfg(), ws, fake, fixedToken("t"), false)

	if err := st.DestroyManagement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) < 2 {
		t.Fatalf("calls: %d", len(fake.Calls))
	}
	last := fake.Calls[len(fake.Calls)-1]
	if last.String() != "/bin/tofu destroy -input=false -auto-approve" {
		t.Errorf("last call = %q", last.String())
	}
	if _, err := os.Stat(ws.Kubeconfig()); err == nil {
		t.Error("kubeconfig not deleted after destroy")
	}
}

func TestDestroyManagementDryRunKeepsKubeconfig(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{ProjectDir: dir, StateDir: dir, BinDir: "/bin", CACert: "/ca"}
	os.WriteFile(ws.Kubeconfig(), []byte("kc"), 0o600)
	st := NewTofuStage(testCfg(), ws, runner.DryRun{Real: &runner.Fake{}}, nil, true)
	if err := st.DestroyManagement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ws.Kubeconfig()); err != nil {
		t.Error("kubeconfig deleted in dry-run")
	}
}

func TestSetCAVarMissingFile(t *testing.T) {
	st := NewTofuStage(testCfg(), Workspace{CACert: filepath.Join(t.TempDir(), "absent.pem")}, &runner.Fake{}, fixedToken("t"), false)
	if err := st.ApplyManagement(context.Background()); err == nil {
		t.Fatal("a missing CA should make ApplyManagement fail")
	}
}

func TestHelmDeployLoginError(t *testing.T) {
	fake := &runner.Fake{Errs: map[string]error{
		"/bin/helm registry login arti.example.com --username u --password-stdin --ca-file /ca.pem": errors.New("nope"),
	}}
	cfg := &config.Config{
		Artifactory: config.Artifactory{URL: "https://arti.example.com/artifactory", Username: "u"},
		Secrets:     config.Secrets{ArtifactoryPassword: "secret"},
	}
	h := NewHelmStage(cfg, Workspace{ProjectDir: "/proj", StateDir: "/proj", BinDir: "/bin", CACert: "/ca.pem"}, fake)
	if err := h.Deploy(context.Background()); err == nil || !strings.Contains(err.Error(), "Helm OCI login") {
		t.Fatalf("want a login error, got %v", err)
	}
}

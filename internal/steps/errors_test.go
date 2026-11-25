package steps

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rondo/internal/runner"
)

type failRunner struct{ failOn string }

func (f failRunner) Run(_ context.Context, c runner.Cmd) error {
	if f.failOn == "" || strings.Contains(c.String(), f.failOn) {
		return errors.New("run fail")
	}
	return nil
}

func (f failRunner) Output(_ context.Context, _ runner.Cmd) (string, error) {
	return "", errors.New("out fail")
}

type errToken struct{}

func (errToken) Token(context.Context) (string, error) { return "", errors.New("token boom") }

func caWS(t *testing.T) Workspace {
	t.Helper()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, []byte("PEM"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Workspace{ProjectDir: dir, StateDir: dir, BinDir: "/bin", CACert: ca}
}

func TestConfigureRegistryTokenError(t *testing.T) {
	st := NewTofuStage(testCfg(), caWS(t), &runner.Fake{}, errToken{}, false)
	if err := st.ConfigureRegistry(context.Background()); err == nil {
		t.Fatal("want a token error")
	}
}

func TestApplyManagementInitError(t *testing.T) {
	st := NewTofuStage(testCfg(), caWS(t), failRunner{failOn: "init"}, fixedToken("t"), false)
	if err := st.ApplyManagement(context.Background()); err == nil {
		t.Fatal("want an init error")
	}
}

func TestApplyManagementPlanError(t *testing.T) {
	st := NewTofuStage(testCfg(), caWS(t), failRunner{failOn: "plan"}, fixedToken("t"), false)
	if err := st.ApplyManagement(context.Background()); err == nil {
		t.Fatal("want a plan error")
	}
}

func TestApplyManagementApplyError(t *testing.T) {
	st := NewTofuStage(testCfg(), caWS(t), failRunner{failOn: "apply"}, fixedToken("t"), false)
	if err := st.ApplyManagement(context.Background()); err == nil {
		t.Fatal("want an apply error")
	}
}

func TestDestroyManagementTokenError(t *testing.T) {
	st := NewTofuStage(testCfg(), caWS(t), &runner.Fake{}, errToken{}, false)
	if err := st.DestroyManagement(context.Background()); err == nil {
		t.Fatal("want an error (ConfigureRegistry/token)")
	}
}

func TestDestroyManagementInitError(t *testing.T) {
	st := NewTofuStage(testCfg(), caWS(t), failRunner{failOn: "init"}, fixedToken("t"), false)
	if err := st.DestroyManagement(context.Background()); err == nil {
		t.Fatal("want an init error")
	}
}

func TestDestroyManagementDestroyError(t *testing.T) {
	st := NewTofuStage(testCfg(), caWS(t), failRunner{failOn: "destroy"}, fixedToken("t"), false)
	if err := st.DestroyManagement(context.Background()); err == nil {
		t.Fatal("want a destroy error")
	}
}

func TestBootstrapTokenLoginError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	ws := caWS(t)
	fake := &runner.Fake{Outputs: map[string]string{
		"/bin/kubectl -n cert-manager get secret cluster-ca-keypair -o jsonpath={.data.ca\\.crt}": "Q0FQRU0=",
	}}
	b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, fake),
		&RancherClient{BaseURL: srv.URL, HTTP: srv.Client()}, false)
	if err := b.BootstrapToken(context.Background()); err == nil {
		t.Fatal("want a login error")
	}
}

func TestBootstrapTokenExportCAError(t *testing.T) {
	ws := caWS(t)
	b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, failRunner{}), nil, false)
	if err := b.BootstrapToken(context.Background()); err == nil {
		t.Fatal("want a CA export error")
	}
}

func TestEnsureRegcredOutputError(t *testing.T) {
	ws := caWS(t)
	b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, failRunner{}), nil, false)
	if err := b.EnsureRegcred(context.Background()); err == nil {
		t.Fatal("want a regcred generation error")
	}
}

func TestExportClusterCAMkdirError(t *testing.T) {
	ws := caWS(t)
	if err := os.WriteFile(filepath.Join(ws.ProjectDir, "certs"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := NewRancherBootstrap(dsCfg(), ws, NewKubeClient(ws, &runner.Fake{}), nil, false)
	if err := b.exportClusterCA(context.Background()); err == nil {
		t.Fatal("MkdirAll under a file should fail")
	}
}

func TestInstallKubeconfigMkdirError(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "homefile")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", bad)
	m := NewMgmtStage(testCfg(), Workspace{ProjectDir: t.TempDir(), StateDir: t.TempDir()}, &runner.Fake{}, false)
	if err := m.InstallKubeconfig(context.Background()); err == nil {
		t.Fatal("MkdirAll ~/.kube under a file should fail")
	}
}

func TestSetupK3sError(t *testing.T) {
	m := NewMgmtStage(mgmtCfg(), Workspace{ProjectDir: "/proj", StateDir: "/proj"}, failRunner{}, false)
	if err := m.SetupK3s(context.Background()); err == nil {
		t.Fatal("want a k3s-setup error")
	}
}

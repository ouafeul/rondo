package steps

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"rondo/internal/config"
	"rondo/internal/runner"
)

func mgmtCfg() *config.Config {
	return &config.Config{Targets: config.Targets{Management: config.K3sTarget{
		ServerNodes:     []config.Node{{Hostname: "k3s-1", IP: "10.0.0.11"}, {Hostname: "k3s-2", IP: "10.0.0.12"}},
		NodeSSHUsername: "u", NodeSSHPassword: "p",
	}}}
}

func TestWaitCloudInitSuccessAfterRetries(t *testing.T) {
	m := NewMgmtStage(mgmtCfg(), Workspace{}, &runner.Fake{}, false)
	m.interval = 0
	m.attempts = 5
	tries := map[string]int{}
	m.probe = func(ip string) bool {
		tries[ip]++
		return tries[ip] >= 3
	}
	if err := m.WaitCloudInit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tries["10.0.0.11"] != 3 || tries["10.0.0.12"] != 3 {
		t.Errorf("probes: %v", tries)
	}
}

func TestWaitCloudInitTimeout(t *testing.T) {
	m := NewMgmtStage(mgmtCfg(), Workspace{}, &runner.Fake{}, false)
	m.interval = 0
	m.attempts = 2
	m.probe = func(string) bool { return false }
	err := m.WaitCloudInit(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cloud-init timeout on 10.0.0.11") {
		t.Fatalf("want a timeout, got %v", err)
	}
}

func TestWaitCloudInitCtxCancel(t *testing.T) {
	m := NewMgmtStage(mgmtCfg(), Workspace{}, &runner.Fake{}, false)
	m.interval = time.Hour
	m.attempts = 2
	m.probe = func(string) bool { return false }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.WaitCloudInit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
}

func TestWaitCloudInitDryRunSkipsProbe(t *testing.T) {
	m := NewMgmtStage(mgmtCfg(), Workspace{}, &runner.Fake{}, true)
	m.probe = func(string) bool { t.Fatal("probe called in dry-run"); return false }
	if err := m.WaitCloudInit(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSetupK3sRunsScriptInProjectDir(t *testing.T) {
	fake := &runner.Fake{}
	m := NewMgmtStage(mgmtCfg(), Workspace{ProjectDir: "/proj", StateDir: "/proj"}, fake, false)
	if err := m.SetupK3s(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := fake.Calls[0]
	if c.String() != "bash /proj/scripts/k3s-setup.sh" || c.Dir != "/proj" {
		t.Errorf("call: %q dir=%q", c.String(), c.Dir)
	}
}

func TestSetupK3sPassesTheSiteStatePathToTheScript(t *testing.T) {
	fake := &runner.Fake{}
	ws := Workspace{ProjectDir: "/rondo/cache/v1.2.3", StateDir: "/rondo/state"}
	m := NewMgmtStage(mgmtCfg(), ws, fake, false)
	if err := m.SetupK3s(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.Calls[0].Env["KUBECONFIG_PATH"]; got != ws.Kubeconfig() {
		t.Errorf("KUBECONFIG_PATH = %q, want %q", got, ws.Kubeconfig())
	}
}

func TestHelmDeployArgvAndStdin(t *testing.T) {
	fake := &runner.Fake{}
	cfg := &config.Config{
		Artifacts: config.Artifacts{Preset: config.PresetArtifactory, Username: "u",
			Artifactory: config.ArtifactoryMirror{URL: "https://arti.example.com/artifactory"}},
		Secrets: config.Secrets{MirrorPassword: "secret"},
	}
	h := NewHelmStage(cfg, Workspace{ProjectDir: "/proj", StateDir: "/proj", BinDir: "/bin", CACert: "/ca.pem"}, fake)
	if err := h.Deploy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 2 {
		t.Fatalf("calls: %d", len(fake.Calls))
	}
	login := fake.Calls[0]
	if login.String() != "/bin/helm registry login arti.example.com --username u --password-stdin --ca-file /ca.pem" {
		t.Errorf("login: %q", login.String())
	}
	if login.Stdin != "secret" {
		t.Error("password missing from stdin")
	}
	sync := fake.Calls[1]
	if sync.String() != "/bin/helmfile --file /proj/helmfile.yaml sync" {
		t.Errorf("sync: %q", sync.String())
	}
	for _, k := range []string{"KUBECONFIG", "PROJECT_DIR", "SSL_CERT_FILE", "MIRROR_CA_CERT_PATH"} {
		if sync.Env[k] == "" {
			t.Errorf("env %s missing", k)
		}
	}
}

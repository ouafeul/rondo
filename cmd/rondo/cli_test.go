package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"rondo"
	"strings"
	"testing"

	"rondo/internal/config"
	"rondo/internal/paths"
	"rondo/internal/steps"
)

// manifestList is the shipped example with two rke2 targets, alpha and beta.
var manifestList = func() string {
	s := strings.Replace(string(rondo.ExampleConfig), "./certs/mirror-ca.pem", "./ca.pem", 1)
	s = strings.Replace(s, "name: rke2-a", "name: alpha", 1)
	beta := `    - name: beta
      rke2:
        vsphereTarget: dc1
        rke2Version: v1.34.5+rke2r1
        cniPlugin: cilium
        ingressLoadBalancerIp: 10.0.1.50
        controlPlaneNodeIps: [10.0.1.31]
        controlPlaneNodeSize: { cpuCores: 4, memoryMb: 8192, diskGb: 100 }
        workerNodeSize: { cpuCores: 4, memoryMb: 8192, diskGb: 100 }

secrets:`
	return strings.Replace(s, "\nsecrets:", "\n"+beta, 1)
}()

var manifestNoDownstream = func() string {
	s := strings.Replace(string(rondo.ExampleConfig), "./certs/mirror-ca.pem", "./ca.pem", 1)
	i, j := strings.Index(s, "  downstreams:"), strings.Index(s, "secrets:")
	return s[:i] + "\n" + s[j:]
}()

func resetFlags() {
	flagConfig = "rondo.yaml"
	flagEnv = ""
	flagLogLevel = "info"
	flagDebug = false
	flagDryRun = false
	flagSkipDownload = false
	flagYes = false
	flagIacVersion = ""
	flagIacChannel = ""
	flagForce = false
}

func setupRepo(t *testing.T, manifest string) *config.Config {
	t.Helper()
	resetFlags()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "manifests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifests", "dependencies.yaml"), []byte("binaries: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "platform", "components"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "platform", "components", "probe.yaml"),
		[]byte("apiVersion: rondo.ouafeul.github.io/v1alpha1\nkind: Component\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), []byte("PEM"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rondo.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	flagDryRun = true
	flagConfig = "rondo.yaml"
	c, err := config.Load("rondo.yaml")
	if err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	return c
}

func TestRunDeployDryRun(t *testing.T) {
	c := setupRepo(t, manifestList)
	d, err := newDeps(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err := steps.Execute(context.Background(), d.deployPlan()); err != nil {
		t.Fatalf("deploy dry-run: %v", err)
	}
}

func TestRunDeployStandaloneDryRunOnUnfetchedVersion(t *testing.T) {
	resetFlags()
	work := t.TempDir()
	t.Chdir(work)
	t.Setenv("RONDO_HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(work, "ca.pem"), []byte("PEM"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "rondo.yaml"), []byte(manifestList), 0o600); err != nil {
		t.Fatal(err)
	}
	flagDryRun = true
	flagConfig = "rondo.yaml"
	flagIacVersion = "v1.0.0-rc.1"
	c, err := config.Load("rondo.yaml")
	if err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	d, err := newDeps(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err := steps.Execute(context.Background(), d.deployPlan()); err != nil {
		t.Fatalf("dry run of a version upgrade refused: %v\n"+
			"--dry-run downloads nothing: requiring the content of a bundle it chose "+
			"not to fetch defeats the only use of a dry run that matters", err)
	}
}

func TestDeployDownstreamsDryRun(t *testing.T) {
	c := setupRepo(t, manifestList)
	d, err := newDeps(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := selectDownstreams(c, nil)
	if err != nil || len(targets) != 2 {
		t.Fatalf("targets = %v, %v", targets, err)
	}
	if err := steps.Execute(context.Background(), d.downstreamsPlan(targets)); err != nil {
		t.Fatalf("deploy downstreams dry-run: %v", err)
	}
	if err := steps.Execute(context.Background(), d.destroyDownstreamsPlan(targets)); err != nil {
		t.Fatalf("destroy downstreams dry-run: %v", err)
	}
}

func TestSelectDownstreams(t *testing.T) {
	c := setupRepo(t, manifestNoDownstream)
	if _, err := selectDownstreams(c, nil); err == nil || !strings.Contains(err.Error(), "no rke2 target") {
		t.Errorf("without an rke2 target: %v", err)
	}
	c = setupRepo(t, manifestList)
	if got, err := selectDownstreams(c, []string{"beta"}); err != nil || len(got) != 1 || got[0].Name != "beta" {
		t.Errorf("selecting beta: %v, %v", got, err)
	}
	if _, err := selectDownstreams(c, []string{"management"}); err == nil || !strings.Contains(err.Error(), "unknown rke2 target") {
		t.Errorf("the management cluster is not an rke2 target: %v", err)
	}
}

func TestRunDestroyManagementDryRun(t *testing.T) {
	c := setupRepo(t, manifestList)
	d, err := newDeps(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err := steps.Execute(context.Background(), d.destroyPlan()); err != nil {
		t.Fatalf("destroy dry-run: %v", err)
	}
}

func TestEnsureToolsSkipDownload(t *testing.T) {
	c := setupRepo(t, manifestList)
	flagSkipDownload = true
	d, err := newDeps(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ensureTools(context.Background()); err != nil {
		t.Fatalf("ensureTools --skip-download: %v", err)
	}
}

func TestResolveCA(t *testing.T) {
	resetFlags()
	dir := t.TempDir()
	t.Chdir(dir)
	flagConfig = "rondo.yaml"

	c := &config.Config{Artifacts: config.Artifacts{CACertificateFile: "certs/ca.pem"}}
	got, err := resolveCA(c)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "certs", "ca.pem") {
		t.Errorf("resolveCA relative = %q", got)
	}

	abs := filepath.Join(dir, "abs.pem")
	c2 := &config.Config{Artifacts: config.Artifacts{CACertificateFile: abs}}
	if got, _ := resolveCA(c2); got != abs {
		t.Errorf("resolveCA absolute = %q", got)
	}

	// No CA file: the system trust store.
	if got, err := resolveCA(&config.Config{}); err != nil || got != "" {
		t.Errorf("resolveCA without a CA = %q, %v; want empty", got, err)
	}
}

func TestResolveConfigPathFallsBackToHome(t *testing.T) {
	resetFlags()
	work := t.TempDir()
	t.Chdir(work)
	home := t.TempDir()
	t.Setenv("RONDO_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "rondo.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	flagConfig = "rondo.yaml"
	resolveConfigPath()
	if flagConfig != filepath.Join(home, "rondo.yaml") {
		t.Errorf("resolveConfigPath did not fall back to RONDO_HOME: %q", flagConfig)
	}
}

func TestResolveConfigPathKeepsExplicit(t *testing.T) {
	resetFlags()
	flagConfig = "/explicit/rondo.yaml"
	resolveConfigPath()
	if flagConfig != "/explicit/rondo.yaml" {
		t.Errorf("explicit path changed: %q", flagConfig)
	}
}

func TestResolveConfigPathKeepsLocal(t *testing.T) {
	resetFlags()
	t.Setenv("RONDO_HOME", t.TempDir())
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "rondo.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	flagConfig = "rondo.yaml"
	resolveConfigPath()
	if flagConfig != "rondo.yaml" {
		t.Errorf("local rondo.yaml overwritten: %q", flagConfig)
	}
}

func TestConfirmYesFlag(t *testing.T) {
	resetFlags()
	flagYes = true
	if err := confirm("destroy the management cluster"); err != nil {
		t.Errorf("--yes should confirm: %v", err)
	}
}

func TestConfirmDryRun(t *testing.T) {
	resetFlags()
	flagDryRun = true
	if err := confirm("destroy the management cluster"); err != nil {
		t.Errorf("dry-run should confirm: %v", err)
	}
}

func TestConfirmAcceptsY(t *testing.T) {
	resetFlags()
	withStdin(t, "y\n")
	if err := confirm("test"); err != nil {
		t.Errorf("answer 'y' refused: %v", err)
	}
}

func TestConfirmRejectsOther(t *testing.T) {
	resetFlags()
	withStdin(t, "n\n")
	if err := confirm("test"); err == nil {
		t.Error("answer 'n' accepted")
	}
}

func withStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	w.Close()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig; r.Close() })
}

func TestCmdVersion(t *testing.T) {
	resetFlags()
	cmd := cmdVersion()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestCmdDeployDryRun(t *testing.T) {
	setupRepo(t, manifestList)
	if err := cmdDeploy().Execute(); err != nil {
		t.Fatalf("deploy: %v", err)
	}
}

func TestCmdDestroyDryRun(t *testing.T) {
	setupRepo(t, manifestList)
	if err := cmdDestroy().Execute(); err != nil {
		t.Fatalf("destroy: %v", err)
	}
}

func TestResolveWorkspaceStandaloneDryRun(t *testing.T) {
	resetFlags()
	work := t.TempDir()
	t.Chdir(work)
	home := t.TempDir()
	t.Setenv("RONDO_HOME", home)
	flagDryRun = true
	c := &config.Config{Artifacts: config.Artifacts{Preset: config.PresetArtifactory, ReleaseChannel: "stable",
		Artifactory: config.ArtifactoryMirror{URL: "https://arti.example.com/artifactory",
			Repositories: config.ArtifactoryRepositories{Generic: "rondo-generic-local"}}}}
	proj, bin, state, err := resolveWorkspace(context.Background(), c, "/ca.pem")
	if err != nil {
		t.Fatalf("resolveWorkspace standalone dry-run: %v", err)
	}
	if !strings.Contains(proj, filepath.Join(home, "cache")) {
		t.Errorf("projectDir = %q (want under cache)", proj)
	}
	if bin != filepath.Join(home, "bin") {
		t.Errorf("binDir = %q", bin)
	}
	if state != filepath.Join(home, "state") {
		t.Errorf("stateDir = %q", state)
	}
}

func TestIacChannelFlagOverridesTheManifest(t *testing.T) {
	resetFlags()
	withChannel := &config.Config{Artifacts: config.Artifacts{ReleaseChannel: "stable"}}
	empty := &config.Config{}

	if got := iacChannel(withChannel); got != "stable" {
		t.Errorf("without the flag, channel = %q, want the manifest's", got)
	}
	if got := iacChannel(empty); got != "" {
		t.Errorf("without flag or manifest, channel = %q (bundle applies `stable`)", got)
	}
	flagIacChannel = "edge-feat-example"
	if got := iacChannel(withChannel); got != "edge-feat-example" {
		t.Errorf("with the flag, channel = %q, the manifest wrongly wins", got)
	}
	if got := iacChannel(empty); got != "edge-feat-example" {
		t.Errorf("with the flag alone, channel = %q", got)
	}
}

func TestResolveWorkspaceStandaloneEdgeChannel(t *testing.T) {
	resetFlags()
	work := t.TempDir()
	t.Chdir(work)
	home := t.TempDir()
	t.Setenv("RONDO_HOME", home)
	flagDryRun = true
	flagIacChannel = "edge-feat-example"
	c := &config.Config{Artifacts: config.Artifacts{Preset: config.PresetArtifactory, ReleaseChannel: "stable",
		Artifactory: config.ArtifactoryMirror{URL: "https://arti.example.com/artifactory",
			Repositories: config.ArtifactoryRepositories{Generic: "rondo-generic-local"}}}}
	proj, _, _, err := resolveWorkspace(context.Background(), c, "/ca.pem")
	if err != nil {
		t.Fatalf("resolveWorkspace edge channel: %v", err)
	}
	if filepath.Base(proj) != "edge-feat-example" {
		t.Errorf("projectDir = %q, want the edge channel (did the manifest's `stable` win?)", proj)
	}
}

func TestResolveWorkspaceStandaloneSuccess(t *testing.T) {
	resetFlags()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "releases: []\n"
	tw.WriteHeader(&tar.Header{Name: "helmfile.yaml.gotmpl", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	sum := sha256.Sum256(buf.Bytes())
	archive := buf.Bytes()
	hexSum := hex.EncodeToString(sum[:])

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/stable.json"):
			w.Write([]byte(`{"version":"v1.0.0"}`))
		case strings.HasSuffix(r.URL.Path, "/iac.tar.gz.sha256"):
			w.Write([]byte(hexSum + "  iac.tar.gz\n"))
		case strings.HasSuffix(r.URL.Path, "/iac.tar.gz"):
			w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	work := t.TempDir()
	t.Chdir(work)
	home := t.TempDir()
	t.Setenv("RONDO_HOME", home)
	caPath := filepath.Join(work, "ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	flagDryRun = false
	c := &config.Config{Artifacts: config.Artifacts{Preset: config.PresetArtifactory, ReleaseChannel: "stable",
		Artifactory: config.ArtifactoryMirror{URL: srv.URL + "/artifactory",
			Repositories: config.ArtifactoryRepositories{Generic: "rondo-generic-local"}}}}
	proj, bin, state, err := resolveWorkspace(context.Background(), c, caPath)
	if err != nil {
		t.Fatalf("resolveWorkspace standalone success: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "helmfile.yaml.gotmpl")); err != nil {
		t.Errorf("bundle not extracted: %v", err)
	}
	if bin != filepath.Join(home, "bin") {
		t.Errorf("binDir = %q", bin)
	}
	if state != filepath.Join(home, "state") {
		t.Errorf("stateDir = %q", state)
	}
	if strings.HasPrefix(state, proj+string(filepath.Separator)) || state == proj {
		t.Errorf("stateDir %q under the versioned bundle %q", state, proj)
	}
}

func TestResolveWorkspaceStandaloneBadCA(t *testing.T) {
	resetFlags()
	work := t.TempDir()
	t.Chdir(work)
	flagDryRun = false
	c := &config.Config{Artifacts: config.Artifacts{Preset: config.PresetArtifactory,
		Artifactory: config.ArtifactoryMirror{URL: "https://arti.example.com/artifactory",
			Repositories: config.ArtifactoryRepositories{Generic: "rondo-generic-local"}}}}
	badCA := filepath.Join(work, "bad.pem")
	if err := os.WriteFile(badCA, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := resolveWorkspace(context.Background(), c, badCA); err == nil {
		t.Fatal("resolveWorkspace should fail on an invalid CA")
	}
}

func TestEnsureToolsBadCAFails(t *testing.T) {
	c := setupRepo(t, manifestList)
	flagDryRun = false
	flagSkipDownload = false
	d, err := newDeps(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ensureTools(context.Background()); err == nil {
		t.Error("ensureTools should fail on an invalid CA")
	}
}

func TestCmdDeployDownstreamsSubcommand(t *testing.T) {
	setupRepo(t, manifestList)
	cmd := cmdDeploy()
	cmd.SetArgs([]string{"downstreams", "alpha"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("deploy downstreams: %v", err)
	}
}

func TestMainHappyPath(t *testing.T) {
	resetFlags()
	origArgs := os.Args
	os.Args = []string{"rondo", "version"}
	t.Cleanup(func() { os.Args = origArgs })
	main()
}

func TestRunVersion(t *testing.T) {
	resetFlags()
	origArgs := os.Args
	os.Args = []string{"rondo", "version"}
	t.Cleanup(func() { os.Args = origArgs })
	if err := run(); err != nil {
		t.Fatalf("run version: %v", err)
	}
}

func TestRunUnknownCommandErrors(t *testing.T) {
	resetFlags()
	origArgs := os.Args
	os.Args = []string{"rondo", "commande-inexistante"}
	t.Cleanup(func() { os.Args = origArgs })
	if err := run(); err == nil {
		t.Fatal("an unknown command should return an error")
	}
}

func TestCmdInitNoOverwrite(t *testing.T) {
	resetFlags()
	home := t.TempDir()
	t.Setenv("RONDO_HOME", home)
	if err := cmdInit().Execute(); err != nil {
		t.Fatalf("first init: %v", err)
	}
	if err := cmdInit().Execute(); err == nil {
		t.Fatal("a second init without --force should refuse to overwrite")
	}
}

func TestCmdDestroyRefusedWithoutConfirmation(t *testing.T) {
	setupRepo(t, manifestList)
	flagDryRun = false
	withStdin(t, "n\n")
	if err := cmdDestroy().Execute(); err == nil {
		t.Fatal("a refused confirmation should cancel")
	}
}

func TestCmdInitWritesALoadableManifest(t *testing.T) {
	resetFlags()
	home := t.TempDir()
	t.Setenv("RONDO_HOME", home)
	flagEnv = "recette"
	if err := cmdInit().Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}
	c, err := config.Load(filepath.Join(paths.EnvDir("recette"), "rondo.yaml"))
	if err != nil {
		t.Fatalf("generated rondo.yaml invalid: %v", err)
	}
	if c.EnvironmentName != "recette" {
		t.Errorf("environmentName = %q", c.EnvironmentName)
	}
	if c.Mirror().Preset != config.PresetGeneric {
		t.Errorf("preset = %q, want generic by default", c.Mirror().Preset)
	}
}

func TestCmdInitWritesTheArtifactoryPreset(t *testing.T) {
	resetFlags()
	t.Setenv("RONDO_HOME", t.TempDir())
	flagEnv = "recette"
	cmd := cmdInit()
	cmd.SetArgs([]string{"--preset", "artifactory"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}
	c, err := config.Load(filepath.Join(paths.EnvDir("recette"), "rondo.yaml"))
	if err != nil {
		t.Fatalf("generated rondo.yaml invalid: %v", err)
	}
	m := c.Mirror()
	if !m.IsArtifactory() || m.OCIRepository != "artifactory.example.local/rondo-docker-local" {
		t.Errorf("mirror = %+v, want the preset artifactory", m)
	}
	if c.EnvironmentName != "recette" {
		t.Errorf("environmentName = %q", c.EnvironmentName)
	}
}

// Only the preset artifactory gets a token minter: any other mirror takes secrets.mirrorToken.
func TestMirrorTokensMintOnlyOnArtifactory(t *testing.T) {
	tokens, minter := mirrorTokens(config.Mirror{Preset: config.PresetGeneric, MirrorToken: "tok"}, "")
	if minter != nil {
		t.Errorf("minter %T on a generic mirror", minter)
	}
	if tok, err := tokens.Token(context.Background()); err != nil || tok != "tok" {
		t.Errorf("token = %q, %v; want the declared one", tok, err)
	}
	arti := config.Mirror{Preset: config.PresetArtifactory, ArtifactoryURL: "https://a",
		Username: "u", Password: "p", ProviderMirrorURL: "https://a/artifactory/files/providers/"}
	_, minter = mirrorTokens(arti, "")
	if _, ok := minter.(*steps.ArtifactoryClient); !ok {
		t.Errorf("minter %T on an Artifactory, want the Artifactory client", minter)
	}
	// secrets.mirrorToken set, an Artifactory neither mints nor revokes: the token is used as is.
	arti.MirrorToken = "tok-manual"
	tokens, minter = mirrorTokens(arti, "")
	if minter != nil {
		t.Errorf("minter %T on an Artifactory with secrets.mirrorToken", minter)
	}
	if tok, err := tokens.Token(context.Background()); err != nil || tok != "tok-manual" {
		t.Errorf("token = %q, %v; want secrets.mirrorToken", tok, err)
	}
}

func TestCmdInitRefusesAnUnknownPreset(t *testing.T) {
	resetFlags()
	home := t.TempDir()
	t.Setenv("RONDO_HOME", home)
	cmd := cmdInit()
	cmd.SetArgs([]string{"--preset", "nexus"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "nexus") {
		t.Fatalf("unknown preset: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "rondo.yaml")); err == nil {
		t.Error("a manifest was written for an unknown preset")
	}
}

func TestClustersFollowTheRancherToken(t *testing.T) {
	c := setupRepo(t, manifestList)
	d, err := newDeps(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	targets, _ := selectDownstreams(c, nil)
	plan := d.downstreamsPlan(targets)
	pos := map[string]int{}
	for i, s := range plan {
		pos[s.Name] = i
	}
	token, ok := pos["Rancher API token"]
	if !ok {
		t.Fatalf("step \"Rancher API token\" not found. Steps: %v", planNames(plan))
	}
	for _, name := range []string{"cluster alpha", "cluster beta"} {
		at, ok := pos[name]
		if !ok || at < token {
			t.Errorf("%s (%d) must follow the Rancher token (%d): the management cluster's CA, exported "+
				"with it, is passed to the nodes. Steps: %v", name, at, token, planNames(plan))
		}
	}
}

func planNames(plan []steps.Step) []string {
	out := make([]string, len(plan))
	for i, s := range plan {
		out[i] = s.Name
	}
	return out
}

func bundleServer(t *testing.T, version string) (*httptest.Server, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "releases: []\n"
	if err := tw.WriteHeader(&tar.Header{
		Name: "helmfile.yaml.gotmpl", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)
	hexSum := hex.EncodeToString(sum[:])

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/stable.json"):
			w.Write([]byte(`{"version":"` + version + `"}`))
		case strings.HasSuffix(r.URL.Path, "/iac.tar.gz.sha256"):
			w.Write([]byte(hexSum + "  iac.tar.gz\n"))
		case strings.HasSuffix(r.URL.Path, "/iac.tar.gz"):
			w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	caPath := filepath.Join(t.TempDir(), "ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return srv, caPath
}

func TestSiteStateIsCreatedWithRestrictedMode(t *testing.T) {
	resetFlags()
	srv, caPath := bundleServer(t, "v1.0.0")
	t.Chdir(t.TempDir())
	t.Setenv("RONDO_HOME", t.TempDir())
	flagDryRun = false
	c := &config.Config{Artifacts: config.Artifacts{Preset: config.PresetArtifactory, ReleaseChannel: "stable",
		Artifactory: config.ArtifactoryMirror{URL: srv.URL + "/artifactory",
			Repositories: config.ArtifactoryRepositories{Generic: "rondo-generic-local"}}}}
	_, _, state, err := resolveWorkspace(context.Background(), c, caPath)
	if err != nil {
		t.Fatalf("resolveWorkspace: %v", err)
	}
	info, err := os.Stat(state)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("state directory in mode %o: open to other users", info.Mode().Perm())
	}
}

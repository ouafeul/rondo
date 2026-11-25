package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rondo/internal/bootstrap"
	"rondo/internal/bundle"
	"rondo/internal/config"
	"rondo/internal/httpx"
	"rondo/internal/log"
	"rondo/internal/paths"
	"rondo/internal/runner"
	"rondo/internal/steps"
	"rondo/internal/synth"
	"rondo/internal/tools"
	"rondo/internal/version"
	"rondo/internal/versions"
)

const downloadTimeout = 10 * time.Minute

func resolveCA(c *config.Config) (string, error) {
	p := paths.ExpandHome(c.Artifactory.CACertificateFile)
	if p == "" {
		return "", fmt.Errorf("artifactory.caCertificateFile is empty")
	}
	if filepath.IsAbs(p) {
		return p, nil
	}
	cfgAbs, err := filepath.Abs(flagConfig)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(cfgAbs), p), nil
}

func resolveWorkspace(ctx context.Context, c *config.Config, caCert string) (projectDir, binDir, stateDir string, err error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", "", "", err
	}
	stateDir = paths.StateDir(c.EnvironmentName)
	if _, statErr := os.Stat(filepath.Join(wd, "manifests", "dependencies.yaml")); statErr == nil {
		if c.EnvironmentName == "" {
			stateDir = wd
		} else if !flagDryRun {
			err = os.MkdirAll(stateDir, 0o700)
		}
		return wd, filepath.Join(wd, ".local", "bin"), stateDir, err
	}
	log.Info("standalone mode: fetching the IaC bundle from Artifactory")
	if flagDryRun {
		return bundle.DryResolve(c, paths.CacheDir(), iacChannel(c), flagIacVersion),
			paths.BinDir(), stateDir, nil
	}
	if err := ensureSiteState(c.EnvironmentName); err != nil {
		return "", "", "", err
	}
	client, err := httpx.NewCAPinned(caCert, downloadTimeout)
	if err != nil {
		return "", "", "", err
	}
	dir, err := bundle.Resolve(ctx, client, c, paths.CacheDir(), iacChannel(c), flagIacVersion)
	if err != nil {
		return "", "", "", err
	}
	return dir, paths.BinDir(), stateDir, nil
}

func iacChannel(c *config.Config) string {
	if flagIacChannel != "" {
		return flagIacChannel
	}
	if c.Artifactory.RondoReleaseChannel != "" {
		return c.Artifactory.RondoReleaseChannel
	}
	return edgeChannelOfBinary(version.Version)
}

func edgeChannelOfBinary(v string) string {
	if !strings.HasPrefix(v, "edge-") {
		return ""
	}
	i := strings.LastIndexByte(v, '-')
	if i < len("edge-") {
		return ""
	}
	sha := v[i+1:]
	if len(sha) < 7 {
		return ""
	}
	for _, r := range sha {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return ""
		}
	}
	return v[:i]
}

func ensureSiteState(env string) error {
	return os.MkdirAll(paths.StateDir(env), 0o700)
}

type deps struct {
	cfg      *config.Config
	ws       steps.Workspace
	tofu     *steps.TofuStage
	mgmt     *steps.MgmtStage
	helm     *steps.HelmStage
	kube     *steps.KubeClient
	boot     *steps.RancherBootstrap
	platform *steps.PlatformStage
	env      map[string]string
}

func newDeps(ctx context.Context, c *config.Config) (*deps, error) {
	if flagEnv != "" && c.EnvironmentName != flagEnv {
		return nil, fmt.Errorf("%s declares environment %q, the selected environment is %q",
			flagConfig, c.EnvironmentName, flagEnv)
	}
	caCert, err := resolveCA(c)
	if err != nil {
		return nil, err
	}
	projectDir, binDir, stateDir, err := resolveWorkspace(ctx, c, caCert)
	if err != nil {
		return nil, err
	}
	env := c.Env()
	vEnv, err := versions.Env(filepath.Join(projectDir, "manifests", "dependencies.yaml"))
	if err != nil {
		log.Warn("versions: dependencies.yaml unreadable, defaults used", "err", err)
	} else {
		for k, v := range vEnv {
			env[k] = v
		}
	}
	env["PATH"] = binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	var run runner.CommandRunner = runner.NewExec(env)
	if flagDryRun {
		run = runner.DryRun{Real: run}
	}
	ws := steps.Workspace{ProjectDir: projectDir, StateDir: stateDir, BinDir: binDir, CACert: caCert}
	arti := &steps.ArtifactoryClient{
		URL: c.Artifactory.URL, User: c.Artifactory.Username,
		Password: c.Secrets.ArtifactoryPassword, CACertPath: caCert,
		DurableTokenTTL: c.Artifactory.MirrorTokenLifetimeSeconds,
	}
	tofu := steps.NewTofuStage(c, ws, run, arti, flagDryRun)
	tofu.Confirm = confirm
	kube := steps.NewKubeClient(ws, run)
	rancher := steps.NewRancherClient(c.RancherHostname())
	boot := steps.NewRancherBootstrap(c, ws, kube, rancher, flagDryRun)
	platform := steps.NewPlatformStage(c, ws, kube, arti, version.Version, flagDryRun)
	mgmt := steps.NewMgmtStage(c, ws, run, flagDryRun)
	mgmt.UseContext = c.EnvironmentName == currentEnv()
	return &deps{
		cfg:      c,
		ws:       ws,
		tofu:     tofu,
		mgmt:     mgmt,
		helm:     steps.NewHelmStage(c, ws, run),
		kube:     kube,
		boot:     boot,
		platform: platform,
		env:      env,
	}, nil
}

func (d *deps) renderBootstrap(_ context.Context) error {
	log.Step("bootstrap manifest rendering (PKI, MetalLB)")
	dir := filepath.Join(d.ws.ProjectDir, ".rondo-bootstrap")
	if flagDryRun {
		log.Info("DRY-RUN synth bootstrap", "dir", dir)
		return nil
	}
	written, err := synth.Synth(dir, bootstrap.All(d.cfg)...)
	if err != nil {
		return err
	}
	for _, p := range written {
		log.Info("bootstrap manifest rendered", "path", p)
	}
	return nil
}

func (d *deps) ensureTools(ctx context.Context) error {
	if flagSkipDownload {
		log.Info("--skip-download: tools assumed present")
		return nil
	}
	inst := &tools.Installer{
		BaseURL:   strings.TrimRight(d.cfg.Artifactory.URL, "/") + "/" + d.cfg.Artifactory.GenericRepository,
		User:      d.cfg.Artifactory.Username,
		Password:  d.cfg.Secrets.ArtifactoryPassword,
		BinDir:    d.ws.BinDir,
		PluginDir: d.ws.HelmPluginsDir(),
		Versions:  d.env,
	}
	if flagDryRun {
		return inst.Describe(ctx)
	}
	client, err := httpx.NewCAPinned(d.ws.CACert, downloadTimeout)
	if err != nil {
		return err
	}
	inst.HTTP = client
	return inst.Install(ctx)
}

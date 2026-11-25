package steps

import (
	"context"
	"fmt"
	"path/filepath"

	"rondo/internal/config"
	"rondo/internal/log"
	"rondo/internal/runner"
)

type HelmStage struct {
	cfg *config.Config
	ws  Workspace
	run runner.CommandRunner
}

func NewHelmStage(cfg *config.Config, ws Workspace, run runner.CommandRunner) *HelmStage {
	return &HelmStage{cfg: cfg, ws: ws, run: run}
}

func (h *HelmStage) env() map[string]string {
	return map[string]string{
		"KUBECONFIG":               h.ws.Kubeconfig(),
		"PROJECT_DIR":              h.ws.ProjectDir,
		"SSL_CERT_FILE":            h.ws.CACert,
		"ARTIFACTORY_CA_CERT_PATH": h.ws.CACert,
		"HELM_PLUGINS":             h.ws.HelmPluginsDir(),
	}
}

func (h *HelmStage) login(ctx context.Context, env map[string]string) error {
	host := h.cfg.ArtifactoryHost()
	if err := h.run.Run(ctx, runner.Cmd{
		Name: h.ws.Tool("helm"),
		Args: []string{"registry", "login", host, "--username", h.cfg.Artifactory.Username, "--password-stdin", "--ca-file", h.ws.CACert},
		Dir:  h.ws.ProjectDir, Env: env, Stdin: h.cfg.Secrets.ArtifactoryPassword,
	}); err != nil {
		return fmt.Errorf("Helm OCI login: %w", err)
	}
	return nil
}

func (h *HelmStage) helmfile(ctx context.Context, env map[string]string, subcmd string) error {
	return h.run.Run(ctx, runner.Cmd{
		Name: h.ws.Tool("helmfile"),
		Args: []string{"--file", filepath.Join(h.ws.ProjectDir, "helmfile.yaml"), subcmd},
		Dir:  h.ws.ProjectDir, Env: env,
	})
}

func (h *HelmStage) Deploy(ctx context.Context) error {
	log.Step("deploying Helm charts (helmfile sync)")
	env := h.env()
	if err := h.login(ctx, env); err != nil {
		return err
	}
	return h.helmfile(ctx, env, "sync")
}

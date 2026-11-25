package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"rondo/internal/config"
	"rondo/internal/log"
	"rondo/internal/runner"
	"rondo/internal/tofustate"
)

type TofuStage struct {
	cfg    *config.Config
	ws     Workspace
	run    runner.CommandRunner
	token  TokenSource
	dryRun bool

	// Confirm is asked before a plan that destroys resources is applied.
	Confirm func(question string) error

	cachedToken string
	regEnv      map[string]string
}

func NewTofuStage(cfg *config.Config, ws Workspace, run runner.CommandRunner, token TokenSource, dryRun bool) *TofuStage {
	return &TofuStage{cfg: cfg, ws: ws, run: run, token: token, dryRun: dryRun, regEnv: map[string]string{}}
}

func (t *TofuStage) tofuEnv(extra map[string]string) map[string]string {
	env := make(map[string]string, len(t.regEnv)+len(extra))
	for k, v := range t.regEnv {
		env[k] = v
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func (t *TofuStage) tofu(ctx context.Context, dir string, extra map[string]string, args ...string) error {
	return t.run.Run(ctx, runner.Cmd{Name: t.ws.Tool("tofu"), Args: args, Dir: dir, Env: t.tofuEnv(extra)})
}

func (t *TofuStage) tofuOutput(ctx context.Context, dir string, extra map[string]string, args ...string) (string, error) {
	return t.run.Output(ctx, runner.Cmd{Name: t.ws.Tool("tofu"), Args: args, Dir: dir, Env: t.tofuEnv(extra)})
}

func (t *TofuStage) ConfigureRegistry(ctx context.Context) error {
	log.Step("OpenTofu registry setup")
	m := t.cfg.Mirror()
	host := m.ProviderMirrorHost()
	// A network mirror takes a bearer token; an OCI mirror, the credentials in the .tofurc.
	if !t.dryRun && m.ProviderMirrorURL != "" {
		if t.cachedToken == "" {
			tok, err := t.token.Token(ctx)
			if err != nil {
				return err
			}
			t.cachedToken = tok
		}
		if t.cachedToken != "" {
			suffix := strings.NewReplacer("-", "__", ".", "_").Replace(host)
			t.regEnv["TF_TOKEN_"+suffix] = t.cachedToken
		}
	}
	if !t.dryRun {
		if err := os.WriteFile(t.ws.TofuRC(), []byte(m.TofuRC("")), 0o600); err != nil {
			return err
		}
	}
	t.regEnv["TF_CLI_CONFIG_FILE"] = t.ws.TofuRC()
	if t.ws.CACert != "" {
		t.regEnv["SSL_CERT_FILE"] = t.ws.CACert
	}
	switch m.StateBackend {
	case tofustate.S3:
		t.regEnv["AWS_ACCESS_KEY_ID"] = m.StateAccessKey
		t.regEnv["AWS_SECRET_ACCESS_KEY"] = m.StateSecretKey
	default:
		if !m.Anonymous() {
			t.regEnv["TF_HTTP_USERNAME"] = m.Username
			t.regEnv["TF_HTTP_PASSWORD"] = m.Password
		}
	}
	log.Info("OpenTofu registry configured", "host", host)
	return nil
}

// backendArgs select the state backend of the module in dir and return the `tofu init`
// arguments that configure it for stack: the http address, or the -backend-config file of the
// s3 bucket.
func (t *TofuStage) backendArgs(dir, stack string) ([]string, error) {
	m := t.cfg.Mirror()
	if !t.dryRun {
		if err := tofustate.Select(dir, m.StateBackend); err != nil {
			return nil, fmt.Errorf("state backend of %s: %w", dir, err)
		}
	}
	if m.StateBackend != tofustate.S3 {
		return []string{
			"-backend-config=address=" + m.StateURL(stack),
			"-backend-config=update_method=PUT",
		}, nil
	}
	path := filepath.Join(t.ws.StateDir, "tofu", stack+".s3.tfbackend")
	if !t.dryRun {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		body := m.StateS3.BackendConfig(m.StateS3.Key(stack))
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return nil, err
		}
	}
	return []string{"-backend-config=" + path}, nil
}

func (t *TofuStage) setCAVar() error {
	if t.dryRun {
		return nil
	}
	raw, err := t.ws.ReadCA()
	if err != nil {
		return fmt.Errorf("CA not found %s: %w", t.ws.CACert, err)
	}
	t.regEnv["TF_VAR_mirror_ca"] = string(raw)
	return nil
}

func (t *TofuStage) ApplyManagement(ctx context.Context) error {
	log.Step("k3s VM provisioning (tofu)")
	if err := t.setCAVar(); err != nil {
		return err
	}
	return t.planAndApply(ctx, t.ws.Stack("k3s"), nil, "k3s", "management")
}

func (t *TofuStage) DestroyManagement(ctx context.Context) error {
	log.Step("destroying management (tofu destroy)")
	if err := t.ConfigureRegistry(ctx); err != nil {
		return err
	}
	if err := t.setCAVar(); err != nil {
		return err
	}
	if err := t.destroy(ctx, t.ws.Stack("k3s"), nil, "k3s"); err != nil {
		return err
	}
	if !t.dryRun {
		os.Remove(t.ws.Kubeconfig())
	}
	return nil
}

// ApplyRKE2 provisions an rke2 target through Rancher and returns its API endpoint and kubeconfig.
func (t *TofuStage) ApplyRKE2(ctx context.Context, target *config.DownstreamTarget, rancherAccess, rancherSecret string) (
	server, kubeconfig string, err error) {
	log.Step("rke2 cluster (tofu)", "target", target.Name)
	env, err := t.rke2Env(target, rancherAccess, rancherSecret)
	if err != nil {
		return "", "", err
	}
	dir := t.ws.Stack("rke2")
	if err := t.planAndApply(ctx, dir, env, target.Name, target.Name); err != nil {
		return "", "", err
	}
	if t.dryRun {
		return "", "", nil
	}
	if kubeconfig, err = t.tofuOutput(ctx, dir, env, "output", "-raw", "kubeconfig"); err != nil {
		return "", "", fmt.Errorf("kubeconfig of %s: %w", target.Name, err)
	}
	if server, err = t.tofuOutput(ctx, dir, env, "output", "-raw", "api_endpoint"); err != nil {
		return "", "", fmt.Errorf("endpoint of %s: %w", target.Name, err)
	}
	return strings.TrimSpace(server), kubeconfig, nil
}

func (t *TofuStage) DestroyRKE2(ctx context.Context, target *config.DownstreamTarget, rancherAccess, rancherSecret string) error {
	log.Step("destroying the rke2 cluster (tofu destroy)", "target", target.Name)
	env, err := t.rke2Env(target, rancherAccess, rancherSecret)
	if err != nil {
		return err
	}
	return t.destroy(ctx, t.ws.Stack("rke2"), env, target.Name)
}

func (t *TofuStage) rke2Env(target *config.DownstreamTarget, rancherAccess, rancherSecret string) (map[string]string, error) {
	env := map[string]string{
		// One module directory, one state per target: each keeps its own .terraform.
		"TF_DATA_DIR":               filepath.Join(t.ws.StateDir, "tofu", target.Name),
		"TF_VAR_rancher_access_key": rancherAccess,
		"TF_VAR_rancher_secret_key": rancherSecret,
	}
	for k, v := range t.cfg.RKE2Vars(target) {
		env["TF_VAR_"+k] = v
	}
	if t.dryRun {
		return env, nil
	}
	arti, err := t.ws.ReadCA()
	if err != nil {
		return nil, fmt.Errorf("mirror CA %s: %w", t.ws.CACert, err)
	}
	cluster, err := os.ReadFile(t.ws.RancherCA()) //nolint:gosec // workspace path
	if err != nil {
		return nil, fmt.Errorf("management cluster CA (%s), exported with the Rancher token: %w",
			t.ws.RancherCA(), err)
	}
	env["TF_VAR_mirror_ca_pem"] = string(arti)
	env["TF_VAR_cluster_ca_pem"] = string(cluster)
	env["TF_VAR_rancher_ca_pem"] = string(cluster)
	return env, nil
}

// init initializes the module in dir on the state of stack.
func (t *TofuStage) init(ctx context.Context, dir string, env map[string]string, stack string) error {
	backend, err := t.backendArgs(dir, stack)
	if err != nil {
		return err
	}
	args := append([]string{"init", "-input=false", "-reconfigure"}, backend...)
	return t.tofu(ctx, dir, env, args...)
}

// planAndApply asks for confirmation before applying a plan that destroys anything.
func (t *TofuStage) planAndApply(ctx context.Context, dir string, env map[string]string, stack, what string) error {
	if err := t.init(ctx, dir, env, stack); err != nil {
		return err
	}
	plan := "tfplan-" + what
	if err := t.tofu(ctx, dir, env, "plan", "-input=false", "-out="+plan); err != nil {
		return err
	}
	if t.dryRun {
		return nil
	}
	js, err := t.tofuOutput(ctx, dir, env, "show", "-json", plan)
	if err != nil {
		return fmt.Errorf("reading the plan (tofu show -json): %w", err)
	}
	res, err := planSummary(js)
	if err != nil {
		return err
	}
	if !res.Changed {
		log.Info("infrastructure already up to date", "target", what)
		return nil
	}
	if res.Destructive() && t.Confirm != nil {
		if err := t.Confirm(fmt.Sprintf("The plan for %s DESTROYS %s. Apply",
			what, strings.Join(res.Destroys, ", "))); err != nil {
			return err
		}
	}
	return t.tofu(ctx, dir, env, "apply", "-input=false", plan)
}

func (t *TofuStage) destroy(ctx context.Context, dir string, env map[string]string, stack string) error {
	if err := t.init(ctx, dir, env, stack); err != nil {
		return err
	}
	return t.tofu(ctx, dir, env, "destroy", "-input=false", "-auto-approve")
}

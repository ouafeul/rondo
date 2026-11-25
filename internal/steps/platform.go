package steps

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"rondo/internal/bootstrap"
	"rondo/internal/config"
	"rondo/internal/kobj"
	"rondo/internal/log"
	"rondo/internal/synth"
)

type PlatformStage struct {
	cfg     *config.Config
	ws      Workspace
	kube    *KubeClient
	arti    MirrorTokenMinter
	dryRun  bool
	version string
	Out     io.Writer
}

type MirrorTokenMinter interface {
	MintDurableToken(ctx context.Context) (string, error)
	RevokeToken(ctx context.Context, token string) error
}

func NewPlatformStage(cfg *config.Config, ws Workspace, kube *KubeClient,
	arti MirrorTokenMinter, version string, dryRun bool) *PlatformStage {
	return &PlatformStage{cfg: cfg, ws: ws, kube: kube, arti: arti, version: version, dryRun: dryRun}
}

func (p *PlatformStage) Install(ctx context.Context) error {
	log.Step("installing the operator", "version", p.version)
	if err := p.applyCRDs(ctx); err != nil {
		return err
	}
	objs := bootstrap.PlatformInstallFor(p.cfg, p.version).Objects()
	if p.dryRun {
		log.Info("DRY-RUN platform manifests", "objects", len(objs))
		return nil
	}
	for _, obj := range objs {
		if err := p.applyObject(ctx, obj); err != nil {
			return fmt.Errorf("manifest %s: %w", describeObject(obj), err)
		}
	}
	log.Info("platform applied", "objects", len(objs))
	return nil
}

func (p *PlatformStage) WaitReady(ctx context.Context) error {
	if p.dryRun {
		log.Info("DRY-RUN wait for the operator to start",
			"deployments", strings.Join(p.platformDeployments(), ", "))
		return nil
	}
	return p.waitRollout(ctx)
}

func (p *PlatformStage) platformDeployments() []string {
	return []string{kobj.ControllerName, kobj.ProbeName, kobj.DashboardName}
}

const rolloutTimeout = 10 * time.Minute

func (p *PlatformStage) waitRollout(ctx context.Context) error {
	deploys := p.platformDeployments()
	log.Info("waiting for the operator's services to start; on the first deployment "+
		"each pod pulls its image from Artifactory, which can take several minutes",
		"deployments", strings.Join(deploys, ", "), "timeout", rolloutTimeout.String())
	for _, name := range deploys {
		log.Info("waiting for the deployment", "deployment", name)
		if err := p.kube.Run(ctx, p.ws.Kubeconfig(), "rollout", "status",
			"deployment/"+name, "-n", kobj.PlatformNamespace,
			"--timeout="+rolloutTimeout.String()); err != nil {
			return fmt.Errorf("operator service %s not available after %s "+
				"(image %s pullable from the cluster? `kubectl -n %s describe pod -l "+
				"app.kubernetes.io/name=%s` will tell): %w",
				name, rolloutTimeout, p.version, kobj.PlatformNamespace, name, err)
		}
	}
	log.Info("operator started", "deployments", len(deploys))
	return nil
}

func (p *PlatformStage) applyCRDs(ctx context.Context) error {
	dir := filepath.Join(p.ws.ProjectDir, "manifests", "crds")
	entries, err := os.ReadDir(dir)
	if p.dryRun {
		if err != nil {
			log.Warn("DRY-RUN: CRDs not found, a real deploy would fail here", "dir", dir)
			return nil
		}
		log.Info("DRY-RUN applying the CRDs", "dir", dir, "files", len(entries))
		return nil
	}
	if err != nil {
		return fmt.Errorf("platform CRDs not found (%s): incomplete bundle? %w", dir, err)
	}
	if err := p.kube.ApplyDir(ctx, p.ws.Kubeconfig(), dir); err != nil {
		return fmt.Errorf("applying the CRDs: %w", err)
	}
	for _, e := range entries {
		name := crdNameFromFile(e.Name())
		if name == "" {
			continue
		}
		if err := p.kube.Run(ctx, p.ws.Kubeconfig(), "wait", "--for=condition=Established",
			"--timeout=60s", "crd/"+name); err != nil {
			return fmt.Errorf("CRD %s not established: %w", name, err)
		}
	}
	return nil
}

func crdNameFromFile(file string) string {
	base := strings.TrimSuffix(file, ".yaml")
	if base == file {
		return ""
	}
	group, plural, ok := strings.Cut(base, "_")
	if !ok {
		return ""
	}
	return plural + "." + group
}

func (p *PlatformStage) ApplyCredentials(ctx context.Context) error {
	log.Step("platform credentials")
	if p.dryRun {
		log.Info("DRY-RUN Secret " + kobj.CredentialsSecret)
		return nil
	}
	creds := kobj.ArtifactoryCredentials(p.cfg.Artifactory.Username, p.cfg.Secrets.ArtifactoryPassword)
	if err := p.applyObject(ctx, creds); err != nil {
		return fmt.Errorf("secret %s: %w", kobj.CredentialsSecret, err)
	}

	ca, err := os.ReadFile(p.ws.CACert) //nolint:gosec // config path, not user input
	if err != nil {
		return fmt.Errorf("reading the Artifactory CA (%s): %w", p.ws.CACert, err)
	}
	if err := p.applyObject(ctx, kobj.ArtifactoryCA(string(ca))); err != nil {
		return fmt.Errorf("configmap %s: %w", kobj.ArtifactoryCACM, err)
	}
	return nil
}

func (p *PlatformStage) ApplyMirrorToken(ctx context.Context) error {
	log.Step("network_mirror token")
	if p.dryRun {
		log.Info("DRY-RUN Secret " + kobj.TofurcSecret)
		return nil
	}
	override := strings.TrimSpace(p.cfg.Secrets.ArtifactoryMirrorToken)
	prior := p.priorMirrorToken(ctx)
	token := override
	minted := false
	if token == "" {
		if p.arti == nil {
			return fmt.Errorf("no token minter and artifactory.mirrorToken is empty")
		}
		t, err := p.arti.MintDurableToken(ctx)
		if err != nil {
			return fmt.Errorf("mirror token: %w", err)
		}
		token, minted = t, true
	}

	mirror := strings.TrimRight(p.cfg.Artifactory.URL, "/") + "/api/terraform/rondo-tofu-local/providers/"
	secret := kobj.TofuMirror(mirror, p.cfg.ArtifactoryHost(), token, override == "")
	if err := p.applyObject(ctx, secret); err != nil {
		if minted {
			if rerr := p.arti.RevokeToken(ctx, token); rerr != nil {
				log.Warn("revoking the orphan token failed", "err", rerr)
			}
		}
		return fmt.Errorf("secret %s: %w", kobj.TofurcSecret, err)
	}
	if prior != "" && prior != token {
		if err := p.arti.RevokeToken(ctx, prior); err != nil {
			log.Warn("revoking the previous mirror token failed (non-blocking)", "err", err)
		}
	}
	return nil
}

func (p *PlatformStage) priorMirrorToken(ctx context.Context) string {
	b64, err := p.kube.Output(ctx, p.ws.Kubeconfig(), "get", "secret", kobj.TofurcSecret,
		"-n", kobj.PlatformNamespace, "-o", dataPath(kobj.TofurcTokenKey))
	if err != nil {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func (p *PlatformStage) ApplyCoreDNSHosts(ctx context.Context) error {
	override := coreDNSOverride(p.cfg)
	log.Step("in-cluster DNS resolution of the ingress hostnames")
	if p.dryRun {
		log.Info("DRY-RUN ConfigMap coredns-custom")
		return nil
	}

	current, _ := p.kube.Output(ctx, p.ws.Kubeconfig(), "get", "configmap", kobj.CoreDNSConfigMap,
		"-n", kobj.CoreDNSNamespace, "-o", dataPath(kobj.CoreDNSOverrideKey))
	if err := p.applyObject(ctx, kobj.CoreDNSCustom(override)); err != nil {
		return fmt.Errorf("coredns-custom (ingress hostnames): %w", err)
	}
	if strings.TrimSpace(current) == strings.TrimSpace(override) {
		return nil
	}
	log.Info("ingress hostnames resolved in-cluster, restarting CoreDNS")
	if err := p.kube.Run(ctx, p.ws.Kubeconfig(), "-n", kobj.CoreDNSNamespace,
		"rollout", "restart", "deployment", kobj.CoreDNSDeployment); err != nil {
		log.Warn("restarting CoreDNS failed (non-blocking), "+
			"run again if resolution fails", "err", err)
	}
	return nil
}

// coreDNSOverride resolves, from management pods, rancher. and rondo. to the management
// ingress, and <anything>.<name or role>.<base> to the ingress of that rke2 target.
func coreDNSOverride(c *config.Config) string {
	base := strings.ReplaceAll(c.IngressBaseDomain, ".", "[.]")
	out := kobj.CoreDNSTemplate("^(rancher|rondo)[.]"+base+"[.]$", c.Targets.Management.IngressLoadBalancerIP)
	for _, t := range c.Targets.Downstreams {
		names := append([]string{t.Name}, t.Roles...)
		out += kobj.CoreDNSTemplate("^.+[.]("+strings.Join(names, "|")+")[.]"+base+"[.]$",
			t.RKE2.IngressLoadBalancerIP)
	}
	return out
}

func (p *PlatformStage) applyObject(ctx context.Context, obj map[string]any) error {
	raw, err := yaml.Marshal(obj)
	if err != nil {
		return err
	}
	return p.kube.ApplyStdin(ctx, p.ws.Kubeconfig(), string(raw))
}

// dataPath is the kubectl jsonpath of one data key, its dots escaped.
func dataPath(key string) string {
	return `jsonpath={.data.` + strings.ReplaceAll(key, ".", `\.`) + `}`
}

func describeObject(obj map[string]any) string {
	kind, _ := obj["kind"].(string)
	meta, _ := obj["metadata"].(map[string]any)
	if meta == nil {
		if m, ok := obj["metadata"].(synth.Object); ok {
			meta = m
		}
	}
	name, _ := meta["name"].(string)
	return kind + "/" + name
}

package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"rondo/internal/config"
	"rondo/internal/kobj"
	"rondo/internal/log"
	"rondo/internal/runner"
)

// ApplySite projects what rondo.yaml declares for the management cluster: its own Target, the
// vSphere Targets, and the Secrets components read.
func (p *PlatformStage) ApplySite(ctx context.Context) error {
	log.Step("site values and Targets")
	if p.dryRun {
		log.Info("DRY-RUN management and vSphere Targets, site Secrets")
		return nil
	}
	artiCA, err := p.artifactoryCA()
	if err != nil {
		return err
	}
	values := kubernetesValues(p.cfg.Targets.Management.IngressLoadBalancerIP,
		p.cfg.Mirror().OCIRepository, artiCA)
	management := config.Placement{Name: config.ManagementRole, Roles: []string{config.ManagementRole}}
	if err := p.applyTarget(ctx, management, kobj.InClusterBackend(), values); err != nil {
		return err
	}
	for i := range p.cfg.Targets.VSphere {
		if err := p.applyVSphereTarget(ctx, &p.cfg.Targets.VSphere[i], artiCA); err != nil {
			return err
		}
	}
	if err := p.applyPlatformValues(ctx); err != nil {
		return err
	}
	if err := p.applyComponentSecrets(ctx); err != nil {
		return err
	}
	return p.ensureVMSSHKey(ctx)
}

// ApplyRKE2Target registers a provisioned rke2 cluster as a Target reached through its kubeconfig.
func (p *PlatformStage) ApplyRKE2Target(ctx context.Context, t *config.DownstreamTarget, server, kubeconfig string) error {
	artiCA, err := p.artifactoryCA()
	if err != nil {
		return err
	}
	if err := p.applyObject(ctx, kobj.KubeconfigSecret(t.Name, kubeconfig)); err != nil {
		return fmt.Errorf("kubeconfig of %s: %w", t.Name, err)
	}
	values := kubernetesValues(t.RKE2.IngressLoadBalancerIP, p.cfg.RegistryHostFor(&t.RKE2), artiCA)
	return p.applyTarget(ctx, t.Placement,
		kobj.KubeconfigBackend(server, kobj.KubeconfigSecretName(t.Name)), values)
}

func kubernetesValues(ingressIP, imageRegistry, artiCA string) map[string]string {
	return map[string]string{
		"ingressIP":     ingressIP,
		"imageRegistry": imageRegistry,
		"artifactoryCA": artiCA,
		"storageClass":  kobj.DefaultStorageClass,
	}
}

func (p *PlatformStage) applyVSphereTarget(ctx context.Context, t *config.VSphereTarget, artiCA string) error {
	name := kobj.VSphereObjectName(t.Name)
	creds := kobj.VSphereCredentials(t.Name, t.VCenterUsername,
		p.cfg.Secrets.VCenterPasswords[t.Name])
	if err := p.applyObject(ctx, creds); err != nil {
		return fmt.Errorf("secret %s: %w", name, err)
	}
	settings := p.cfg.VSphereVars(t)
	delete(settings, kobj.VSphereUserKey)
	delete(settings, kobj.VSpherePasswordKey)
	settings["artifactory_ca_pem"] = artiCA
	if err := p.applyObject(ctx, kobj.VSphereSettings(t.Name, settings)); err != nil {
		return fmt.Errorf("configmap %s: %w", name, err)
	}
	values := map[string]string{"vmTemplate": t.VMTemplate, "sshUser": kobj.VMSSHUser}
	return p.applyTarget(ctx, t.Placement, kobj.VSphereBackend(name), values)
}

// applyTarget applies the Target of t; its componentValues, set in the manifest, win over values.
func (p *PlatformStage) applyTarget(ctx context.Context, t config.Placement, backend kobj.Object,
	values map[string]string) error {
	for k, v := range t.ComponentValues {
		values[k] = v
	}
	if err := p.applyObject(ctx, kobj.Target(t.Name, t.Roles, backend, values)); err != nil {
		return fmt.Errorf("Target %s: %w", t.Name, err)
	}
	log.Info("Target set", "target", t.Name, "roles", strings.Join(t.Roles, ","))
	return nil
}

func (p *PlatformStage) artifactoryCA() (string, error) {
	pem, err := os.ReadFile(p.ws.CACert) //nolint:gosec // config path
	if err != nil {
		return "", fmt.Errorf("reading the Artifactory CA (%s): %w", p.ws.CACert, err)
	}
	return string(pem), nil
}

func (p *PlatformStage) applyPlatformValues(ctx context.Context) error {
	authURL, err := p.cfg.Mirror().FilesAuthURL()
	if err != nil {
		return err
	}
	secret := kobj.OpaqueSecret(kobj.PlatformValuesSecret,
		platformValues(p.cfg, authURL))
	if err := p.applyObject(ctx, secret); err != nil {
		return fmt.Errorf("secret %s: %w", kobj.PlatformValuesSecret, err)
	}
	return nil
}

func platformValues(c *config.Config, authURL string) map[string]string {
	return map[string]string{
		"artifactoryUser":     c.Mirror().Username,
		"artifactoryPassword": c.Mirror().Password,
		"artifactoryAuthURL":  authURL,
	}
}

func (p *PlatformStage) applyComponentSecrets(ctx context.Context) error {
	names := make([]string, 0, len(p.cfg.Secrets.Components))
	for k := range p.cfg.Secrets.Components {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		data := p.cfg.Secrets.Components[name]
		if err := p.applyObject(ctx, kobj.OpaqueSecret(name, data)); err != nil {
			return fmt.Errorf("component secret %s: %w", name, err)
		}
		log.Info("component secret set", "secret", name, "keys", len(data))
	}
	return nil
}

func (p *PlatformStage) ensureVMSSHKey(ctx context.Context) error {
	out, err := p.kube.Output(ctx, p.ws.Kubeconfig(), "get", "secret", kobj.VMSSHSecret,
		"-n", kobj.PlatformNamespace, "-o", dataPath(kobj.VMSSHPublicKey))
	if err == nil && strings.TrimSpace(out) != "" {
		log.Info("machine access keys already in place, no rotation",
			"secret", kobj.VMSSHSecret)
		return nil
	}

	dir, err := os.MkdirTemp("", "rondo-vm-key-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	keyPath := filepath.Join(dir, "id_ed25519")

	if err := p.kube.run().Run(ctx, runner.Cmd{
		Name: "ssh-keygen",
		Args: []string{"-t", "ed25519", "-N", "", "-C", "rondo-vm@" + kobj.PlatformNamespace,
			"-f", keyPath},
		Dir: dir,
	}); err != nil {
		return fmt.Errorf("generating the machine access key pair: %w", err)
	}
	priv, err := os.ReadFile(keyPath) //nolint:gosec // controlled temp path
	if err != nil {
		return err
	}
	pub, err := os.ReadFile(keyPath + ".pub") //nolint:gosec // same
	if err != nil {
		return err
	}
	secret := kobj.VMSSHKey(string(priv), strings.TrimSpace(string(pub)))
	if err := p.applyObject(ctx, secret); err != nil {
		return fmt.Errorf("secret %s: %w", kobj.VMSSHSecret, err)
	}
	log.Info("machine access key pair generated", "secret", kobj.VMSSHSecret)
	return nil
}

package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"rondo/api/v1alpha1"
	"rondo/internal/log"
	"rondo/internal/runner"
)

func VMInstall(ctx context.Context, rt *Runtime) error {
	rv, err := rt.loadVM(ctx)
	if err != nil {
		return err
	}
	if rv.vm.Installer == nil && rv.vm.Ansible == nil {
		log.Info("no installer or Ansible content declared, nothing to install",
			"component", rv.component.Name)
		return nil
	}
	logStep("installing on machines", "component", rv.component.Name,
		"target", rv.target.Name, "groups", len(rv.vm.Groups))

	inventory, err := rt.machineInventory(ctx, rv)
	if err != nil {
		return err
	}
	dir, err := rt.fetchAnsibleContent(ctx, rv)
	if err != nil {
		return err
	}
	keyPath, err := rt.materializeSSHKey(ctx)
	if err != nil {
		return err
	}

	user, password, err := artifactoryCreds()
	if err != nil {
		return err
	}
	secrets, err := rt.installerSecrets(ctx, rv)
	if err != nil {
		return err
	}
	vars, err := BuildAnsibleVars(rv.component, rv.target,
		os.Getenv(EnvArtifactoryURL), user, password, secrets,
		rv.release.Spec.Policy.TimeoutOr(0))
	if err != nil {
		return err
	}

	env := rt.ansibleRunEnv(keyPath)
	if u := rv.target.Spec.Values[v1alpha1.ValueSSHUser]; u != "" {
		env["ANSIBLE_REMOTE_USER"] = u
	}

	for _, g := range rv.vm.Groups {
		if g.Replicas == 0 {
			continue
		}
		cfg, cErr := GroupConfig(rv.vm, g.Name)
		if cErr != nil {
			return cErr
		}
		vars.Config = cfg
		log.Info("installing group", "group", g.Name,
			"config keys", len(sortedConfigKeys(cfg)))

		varsPath, vErr := rt.writeVarsFile(g.Name, vars)
		if vErr != nil {
			return vErr
		}
		args := []string{
			"-i", inventory,
			"--limit", g.Name,
			"-e", "@" + varsPath,
			"-e", "rondo_group=" + g.Name,
			playbookOf(rv),
		}
		if err := rt.Run.Run(ctx, runner.Cmd{
			Name: "ansible-playbook", Args: args, Dir: dir, Env: env,
		}); err != nil {
			return fmt.Errorf("installing group %q: %w", g.Name, err)
		}
		log.Info("group installed", "group", g.Name)
	}
	log.Info("install complete", "component", rv.component.Name)
	return nil
}

func playbookOf(rv *releaseVM) string {
	if rv.vm.Ansible != nil {
		playbook := rv.vm.Ansible.Playbook
		if playbook == "" {
			playbook = "site.yml"
		}
		return playbook
	}
	return "site.yml"
}

func (rt *Runtime) machineInventory(ctx context.Context, rv *releaseVM) (string, error) {
	ref := rv.release.Status.MachinesSecretRef
	if ref == nil {
		return "", fmt.Errorf("no machines Secret: provisioning must have " +
			"succeeded before install")
	}
	var s corev1.Secret
	if err := rt.Client.Get(ctx, types.NamespacedName{
		Namespace: rv.release.Namespace, Name: ref.Name,
	}, &s); err != nil {
		return "", fmt.Errorf("reading machines Secret %s: %w", ref.Name, err)
	}
	raw, ok := s.Data["ansible_inventory"]
	if !ok || len(strings.TrimSpace(string(raw))) == 0 {
		return "", fmt.Errorf("inventory missing or empty in Secret %s: the playbook "+
			"would configure no machine", ref.Name)
	}
	return rt.writeFile("inventory.ini", raw)
}

func (rt *Runtime) fetchAnsibleContent(ctx context.Context, rv *releaseVM) (string, error) {
	url := os.Getenv(EnvAnsibleContentURL)
	if a := rv.vm.Ansible; a != nil {
		base := strings.TrimRight(os.Getenv(EnvArtifactoryURL), "/")
		url = base + "/" + strings.Trim(a.Repo, "/") + "/" + strings.TrimLeft(a.Path, "/")
		log.Info("component Ansible content", "version", a.Version)
	}
	if url == "" {
		return "", fmt.Errorf("%s required (first-party Ansible content tarball)", EnvAnsibleContentURL)
	}
	dir := filepath.Join(rt.dir(), "ansible-content")
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	archive := filepath.Join(rt.dir(), "ansible-content.tar.gz")
	if err := downloadArchive(ctx, url, archive); err != nil {
		return "", fmt.Errorf("Ansible content %s: %w", url, err)
	}
	if err := rt.Run.Run(ctx, runner.Cmd{
		Name: "tar", Args: []string{"xzf", archive, "-C", dir}, Dir: rt.dir(),
	}); err != nil {
		return "", fmt.Errorf("extracting Ansible content: %w", err)
	}
	return dir, nil
}

func (rt *Runtime) materializeSSHKey(ctx context.Context) (string, error) {
	_, priv, err := rt.sshKeys(ctx)
	if err != nil {
		return "", err
	}
	return rt.writeFile("vm_id_ed25519", []byte(priv))
}

func (rt *Runtime) installerSecrets(ctx context.Context, rv *releaseVM) (map[string]string, error) {
	inst := rv.vm.Installer
	if inst == nil || len(inst.SecretsFrom) == 0 {
		return map[string]string{}, nil
	}
	out := map[string]string{}
	for _, src := range inst.SecretsFrom {
		data, err := rt.readValuesSource(ctx, src)
		if err != nil {
			return nil, err
		}
		for k, v := range data {
			out[k] = v
		}
	}
	log.Info("installer secrets read", "keys", len(out))
	return out, nil
}

func (rt *Runtime) writeVarsFile(group string, vars *AnsibleVars) (string, error) {
	raw, err := json.Marshal(vars)
	if err != nil {
		return "", fmt.Errorf("serializing Ansible variables: %w", err)
	}
	return rt.writeFile("ansible-vars-"+group+".json", raw)
}

func (rt *Runtime) ansibleRunEnv(keyPath string) map[string]string {
	return map[string]string{
		"ANSIBLE_FORCE_COLOR":         "0",
		"ANSIBLE_NOCOLOR":             "1",
		"ANSIBLE_LOCAL_TEMP":          filepath.Join(rt.dir(), ".ansible", "tmp"),
		"ANSIBLE_RETRY_FILES_ENABLED": "0",
		"ANSIBLE_PRIVATE_KEY_FILE":    keyPath,
		"ANSIBLE_HOST_KEY_CHECKING":   "False",
		"HOME":                        rt.dir(),
	}
}

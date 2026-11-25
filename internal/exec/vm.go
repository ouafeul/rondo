package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"rondo/api/v1alpha1"
	"rondo/internal/kobj"
	"rondo/internal/log"
	"rondo/internal/runner"
	"rondo/internal/tofustate"
	"rondo/internal/values"
)

const (
	EnvVMModuleURL       = "RONDO_VM_MODULE_URL"
	EnvAnsibleContentURL = "RONDO_ANSIBLE_CONTENT_URL"
	EnvArtifactoryURL    = "ARTIFACTORY_URL"
	EnvSSHKeySecret      = "RONDO_VM_SSH_SECRET"
)

func init() {
	Verbs["vm-apply"] = VMApply
	Verbs["vm-plan"] = VMPlan
	Verbs["vm-destroy"] = VMDestroy
	Verbs["vm-install"] = VMInstall
}

type releaseVM struct {
	release   *v1alpha1.Enrollment
	component *v1alpha1.Component
	target    *v1alpha1.Target
	vm        *v1alpha1.VMSpec
}

func (rt *Runtime) loadVM(ctx context.Context) (*releaseVM, error) {
	enr, comp, target, err := rt.loadEnrollment(ctx)
	if err != nil {
		return nil, err
	}
	if comp.Spec.VM == nil {
		return nil, fmt.Errorf("Component %s is not a machine component "+
			"(spec.vm missing): VM verb called by mistake", comp.Name)
	}
	if target.Spec.VSphere == nil {
		return nil, fmt.Errorf("Target %s is not a vsphere Target: a machine "+
			"component requires a vsphere Target", target.Name)
	}
	vm, err := values.ResolveVM(comp.Spec.VM, values.TargetVars(target))
	if err != nil {
		return nil, fmt.Errorf("Component %s on target %s: %w", comp.Name, target.Name, err)
	}
	comp.Spec.VM = vm
	return &releaseVM{release: enr, component: comp, target: target, vm: vm}, nil
}

func VMPlan(ctx context.Context, rt *Runtime) error { return rt.vmTofu(ctx, "plan") }

func VMApply(ctx context.Context, rt *Runtime) error { return rt.vmTofu(ctx, "apply") }

func VMDestroy(ctx context.Context, rt *Runtime) error { return rt.vmTofu(ctx, "destroy") }

func (rt *Runtime) vmTofu(ctx context.Context, action string) error {
	rv, err := rt.loadVM(ctx)
	if err != nil {
		return err
	}
	logStep("machines: "+action, "component", rv.component.Name,
		"target", rv.target.Name, "groups", len(rv.vm.Groups))

	dir, err := rt.fetchVMModule(ctx)
	if err != nil {
		return err
	}
	env, err := rt.vmTofuEnv(ctx, rv)
	if err != nil {
		return err
	}
	if err := tofustate.Select(dir, stateBackend()); err != nil {
		return fmt.Errorf("state backend of the machine module: %w", err)
	}
	sc := &tofuWorkdir{dir: dir, env: env}
	if err := sc.initModule(ctx, rt); err != nil {
		return err
	}

	switch action {
	case "plan":
		out, perr := sc.tofuOutput(ctx, rt, "plan", "-input=false", "-no-color")
		if perr != nil {
			return fmt.Errorf("tofu plan (machines): %w", perr)
		}
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			log.StreamLine(line)
		}
		return nil
	case "destroy":
		if err := sc.tofu(ctx, rt, "destroy", "-input=false", "-no-color", "-auto-approve"); err != nil {
			return fmt.Errorf("tofu destroy (machines): %w", err)
		}
		return nil
	default:
		if err := sc.tofu(ctx, rt, "apply", "-input=false", "-no-color", "-auto-approve"); err != nil {
			return fmt.Errorf("tofu apply (machines): %w", err)
		}
		if err := rt.publishMachines(ctx, rv, sc); err != nil {
			return fmt.Errorf("publishing inventory (the apply itself succeeded): %w", err)
		}
		return nil
	}
}

func (rt *Runtime) fetchVMModule(ctx context.Context) (string, error) {
	url := os.Getenv(EnvVMModuleURL)
	if url == "" {
		return "", fmt.Errorf("%s required (terraform/vm module tarball)", EnvVMModuleURL)
	}
	dir := filepath.Join(rt.dir(), "vm-module")
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	archive := filepath.Join(rt.dir(), "vm-module.tar.gz")
	if err := downloadArchive(ctx, url, archive); err != nil {
		return "", fmt.Errorf("machine module %s: %w", url, err)
	}
	if err := rt.Run.Run(ctx, runner.Cmd{
		Name: "tar", Args: []string{"xzf", archive, "-C", dir}, Dir: rt.dir(),
	}); err != nil {
		return "", fmt.Errorf("extracting machine module: %w", err)
	}
	return dir, nil
}

func (rt *Runtime) vmTofuEnv(ctx context.Context, rv *releaseVM) (map[string]string, error) {
	env := map[string]string{
		"TF_INPUT":         "0",
		"TF_IN_AUTOMATION": "1",
		"TF_DATA_DIR":      filepath.Join(rt.dir(), ".tofu-vm"),
	}

	groups, err := VMGroupVars(rv.vm, rv.component.Name, rv.target)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(groups)
	if err != nil {
		return nil, err
	}
	env["TF_VAR_groups"] = string(raw)
	env["TF_VAR_component"] = rv.component.Name

	template := rv.vm.Template
	if template == "" {
		template = rv.target.Spec.Values[v1alpha1.ValueVMTemplate]
	}
	if template == "" {
		return nil, fmt.Errorf("no machine template: set spec.vm.template "+
			"or value %q of Target %s", v1alpha1.ValueVMTemplate, rv.target.Name)
	}
	env["TF_VAR_vsphere_template"] = template

	place := rv.target.Spec.VSphere
	if place == nil {
		return nil, fmt.Errorf("Target %s has no vsphere block", rv.target.Name)
	}
	if place.SettingsConfigMapRef != nil {
		settings, serr := rt.readEnumerated(ctx, place.SettingsConfigMapRef.Name, false, vmSettingKeys)
		if serr != nil {
			return nil, serr
		}
		for k, v := range settings {
			env["TF_VAR_"+k] = v
		}
	}
	if place.CredentialsSecretRef != nil {
		creds, cerr := rt.readEnumerated(ctx, place.CredentialsSecretRef.Name, true,
			[]string{kobj.VSphereUserKey, kobj.VSpherePasswordKey})
		if cerr != nil {
			return nil, cerr
		}
		for k, v := range creds {
			env["TF_VAR_"+k] = v
		}
	}

	pub, _, err := rt.sshKeys(ctx)
	if err != nil {
		return nil, err
	}
	env["TF_VAR_ssh_authorized_key"] = pub
	if user := rv.target.Spec.Values[v1alpha1.ValueSSHUser]; user != "" {
		env["TF_VAR_ssh_user"] = user
	}

	if err := rt.vmBackendEnv(rv, env); err != nil {
		return nil, err
	}
	if rc, rcErr := rt.materializeTofurc(ctx, rv.release.Namespace); rcErr != nil {
		return nil, rcErr
	} else if rc != "" {
		env["TF_CLI_CONFIG_FILE"] = rc
	}
	return env, nil
}

var vmSettingKeys = []string{
	"vsphere_server", "vsphere_allow_unverified_ssl", "vsphere_datacenter",
	"vsphere_cluster", "vsphere_datastore", "vsphere_network",
	"vsphere_folder", "vsphere_resource_pool",
	"network_gateway", "network_netmask", "network_dns", "network_domain",
	"mirror_ca_pem",
}

func (rt *Runtime) readEnumerated(ctx context.Context, name string, secret bool, keys []string) (map[string]string, error) {
	out := map[string]string{}
	key := types.NamespacedName{Namespace: rt.Namespace, Name: name}
	if secret {
		var s corev1.Secret
		if err := rt.Client.Get(ctx, key, &s); err != nil {
			return nil, fmt.Errorf("reading Secret %s: %w", name, err)
		}
		for _, k := range keys {
			v, ok := s.Data[k]
			if !ok {
				return nil, fmt.Errorf("key %q missing from Secret %s", k, name)
			}
			out[k] = string(v)
		}
		return out, nil
	}
	var cm corev1.ConfigMap
	if err := rt.Client.Get(ctx, key, &cm); err != nil {
		return nil, fmt.Errorf("reading ConfigMap %s: %w", name, err)
	}
	var missing []string
	for _, k := range keys {
		if v, ok := cm.Data[k]; ok {
			out[k] = v
		} else {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		log.Info("placement settings missing (module defaults applied)",
			"configmap", name, "keys", strings.Join(missing, ","))
	}
	return out, nil
}

// stateBackend is the backend the Job's environment selects.
func stateBackend() string {
	if os.Getenv(EnvStateS3) != "" {
		return tofustate.S3
	}
	return tofustate.HTTP
}

// vmBackendEnv configures the state of the machines of rv: the s3 backend of EnvStateS3 (its
// keys come with the credentials Secret), else the http one under EnvStateBaseURL.
func (rt *Runtime) vmBackendEnv(rv *releaseVM, env map[string]string) error {
	if raw := os.Getenv(EnvStateS3); raw != "" {
		bucket, err := tofustate.Decode(raw)
		if err != nil {
			return err
		}
		key := bucket.Key("vm", rv.release.Name)
		path, err := rt.writeFile("state.s3.tfbackend", []byte(bucket.BackendConfig(key)))
		if err != nil {
			return err
		}
		env["TF_CLI_ARGS_init"] = "-backend-config=" + path
		log.Info("machine state", "bucket", bucket.Bucket, "key", key)
		return nil
	}
	base := os.Getenv(EnvStateBaseURL)
	if base == "" {
		if os.Getenv(EnvAllowLocalState) == "1" {
			log.Warn("LOCAL (ephemeral) state for machines, simulator only")
			return nil
		}
		return fmt.Errorf("no state configured (%s): an apply whose state is lost "+
			"leaves machines nothing knows about anymore", EnvStateBaseURL)
	}
	user, password, err := artifactoryCreds()
	if err != nil {
		return err
	}
	addr := strings.TrimRight(base, "/") + "/vm/" + rv.release.Name + "/terraform.tfstate"
	env["TF_HTTP_ADDRESS"] = addr
	env["TF_HTTP_UPDATE_METHOD"] = "PUT"
	if user != "" {
		env["TF_HTTP_USERNAME"] = user
		env["TF_HTTP_PASSWORD"] = password
	}
	log.Info("machine state", "address", addr)
	return nil
}

func (rt *Runtime) sshKeys(ctx context.Context) (pub, priv string, err error) {
	name := os.Getenv(EnvSSHKeySecret)
	if name == "" {
		return "", "", fmt.Errorf("%s required (Secret holding the machine access key pair)", EnvSSHKeySecret)
	}
	var s corev1.Secret
	if err := rt.Client.Get(ctx, types.NamespacedName{
		Namespace: rt.Namespace, Name: name,
	}, &s); err != nil {
		return "", "", fmt.Errorf("reading key Secret %s: %w", name, err)
	}
	pubRaw, ok := s.Data[kobj.VMSSHPublicKey]
	if !ok {
		return "", "", fmt.Errorf("key `%s` missing from Secret %s", kobj.VMSSHPublicKey, name)
	}
	privRaw, ok := s.Data[kobj.VMSSHPrivateKey]
	if !ok {
		return "", "", fmt.Errorf("key `%s` missing from Secret %s", kobj.VMSSHPrivateKey, name)
	}
	return strings.TrimSpace(string(pubRaw)), string(privRaw), nil
}

func (rt *Runtime) publishMachines(ctx context.Context, rv *releaseVM, sc *tofuWorkdir) error {
	raw, err := sc.tofuOutput(ctx, rt, "output", "-json")
	if err != nil {
		return fmt.Errorf("tofu output -json (machines): %w", err)
	}
	var outputs map[string]struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal([]byte(raw), &outputs); err != nil {
		return fmt.Errorf("unreadable `tofu output -json` output: %w", err)
	}

	data := map[string][]byte{}
	for name, o := range outputs {
		switch v := o.Value.(type) {
		case nil:
			data[name] = nil
		case string:
			data[name] = []byte(v)
		default:
			b, mErr := json.Marshal(v)
			if mErr != nil {
				return fmt.Errorf("output %q not serializable: %w", name, mErr)
			}
			data[name] = b
		}
	}
	if _, ok := data["ansible_inventory"]; !ok {
		return fmt.Errorf("output `ansible_inventory` missing: the machine module " +
			"produced no inventory, the install could target nobody")
	}

	name := rv.release.Name + "-machines"
	secret := &corev1.Secret{}
	secret.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	secret.SetName(name)
	secret.SetNamespace(rv.release.Namespace)
	secret.Type = corev1.SecretTypeOpaque
	secret.Data = data
	secret.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by":       fieldManager,
		"rondo.ouafeul.github.io/enrollment": rv.release.Name,
	})
	if err := rt.Client.Patch(ctx, secret, client.Apply,
		client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
		return fmt.Errorf("writing machines Secret %s: %w", name, err)
	}

	patch := &unstructured.Unstructured{}
	patch.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("Enrollment"))
	patch.SetNamespace(rv.release.Namespace)
	patch.SetName(rv.release.Name)
	if err := unstructured.SetNestedMap(patch.Object, map[string]any{
		"machinesSecretRef": map[string]any{"name": name},
	}, "status"); err != nil {
		return err
	}
	log.Info("inventory published", "secret", name, "machines", string(data["machine_count"]))
	return rt.Client.Status().Patch(ctx, patch, client.Apply,
		client.FieldOwner(fieldManager), client.ForceOwnership)
}

type TFVMGroup struct {
	Name     string   `json:"name"`
	IPs      []string `json:"ips"`
	CPU      int      `json:"cpu"`
	MemoryMB int      `json:"memory_mb"`
	DiskGB   int      `json:"disk_gb"`
}

var vmDefaultSizing = v1alpha1.VMSizing{CPU: 2, MemoryMB: 4096, DiskGB: 40}

func VMGroupVars(vm *v1alpha1.VMSpec, component string, target *v1alpha1.Target) ([]TFVMGroup, error) {
	out := make([]TFVMGroup, 0, len(vm.Groups))
	for _, g := range vm.Groups {
		if g.Replicas == 0 {
			log.Info("group with zero replicas, skipped", "group", g.Name)
			continue
		}
		ips, err := vmGroupIPs(component, g, target)
		if err != nil {
			return nil, err
		}
		if len(ips) < g.Replicas {
			return nil, fmt.Errorf("group %q: %d replicas requested but %d IP(s) "+
				"provided by Target %s, set value %q",
				g.Name, g.Replicas, len(ips), target.Name, g.IPsKey(component))
		}
		ips = ips[:g.Replicas]

		sizing := vmDefaultSizing
		if vm.Sizing != nil {
			sizing = *vm.Sizing
		}
		if g.Sizing != nil {
			sizing = *g.Sizing
		}
		out = append(out, TFVMGroup{
			Name: g.Name, IPs: ips,
			CPU: sizing.CPU, MemoryMB: sizing.MemoryMB, DiskGB: sizing.DiskGB,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no group to provision (all at zero replicas)")
	}
	return out, nil
}

func vmGroupIPs(component string, g v1alpha1.VMGroup, target *v1alpha1.Target) ([]string, error) {
	key := g.IPsKey(component)
	raw, ok := target.Spec.Values[key]
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("group %q: value %q of Target %s is missing; "+
			"machine IPs are SITE values, projected by rondo",
			g.Name, key, target.Name)
	}
	var ips []string
	if err := json.Unmarshal([]byte(raw), &ips); err != nil {
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				ips = append(ips, p)
			}
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("group %q: value %q of Target %s contains no IP",
			g.Name, key, target.Name)
	}
	return ips, nil
}

type AnsibleConfigValue struct {
	Kind  string `json:"kind"`
	Value any    `json:"value"`
}

type AnsibleVars struct {
	Component           string `json:"rondo_component"`
	ArtifactoryURL      string `json:"rondo_artifactory_url"`
	ArtifactoryUser     string `json:"rondo_artifactory_user"`
	ArtifactoryPassword string `json:"rondo_artifactory_password"`
	// ArtifactoryCAPath, set to empty, says the machines trust the mirror through their system
	// store; nil keeps the role's default, the CA cloud-init installed.
	ArtifactoryCAPath *string                       `json:"rondo_artifactory_ca_path,omitempty"`
	RPMRepos          []AnsibleRPMRepo              `json:"rondo_rpm_repos"`
	Artifacts         []AnsibleArtifact             `json:"rondo_artifacts"`
	Config            map[string]AnsibleConfigValue `json:"rondo_config"`
	Installer         map[string]any                `json:"rondo_installer"`
	Secrets           map[string]string             `json:"rondo_secrets"`
	RunInstaller      bool                          `json:"rondo_run_installer"`
}

type AnsibleRPMRepo struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	BaseURL        string `json:"baseurl"`
	GPGCheck       bool   `json:"gpgcheck"`
	GPGKey         string `json:"gpgkey,omitempty"`
	ModuleHotfixes bool   `json:"module_hotfixes"`
	Priority       int    `json:"priority,omitempty"`
}

type AnsibleArtifact struct {
	URL     string `json:"url"`
	Digest  string `json:"digest,omitempty"`
	Dest    string `json:"dest,omitempty"`
	Extract bool   `json:"extract"`
}

const (
	InstallPhaseTimeoutMargin = 60 * time.Second

	// Mirrors jobs.DefaultTimeout.
	defaultJobBudget = 10 * time.Minute
)

func BuildAnsibleVars(comp *v1alpha1.Component, target *v1alpha1.Target,
	artifactoryURL, user, password string, secrets map[string]string,
	budget time.Duration) (*AnsibleVars, error) {
	vm := comp.Spec.VM
	if vm == nil {
		return nil, fmt.Errorf("Component %s has no spec.vm", comp.Name)
	}
	base := strings.TrimRight(artifactoryURL, "/")

	out := &AnsibleVars{
		Component:           comp.Name,
		ArtifactoryURL:      base,
		ArtifactoryUser:     user,
		ArtifactoryPassword: password,
		RPMRepos:            make([]AnsibleRPMRepo, 0, len(vm.RPMRepos)),
		Artifacts:           make([]AnsibleArtifact, 0, len(vm.Artifacts)),
		Config:              map[string]AnsibleConfigValue{},
		Installer:           map[string]any{},
		Secrets:             secrets,
		RunInstaller:        vm.Installer != nil,
	}

	for _, r := range vm.RPMRepos {
		url := base + "/" + strings.Trim(r.Repo, "/")
		if p := strings.Trim(r.Path, "/"); p != "" {
			url += "/" + p
		}
		if r.GPGCheckEnabled() && r.GPGKey == "" {
			return nil, fmt.Errorf("RPM repository %q: gpgCheck is on but gpgKey is "+
				"empty; a verified repository without a key makes dnf fail on every package", r.Name)
		}
		out.RPMRepos = append(out.RPMRepos, AnsibleRPMRepo{
			Name: r.Name, Description: r.Description, BaseURL: url,
			GPGCheck: r.GPGCheckEnabled(), GPGKey: r.GPGKey,
			ModuleHotfixes: r.ModuleHotfixes, Priority: r.Priority,
		})
	}

	for _, a := range vm.Artifacts {
		out.Artifacts = append(out.Artifacts, AnsibleArtifact{
			URL:    base + "/" + strings.Trim(a.Repo, "/") + "/" + strings.TrimLeft(a.Path, "/"),
			Digest: a.Digest, Dest: a.Dest, Extract: a.Extract,
		})
	}

	for k, v := range vm.Config {
		cv, err := configValue(k, v)
		if err != nil {
			return nil, err
		}
		out.Config[k] = cv
	}

	if inst := vm.Installer; inst != nil {
		phases, err := installPhases(inst, budget)
		if err != nil {
			return nil, err
		}
		out.Installer = map[string]any{
			"phases":        phases,
			"config_file":   inst.ConfigFile,
			"config_format": string(inst.ConfigFormatOr()),
			"secrets_file":  inst.SecretsFile,
			"steps_flag":    inst.StepsFlagOr(),
			"skip_flag":     inst.SkipFlagOr(),
			"config_flag":   inst.ConfigFlag,
			"secrets_flag":  inst.SecretsFlag,
			"become":        inst.BecomeEnabled(),
		}
		if inst.SecretsFile != "" && len(secrets) == 0 {
			return nil, fmt.Errorf("the installer declares a secrets file (%q) "+
				"but no key was read from secretsFrom", inst.SecretsFile)
		}
	}
	_ = target
	return out, nil
}

func installPhases(inst *v1alpha1.VMInstaller, budget time.Duration) ([]map[string]any, error) {
	seq := inst.Sequence()
	if len(seq) == 0 {
		return nil, fmt.Errorf("the installer declares neither `script` nor `phases`: " +
			"there is no entry point to run")
	}

	fallback, err := inst.TimeoutDuration()
	if err != nil {
		return nil, err
	}
	if fallback == 0 {
		fallback = derivedPhaseTimeout(budget)
	}

	homeDir := path.Dir(seq[0].Script)
	byConvention := (inst.ConfigFile != "" && inst.ConfigFlag == "") ||
		(inst.SecretsFile != "" && inst.SecretsFlag == "")

	seen := make(map[string]bool, len(seq))
	out := make([]map[string]any, 0, len(seq))
	for _, p := range seq {
		if seen[p.Name] {
			return nil, fmt.Errorf("phase %q declared twice: phase names "+
				"identify the failing one in the logs, a duplicate makes the trace "+
				"useless", p.Name)
		}
		seen[p.Name] = true

		if dir := path.Dir(p.Script); byConvention && dir != homeDir {
			return nil, fmt.Errorf("phase %q: its executable is in %q while "+
				"phase %q is in %q. The configuration and secrets are rendered once, "+
				"in the directory of the first entry point: set "+
				"`configFlag`/`secretsFlag` to pass absolute paths, or "+
				"co-locate the executables", p.Name, dir, seq[0].Name, homeDir)
		}

		d, tErr := p.TimeoutDuration()
		if tErr != nil {
			return nil, tErr
		}
		if d == 0 {
			d = fallback
		}
		out = append(out, map[string]any{
			"name":       p.Name,
			"script":     p.Script,
			"args":       orEmpty(p.Args),
			"steps":      orEmpty(p.Steps),
			"skip_steps": orEmpty(p.SkipSteps),
			"timeout":    int(d.Seconds()),
		})
	}
	return out, nil
}

func derivedPhaseTimeout(budget time.Duration) time.Duration {
	if budget <= 0 {
		budget = defaultJobBudget
	}
	if budget > InstallPhaseTimeoutMargin {
		return budget - InstallPhaseTimeoutMargin
	}
	return budget
}

func GroupConfig(vm *v1alpha1.VMSpec, group string) (map[string]AnsibleConfigValue, error) {
	out := map[string]AnsibleConfigValue{}
	for k, v := range vm.Config {
		cv, err := configValue(k, v)
		if err != nil {
			return nil, err
		}
		out[k] = cv
	}
	for _, g := range vm.Groups {
		if g.Name != group {
			continue
		}
		for k, v := range g.Config {
			cv, err := configValue(k, v)
			if err != nil {
				return nil, err
			}
			out[k] = cv
		}
		return out, nil
	}
	return nil, fmt.Errorf("group %q unknown to the component", group)
}

func configValue(key string, v v1alpha1.VMConfigValue) (AnsibleConfigValue, error) {
	switch {
	case v.Value != nil:
		return AnsibleConfigValue{Kind: "string", Value: *v.Value}, nil
	case v.List != nil:
		return AnsibleConfigValue{Kind: "list", Value: v.List}, nil
	case v.Bool != nil:
		return AnsibleConfigValue{Kind: "bool", Value: *v.Bool}, nil
	case v.Number != nil:
		return AnsibleConfigValue{Kind: "number", Value: *v.Number}, nil
	default:
		return AnsibleConfigValue{}, fmt.Errorf("configuration value %q is empty: "+
			"set exactly one of value, list, bool or number", key)
	}
}

func orEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func sortedConfigKeys(m map[string]AnsibleConfigValue) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

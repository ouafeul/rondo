package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"rondo/api/v1alpha1"
	"rondo/internal/config"
	"rondo/internal/kobj"
	"rondo/internal/kubeconfig"
	"rondo/internal/log"
	"rondo/internal/runner"
	"rondo/internal/steps"
)

// DeployOptions are those of `deploy`, `deploy downstreams` and `component install` the engine
// reads.
type DeployOptions struct {
	RondoVersion string
	Fresh        bool
	Timeout      time.Duration
	MaxFailures  int32
	// what happened before this instant belongs to an earlier run
	since time.Time
}

// sandbox is the site the sandbox stands for: an rondo.yaml built in memory from its site and
// installed components, the management cluster's kubeconfig, and the CLI's own PlatformStage.
type sandbox struct {
	s     Settings
	site  *site
	plans []*plan
	// the component `component install` or `uninstall` works on
	p       *plan
	cfg     *config.Config
	ws      steps.Workspace
	kube    *steps.KubeClient
	stage   *steps.PlatformStage
	version string
	// the commands that are not kubectl: docker, helm
	run runner.CommandRunner
}

func openSandbox(s Settings, version string) (*sandbox, error) {
	st, err := loadSite(s)
	if err != nil {
		return nil, err
	}
	plans, err := loadPlans(s)
	if err != nil {
		return nil, err
	}
	return newSandbox(s, st, plans, version)
}

func newSandbox(s Settings, st *site, plans []*plan, version string) (*sandbox, error) {
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		return nil, fmt.Errorf("kubectl not found in PATH: %w", err)
	}
	mgmt := st.management()
	if mgmt == nil {
		return nil, fmt.Errorf("sandbox without a management cluster: run `rondo-sandbox deploy`")
	}
	ws := steps.Workspace{
		ProjectDir: s.RondoDir,
		StateDir:   s.clusterDir(mgmt.Name),
		BinDir:     filepath.Dir(kubectl),
		CACert:     s.caPath(),
	}
	cfg := siteConfig(st, plans)
	kube := steps.NewKubeClient(ws, runner.NewExec(nil))
	return &sandbox{s: s, site: st, plans: plans, cfg: cfg, ws: ws, kube: kube, version: version,
		run: runner.NewExec(nil), stage: steps.NewPlatformStage(cfg, ws, kube, nil, version, false)}, nil
}

// planOf is the installed component an Enrollment belongs to.
func (sb *sandbox) planOf(enrollment string) *plan {
	for _, p := range sb.plans {
		if slices.Contains(p.Enrollments, enrollment) {
			return p
		}
	}
	return nil
}

// component selects an installed component for install or uninstall.
func (sb *sandbox) component(name string) error {
	for _, p := range sb.plans {
		if p.Component == name {
			sb.p = p
			return nil
		}
	}
	return fmt.Errorf("component %q is not planned on the sandbox (installed: %s)", name,
		orDefault(strings.Join(sb.components(), ", "), "none"))
}

func (sb *sandbox) components() []string {
	out := make([]string, len(sb.plans))
	for i, p := range sb.plans {
		out[i] = p.Component
	}
	return out
}

// enrollmentNames are the installed components' Enrollments.
func (sb *sandbox) enrollmentNames() []string {
	var out []string
	for _, p := range sb.plans {
		out = append(out, p.Enrollments...)
	}
	sort.Strings(out)
	return out
}

func (sb *sandbox) machines() []machine {
	var out []machine
	for _, p := range sb.plans {
		out = append(out, p.Machines...)
	}
	return out
}

// secrets are the Secrets of the site and of the installed components, by name.
func (sb *sandbox) secrets() map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, src := range append([]map[string]map[string]string{sb.site.Secrets}, sb.planSecrets()...) {
		for name, data := range src {
			if out[name] == nil {
				out[name] = map[string]string{}
			}
			for k, v := range data {
				out[name][k] = v
			}
		}
	}
	return out
}

func (sb *sandbox) planSecrets() []map[string]map[string]string {
	out := make([]map[string]map[string]string, len(sb.plans))
	for i, p := range sb.plans {
		out[i] = p.Secrets
	}
	return out
}

// siteConfig is the rondo.yaml of the sandbox: the management cluster, each downstream deployed,
// the vSphere Targets of rondo-sandbox.yaml with the machine IPs of the installed components, the
// stand-in as a generic mirror. Images come from the stand-in's root on every target, where a site
// uses <registry>/<repository> on the management and its rke2 system registry elsewhere.
func siteConfig(st *site, plans []*plan) *config.Config {
	cfg := &config.Config{
		IngressBaseDomain: baseDomain,
		Artifacts: config.Artifacts{
			Preset:   config.PresetGeneric,
			Username: sandboxSecret,
			OCI:      config.OCIEndpoint{Registry: registryHost, Repository: dockerRepo, ChartsRepository: helmRepo},
			Files:    config.FilesEndpoint{URL: filesURL, Repository: genericRepo},
			RPM:      config.RPMEndpoint{URL: filesURL, Repository: rpmRepo},
			Tofu: config.TofuEndpoints{ProviderMirror: providerMirrorURL,
				State: config.TofuState{HTTP: &config.HTTPState{URL: filesURL + "/" + tofuStateRepo}}},
		},
		Secrets: config.Secrets{
			MirrorPassword:           sandboxSecret,
			MirrorToken:              sandboxSecret,
			RancherBootstrapPassword: sandboxSecret,
			K3sClusterToken:          sandboxSecret,
			VCenterPasswords:         map[string]string{},
		},
	}
	// Env() reads the vSphere placement of the management cluster, which the sandbox does not
	// have: it gets the first vSphere Target, or a stub nothing applies. No vCenter is ever
	// reached: the machine module of the sandbox has no vSphere.
	targets := st.VSphere
	if len(targets) == 0 {
		targets = []targetSpec{{Name: vsphereStub}}
	}
	for _, t := range targets {
		values := map[string]string{}
		for k, v := range t.ComponentValues {
			values[k] = v
		}
		for _, p := range plans {
			for k, v := range p.VMValues[t.Name] {
				values[k] = v
			}
		}
		cfg.Targets.VSphere = append(cfg.Targets.VSphere, config.VSphereTarget{
			Placement:      config.Placement{Name: t.Name, Roles: t.Roles, ComponentValues: values},
			VCenterAddress: "vcenter." + baseDomain, VCenterUsername: sandboxSecret,
			Datacenter: "sandbox", ComputeCluster: "sandbox", Datastore: "sandbox",
			VMTemplate: machineTemplate,
			VMNetwork: config.VMNetwork{PortGroup: "rondo-sandbox", GatewayIP: st.subnetIP(1), NetmaskBits: 24,
				DNSServerIPs: []string{"127.0.0.11"}},
		})
		cfg.Secrets.VCenterPasswords[t.Name] = sandboxSecret
	}
	placement := cfg.Targets.VSphere[0].Name
	for _, c := range st.Clusters {
		if c.Management {
			cfg.Targets.Management = config.K3sTarget{VSphereTarget: placement, IngressLoadBalancerIP: c.IngressIP}
			continue
		}
		cfg.Targets.Downstreams = append(cfg.Targets.Downstreams, config.DownstreamTarget{
			Placement: config.Placement{Name: c.Target, Roles: c.Roles, ComponentValues: st.values(&c)},
			RKE2: config.RKE2Target{VSphereTarget: placement, IngressLoadBalancerIP: c.IngressIP,
				RegistryHost: registryHost},
		})
	}
	return cfg
}

func (sb *sandbox) downstream(name string) *config.DownstreamTarget {
	for i := range sb.cfg.Targets.Downstreams {
		if sb.cfg.Targets.Downstreams[i].Name == name {
			return &sb.cfg.Targets.Downstreams[i]
		}
	}
	return nil
}

// RunDeploy is `rondo deploy` on the sandbox's management, minus what only vSphere does: the
// operator, the management and vSphere Targets, the site's Secrets, RONDO's Components, and the
// base layer when the management carries other roles.
func RunDeploy(ctx context.Context, s Settings, o DeployOptions) error {
	sb, err := openSandbox(s, o.RondoVersion)
	if err != nil {
		return err
	}
	o.since = time.Now()
	for _, step := range []func(context.Context) error{
		sb.stage.Install, sb.stage.ApplyCredentials, sb.stage.ApplyCoreDNSHosts,
		sb.coreDNSRoles, sb.applySite,
	} {
		if err := step(ctx); err != nil {
			return err
		}
	}
	// WaitReady would wait its ten minutes on an operator already crashing.
	if err := sb.operatorCrash(ctx); err != nil {
		return err
	}
	if err := sb.stage.WaitReady(ctx); err != nil {
		return err
	}
	if err := sb.stage.ApplyTree(ctx, "rondo", filepath.Join(sb.s.RondoDir, "platform", "components"),
		sb.version); err != nil {
		return err
	}
	mgmt := *sb.site.management()
	if !sb.hasBase(mgmt) {
		if err := sb.stage.UninstallTree(ctx, steps.BaseTreeName(mgmt.Target)); err != nil {
			return err
		}
	} else if err := sb.stage.ApplyBaseEnrollments(ctx, mgmt.Target,
		filepath.Join(sb.s.RondoDir, "platform", "enrollments"), sb.version); err != nil {
		return err
	}
	if err := sb.probeGrant(ctx); err != nil {
		return err
	}
	if sb.hasBase(mgmt) {
		base, err := sb.baseEnrollments([]cluster{mgmt})
		if err != nil {
			return err
		}
		if err := sb.await(ctx, o, base, "management base layer"); err != nil {
			return err
		}
	}
	next := "`rondo-sandbox component install <directory>`"
	if len(sb.site.Downstreams) > 0 {
		next = "`rondo-sandbox deploy downstreams`, then " + next
	}
	log.Info("management ready: RONDO is running, its Targets are applied; next "+next, "version", sb.version)
	return nil
}

// RunDownstreams is `rondo deploy downstreams` for the downstreams named (all deployed by
// default), their k3d clusters made: each registered as an rke2 Target, its base layer installed
// and awaited.
func RunDownstreams(ctx context.Context, s Settings, names []string, o DeployOptions) error {
	sb, err := openSandbox(s, o.RondoVersion)
	if err != nil {
		return err
	}
	selected, err := selectDeployed(sb.site, names)
	if err != nil {
		return err
	}
	o.since = time.Now()
	if err := sb.stage.ApplyCoreDNSHosts(ctx); err != nil {
		return err
	}
	for _, c := range selected {
		raw, err := os.ReadFile(filepath.Join(sb.s.clusterDir(c.Name), "kubeconfig.internal.yaml"))
		if err != nil {
			return fmt.Errorf("kubeconfig of cluster %s: %w", c.Name, err)
		}
		info, err := kubeconfig.Parse(raw)
		if err != nil {
			return err
		}
		if err := sb.stage.ApplyRKE2Target(ctx, sb.downstream(c.Target), info.Server, string(raw)); err != nil {
			return err
		}
		if err := sb.stage.ApplyBaseEnrollments(ctx, c.Target,
			filepath.Join(sb.s.RondoDir, "platform", "enrollments"), sb.version); err != nil {
			return err
		}
		log.Info("Target applied", "target", c.Target, "roles", strings.Join(c.Roles, ","), "cluster", c.Name)
	}
	base, err := sb.baseEnrollments(selected)
	if err != nil {
		return err
	}
	if err := sb.await(ctx, o, base, "downstreams base layer"); err != nil {
		return err
	}
	log.Info("downstreams ready: `rondo-sandbox component install <directory>` installs a component on them")
	return nil
}

// RunDestroyDownstreams is `rondo destroy downstreams` for the downstreams named (all deployed by
// default): their base layer uninstalled and their Target removed, then the clusters the script
// deletes written to clusters.txt.
func RunDestroyDownstreams(ctx context.Context, s Settings, names []string) error {
	sb, err := openSandbox(s, "")
	if err != nil {
		return err
	}
	selected, err := selectDeployed(sb.site, names)
	if err != nil {
		return err
	}
	var gone []string
	for _, c := range selected {
		for _, p := range sb.plans {
			for e, t := range p.Targets {
				if t == c.Target {
					log.Warn("Enrollment of an installed component on a destroyed downstream: it will stay "+
						"failed; `rondo-sandbox component uninstall` removes it", "enrollment", e,
						"component", p.Component, "target", t)
				}
			}
		}
		if err := sb.stage.UninstallTree(ctx, steps.BaseTreeName(c.Target)); err != nil {
			return err
		}
		if err := sb.stage.DeleteTarget(ctx, c.Target); err != nil {
			return err
		}
		gone = append(gone, c.Name)
		sb.site.Clusters = slices.DeleteFunc(sb.site.Clusters, func(k cluster) bool { return k.Name == c.Name })
	}
	if err := saveSite(s, sb.site); err != nil {
		return err
	}
	sb.cfg = siteConfig(sb.site, sb.plans)
	sb.stage = steps.NewPlatformStage(sb.cfg, sb.ws, sb.kube, nil, sb.version, false)
	if err := sb.stage.ApplyCoreDNSHosts(ctx); err != nil {
		return err
	}
	return writeLines(filepath.Join(s.StateDir, "clusters.txt"), gone)
}

// RunInstall is `rondo component install <name> --from <tree>` for a published component, then
// RONDO's verdict.
func RunInstall(ctx context.Context, s Settings, component string, o DeployOptions) error {
	sb, err := openSandbox(s, "")
	if err != nil {
		return err
	}
	if err := sb.component(component); err != nil {
		return err
	}
	if sb.p.Tree == "" {
		return fmt.Errorf("tree of component %s missing: `publish` did not complete", component)
	}
	o.since = time.Now()
	if err := sb.applyComponentSite(ctx); err != nil {
		return err
	}
	if err := sb.install(ctx, o.Fresh); err != nil {
		return err
	}
	return sb.await(ctx, o, sb.p.Enrollments, "component "+component)
}

// RunUninstall is `rondo component uninstall <name>`: its Enrollments and their workloads gone,
// its machines' IPs taken off its vSphere Target. The script then removes the machines.
func RunUninstall(ctx context.Context, s Settings, component string) error {
	sb, err := openSandbox(s, "")
	if err != nil {
		return err
	}
	if err := sb.component(component); err != nil {
		return err
	}
	if err := sb.stage.UninstallTree(ctx, component); err != nil {
		return err
	}
	if err := sb.waitGone(ctx, sb.p.Enrollments, 15*time.Minute); err != nil {
		return err
	}
	hadMachines := len(sb.p.Machines) > 0
	if err := os.Remove(s.planPath(component)); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(s.StateDir, "trees", component)); err != nil {
		return err
	}
	if err := writeInstalled(s); err != nil {
		return err
	}
	if hadMachines {
		if sb, err = openSandbox(s, ""); err != nil {
			return err
		}
		if err := sb.applyVSphere(ctx); err != nil {
			return err
		}
	}
	log.Info("component uninstalled", "component", component)
	return nil
}

// hasBase says whether a cluster gets the base layer through Enrollments. A site's management
// never does (helmfile installs its own); the sandbox's does only when it carries other roles,
// whose dependsOn name base Components.
func (sb *sandbox) hasBase(c cluster) bool {
	return !c.Management || len(c.Roles) > 1
}

const probeGrantName = "rondo-sandbox-probe-read"

// probeGrant lets the probe read, on a management carrying the base layer, what a site keeps on
// downstreams, which it reads with their kubeconfig: Secrets, and the base layer's resources
// outside `view`. Without it they stay Unknown and nothing that depends on them starts.
func (sb *sandbox) probeGrant(ctx context.Context) error {
	if !sb.hasBase(*sb.site.management()) {
		return sb.kube.Run(ctx, sb.ws.Kubeconfig(), "delete", "clusterrole,clusterrolebinding",
			probeGrantName, "--ignore-not-found")
	}
	read := []any{"get", "list", "watch"}
	for _, obj := range []kobj.Object{{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata":   kobj.Object{"name": probeGrantName},
		"rules": []any{
			kobj.Object{"apiGroups": []any{""}, "resources": []any{"secrets"}, "verbs": read},
			kobj.Object{"apiGroups": []any{"cert-manager.io", "metallb.io"}, "resources": []any{"*"},
				"verbs": read},
		},
	}, {
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata":   kobj.Object{"name": probeGrantName},
		"roleRef": kobj.Object{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole",
			"name": probeGrantName},
		"subjects": []any{kobj.Object{"kind": "ServiceAccount", "name": kobj.ProbeName,
			"namespace": kobj.PlatformNamespace}},
	}} {
		raw, err := yaml.Marshal(obj)
		if err != nil {
			return err
		}
		if err := sb.kube.ApplyStdin(ctx, sb.ws.Kubeconfig(), string(raw)); err != nil {
			return err
		}
	}
	log.Info("probe allowed to read Secrets and resources of the management base layer: "+
		"specific to the one-cluster sandbox", "clusterrole", probeGrantName)
	return nil
}

// coreDNSRoles resolves <x>.<role>.<domain> to the management ingress for the roles it
// carries: on a site those roles live on rke2 targets, which ApplyCoreDNSHosts covers.
func (sb *sandbox) coreDNSRoles(ctx context.Context) error {
	m := sb.site.management()
	roles := slices.DeleteFunc(slices.Clone(m.Roles), func(r string) bool { return r == managementRole })
	override := ""
	if len(roles) > 0 {
		override = fmt.Sprintf("template IN A {\n  match ^.+[.](%s)[.]%s[.]$\n  answer \"{{ .Name }} 60 IN A %s\"\n  fallthrough\n}\n",
			strings.Join(roles, "|"), strings.ReplaceAll(baseDomain, ".", "[.]"), m.IngressIP)
	}
	const key = "rondo-sandbox-roles.override"
	current, _ := sb.kube.Output(ctx, sb.ws.Kubeconfig(), "-n", "kube-system", "get", "configmap",
		"coredns-custom", "-o", `jsonpath={.data.rondo-sandbox-roles\.override}`)
	if strings.TrimSpace(current) == strings.TrimSpace(override) {
		return nil
	}
	patch, err := json.Marshal(map[string]any{"data": map[string]string{key: override}})
	if err != nil {
		return err
	}
	if err := sb.kube.Run(ctx, sb.ws.Kubeconfig(), "-n", "kube-system", "patch", "configmap",
		"coredns-custom", "--type=merge", "-p", string(patch)); err != nil {
		return fmt.Errorf("coredns-custom (management roles): %w", err)
	}
	return sb.kube.Run(ctx, sb.ws.Kubeconfig(), "-n", "kube-system", "rollout", "restart",
		"deployment", "coredns")
}

// applySite is ApplySite for the sandbox's management Target, its platform values, the Secrets
// of the site and of the installed components, and the vSphere Targets of rondo-sandbox.yaml.
// Unlike a site's, the management Target may carry other roles: targets.management.roles.
func (sb *sandbox) applySite(ctx context.Context) error {
	log.Step("site values and Targets")
	ca, err := os.ReadFile(sb.s.caPath())
	if err != nil {
		return err
	}
	values := map[string]string{
		"ingressIP":     sb.cfg.Targets.Management.IngressLoadBalancerIP,
		"imageRegistry": registryHost,
		"artifactoryCA": string(ca),
		"storageClass":  kobj.DefaultStorageClass,
	}
	for k, v := range sb.site.Management.ComponentValues {
		values[k] = v
	}
	roles := sb.site.management().Roles
	mirror := sb.cfg.Mirror()
	authURL, err := mirror.FilesAuthURL()
	if err != nil {
		return err
	}
	objs := []kobj.Object{
		kobj.Target(config.ManagementRole, roles, kobj.InClusterBackend(), values),
		kobj.OpaqueSecret(kobj.PlatformValuesSecret, map[string]string{
			"artifactoryUser":     mirror.Username,
			"artifactoryPassword": mirror.Password,
			"artifactoryAuthURL":  authURL,
		}),
	}
	for name, data := range sb.secrets() {
		objs = append(objs, kobj.OpaqueSecret(name, data))
	}
	vsphere, err := sb.vsphereObjects(string(ca))
	if err != nil {
		return err
	}
	if err := sb.apply(ctx, append(objs, vsphere...)); err != nil {
		return err
	}
	log.Info("Target applied", "target", config.ManagementRole, "roles", strings.Join(roles, ","))
	for _, t := range sb.site.VSphere {
		log.Info("Target applied", "target", t.Name, "roles", strings.Join(t.Roles, ","), "machines", "containers")
	}
	return nil
}

// applyComponentSite applies, before a component's tree, what a site's rondo.yaml gives it: its
// Secrets, and its machines' IPs on their vSphere Target.
func (sb *sandbox) applyComponentSite(ctx context.Context) error {
	var objs []kobj.Object
	for name, data := range sb.p.Secrets {
		objs = append(objs, kobj.OpaqueSecret(name, data))
	}
	if err := sb.apply(ctx, objs); err != nil {
		return err
	}
	if len(sb.p.Machines) == 0 {
		return nil
	}
	return sb.applyVSphere(ctx)
}

// applyVSphere applies the vSphere Targets again, with the machine IPs of the installed
// components.
func (sb *sandbox) applyVSphere(ctx context.Context) error {
	ca, err := os.ReadFile(sb.s.caPath())
	if err != nil {
		return err
	}
	objs, err := sb.vsphereObjects(string(ca))
	if err != nil {
		return err
	}
	return sb.apply(ctx, objs)
}

func (sb *sandbox) apply(ctx context.Context, objs []kobj.Object) error {
	for _, obj := range objs {
		raw, err := yaml.Marshal(obj)
		if err != nil {
			return err
		}
		if err := sb.kube.ApplyStdin(ctx, sb.ws.Kubeconfig(), string(raw)); err != nil {
			return err
		}
	}
	return nil
}

// vsphereObjects are what `rondo deploy` applies for each vSphere Target of rondo-sandbox.yaml
// (internal/steps, applyVSphereTarget), plus the machines' key, which the sandbox made before
// starting them, and tofu's provider mirror, with the fixed token of a site that sets
// secrets.mirrorToken: the sandbox's machine module needs no provider. None without a
// vSphere Target.
func (sb *sandbox) vsphereObjects(ca string) ([]kobj.Object, error) {
	if len(sb.site.VSphere) == 0 {
		return nil, nil
	}
	mirror := sb.cfg.Mirror()
	var objs []kobj.Object
	for _, declared := range sb.site.VSphere {
		t := sb.cfg.VSphereNamed(declared.Name)
		name := kobj.VSphereObjectName(t.Name)
		settings := sb.cfg.VSphereVars(t)
		delete(settings, kobj.VSphereUserKey)
		delete(settings, kobj.VSpherePasswordKey)
		settings["mirror_ca_pem"] = ca
		values := map[string]string{v1alpha1.ValueVMTemplate: t.VMTemplate, v1alpha1.ValueSSHUser: kobj.VMSSHUser}
		for k, v := range t.ComponentValues {
			values[k] = v
		}
		objs = append(objs,
			kobj.VSphereCredentials(t.Name, t.VCenterUsername, sb.cfg.Secrets.VCenterPasswords[t.Name]),
			kobj.VSphereSettings(t.Name, settings),
			kobj.Target(t.Name, t.Roles, kobj.VSphereBackend(name), values))
	}
	priv, pub, err := machineKey(sb.s)
	if err != nil {
		return nil, err
	}
	return append(objs, kobj.VMSSHKey(priv, pub),
		kobj.TofuMirror(mirror.TofuRC(mirror.MirrorToken), mirror.MirrorToken, false)), nil
}

// install is `rondo component install <name> --from <tree>`. A redeploy also clears the
// failure counters of what it waits for: the developer has just changed something, retry
// at once.
func (sb *sandbox) install(ctx context.Context, fresh bool) error {
	if fresh {
		if err := sb.stage.UninstallTree(ctx, sb.p.Component); err != nil {
			return err
		}
		if err := sb.waitGone(ctx, sb.p.Enrollments, 15*time.Minute); err != nil {
			return err
		}
	}
	if err := sb.stage.ApplyTree(ctx, sb.p.Component, sb.p.Tree, "sandbox"); err != nil {
		return err
	}
	list, err := sb.enrollments(ctx)
	if err != nil {
		return err
	}
	watched, err := sb.watched(sb.p.Enrollments)
	if err != nil {
		return err
	}
	for _, e := range list {
		if !watched[e.Name] || e.Status.ConsecutiveFailures == 0 {
			continue
		}
		if err := sb.kube.Run(ctx, sb.ws.Kubeconfig(), "-n", kobj.PlatformNamespace, "patch",
			"enrollments.rondo.ouafeul.github.io", e.Name, "--subresource=status", "--type=merge",
			"-p", `{"status":{"consecutiveFailures":0}}`); err != nil {
			return err
		}
	}
	return nil
}

func (sb *sandbox) waitGone(ctx context.Context, names []string, timeout time.Duration) error {
	log.Step("removing the component's workloads")
	deadline := time.Now().Add(timeout)
	for {
		list, err := sb.enrollments(ctx)
		if err != nil {
			return err
		}
		left := 0
		for _, e := range list {
			if slices.Contains(names, e.Name) {
				left++
			}
		}
		if left == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d Enrollment(s) still present after %s: their uninstall is stuck", left, timeout)
		}
		if err := sleep(ctx, 5*time.Second); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

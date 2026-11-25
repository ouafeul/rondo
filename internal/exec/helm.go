package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"rondo/api/v1alpha1"
	"rondo/internal/kubeclient"
	"rondo/internal/kubeconfig"
	"rondo/internal/log"
	"rondo/internal/probe"
	"rondo/internal/runner"
	"rondo/internal/values"
)

type releaseContext struct {
	release        *v1alpha1.Enrollment
	component      *v1alpha1.Component
	target         *v1alpha1.Target
	kubeconfigPath string
	releaseName    string
}

// loadEnrollment reads the Job's Enrollment, its Component and its resolved Target.
func (rt *Runtime) loadEnrollment(ctx context.Context) (*v1alpha1.Enrollment, *v1alpha1.Component,
	*v1alpha1.Target, error) {
	if rt.Kind != "Enrollment" {
		return nil, nil, nil, fmt.Errorf("verb called on a %s (want Enrollment)", rt.Kind)
	}
	var enr v1alpha1.Enrollment
	if err := rt.Client.Get(ctx, rt.Ref, &enr); err != nil {
		return nil, nil, nil, fmt.Errorf("reading Enrollment %s: %w", rt.Ref, err)
	}
	var comp v1alpha1.Component
	if err := rt.Client.Get(ctx, types.NamespacedName{
		Namespace: rt.Ref.Namespace, Name: enr.Spec.Component,
	}, &comp); err != nil {
		return nil, nil, nil, fmt.Errorf("reading Component %s: %w", enr.Spec.Component, err)
	}
	var target v1alpha1.Target
	if err := rt.Client.Get(ctx, types.NamespacedName{
		Namespace: rt.Ref.Namespace, Name: enr.Status.ResolvedTarget,
	}, &target); err != nil {
		return nil, nil, nil, fmt.Errorf("reading Target %q: %w", enr.Status.ResolvedTarget, err)
	}
	return &enr, &comp, &target, nil
}

func (rt *Runtime) load(ctx context.Context) (*releaseContext, error) {
	enr, comp, target, err := rt.loadEnrollment(ctx)
	if err != nil {
		return nil, err
	}
	rc := &releaseContext{release: enr, component: comp, target: target}
	rc.releaseName = comp.Spec.ReleaseName
	if rc.releaseName == "" {
		rc.releaseName = comp.Name
	}

	if !target.Spec.InCluster() {
		path, err := rt.materializeKubeconfig(ctx, target)
		if err != nil {
			return nil, err
		}
		rc.kubeconfigPath = path
	}
	return rc, nil
}

func (rt *Runtime) materializeKubeconfig(ctx context.Context, target *v1alpha1.Target) (string, error) {
	raw, err := kubeclient.KubeconfigBytes(ctx, rt.Client, target)
	if err != nil {
		return "", err
	}
	info, err := kubeconfig.Parse(raw)
	if err != nil {
		return "", err
	}
	log.Info("remote target", "target", target.Name, "server", info.Server, "auth", info.AuthMethod)
	return rt.writeFile("kubeconfig", raw)
}

func (rc *releaseContext) helmEnv(rt *Runtime) map[string]string {
	env := map[string]string{}
	if rc.kubeconfigPath != "" {
		env["KUBECONFIG"] = rc.kubeconfigPath
	}
	_ = rt
	return env
}

func (rc *releaseContext) login(ctx context.Context, rt *Runtime) error {
	host, err := registryHost(rc.component.Spec.Chart.OCI)
	if err != nil {
		return err
	}
	user, password, err := artifactoryCreds()
	if err != nil {
		return err
	}
	if user == "" {
		log.Info("anonymous mirror, no registry login", "registry", host)
		return nil
	}
	args := []string{"registry", "login", host, "--username", user, "--password-stdin"}
	if ca := os.Getenv(EnvCAFile); ca != "" {
		args = append(args, "--ca-file", ca)
	}
	if err := rt.Run.Run(ctx, runner.Cmd{
		Name: "helm", Args: args, Dir: rt.dir(), Env: rc.helmEnv(rt), Stdin: password,
	}); err != nil {
		return fmt.Errorf("helm login to %s: %w", host, err)
	}
	return nil
}

func (rc *releaseContext) valuesFile(ctx context.Context, rt *Runtime) (string, error) {
	v, err := values.ForEnrollment(rc.component, rc.release, rc.target)
	if err != nil {
		return "", err
	}
	if v == nil || len(v.Raw) == 0 {
		return "", nil
	}
	raw := v.Raw
	sources := append(append([]v1alpha1.ValuesSource{}, rc.component.Spec.ValuesFrom...),
		rc.release.Spec.ValuesFrom...)
	if len(sources) > 0 {
		resolved, err := rt.resolveSecrets(ctx, raw, sources)
		if err != nil {
			return "", err
		}
		raw = resolved
	}
	return rt.writeFile("values.json", raw)
}

const secretTokenPrefix = "${secrets."

func (rt *Runtime) resolveSecrets(ctx context.Context, raw []byte, sources []v1alpha1.ValuesSource) ([]byte, error) {
	secrets := map[string]string{}
	for _, src := range sources {
		data, err := rt.readValuesSource(ctx, src)
		if err != nil {
			return nil, err
		}
		for k, val := range data {
			secrets[k] = val
		}
	}

	out := string(raw)
	var missing []string
	for {
		start := strings.Index(out, secretTokenPrefix)
		if start < 0 {
			break
		}
		end := strings.Index(out[start:], "}")
		if end < 0 {
			return nil, fmt.Errorf("unterminated `%s` token in values", secretTokenPrefix)
		}
		end += start
		key := out[start+len(secretTokenPrefix) : end]
		val, ok := secrets[key]
		if !ok {
			missing = append(missing, key)
			val = ""
		}
		out = out[:start] + jsonEscape(val) + out[end+1:]
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("unresolved secret tokens: %s "+
			"(keys declared in valuesFrom: %s)",
			strings.Join(missing, ", "), strings.Join(sortedKeys(secrets), ", "))
	}
	log.Info("secrets injected into values", "tokens", len(secrets))
	return []byte(out), nil
}

func (rt *Runtime) readValuesSource(ctx context.Context, src v1alpha1.ValuesSource) (map[string]string, error) {
	out := map[string]string{}
	switch {
	case src.SecretRef != nil:
		var s corev1.Secret
		if err := rt.Client.Get(ctx, types.NamespacedName{
			Namespace: rt.Namespace, Name: src.SecretRef.Name,
		}, &s); err != nil {
			return nil, fmt.Errorf("valuesFrom Secret %s: %w", src.SecretRef.Name, err)
		}
		for _, k := range src.Keys {
			v, ok := s.Data[k]
			if !ok {
				return nil, fmt.Errorf("key %q missing from Secret %s", k, src.SecretRef.Name)
			}
			out[k] = string(v)
		}
	case src.ConfigMapRef != nil:
		var cm corev1.ConfigMap
		if err := rt.Client.Get(ctx, types.NamespacedName{
			Namespace: rt.Namespace, Name: src.ConfigMapRef.Name,
		}, &cm); err != nil {
			return nil, fmt.Errorf("valuesFrom ConfigMap %s: %w", src.ConfigMapRef.Name, err)
		}
		for _, k := range src.Keys {
			v, ok := cm.Data[k]
			if !ok {
				return nil, fmt.Errorf("key %q missing from ConfigMap %s", k, src.ConfigMapRef.Name)
			}
			out[k] = v
		}
	default:
		return nil, fmt.Errorf("valuesFrom has neither configMapRef nor secretRef")
	}
	return out, nil
}

func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b[1 : len(b)-1])
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (rc *releaseContext) upgradeArgs(valuesPath string) []string {
	p := rc.release.Spec.Policy
	args := []string{
		"upgrade", "--install", rc.releaseName, rc.component.Spec.Chart.OCI,
		"--version", rc.component.Spec.Chart.Version,
		"--namespace", rc.component.Spec.Namespace,
	}
	if p.CreateNamespaceEnabled() {
		args = append(args, "--create-namespace")
	}
	if p.WaitEnabled() {
		args = append(args, "--wait")
	}
	if p.AtomicEnabled() {
		args = append(args, "--atomic")
	}
	args = append(args, "--timeout", rc.timeout())
	if valuesPath != "" {
		args = append(args, "-f", valuesPath)
	}
	return args
}

func (rc *releaseContext) timeout() string {
	d := rc.release.Spec.Policy.TimeoutOr(0)
	if d <= 0 {
		return "9m"
	}
	if d > 30*time.Second {
		d -= 30 * time.Second
	}
	return d.String()
}

func HelmApply(ctx context.Context, rt *Runtime) error {
	rc, err := rt.load(ctx)
	if err != nil {
		return err
	}
	logStep("apply helm", "release", rc.releaseName, "chart", rc.component.Spec.Chart.OCI,
		"version", rc.component.Spec.Chart.Version, "target", rc.target.Name)

	if err := rc.login(ctx, rt); err != nil {
		return err
	}
	valuesPath, err := rc.valuesFile(ctx, rt)
	if err != nil {
		return err
	}
	if err := rt.Run.Run(ctx, runner.Cmd{
		Name: "helm", Args: rc.upgradeArgs(valuesPath), Dir: rt.dir(), Env: rc.helmEnv(rt),
	}); err != nil {
		return fmt.Errorf("helm upgrade: %w", err)
	}

	if err := rt.publishState(ctx, rc); err != nil {
		log.Warn("publishing inventory and health (non-blocking)", "err", err)
	}
	return nil
}

func HelmUninstall(ctx context.Context, rt *Runtime) error {
	rc, err := rt.load(ctx)
	if err != nil {
		return err
	}
	logStep("uninstall helm", "release", rc.releaseName, "target", rc.target.Name)
	err = rt.Run.Run(ctx, runner.Cmd{
		Name: "helm",
		Args: []string{"uninstall", rc.releaseName, "--namespace", rc.component.Spec.Namespace,
			"--wait", "--timeout", rc.timeout()},
		Dir: rt.dir(), Env: rc.helmEnv(rt),
	})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			log.Info("helm release already gone, nothing to uninstall", "release", rc.releaseName)
			return nil
		}
		return fmt.Errorf("helm uninstall: %w", err)
	}
	return nil
}

func (rt *Runtime) publishState(ctx context.Context, rc *releaseContext) error {
	manifest, err := rt.Run.Output(ctx, runner.Cmd{
		Name: "helm",
		Args: []string{"get", "manifest", rc.releaseName, "--namespace", rc.component.Spec.Namespace},
		Dir:  rt.dir(), Env: rc.helmEnv(rt),
	})
	if err != nil {
		return fmt.Errorf("helm get manifest: %w", err)
	}
	inventory, err := parseInventory(manifest, rc.component.Spec.Namespace)
	if err != nil {
		return err
	}
	health, message := rt.assess(ctx, rc, inventory)
	log.Info("state published", "objects", len(inventory), "health", health)
	return rt.patchStatus(ctx, rc.release, inventory, health, message)
}

func parseInventory(manifest, defaultNS string) ([]v1alpha1.InventoryEntry, error) {
	var out []v1alpha1.InventoryEntry
	for _, doc := range strings.Split(manifest, "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			log.Warn("manifest document skipped (unreadable)", "err", err)
			continue
		}
		u := unstructured.Unstructured{Object: obj}
		gvk := u.GroupVersionKind()
		if gvk.Kind == "" || u.GetName() == "" {
			continue
		}
		ns := u.GetNamespace()
		if ns == "" && isNamespaced(gvk.Kind) {
			ns = defaultNS
		}
		out = append(out, v1alpha1.InventoryEntry{
			Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind,
			Namespace: ns, Name: u.GetName(),
		})
	}
	return out, nil
}

var clusterScopedKinds = map[string]bool{
	"Namespace": true, "ClusterRole": true, "ClusterRoleBinding": true,
	"CustomResourceDefinition": true, "PriorityClass": true, "StorageClass": true,
	"ClusterIssuer": true, "ValidatingWebhookConfiguration": true,
	"MutatingWebhookConfiguration": true, "APIService": true,
	"ValidatingAdmissionPolicy": true, "ValidatingAdmissionPolicyBinding": true,
}

func isNamespaced(kind string) bool { return !clusterScopedKinds[kind] }

func (rt *Runtime) assess(ctx context.Context, rc *releaseContext, inventory []v1alpha1.InventoryEntry) (string, string) {
	target, err := kubeclient.ForTarget(ctx, rt.Client, rc.target)
	if err != nil {
		return v1alpha1.HealthUnknown, "target client unavailable: " + err.Error()
	}
	return probe.AssessInventoryWith(ctx, target, inventory, rc.component.Spec.Health)
}

func (rt *Runtime) patchStatus(ctx context.Context, rel *v1alpha1.Enrollment,
	inventory []v1alpha1.InventoryEntry, health, message string) error {
	patch := &unstructured.Unstructured{}
	patch.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("Enrollment"))
	patch.SetNamespace(rel.Namespace)
	patch.SetName(rel.Name)

	entries := make([]any, 0, len(inventory))
	for _, e := range inventory {
		entry := map[string]any{"version": e.Version, "kind": e.Kind, "name": e.Name}
		if e.Group != "" {
			entry["group"] = e.Group
		}
		if e.Namespace != "" {
			entry["namespace"] = e.Namespace
		}
		entries = append(entries, entry)
	}
	if err := unstructured.SetNestedMap(patch.Object, map[string]any{
		"inventory":      entries,
		"inventoryCount": int64(len(inventory)),
		"health":         health,
		"healthMessage":  truncate(message, 2048),
	}, "status"); err != nil {
		return err
	}
	return rt.Client.Status().Patch(ctx, patch, client.Apply,
		client.FieldOwner(fieldManager), client.ForceOwnership)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

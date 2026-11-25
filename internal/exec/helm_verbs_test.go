package exec

import (
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"rondo/api/v1alpha1"
	"rondo/internal/runner"
)

func chartComponent(mutate func(*v1alpha1.Component)) *v1alpha1.Component {
	c := &v1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: "victoria-logs", Namespace: "rondo-system"},
		Spec: v1alpha1.ComponentSpec{
			Chart: &v1alpha1.ChartSource{
				OCI: "oci://arti.local/rondo-helm-local/victoria-logs-single", Version: "0.11.7",
			},
			Namespace: "logging",
		},
	}
	if mutate != nil {
		mutate(c)
	}
	return c
}

func chartRelease(mutate func(*v1alpha1.Enrollment)) *v1alpha1.Enrollment {
	r := &v1alpha1.Enrollment{
		ObjectMeta: metav1.ObjectMeta{Name: "victoria-logs-acme", Namespace: "rondo-system"},
		Spec: v1alpha1.EnrollmentSpec{
			Component: "victoria-logs",
			Target:    "acme",
		},
		Status: v1alpha1.EnrollmentStatus{ResolvedTarget: "acme"},
	}
	if mutate != nil {
		mutate(r)
	}
	return r
}

func inClusterTarget() *v1alpha1.Target {
	return &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "acme", Namespace: "rondo-system"},
		Spec:       v1alpha1.TargetSpec{Kubernetes: &v1alpha1.KubernetesCluster{InCluster: true}},
	}
}

func findCall(t *testing.T, calls []runner.Cmd, first string) runner.Cmd {
	t.Helper()
	for _, c := range calls {
		if len(c.Args) > 0 && c.Args[0] == first {
			return c
		}
	}
	t.Fatalf("no `helm %s` call; calls = %v", first, calls)
	return runner.Cmd{}
}

func TestHelmApplyCommandLine(t *testing.T) {
	t.Setenv(EnvArtifactoryUser, "svc")
	t.Setenv(EnvArtifactoryPassword, "pw")
	t.Setenv(EnvCAFile, "/etc/ssl/arti-ca.pem")

	rel := chartRelease(func(r *v1alpha1.Enrollment) {
		r.Spec.Policy.Timeout = &metav1.Duration{Duration: 600_000_000_000}
	})
	rt, fk := newRuntime(t, "Enrollment", "victoria-logs-acme",
		rel, chartComponent(nil), inClusterTarget())
	fk.Outputs["helm get manifest victoria-logs --namespace logging"] = ""

	if err := HelmApply(t.Context(), rt); err != nil {
		t.Fatalf("HelmApply: %v", err)
	}

	login := findCall(t, fk.Calls, "registry")
	if login.Stdin != "pw" {
		t.Error("the password is not passed via stdin")
	}
	if strings.Contains(login.String(), "pw") {
		t.Errorf("password present in argv: %s", login.String())
	}
	if !strings.Contains(login.String(), "arti.local") {
		t.Errorf("registry host = %s", login.String())
	}
	if !strings.Contains(login.String(), "--ca-file /etc/ssl/arti-ca.pem") {
		t.Error("--ca-file missing: login would fail on the internal CA")
	}

	up := findCall(t, fk.Calls, "upgrade")
	got := up.String()
	for _, want := range []string{
		"upgrade --install victoria-logs oci://arti.local/rondo-helm-local/victoria-logs-single",
		"--version 0.11.7",
		"--namespace logging",
		"--create-namespace",
		"--wait",
		"--atomic",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("argument %q missing from: %s", want, got)
		}
	}
	if strings.Contains(got, "--server-side") {
		t.Errorf("helm-apply passes --server-side: %s", got)
	}
	if !strings.Contains(got, "--timeout 9m30s") {
		t.Errorf("timeout = %s, want 9m30s (10m - 30s margin)", got)
	}
	if strings.Contains(got, "--dry-run") {
		t.Error("--dry-run on an apply")
	}
}

func TestHelmApplyDefaultsOff(t *testing.T) {
	t.Setenv(EnvArtifactoryUser, "u")
	t.Setenv(EnvArtifactoryPassword, "p")
	rel := chartRelease(func(r *v1alpha1.Enrollment) {
		r.Spec.Policy.CreateNamespace = v1alpha1.Bool(false)
		r.Spec.Policy.Wait = v1alpha1.Bool(false)
		r.Spec.Policy.Atomic = v1alpha1.Bool(false)
	})
	rt, fk := newRuntime(t, "Enrollment", "victoria-logs-acme",
		rel, chartComponent(nil), inClusterTarget())
	fk.Outputs["helm get manifest victoria-logs --namespace logging"] = ""
	if err := HelmApply(t.Context(), rt); err != nil {
		t.Fatalf("HelmApply: %v", err)
	}
	got := findCall(t, fk.Calls, "upgrade").String()
	for _, unwanted := range []string{"--create-namespace", "--wait", "--atomic"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("%s present despite an explicit false: %s", unwanted, got)
		}
	}
	if !strings.Contains(got, "--timeout 9m") {
		t.Errorf("fallback timeout missing: %s", got)
	}
}

func TestHelmApplyUsesReleaseName(t *testing.T) {
	t.Setenv(EnvArtifactoryUser, "u")
	t.Setenv(EnvArtifactoryPassword, "p")
	comp := chartComponent(func(c *v1alpha1.Component) { c.Spec.ReleaseName = "vlogs" })
	rt, fk := newRuntime(t, "Enrollment", "victoria-logs-acme",
		chartRelease(nil), comp, inClusterTarget())
	fk.Outputs["helm get manifest vlogs --namespace logging"] = ""
	if err := HelmApply(t.Context(), rt); err != nil {
		t.Fatalf("HelmApply: %v", err)
	}
	if got := findCall(t, fk.Calls, "upgrade").String(); !strings.Contains(got, "--install vlogs ") {
		t.Errorf("release name = %s", got)
	}
}

func TestHelmApplyWritesValuesFile(t *testing.T) {
	t.Setenv(EnvArtifactoryUser, "u")
	t.Setenv(EnvArtifactoryPassword, "p")
	rel := chartRelease(func(r *v1alpha1.Enrollment) {
		r.Spec.Values = &apiextensionsv1.JSON{
			Raw: []byte(`{"server":{"persistentVolume":{"storageClass":"longhorn"}}}`),
		}
	})
	rt, fk := newRuntime(t, "Enrollment", "victoria-logs-acme",
		rel, chartComponent(nil), inClusterTarget())
	fk.Outputs["helm get manifest victoria-logs --namespace logging"] = ""
	if err := HelmApply(t.Context(), rt); err != nil {
		t.Fatalf("HelmApply: %v", err)
	}
	got := findCall(t, fk.Calls, "upgrade").String()
	if !strings.Contains(got, "-f ") {
		t.Errorf("no values file: %s", got)
	}
	if strings.Contains(got, "--set") {
		t.Error("--set used: dots and commas would be reinterpreted")
	}
}

func TestHelmUninstallIdempotent(t *testing.T) {
	rt, fk := newRuntime(t, "Enrollment", "victoria-logs-acme",
		chartRelease(nil), chartComponent(nil), inClusterTarget())
	cmd := "helm uninstall victoria-logs --namespace logging --wait --timeout 9m"
	fk.Errs[cmd] = errNotFound{}
	if err := HelmUninstall(t.Context(), rt); err != nil {
		t.Fatalf("missing release treated as a failure: %v", err)
	}
}

type errNotFound struct{}

func (errNotFound) Error() string {
	return `Error: uninstall: Release not loaded: x: release: not found`
}

func TestHelmUninstallPropagatesRealFailure(t *testing.T) {
	rt, fk := newRuntime(t, "Enrollment", "victoria-logs-acme",
		chartRelease(nil), chartComponent(nil), inClusterTarget())
	cmd := "helm uninstall victoria-logs --namespace logging --wait --timeout 9m"
	fk.Errs[cmd] = errTimeout{}
	if err := HelmUninstall(t.Context(), rt); err == nil {
		t.Fatal("real failure swallowed")
	}
}

type errTimeout struct{}

func (errTimeout) Error() string { return "timed out waiting for the condition" }

func TestHelmApplyMissingComponent(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "victoria-logs-acme", chartRelease(nil), inClusterTarget())
	err := HelmApply(t.Context(), rt)
	if err == nil {
		t.Fatal("missing Component accepted")
	}
	if !strings.Contains(err.Error(), "victoria-logs") {
		t.Errorf("message = %q", err)
	}
}

func TestHelmApplyMissingTarget(t *testing.T) {
	rt, _ := newRuntime(t, "Enrollment", "victoria-logs-acme", chartRelease(nil), chartComponent(nil))
	if err := HelmApply(t.Context(), rt); err == nil {
		t.Fatal("missing Target accepted")
	}
}

func TestHelmApplyRefusesHalfCredentials(t *testing.T) {
	t.Setenv(EnvArtifactoryUser, "svc")
	t.Setenv(EnvArtifactoryPassword, "")
	rt, _ := newRuntime(t, "Enrollment", "victoria-logs-acme",
		chartRelease(nil), chartComponent(nil), inClusterTarget())
	if err := HelmApply(t.Context(), rt); err == nil {
		t.Fatal("a username without a password accepted")
	}
}

func TestHelmApplyOnAnAnonymousMirrorDoesNotLogIn(t *testing.T) {
	t.Setenv(EnvArtifactoryUser, "")
	t.Setenv(EnvArtifactoryPassword, "")
	t.Setenv(EnvCAFile, "")
	rt, fk := newRuntime(t, "Enrollment", "victoria-logs-acme",
		chartRelease(nil), chartComponent(nil), inClusterTarget())
	fk.Outputs["helm get manifest victoria-logs --namespace logging"] = ""
	if err := HelmApply(t.Context(), rt); err != nil {
		t.Fatalf("HelmApply: %v", err)
	}
	for _, c := range fk.Calls {
		if len(c.Args) > 0 && c.Args[0] == "registry" {
			t.Errorf("registry login on an anonymous mirror: %s", c.String())
		}
		if strings.Contains(c.String(), "--ca-file") {
			t.Errorf("--ca-file without a CA: %s", c.String())
		}
	}
	findCall(t, fk.Calls, "upgrade")
}

func TestParseInventoryScopes(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: vl
---
apiVersion: v1
kind: Namespace
metadata:
  name: logging
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: vl-reader
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: vl-conf
  namespace: other
`
	got, err := parseInventory(manifest, "logging")
	if err != nil {
		t.Fatalf("parseInventory: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("%d entries, want 4: %+v", len(got), got)
	}
	byKind := map[string]v1alpha1.InventoryEntry{}
	for _, e := range got {
		byKind[e.Kind] = e
	}
	if e := byKind["Deployment"]; e.Namespace != "logging" || e.Group != "apps" {
		t.Errorf("Deployment = %+v", e)
	}
	if e := byKind["Namespace"]; e.Namespace != "" {
		t.Errorf("Namespace was given a namespace: %+v", e)
	}
	if e := byKind["ClusterRole"]; e.Namespace != "" {
		t.Errorf("ClusterRole was given a namespace: %+v", e)
	}
	if e := byKind["ConfigMap"]; e.Namespace != "other" {
		t.Errorf("ConfigMap = %+v", e)
	}
}

func TestParseInventorySkipsJunk(t *testing.T) {
	manifest := `# Source: chart/templates/empty.yaml
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: good
---
kind: NoName
---
	this: [is not
	valid: yaml
`
	got, err := parseInventory(manifest, "ns")
	if err != nil {
		t.Fatalf("parseInventory: %v", err)
	}
	if len(got) != 1 || got[0].Name != "good" {
		t.Errorf("entries = %+v, want only the identifiable one", got)
	}
}

func TestIsNamespaced(t *testing.T) {
	for kind, want := range map[string]bool{
		"Deployment": true, "ConfigMap": true,
		"Namespace": false, "ClusterRole": false, "CustomResourceDefinition": false,
		"PriorityClass": false, "ClusterIssuer": false, "StorageClass": false,
	} {
		if got := isNamespaced(kind); got != want {
			t.Errorf("isNamespaced(%q) = %v", kind, got)
		}
	}
}

func TestPatchStatusWritesInventory(t *testing.T) {
	rel := chartRelease(nil)
	rt, _ := newRuntime(t, "Enrollment", "victoria-logs-acme", rel, chartComponent(nil), inClusterTarget())
	inv := []v1alpha1.InventoryEntry{
		{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "logging", Name: "vl"},
		{Version: "v1", Kind: "Namespace", Name: "logging"},
	}
	if err := rt.patchStatus(t.Context(), rel, inv, v1alpha1.HealthHealthy, "ok"); err != nil {
		t.Fatalf("patchStatus: %v", err)
	}
	var got v1alpha1.Enrollment
	if err := rt.Client.Get(t.Context(),
		types.NamespacedName{Namespace: "rondo-system", Name: "victoria-logs-acme"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Health != v1alpha1.HealthHealthy {
		t.Errorf("health = %q", got.Status.Health)
	}
	if got.Status.InventoryCount != 2 || len(got.Status.Inventory) != 2 {
		t.Errorf("inventory = %d entries / count %d", len(got.Status.Inventory), got.Status.InventoryCount)
	}
}

func TestTruncateBoundsStatusMessage(t *testing.T) {
	if got := truncate("short", 2048); got != "short" {
		t.Errorf("truncate of a short message = %q", got)
	}
	long := strings.Repeat("x", 3000)
	got := truncate(long, 2048)
	if len(got) != 2048 {
		t.Errorf("length = %d, want 2048", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("truncation not marked")
	}
}

func TestJSONEscape(t *testing.T) {
	for in, want := range map[string]string{
		`simple`: `simple`,
		`a"b`:    `a\"b`,
		`a\b`:    `a\\b`,
		"a\nb":   `a\nb`,
		"a\tb":   `a\tb`,
	} {
		if got := jsonEscape(in); got != want {
			t.Errorf("jsonEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSortedKeysIsDeterministic(t *testing.T) {
	got := sortedKeys(map[string]string{"c": "", "a": "", "b": ""})
	if strings.Join(got, ",") != "a,b,c" {
		t.Errorf("sortedKeys = %v", got)
	}
}

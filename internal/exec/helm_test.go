package exec

import (
	"os"
	"strings"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"rondo/api/v1alpha1"
)

func testReleaseCtx(mutate func(*releaseContext)) *releaseContext {
	rc := &releaseContext{
		release: &v1alpha1.Enrollment{
			ObjectMeta: metav1.ObjectMeta{Name: "kps-acme", Namespace: "rondo-system"},
		},
		component: &v1alpha1.Component{
			ObjectMeta: metav1.ObjectMeta{Name: "kps", Namespace: "rondo-system"},
			Spec: v1alpha1.ComponentSpec{
				Chart: &v1alpha1.ChartSource{
					OCI:     "oci://arti.local/rondo-helm-local/kube-prometheus-stack",
					Version: "83.3.0",
				},
				Namespace: "monitoring",
			},
		},
		target:      &v1alpha1.Target{ObjectMeta: metav1.ObjectMeta{Name: "acme"}},
		releaseName: "kps",
	}
	if mutate != nil {
		mutate(rc)
	}
	return rc
}

func TestUpgradeArgsHonoursPolicy(t *testing.T) {
	args := strings.Join(testReleaseCtx(nil).upgradeArgs(""), " ")
	for _, want := range []string{
		"upgrade --install kps oci://arti.local/rondo-helm-local/kube-prometheus-stack",
		"--version 83.3.0", "--namespace monitoring",
		"--create-namespace", "--wait", "--atomic", "--timeout",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args = %q, want it to contain %q", args, want)
		}
	}
}

func TestUpgradeArgsOmitsServerSideUnknownToHelm3(t *testing.T) {
	args := strings.Join(testReleaseCtx(nil).upgradeArgs(""), " ")
	if strings.Contains(args, "--server-side") {
		t.Errorf("args = %q: helm 3 would answer `unknown flag: --server-side` and "+
			"the Release would never apply", args)
	}
}

func TestUpgradeArgsNeverDryRun(t *testing.T) {
	if args := strings.Join(testReleaseCtx(nil).upgradeArgs(""), " "); strings.Contains(args, "--dry-run") {
		t.Errorf("args = %q: the Job would apply nothing yet exit 0", args)
	}
}

func TestUpgradeArgsRespectsDisabledFlags(t *testing.T) {
	rc := testReleaseCtx(func(rc *releaseContext) {
		rc.release.Spec.Policy.Wait = v1alpha1.Bool(false)
		rc.release.Spec.Policy.Atomic = v1alpha1.Bool(false)
		rc.release.Spec.Policy.CreateNamespace = v1alpha1.Bool(false)
	})
	args := strings.Join(rc.upgradeArgs(""), " ")
	for _, forbidden := range []string{"--wait", "--atomic", "--create-namespace"} {
		if strings.Contains(args, forbidden) {
			t.Errorf("%s present although the policy disables it: args = %q", forbidden, args)
		}
	}
}

func TestUpgradeArgsIncludesValuesFile(t *testing.T) {
	args := strings.Join(testReleaseCtx(nil).upgradeArgs("/work/values.json"), " ")
	if !strings.Contains(args, "-f /work/values.json") {
		t.Errorf("args = %q, want it to contain -f", args)
	}
}

func TestTimeoutLeavesMarginForRollback(t *testing.T) {
	rc := testReleaseCtx(func(rc *releaseContext) {
		rc.release.Spec.Policy.Timeout = &metav1.Duration{Duration: 10 * time.Minute}
	})
	got := rc.timeout()
	d, err := time.ParseDuration(got)
	if err != nil {
		t.Fatalf("timeout %q not parsable by helm: %v", got, err)
	}
	if d >= 10*time.Minute {
		t.Errorf("timeout = %v, want it below the Job timeout (margin for rollback)", d)
	}
	if _, err := time.ParseDuration(testReleaseCtx(nil).timeout()); err != nil {
		t.Errorf("default timeout not parsable: %v", err)
	}
}

func TestValuesFileIsRawJSON(t *testing.T) {
	raw := []byte(`{"grafana":{"expr":"sum(rate(x[${__interval}]))"}}`)
	rc := testReleaseCtx(func(rc *releaseContext) {
		rc.release.Spec.Values = &apiextensionsv1.JSON{Raw: raw}
	})
	rt := &Runtime{WorkDir: t.TempDir()}
	path, err := rc.valuesFile(t.Context(), rt)
	if err != nil {
		t.Fatalf("valuesFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Errorf("values rewritten:\n got %s\nwant %s", got, raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions %o, want 600 (values may carry secrets)", perm)
	}

	if p, err := testReleaseCtx(nil).valuesFile(t.Context(), rt); err != nil || p != "" {
		t.Errorf("valuesFile(no values) = (%q, %v), want (\"\", nil)", p, err)
	}
}

func TestRegistryHost(t *testing.T) {
	for in, want := range map[string]string{
		"oci://arti.local/rondo-helm-local/metallb":      "arti.local",
		"oci://arti.local:5000/rondo-helm-local/metallb": "arti.local:5000",
	} {
		got, err := registryHost(in)
		if err != nil {
			t.Fatalf("registryHost(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("registryHost(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"https://arti.local/x", "arti.local/x", "oci://"} {
		if _, err := registryHost(bad); err == nil {
			t.Errorf("registryHost(%q) succeeded, want an error", bad)
		}
	}
}

func TestParseInventoryReadsIdentities(t *testing.T) {
	manifest := `---
# Source: kps/templates/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: grafana
  namespace: monitoring
spec:
  replicas: 2
---
# Source: kps/templates/svc.yaml
apiVersion: v1
kind: Service
metadata:
  name: grafana
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kps-reader
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: dashboards
  namespace: other-ns
`
	inv, err := parseInventory(manifest, "monitoring")
	if err != nil {
		t.Fatalf("parseInventory: %v", err)
	}
	if len(inv) != 4 {
		t.Fatalf("%d entries, want 4: %+v", len(inv), inv)
	}
	byKind := map[string]v1alpha1.InventoryEntry{}
	for _, e := range inv {
		byKind[e.Kind] = e
	}
	if got := byKind["Service"].Namespace; got != "monitoring" {
		t.Errorf("Service.namespace = %q, want monitoring (the release default)", got)
	}
	if got := byKind["ConfigMap"].Namespace; got != "other-ns" {
		t.Errorf("ConfigMap.namespace = %q, want other-ns", got)
	}
	if got := byKind["ClusterRole"].Namespace; got != "" {
		t.Errorf("ClusterRole.namespace = %q, want empty (cluster-scoped)", got)
	}
	if got := byKind["Deployment"]; got.Group != "apps" || got.Version != "v1" {
		t.Errorf("Deployment GVK = %s/%s, want apps/v1", got.Group, got.Version)
	}
	if got := byKind["Service"]; got.Group != "" || got.Version != "v1" {
		t.Errorf("Service GVK = %q/%q, want \"\"/v1", got.Group, got.Version)
	}
}

func TestParseInventoryToleratesNoise(t *testing.T) {
	inv, err := parseInventory(`---
---
# only a comment
---
apiVersion: v1
kind: Service
metadata: {name: ok}
---
this: is not a k8s object
`, "ns")
	if err != nil {
		t.Fatalf("parseInventory: %v", err)
	}
	if len(inv) != 1 || inv[0].Kind != "Service" {
		t.Errorf("inventory = %+v, want only the Service entry", inv)
	}
}

func TestRefFromEnvValidates(t *testing.T) {
	t.Setenv(EnvTargetRef, "rondo-system/kps-acme")
	t.Setenv(EnvTargetKind, "Release")
	t.Setenv(EnvNamespace, "rondo-system")
	ref, kind, ns, err := RefFromEnv()
	if err != nil {
		t.Fatalf("RefFromEnv: %v", err)
	}
	if ref.Namespace != "rondo-system" || ref.Name != "kps-acme" || kind != "Release" || ns != "rondo-system" {
		t.Errorf("RefFromEnv = (%v, %q, %q)", ref, kind, ns)
	}

	for name, env := range map[string]map[string]string{
		"missing ref":  {EnvTargetKind: "Release"},
		"missing kind": {EnvTargetRef: "ns/name"},
		"ref no slash": {EnvTargetRef: "name", EnvTargetKind: "Release"},
		"empty ref ns": {EnvTargetRef: "/name", EnvTargetKind: "Release"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(EnvTargetRef, "")
			t.Setenv(EnvTargetKind, "")
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, _, _, err := RefFromEnv(); err == nil {
				t.Error("invalid reference accepted")
			}
		})
	}
}

func TestArtifactoryCredsBothOrNeither(t *testing.T) {
	t.Setenv(EnvArtifactoryUser, "")
	t.Setenv(EnvArtifactoryPassword, "")
	if u, p, err := artifactoryCreds(); err != nil || u != "" || p != "" {
		t.Errorf("anonymous mirror: artifactoryCreds = (%q, %q, %v)", u, p, err)
	}
	t.Setenv(EnvArtifactoryUser, "svc")
	if _, _, err := artifactoryCreds(); err == nil {
		t.Fatal("a username without a password accepted")
	} else if !strings.Contains(err.Error(), "envFrom") {
		t.Errorf("message = %q, want it to say where the credentials come from", err)
	}
	t.Setenv(EnvArtifactoryUser, "svc")
	t.Setenv(EnvArtifactoryPassword, "pw")
	if u, p, err := artifactoryCreds(); err != nil || u != "svc" || p != "pw" {
		t.Errorf("artifactoryCreds = (%q, %q, %v)", u, p, err)
	}
}

func TestDispatchRejectsUnknownVerb(t *testing.T) {
	err := Dispatch(t.Context(), "kube-apply", &Runtime{})
	if err == nil {
		t.Fatal("unimplemented verb accepted")
	}
	if !strings.Contains(err.Error(), "helm-apply") {
		t.Errorf("message = %q, want it to list the known verbs", err)
	}
}

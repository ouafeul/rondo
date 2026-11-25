package v1alpha1_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

const testNamespace = "default"

var testEnvCfg *rest.Config

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		requireEnvtestOrDie("api/v1alpha1")
		os.Exit(m.Run())
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "manifests", "crds")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting envtest: %v\n", err)
		os.Exit(1)
	}
	if err := checkAPIServerMatchesDeployed(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		_ = env.Stop()
		os.Exit(1)
	}
	testEnvCfg = cfg
	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stopping envtest: %v\n", err)
	}
	os.Exit(code)
}

func checkAPIServerMatchesDeployed(cfg *rest.Config) error {
	want, err := deployedK8sMinor()
	if err != nil {
		return err
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("discovery client: %w", err)
	}
	v, err := dc.ServerVersion()
	if err != nil {
		return fmt.Errorf("apiserver version: %w", err)
	}
	got := v.Major + "." + strings.TrimRight(v.Minor, "+")
	if got != want {
		return fmt.Errorf("envtest apiserver %s, but the platform deploys k8s %s "+
			"(binaries.k3s in manifests/dependencies.yaml).\n"+
			"A gate that tests a DIFFERENT version from the one shipped does not prove what "+
			"it claims to prove: a CEL rule accepted by a recent apiserver and "+
			"rejected by the deployed one would make the CRD unusable in production.\n"+
			"Fix:\n"+
			"  export KUBEBUILDER_ASSETS=\"$(setup-envtest use \"$(scripts/ci/envtest-version.sh)\" -p path)\"",
			got, want)
	}
	return nil
}

func deployedK8sMinor() (string, error) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifests", "dependencies.yaml"))
	if err != nil {
		return "", fmt.Errorf("reading the dependency manifest: %w", err)
	}
	var deps struct {
		Binaries []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"binaries"`
	}
	if err := yaml.Unmarshal(raw, &deps); err != nil {
		return "", fmt.Errorf("parsing the dependency manifest: %w", err)
	}
	for _, b := range deps.Binaries {
		if b.Name != "k3s" {
			continue
		}
		m := k8sMinorRe.FindStringSubmatch(b.Version)
		if m == nil {
			return "", fmt.Errorf("unreadable k3s version: %q", b.Version)
		}
		return m[1] + "." + m[2], nil
	}
	return "", fmt.Errorf("binaries.k3s entry missing from manifests/dependencies.yaml")
}

var k8sMinorRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.`)

func requireEnvtestOrDie(pkg string) {
	if os.Getenv("RONDO_REQUIRE_ENVTEST") != "1" {
		return
	}
	fmt.Fprintf(os.Stderr, "RONDO_REQUIRE_ENVTEST=1 but KUBEBUILDER_ASSETS is empty: "+
		"the envtest tests of %s would be skipped silently.\n"+
		"Set KUBEBUILDER_ASSETS:\n"+
		"  go install sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.25.3\n"+
		"  export KUBEBUILDER_ASSETS=\"$(setup-envtest use -p path)\"\n", pkg)
	os.Exit(1)
}

func startEnv(t *testing.T) client.Client {
	t.Helper()
	if testEnvCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS not set (see setup-envtest)")
	}
	c, err := client.New(testEnvCfg, client.Options{})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c
}

func object(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("invalid test YAML: %v", err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetNamespace(testNamespace)
	return u
}

type validationCase struct {
	name   string
	accept bool
	doc    string
}

func run(t *testing.T, c client.Client, cases []validationCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := c.Create(ctx, object(t, tc.doc), client.DryRunAll)
			switch {
			case tc.accept && err != nil:
				t.Errorf("should have been ACCEPTED, rejected by the apiserver: %v", err)
			case !tc.accept && err == nil:
				t.Error("should have been rejected, accepted by the apiserver: the schema lets this configuration through")
			case !tc.accept:
				t.Logf("rejected as expected: %v", err)
			}
		})
	}
}

func TestTargetValidation(t *testing.T) {
	c := startEnv(t)
	run(t, c, []validationCase{
		{"inCluster", true, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Target
metadata: {name: ok-mgmt}
spec: {kubernetes: {inCluster: true}}`},
		{"remote with kubeconfig, roles and site values", true, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Target
metadata: {name: ok-rke2}
spec:
  roles: [monitoring]
  kubernetes:
    server: "https://10.0.0.31:6443"
    kubeconfigSecretRef: {name: rke2-kubeconfig, key: kubeconfig}
  values: {ingressIP: "10.0.0.50", storageClass: longhorn}`},
		{"vsphere", true, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Target
metadata: {name: ok-dc1}
spec:
  vsphere: {credentialsSecretRef: {name: c}, settingsConfigMapRef: {name: s}}`},
		{"neither kubernetes nor vsphere", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Target
metadata: {name: ko-empty}
spec: {roles: [x]}`},
		{"kubernetes and vsphere together", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Target
metadata: {name: ko-two}
spec:
  kubernetes: {inCluster: true}
  vsphere: {}`},
		{"remote without kubeconfig", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Target
metadata: {name: ko-orphan}
spec: {kubernetes: {server: "https://10.0.0.31:6443"}}`},
		{"inCluster and kubeconfig together", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Target
metadata: {name: ko-both}
spec:
  kubernetes:
    inCluster: true
    kubeconfigSecretRef: {name: k, key: kubeconfig}`},
		{"server over http", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Target
metadata: {name: ko-http}
spec:
  kubernetes:
    server: "http://10.0.0.31:6443"
    kubeconfigSecretRef: {name: k, key: kubeconfig}`},
	})
}

func TestComponentValidation(t *testing.T) {
	c := startEnv(t)
	run(t, c, []validationCase{
		{"OCI chart and exact version", true, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ok-metallb}
spec:
  chart: {oci: "oci://arti.local/rondo-helm-local/metallb", version: "0.14.9"}
  namespace: metallb-system`},
		{"version with leading v", true, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ok-certmgr}
spec:
  chart: {oci: "oci://arti.local/rondo-helm-local/cert-manager", version: "v1.16.2"}
  namespace: cert-manager`},
		{"semver range", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ko-range}
spec:
  chart: {oci: "oci://arti.local/rondo-helm-local/metallb", version: "0.*"}
  namespace: metallb-system`},
		{"chart over https instead of oci", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ko-https}
spec:
  chart: {oci: "https://arti.local/rondo-helm-local/metallb", version: "0.14.9"}
  namespace: metallb-system`},
		{"invalid namespace", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ko-ns}
spec:
  chart: {oci: "oci://arti.local/rondo-helm-local/metallb", version: "0.14.9"}
  namespace: MetalLB_System`},
		{"chart without namespace", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ko-chart-no-ns}
spec:
  chart: {oci: "oci://arti.local/rondo-helm-local/metallb", version: "0.14.9"}`},
		{"vm without namespace", true, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ok-vm-no-ns}
spec:
  vm:
    groups:
      - name: app
        replicas: 1
        specs: {cpu: 2, memoryMb: 4096, diskGb: 40}`},
		{"valuesFrom without keys", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ko-joker}
spec:
  chart: {oci: "oci://arti.local/rondo-helm-local/x", version: "1.0.0"}
  namespace: default
  valuesFrom: [{secretRef: {name: s}}]`},
	})
}

func TestVMInstallerValidation(t *testing.T) {
	c := startEnv(t)
	run(t, c, []validationCase{
		{"sequence of four entry points", true, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ok-mft-phases}
spec:
  vm:
    groups: [{name: production, replicas: 1}]
    installer:
      version: "1.0.0"
      configFile: install.conf
      secretsFile: secrets.env
      configFlag: -c
      secretsFlag: -e
      phases:
        - {name: install, script: demo-mft/deploy/install.sh, args: ["--yes"], skipSteps: [repo]}
        - {name: harden, script: demo-mft/deploy/harden.sh}
        - {name: custom, script: demo-mft/deploy/custom.sh}
        - {name: groups, script: demo-mft/deploy/groups.sh, args: ["--yes"], timeout: 5m}`},
		{"short form with a single entry point", true, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ok-single-script}
spec:
  vm:
    groups: [{name: app, replicas: 1}]
    installer:
      version: "2.1.0"
      script: install.sh
      args: ["--non-interactive"]
      skipSteps: [repo]`},
		{"script AND phases", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ko-both-forms}
spec:
  vm:
    groups: [{name: app, replicas: 1}]
    installer:
      version: "1.0.0"
      script: install.sh
      phases: [{name: install, script: install.sh}]`},
		{"neither script nor phases", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ko-no-entry-point}
spec:
  vm:
    groups: [{name: app, replicas: 1}]
    installer:
      version: "1.0.0"`},
		{"installer-level args with phases", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ko-orphan-args}
spec:
  vm:
    groups: [{name: app, replicas: 1}]
    installer:
      version: "1.0.0"
      args: ["--yes"]
      phases: [{name: install, script: install.sh}]`},
		{"installer-level skipSteps with phases", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ko-orphan-skip}
spec:
  vm:
    groups: [{name: app, replicas: 1}]
    installer:
      version: "1.0.0"
      skipSteps: [repo]
      phases: [{name: install, script: install.sh}]`},
		{"unnamed phase", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ko-unnamed-phase}
spec:
  vm:
    groups: [{name: app, replicas: 1}]
    installer:
      version: "1.0.0"
      phases: [{script: install.sh}]`},
		{"invalid phase name", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ko-bad-phase-name}
spec:
  vm:
    groups: [{name: app, replicas: 1}]
    installer:
      version: "1.0.0"
      phases: [{name: Install_SH, script: install.sh}]`},
		{"time bound that is not a Go duration", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata: {name: ko-timeout}
spec:
  vm:
    groups: [{name: app, replicas: 1}]
    installer:
      version: "1.0.0"
      script: install.sh
      timeout: 45min`},
	})
}

func TestEnrollmentValidation(t *testing.T) {
	c := startEnv(t)
	run(t, c, []validationCase{
		{"enrollment on a role", true, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Enrollment
metadata: {name: ok-monitoring}
spec: {component: monitoring, target: monitoring}`},
		{"enrollment without component", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Enrollment
metadata: {name: ko-no-component}
spec: {target: monitoring}`},
		{"enrollment without target", false, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Enrollment
metadata: {name: ko-no-target}
spec: {component: monitoring}`},
	})
}

func TestDefaultsArePreserved(t *testing.T) {
	c := startEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("DeliveryPolicy", func(t *testing.T) {
		obj := object(t, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Enrollment
metadata: {name: defaults-enroll}
spec: {component: x, target: mgmt}`)
		if err := c.Create(ctx, obj, client.DryRunAll); err != nil {
			t.Fatalf("create: %v", err)
		}
		for _, want := range []struct {
			path []string
			val  bool
		}{
			{[]string{"spec", "policy", "createNamespace"}, true},
			{[]string{"spec", "policy", "atomic"}, true},
			{[]string{"spec", "policy", "wait"}, true},
			{[]string{"spec", "policy", "skipDryRunOnMissingCRD"}, false},
		} {
			got, found, err := unstructured.NestedBool(obj.Object, want.path...)
			if err != nil || !found {
				t.Errorf("%v: not defaulted (found=%v, err=%v)", want.path, found, err)
				continue
			}
			if got != want.val {
				t.Errorf("%v = %v, want %v", want.path, got, want.val)
			}
		}
		if got, found, _ := unstructured.NestedString(obj.Object, "spec", "policy", "timeout"); !found || got != "10m" {
			t.Errorf("timeout = %q (found=%v), want 10m", got, found)
		}
	})
}

func TestStatusIsASubresource(t *testing.T) {
	c := startEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	obj := object(t, `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Enrollment
metadata: {name: sub-enrollment}
spec: {component: c, target: t}
status:
  health: Healthy
  appliedChartVersion: "9.9.9"`)
	if err := c.Create(ctx, obj, client.DryRunAll); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got, found, _ := unstructured.NestedString(obj.Object, "status", "health"); found && got != "" {
		t.Errorf("status.health = %q kept on create: status is not a subresource", got)
	}
}

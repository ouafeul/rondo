package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScalarWhereObjectExpected(t *testing.T) {
	requireError(t, validate(t, map[string]string{
		"components/demo.yaml": `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: demo
spec: "a chart"
`,
	}), "spec")
}

func TestArrayWhereObjectExpected(t *testing.T) {
	requireError(t, validate(t, map[string]string{
		"components/demo.yaml": `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: demo
spec:
  chart:
    - oci: oci://arti.local/rondo-helm-local/demo
      version: 1.2.3
  namespace: demo
`,
	}), "chart")
}

func TestObjectWhereArrayExpected(t *testing.T) {
	requireError(t, validate(t, map[string]string{
		"components/demo.yaml": `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: demo
spec:
  chart:
    oci: oci://arti.local/rondo-helm-local/demo
    version: 1.2.3
  namespace: demo
  dependsOn:
    name: other
`,
	}), "dependsOn")
}

func TestUnknownFieldInsideArrayItem(t *testing.T) {
	requireError(t, validate(t, map[string]string{
		"components/demo.yaml": `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: demo
spec:
  chart:
    oci: oci://arti.local/rondo-helm-local/demo
    version: 1.2.3
  namespace: demo
  valuesFrom:
    - secretRef:
        name: s
      keyList: [a]
`,
	}), "keyList")
}

func TestBooleanWhereStringExpected(t *testing.T) {
	requireError(t, validate(t, map[string]string{
		"components/demo.yaml": `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: demo
spec:
  chart:
    oci: oci://arti.local/rondo-helm-local/demo
    version: 1.2.3
  namespace: true
`,
	}), "namespace")
}

func TestUnknownFieldInVMSpec(t *testing.T) {
	requireError(t, validate(t, map[string]string{
		"components/mft.yaml": `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: mft
spec:
  vm:
    template: rhel9
    groupList:
      - name: app
        replicas: 1
`,
	}), "groupList")
}

func TestValuesAreNotSchemaChecked(t *testing.T) {
	requireNoError(t, validate(t, map[string]string{
		"components/demo.yaml": `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: demo
spec:
  chart:
    oci: oci://arti.local/rondo-helm-local/demo
    version: 1.2.3
  namespace: demo
  values:
    nimporte:
      quoi: [1, 2, 3]
    unBool: true
`,
		"enrollments/demo.yaml": chartEnrollment,
	}))
}

func TestBothChartAndVMIsAnError(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/demo.yaml": `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: demo
spec:
  chart:
    oci: oci://arti.local/rondo-helm-local/demo
    version: 1.2.3
  namespace: demo
  vm:
    template: rhel9
    groups:
      - name: app
        replicas: 1
`,
	})
	if !HasErrors(findings) {
		t.Fatalf("chart AND vm accepted. Diagnostics: %s", render(findings))
	}
}

func TestNeitherChartNorVMIsAnError(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/empty.yaml": `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: empty
spec:
  namespace: demo
`,
	})
	if !HasErrors(findings) {
		t.Fatalf("Component with neither chart nor vm accepted. Diagnostics: %s", render(findings))
	}
}

func TestLoadDependenciesReadsHelmCharts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dependencies.yaml")
	if err := os.WriteFile(path, []byte(`helm_charts:
  - name: metallb
    version: 0.14.9
    kind: chart
    path: metallb
  - name: cert-manager
    version: v1.16.2
binaries:
  - name: tofu
`), 0o600); err != nil {
		t.Fatal(err)
	}
	deps, err := LoadDependencies(path)
	if err != nil {
		t.Fatalf("LoadDependencies: %v", err)
	}
	if len(deps.Charts) != 2 {
		t.Fatalf("%d charts, want 2: %+v", len(deps.Charts), deps.Charts)
	}
	if deps.Charts["metallb"].Version != "0.14.9" {
		t.Errorf("metallb = %+v", deps.Charts["metallb"])
	}
}

func TestLoadDependenciesReadsArtifactsAndRPMRepos(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dependencies.yaml")
	if err := os.WriteFile(path, []byte(`helm_charts:
  - name: metallb
    version: 0.14.9
binaries:
  - name: rke2
    version: "v1.34.5+rke2r1"
    kind: file
    path: "rke2/{version}/rke2.linux-amd64"
grafana_plugins:
  - name: vl-datasource
    version: "v0.26.3"
    kind: grafana_plugin
    path: "grafana-plugins/{version}/plugin.zip"
rpm_packages:
  - name: httpd
    version: el9
    kind: rpm
    path: el9
`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := LoadDependencies(path)
	if err != nil {
		t.Fatalf("LoadDependencies: %v", err)
	}
	for _, want := range []string{
		"rke2/v1.34.5+rke2r1/rke2.linux-amd64",
		"grafana-plugins/v0.26.3/plugin.zip",
	} {
		if !m.Artifacts[want] {
			t.Errorf("artifact %q missing from the mirrored surface: %+v", want, m.Artifacts)
		}
	}
	if !m.RPMPaths["el9"] {
		t.Errorf("RPM repository path missing: %+v", m.RPMPaths)
	}
}

func TestLoadDependenciesRefusesEmptyManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deps.yaml")
	if err := os.WriteFile(path, []byte("binaries:\n  - name: tofu\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDependencies(path); err == nil {
		t.Fatal("manifest without helm_charts accepted")
	}
}

func TestLoadDependenciesRejectsUnreadable(t *testing.T) {
	if _, err := LoadDependencies(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("missing file accepted")
	}
	path := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(path, []byte("helm_charts: [yes: no: maybe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDependencies(path); err == nil {
		t.Fatal("unreadable YAML accepted")
	}
}

func TestValidateDirWarnsOnUnreadableDeps(t *testing.T) {
	root := writeTree(t, map[string]string{
		"components/demo.yaml":  chartComponent,
		"enrollments/demo.yaml": chartEnrollment,
	})
	findings, err := ValidateDir(root, crdDir(t),
		filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("ValidateDir: %v", err)
	}
	if HasErrors(findings) {
		t.Fatalf("missing manifest treated as an error: %s", render(findings))
	}
	if !strings.Contains(render(findings), "NOT verified") {
		t.Errorf("the missing verification is not reported: %s", render(findings))
	}
}

func TestValidateDirRejectsMissingCRDDir(t *testing.T) {
	root := writeTree(t, map[string]string{"components/demo.yaml": chartComponent})
	_, err := ValidateDir(root, filepath.Join(t.TempDir(), "absent"), "")
	if err == nil {
		t.Fatal("missing CRD directory accepted")
	}
	if !strings.Contains(err.Error(), "gen-crds.sh") {
		t.Errorf("message = %q, must say how to generate the CRDs", err)
	}
}

func TestFindingStringIsEditorFriendly(t *testing.T) {
	f := Finding{Path: "components/demo.yaml", Level: LevelError, Message: "unknown field"}
	got := f.String()
	if !strings.HasPrefix(got, "components/demo.yaml: ") {
		t.Errorf("String() = %q", got)
	}
	if !strings.Contains(got, "unknown field") {
		t.Errorf("String() = %q", got)
	}
}

func TestHasErrorsIgnoresWarnings(t *testing.T) {
	if HasErrors([]Finding{{Level: LevelWarning, Message: "x"}}) {
		t.Error("a warning counts as an error")
	}
	if !HasErrors([]Finding{{Level: LevelWarning}, {Level: LevelError}}) {
		t.Error("an error is not detected")
	}
	if HasErrors(nil) {
		t.Error("HasErrors(nil) = true")
	}
}

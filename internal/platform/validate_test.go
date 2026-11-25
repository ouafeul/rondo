package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func crdDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "manifests", "crds")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("CRDs missing (%s), run scripts/ci/gen-crds.sh", dir)
	}
	return dir
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func validate(t *testing.T, files map[string]string) []Finding {
	t.Helper()
	findings, err := ValidateDir(writeTree(t, files), crdDir(t), "")
	if err != nil {
		t.Fatalf("ValidateDir: %v", err)
	}
	return findings
}

func requireError(t *testing.T, findings []Finding, substr string) {
	t.Helper()
	for _, f := range findings {
		if f.Level == LevelError && strings.Contains(f.Message, substr) {
			return
		}
	}
	t.Fatalf("no error containing %q. Diagnostics: %s", substr, render(findings))
}

func requireNoError(t *testing.T, findings []Finding) {
	t.Helper()
	if HasErrors(findings) {
		t.Fatalf("unexpected errors: %s", render(findings))
	}
}

func render(findings []Finding) string {
	var b strings.Builder
	for _, f := range findings {
		b.WriteString("\n  " + f.String())
	}
	if b.Len() == 0 {
		return " (none)"
	}
	return b.String()
}

const chartComponent = `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: demo
spec:
  chart:
    oci: oci://arti.local/rondo-helm-local/demo
    version: 1.2.3
  namespace: demo
`

const chartEnrollment = `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Enrollment
metadata:
  name: demo
spec:
  component: demo
  target: management
`

func TestValidTreePasses(t *testing.T) {
	requireNoError(t, validate(t, map[string]string{
		"components/demo.yaml":  chartComponent,
		"enrollments/demo.yaml": chartEnrollment,
	}))
}

func TestSingleFileWithBothObjects(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/demo.yaml": chartComponent + "---\n" + chartEnrollment,
	})
	requireNoError(t, findings)
}

func TestUnknownFieldIsAnError(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/demo.yaml": strings.Replace(chartComponent,
			"  namespace: demo", "  namespace: demo\n  namspace: typo", 1),
		"enrollments/demo.yaml": chartEnrollment,
	})
	requireError(t, findings, "unknown field")
	requireError(t, findings, "namespace")
}

func TestMissingComponentRef(t *testing.T) {
	findings := validate(t, map[string]string{
		"enrollments/demo.yaml": chartEnrollment,
	})
	requireError(t, findings, `Component "demo", which does not exist`)
}

func TestComponentWithoutEnrollmentWarns(t *testing.T) {
	findings := validate(t, map[string]string{"components/demo.yaml": chartComponent})
	requireNoError(t, findings)
	var found bool
	for _, f := range findings {
		if f.Level == LevelWarning && strings.Contains(f.Message, "enrolled by no Enrollment") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no warning about the unenrolled component: %s", render(findings))
	}
}

func TestDuplicateObjectIsAnError(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/demo.yaml":  chartComponent,
		"components/demo2.yaml": chartComponent,
		"enrollments/demo.yaml": chartEnrollment,
	})
	requireError(t, findings, "already declared")
}

func TestDependsOnCycle(t *testing.T) {
	comp := func(name, dep string) string {
		return `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: ` + name + `
spec:
  chart:
    oci: oci://arti.local/rondo-helm-local/` + name + `
    version: 1.0.0
  namespace: demo
  dependsOn:
  - name: ` + dep + `
`
	}
	findings := validate(t, map[string]string{
		"components/a.yaml": comp("a", "b"),
		"components/b.yaml": comp("b", "a"),
	})
	requireError(t, findings, "dependency cycle")
}

func TestDependsOnAnotherTreeOnlyWarns(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/demo.yaml": strings.Replace(chartComponent,
			"  namespace: demo", "  namespace: demo\n  dependsOn:\n  - name: traefik", 1),
	})
	requireNoError(t, findings)
	for _, f := range findings {
		if strings.Contains(f.Message, `depends on "traefik"`) {
			return
		}
	}
	t.Fatalf("no warning about the dependency outside the tree: %s", render(findings))
}

func TestDependsOnSelf(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/demo.yaml": strings.Replace(chartComponent,
			"  namespace: demo", "  namespace: demo\n  dependsOn:\n  - name: demo", 1),
	})
	requireError(t, findings, "depends on itself")
}

func TestUndeclaredSecretToken(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/demo.yaml": strings.Replace(chartComponent,
			"  namespace: demo",
			"  namespace: demo\n  values:\n    adminPassword: ${secrets.grafanaAdminPassword}", 1),
		"enrollments/demo.yaml": chartEnrollment,
	})
	requireError(t, findings, "${secrets.grafanaAdminPassword}")
}

func TestDeclaredSecretTokenPasses(t *testing.T) {
	requireNoError(t, validate(t, map[string]string{
		"components/demo.yaml": strings.Replace(chartComponent,
			"  namespace: demo",
			`  namespace: demo
  valuesFrom:
  - secretRef:
      name: rondo-platform-values
    keys: [grafanaAdminPassword]
  values:
    adminPassword: ${secrets.grafanaAdminPassword}`, 1),
		"enrollments/demo.yaml": chartEnrollment,
	}))
}

func TestUnknownTargetTokenIsAnError(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/demo.yaml": strings.Replace(chartComponent,
			"  namespace: demo", "  namespace: demo\n  values:\n    x: ${target.unknown}", 1),
		"enrollments/demo.yaml": chartEnrollment,
	})
	requireError(t, findings, "allowed target tokens")
}

func TestPlatformPlaceholdersArePreserved(t *testing.T) {
	requireNoError(t, validate(t, map[string]string{
		"components/demo.yaml": `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: demo
spec:
  chart:
    oci: oci://${OCI_REGISTRY}/${CHARTS_REPOSITORY}/demo
    version: 1.0.0
  namespace: demo
  values:
    image:
      registry: ${OCI_REGISTRY}/${IMAGES_REPOSITORY}
`,
		"enrollments/demo.yaml": chartEnrollment,
	}))
}

func TestGrafanaTemplateVarsArePreserved(t *testing.T) {
	requireNoError(t, validate(t, map[string]string{
		"components/demo.yaml": strings.Replace(chartComponent,
			"  namespace: demo",
			"  namespace: demo\n  values:\n    expr: sum(rate(x[${__interval}]))", 1),
		"enrollments/demo.yaml": chartEnrollment,
	}))
}

func TestUnknownKindIsAnError(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/x.yaml": `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Machin
metadata:
  name: x
`,
	})
	requireError(t, findings, "unknown to the platform")
}

func TestWrongAPIVersionIsAnError(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/demo.yaml": strings.Replace(chartComponent,
			"apiVersion: rondo.ouafeul.github.io/v1alpha1", "apiVersion: apps/v1", 1),
	})
	requireError(t, findings, "want rondo.ouafeul.github.io/v1alpha1")
}

func TestWrongTypeIsAnError(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/demo.yaml": vmComponent(`
    - name: app
      replicas: "two"`),
	})
	requireError(t, findings, "integer expected")
}

func vmComponent(groups string) string { return vmComponentNamed("demo", groups) }

func vmComponentNamed(name, groups string) string {
	return `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: ` + name + `
spec:
  vm:
    groups:` + groups + `
`
}

func TestVMComponentPasses(t *testing.T) {
	requireNoError(t, validate(t, map[string]string{
		"components/demo.yaml": vmComponent(`
    - name: app
      replicas: 1`),
		"enrollments/demo.yaml": chartEnrollment,
	}))
}

func TestVMConfigSecretTokenIsAnError(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/demo.yaml": vmComponent(`
    - name: app
      replicas: 1`) + `    config:
      DB_PASSWORD:
        value: ${secrets.dbPassword}
`,
		"enrollments/demo.yaml": chartEnrollment,
	})
	requireError(t, findings, "${secrets.dbPassword}")
	requireError(t, findings, "installer.secretsFile")
}

func TestVMGroupConfigSecretTokenIsAnError(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/demo.yaml": vmComponent(`
    - name: app
      replicas: 1
      config:
        JOIN_TOKEN:
          list:
          - ${secrets.joinToken}`),
		"enrollments/demo.yaml": chartEnrollment,
	})
	requireError(t, findings, "${secrets.joinToken}")
	requireError(t, findings, "installer.secretsFile")
}

func TestChartValuesSecretTokenPasses(t *testing.T) {
	requireNoError(t, validate(t, map[string]string{
		"components/demo.yaml": strings.Replace(chartComponent, "  namespace: demo",
			`  namespace: demo
  values:
    adminPassword: ${secrets.adminPassword}
  valuesFrom:
  - secretRef:
      name: demo-secrets
    keys:
    - adminPassword`, 1),
		"enrollments/demo.yaml": chartEnrollment,
	}))
}

func TestStatusInTreeWarns(t *testing.T) {
	findings := validate(t, map[string]string{
		"components/demo.yaml":  chartComponent,
		"enrollments/demo.yaml": chartEnrollment + "status: {}\n",
	})
	requireNoError(t, findings)
	var found bool
	for _, f := range findings {
		if strings.Contains(f.Message, "OBSERVED state") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no warning about the status: %s", render(findings))
	}
}

func TestMisplacedFileWarns(t *testing.T) {
	findings := validate(t, map[string]string{
		"enrollments/demo.yaml": chartComponent,
	})
	var found bool
	for _, f := range findings {
		if f.Level == LevelWarning && strings.Contains(f.Message, "filed under") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no misplaced-file warning: %s", render(findings))
	}
}

func mirrorOf(charts map[string]Dependency, artifacts, rpmPaths []string) *Mirror {
	m := &Mirror{
		Charts:    charts,
		Artifacts: map[string]bool{},
		RPMPaths:  map[string]bool{},
	}
	if m.Charts == nil {
		m.Charts = map[string]Dependency{}
	}
	for _, a := range artifacts {
		m.Artifacts[a] = true
	}
	for _, r := range rpmPaths {
		m.RPMPaths[r] = true
	}
	return m
}

func TestChartVersionMustMatchDependencies(t *testing.T) {
	charts := map[string]Dependency{"demo": {Name: "demo", Version: "1.2.3"}}
	tree, err := Load(writeTree(t, map[string]string{"components/demo.yaml": chartComponent}))
	if err != nil {
		t.Fatal(err)
	}
	if got := tree.ValidateDependencies(mirrorOf(charts, nil, nil)); len(got) != 0 {
		t.Fatalf("identical version rejected: %s", render(got))
	}

	charts["demo"] = Dependency{Name: "demo", Version: "9.9.9"}
	requireError(t, tree.ValidateDependencies(mirrorOf(charts, nil, nil)),
		"only the mirrored version exists")
}

func TestChartAbsentFromDependencies(t *testing.T) {
	tree, err := Load(writeTree(t, map[string]string{"components/demo.yaml": chartComponent}))
	if err != nil {
		t.Fatal(err)
	}
	findings := tree.ValidateDependencies(mirrorOf(map[string]Dependency{
		"other": {Name: "other", Version: "1.0.0"},
	}, nil, nil))
	requireError(t, findings, "it will NOT be mirrored")
}

func TestFirstPartyChartsSkipDependencies(t *testing.T) {
	comp := strings.Replace(chartComponent,
		"oci://arti.local/rondo-helm-local/demo",
		"oci://arti.local/rondo-helm-local/downstream-bootstrap", 1)
	tree, err := Load(writeTree(t, map[string]string{"components/db.yaml": comp}))
	if err != nil {
		t.Fatal(err)
	}
	if got := tree.ValidateDependencies(mirrorOf(nil, nil, nil)); len(got) != 0 {
		t.Fatalf("a first-party chart was required in dependencies.yaml: %s", render(got))
	}
}

const vmMirrorComponent = `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: demo
spec:
  vm:
    groups:
      - name: app
        replicas: 1
    rpmRepos:
      - name: rondo-rpms
        repo: rondo-rpms-local
        path: el9
        gpgCheck: false
    artifacts:
      - repo: rondo-generic-local
        path: demo/1.0.0/demo.tar.gz
`

func vmTree(t *testing.T) *Tree {
	t.Helper()
	tree, err := Load(writeTree(t, map[string]string{"components/demo.yaml": vmMirrorComponent}))
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestVMArtifactMustBeMirrored(t *testing.T) {
	tree := vmTree(t)
	findings := tree.ValidateDependencies(mirrorOf(nil,
		[]string{"other/2.0.0/other.tar.gz"},
		[]string{"el9"}))
	requireError(t, findings, "demo/1.0.0/demo.tar.gz")
	requireError(t, findings, "spec.vm.artifacts[0]")
}

func TestVMArtifactMirroredPasses(t *testing.T) {
	tree := vmTree(t)
	got := tree.ValidateDependencies(mirrorOf(nil,
		[]string{"demo/1.0.0/demo.tar.gz"},
		[]string{"el9"}))
	if HasErrors(got) {
		t.Fatalf("mirrored artifact rejected: %s", render(got))
	}
}

func vmArtifactComponent(name, provenance, path string) string {
	art := `      - repo: rondo-generic-local
        path: ` + path + `
`
	if provenance != "" {
		art += "        provenance: " + provenance + "\n"
	}
	return `apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: ` + name + `
spec:
  vm:
    groups:
      - name: app
        replicas: 1
    artifacts:
` + art
}

func artifactTree(t *testing.T, doc string) *Tree {
	t.Helper()
	tree, err := Load(writeTree(t, map[string]string{"components/demo.yaml": doc}))
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func emptyMirror() *Mirror {
	return mirrorOf(nil, nil, nil)
}

func TestUpstreamArtifactMustBeMirrored(t *testing.T) {
	tree := artifactTree(t, vmArtifactComponent("demo", "", "nextcloud/33.0.0/nc.tar.bz2"))
	findings := tree.ValidateDependencies(emptyMirror())
	requireError(t, findings, "provenance `upstream` (the default)")
	requireError(t, findings, "provenance: component")

	tree = artifactTree(t, vmArtifactComponent("demo", "upstream", "nextcloud/33.0.0/nc.tar.bz2"))
	requireError(t, tree.ValidateDependencies(emptyMirror()), "will NOT be mirrored")

	m := emptyMirror()
	m.Artifacts["nextcloud/33.0.0/nc.tar.bz2"] = true
	if got := tree.ValidateDependencies(m); HasErrors(got) {
		t.Fatalf("mirrored upstream COTS rejected: %s", render(got))
	}
}

func TestComponentArtifactMustFollowTheConvention(t *testing.T) {
	tree := artifactTree(t, vmArtifactComponent("demo", "component", "demo/1.0.0/demo.tar.gz"))
	findings := tree.ValidateDependencies(emptyMirror())
	requireError(t, findings, "does not follow the publishing convention")
	requireError(t, findings, "published by the component's CI")

	tree = artifactTree(t, vmArtifactComponent("demo", "component",
		"components/another/1.0.0/another.tar.gz"))
	requireError(t, tree.ValidateDependencies(emptyMirror()), "publishing convention")

	tree = artifactTree(t, vmArtifactComponent("demo", "component",
		"components/demo/1.0.0/demo.tar.gz"))
	if got := tree.ValidateDependencies(emptyMirror()); HasErrors(got) {
		t.Fatalf("conforming first-party artifact rejected: %s", render(got))
	}
}

func TestVMRPMRepoMustBeFed(t *testing.T) {
	tree := vmTree(t)
	findings := tree.ValidateDependencies(mirrorOf(nil,
		[]string{"demo/1.0.0/demo.tar.gz"},
		[]string{"el8"}))
	requireError(t, findings, `package repository path "el9"`)
}

func TestComponentArtifactConventionSurvivesAPlaceholderRepo(t *testing.T) {
	portable := func(path string) string {
		return strings.Replace(
			vmArtifactComponent("demo", "component", path),
			"repo: rondo-generic-local", "repo: ${FILES_REPOSITORY}", 1)
	}

	findings := artifactTree(t, portable("components/another/1.0.0/x.tar.gz")).
		ValidateDependencies(emptyMirror())
	requireError(t, findings, "publishing convention")

	findings = artifactTree(t, portable("components/demo/1.0.0/demo.tar.gz")).
		ValidateDependencies(emptyMirror())
	if HasErrors(findings) {
		t.Fatalf("conforming artifact with a portable repository rejected: %s", render(findings))
	}
	for _, f := range findings {
		if strings.Contains(f.Message, "NOT verified") {
			t.Errorf("coverage warning on a conforming first-party "+
				"artifact: %s", render(findings))
		}
	}
}

func TestVMPlaceholderIsNotVerified(t *testing.T) {
	comp := strings.Replace(vmMirrorComponent,
		"path: demo/1.0.0/demo.tar.gz", "path: demo/${DEMO_VERSION}/demo.tar.gz", 1)
	tree, err := Load(writeTree(t, map[string]string{"components/demo.yaml": comp}))
	if err != nil {
		t.Fatal(err)
	}
	findings := tree.ValidateDependencies(mirrorOf(nil, nil, []string{"el9"}))
	if HasErrors(findings) {
		t.Fatalf("placeholder treated as an error: %s", render(findings))
	}
	var warned bool
	for _, f := range findings {
		if f.Level == LevelWarning && strings.Contains(f.Message, "NOT verified") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("no warning about the placeholder: %s", render(findings))
	}
}

func TestVersionComparisonToleratesVPrefix(t *testing.T) {
	if !sameVersion("v1.16.2", "1.16.2") {
		t.Error("v1.16.2 and 1.16.2 considered different")
	}
	if sameVersion("1.16.2", "1.16.3") {
		t.Error("different versions considered identical")
	}
}

func TestChartNameFromOCI(t *testing.T) {
	cases := map[string]string{
		"oci://arti.local/rondo-helm-local/metallb":  "metallb",
		"oci://${OCI_REGISTRY}/${REPO}/cert-manager": "cert-manager",
		"${CHARTS_OCI}/traefik":                      "traefik",
		"${CHARTS_OCI}/":                             "",
		"https://arti.local/x":                       "",
		"oci://arti.local/":                          "",
	}
	for in, want := range cases {
		if got := chartNameFromOCI(in); got != want {
			t.Errorf("chartNameFromOCI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitDocumentsKeepsLiteralBlocks(t *testing.T) {
	doc := `kind: Component
spec:
  values:
    config: |
      [INPUT]
      ---
      Name tail
`
	if got := splitDocuments(doc); len(got) != 1 {
		t.Fatalf("%d documents, want 1 (an indented --- is not a separator)", len(got))
	}
	if got := splitDocuments("a: 1\n---\nb: 2\n"); len(got) != 2 {
		t.Fatalf("%d documents, want 2", len(got))
	}
}

func TestLoadRejectsBrokenYAML(t *testing.T) {
	root := writeTree(t, map[string]string{"components/x.yaml": "spec:\n  a: [1,\n"})
	if _, err := Load(root); err == nil {
		t.Fatal("broken YAML accepted")
	}
}

func TestValidateDirRejectsEmptyTree(t *testing.T) {
	if _, err := ValidateDir(t.TempDir(), crdDir(t), ""); err == nil {
		t.Fatal("empty tree accepted")
	}
}

func TestLoadSchemasFromDirRejectsMissingDir(t *testing.T) {
	if _, err := LoadSchemasFromDir(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing CRD directory accepted")
	}
}

func TestEditDistanceSuggestsClosestField(t *testing.T) {
	props := map[string]*Schema{"namespace": {}, "releaseName": {}, "values": {}}
	if got := suggest("namspace", props); !strings.Contains(got, "namespace") {
		t.Errorf("suggest(namspace) = %q", got)
	}
	if got := suggest("zzzzzzzzzzzz", props); got != "" {
		t.Errorf("suggest(zzzz) = %q, want no suggestion", got)
	}
}

func TestLoadIgnoresWhatIsNotAManifestDir(t *testing.T) {
	root := writeTree(t, map[string]string{
		"components/demo.yaml": chartComponent,
		"helmfile.yaml":        "repositories:\n  - url: {{ .Values.foo }}\n",
		"charts/tpl/dep.yaml":  "kind: Deployment\n{{- if .Values.x }}\n",
		".hidden/note.yml":     "a:\n- 1\n",
		"platform.yaml":        "minRondoVersion: 0.1.0\n",
	})

	tree, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(tree.Objects) != 1 {
		var got []string
		for _, o := range tree.Objects {
			got = append(got, o.Path)
		}
		t.Fatalf("loaded objects = %v, want only components/demo.yaml", got)
	}
}

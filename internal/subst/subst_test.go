package subst

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSite() map[string]string {
	return map[string]string{
		"REGISTRY_HOST":            "arti.local",
		"ARTIFACTORY_URL":          "https://arti.local/artifactory",
		"ARTIFACTORY_HELM_REPO":    "rondo-helm-local",
		"ARTIFACTORY_DOCKER_REPO":  "rondo-docker-local",
		"ARTIFACTORY_GENERIC_REPO": "rondo-generic-local",
		"ARTIFACTORY_RPM_REPO":     "rondo-rpms-multi-local",
	}
}

func lookupIn(site map[string]string) func(string) string {
	return func(name string) string { return site[name] }
}

func TestSubstituteConcretizesPlatformVars(t *testing.T) {
	values := Values(lookupIn(testSite()))
	in := "oci: oci://${REGISTRY_HOST}/${ARTIFACTORY_HELM_REPO}/metallb\n"
	out, changed, err := substitute(in, values)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if !changed {
		t.Error("no change reported")
	}
	if want := "oci://arti.local/rondo-helm-local/metallb"; !strings.Contains(out, want) {
		t.Errorf("output = %q, must contain %q", out, want)
	}
}

func TestSubstituteBuildsDownloadURLWithoutHardcodedContextPath(t *testing.T) {
	values := Values(lookupIn(testSite()))
	in := "baseurl: ${ARTIFACTORY_URL}/${ARTIFACTORY_RPM_REPO}/mft\n"
	out, _, err := substitute(in, values)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	const want = "https://arti.local/artifactory/rondo-rpms-multi-local/mft"
	if !strings.Contains(out, want) {
		t.Errorf("output = %q, must contain %q", out, want)
	}
	if strings.Contains(out, "artifactory//") {
		t.Errorf("double slash in %q, the site URL must be normalised at load time", out)
	}
}

func TestSubstituteLeavesOtherLevelsIntact(t *testing.T) {
	values := Values(lookupIn(testSite()))
	in := "storageClassName: ${target.values.storageClass}\n" +
		"adminPassword: ${secrets.grafanaAdminPassword}\n" +
		"expr: sum(rate(x[${__interval}]))\n"
	out, changed, err := substitute(in, values)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if changed {
		t.Error("change reported although no platform placeholder is present")
	}
	if out != in {
		t.Errorf("document altered:\n got %q\nwant %q", out, in)
	}
}

func TestSubstituteRejectsUnknownPlaceholder(t *testing.T) {
	values := Values(lookupIn(testSite()))
	if _, _, err := substitute("host: ${VICTORIA_LOGS_HOSTNAME}\n", values); err == nil {
		t.Fatal("unknown placeholder accepted")
	} else if !strings.Contains(err.Error(), "VICTORIA_LOGS_HOSTNAME") {
		t.Errorf("message = %q, must name the faulty placeholder", err)
	}
}

func TestSubstituteRejectsGrafanaDatasourceShape(t *testing.T) {
	values := Values(lookupIn(testSite()))
	_, _, err := substitute("datasource: ${DS_PROMETHEUS}\n", values)
	if err == nil {
		t.Fatal("${DS_PROMETHEUS} accepted: the shape check was loosened, and " +
			"a misspelled ${ARTIFACTORY_URLL} would pass with it")
	}
	if !strings.Contains(err.Error(), "DS_PROMETHEUS") {
		t.Errorf("message = %q, must name the faulty token", err)
	}
	out, changed, err := substitute("expr: sum(rate(x[${__interval}]))\n", values)
	if err != nil {
		t.Fatalf("${__interval} rejected: %v", err)
	}
	if changed || !strings.Contains(out, "${__interval}") {
		t.Errorf("${__interval} altered: %q", out)
	}
}

func TestValuesOmitsUnsetVars(t *testing.T) {
	values := Values(lookupIn(map[string]string{
		"REGISTRY_HOST":         "arti.local",
		"ARTIFACTORY_HELM_REPO": "",
		"ARTIFACTORY_RPM_REPO":  "   ",
	}))
	for _, absent := range []string{
		"${ARTIFACTORY_HELM_REPO}", "${ARTIFACTORY_DOCKER_REPO}", "${ARTIFACTORY_RPM_REPO}",
	} {
		if _, ok := values[absent]; ok {
			t.Errorf("empty %s must NOT enter the values (it would substitute the empty string)", absent)
		}
	}
	if values["${REGISTRY_HOST}"] != "arti.local" {
		t.Errorf("${REGISTRY_HOST} = %q, want arti.local", values["${REGISTRY_HOST}"])
	}
}

func TestApplyFailsOnReferencedUnsetVar(t *testing.T) {
	site := testSite()
	site["ARTIFACTORY_RPM_REPO"] = ""
	values := Values(lookupIn(site))

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.yaml"),
		[]byte("oci: oci://${REGISTRY_HOST}/${ARTIFACTORY_HELM_REPO}/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(dir, values); err != nil {
		t.Fatalf("unset but unreferenced variable: must be tolerated, got %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"),
		[]byte("baseurl: https://h/artifactory/${ARTIFACTORY_RPM_REPO}/mft\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(dir, values)
	if err == nil {
		t.Fatal("referenced placeholder without a variable: must FAIL")
	}
	for _, want := range []string{"bad.yaml", "ARTIFACTORY_RPM_REPO"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message = %q, must name %s", err, want)
		}
	}
}

func TestApplySkipsGitAndNonManifests(t *testing.T) {
	values := Values(lookupIn(testSite()))
	root := t.TempDir()
	files := map[string]string{
		"platform/components/a.yaml": "oci: oci://${REGISTRY_HOST}/x/a\n",
		"README.md":                  "see ${REGISTRY_HOST}\n",
		".git/config":                "url = ${REGISTRY_HOST}\n",
		".git/refs/keep.yaml":        "url = ${REGISTRY_HOST}\n",
	}
	for rel, body := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	touched, err := Apply(root, values)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if touched != 1 {
		t.Errorf("files touched = %d, want 1 (the only manifest)", touched)
	}
	got, err := os.ReadFile(filepath.Join(root, "platform/components/a.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "arti.local") {
		t.Errorf("manifest not substituted: %s", got)
	}
	for _, rel := range []string{"README.md", ".git/config", ".git/refs/keep.yaml"} {
		raw, rerr := os.ReadFile(filepath.Join(root, rel))
		if rerr != nil {
			t.Fatal(rerr)
		}
		if !strings.Contains(string(raw), "${REGISTRY_HOST}") {
			t.Errorf("%s was substituted although it must not be: %s", rel, raw)
		}
	}
}

func TestCheckDetectsWithoutWriting(t *testing.T) {
	site := testSite()
	site["ARTIFACTORY_RPM_REPO"] = ""
	values := Values(lookupIn(site))

	root := t.TempDir()
	const ok = "oci: oci://${REGISTRY_HOST}/${ARTIFACTORY_HELM_REPO}/x\n"
	okPath := filepath.Join(root, "ok.yaml")
	if err := os.WriteFile(okPath, []byte(ok), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Check(root, values); err != nil {
		t.Fatalf("Check on a resolvable tree: %v", err)
	}
	raw, err := os.ReadFile(okPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != ok {
		t.Errorf("Check REWROTE the source file:\n got %q\nwant %q", raw, ok)
	}

	if err := os.WriteFile(filepath.Join(root, "bad.yaml"),
		[]byte("baseurl: ${ARTIFACTORY_RPM_REPO}/mft\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = Check(root, values)
	if err == nil {
		t.Fatal("Check silent on an unresolvable token")
	}
	if !strings.Contains(err.Error(), "bad.yaml") {
		t.Errorf("message = %q, must name the faulty file", err)
	}
}

func TestPlaceholdersCoversWhitelist(t *testing.T) {
	got := Placeholders()
	if len(got) != len(substitutionVars) {
		t.Fatalf("Placeholders() returns %d entries for %d whitelisted variables",
			len(got), len(substitutionVars))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("Placeholders() not sorted: %q before %q", got[i-1], got[i])
		}
	}
	for _, p := range got {
		if _, ok := substitutionVars[p]; !ok {
			t.Errorf("Placeholders() returns %q, which is not in the whitelist", p)
		}
	}
}

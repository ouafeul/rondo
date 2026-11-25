package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rondo"
)

// loadArtifactory is the example with the artifacts section of preset: artifactory.
func loadArtifactory(t *testing.T, mutate func(string) string) (*Config, error) {
	t.Helper()
	raw, err := rondo.ExampleConfigFor(PresetArtifactory)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if mutate != nil {
		content = mutate(content)
	}
	p := filepath.Join(t.TempDir(), "rondo.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

// resolved is every endpoint a manifest resolves to, as the CLI, the operator and the Jobs read it.
func resolved(t *testing.T, c *Config) map[string]string {
	t.Helper()
	m := c.Mirror()
	auth, err := m.FilesAuthURL()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{
		"files.base":         m.FilesBase,
		"files.repo":         m.FilesRepoURL(),
		"files.auth":         auth,
		"oci.registry":       m.OCIRegistry,
		"oci.images":         m.OCIRepository,
		"oci.charts":         m.ChartsOCI,
		"rpm.base":           m.RPMBase,
		"rpm.repo":           m.RPMRepo,
		"rke2.registry":      c.RegistryHostFor(&c.Targets.Downstreams[0].RKE2),
		"tofu.mirror":        m.ProviderMirrorURL,
		"tofu.mirrorHost":    m.ProviderMirrorHost(),
		"tofu.stateBase":     m.StateBaseURL(),
		"tofu.state.k3s":     m.StateURL("k3s"),
		"artifactory.api":    m.ArtifactoryURL,
		"credentials.user":   m.Username,
		"credentials.secret": m.Password,
	}
	for k, v := range c.Env() {
		if strings.HasPrefix(k, "ARTIFACTORY_") || k == "REGISTRY_HOST" || strings.HasPrefix(k, "TF_VAR_artifactory_") {
			out["env."+k] = v
		}
	}
	return out
}

func checkResolved(t *testing.T, got, want map[string]string) {
	t.Helper()
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s = %q is not checked", k, got[k])
		}
	}
}

// The values are those the manifest's artifactory section resolved to before the artifacts
// section, so an Artifactory site keeps its URLs.
func TestArtifactoryPresetResolvesAsBefore(t *testing.T) {
	for name, mutate := range map[string]func(string) string{
		"as written": func(s string) string {
			return strings.Replace(s, `rpm: ""`, `rpm: rondo-rpms-local`, 1)
		},
		"trailing slash": func(s string) string {
			s = strings.Replace(s, `rpm: ""`, `rpm: rondo-rpms-local`, 1)
			return strings.Replace(s, "example.local/artifactory\n", "example.local/artifactory/\n", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := loadArtifactory(t, mutate)
			if err != nil {
				t.Fatal(err)
			}
			if !c.Mirror().IsArtifactory() {
				t.Fatal("preset artifactory not seen")
			}
			checkResolved(t, resolved(t, c), map[string]string{
				"files.base":         "https://artifactory.example.local/artifactory",
				"files.repo":         "https://artifactory.example.local/artifactory/rondo-generic-local",
				"files.auth":         "https://svc-rondo:CHANGEME@artifactory.example.local/artifactory",
				"oci.registry":       "artifactory.example.local",
				"oci.images":         "artifactory.example.local/rondo-docker-local",
				"oci.charts":         "oci://artifactory.example.local/rondo-helm-local",
				"rpm.base":           "https://artifactory.example.local/artifactory",
				"rpm.repo":           "rondo-rpms-local",
				"rke2.registry":      "rondo-docker-local.artifactory.example.local",
				"tofu.mirror":        "https://artifactory.example.local/artifactory/api/terraform/rondo-tofu-local/providers/",
				"tofu.mirrorHost":    "artifactory.example.local",
				"tofu.stateBase":     "https://artifactory.example.local/artifactory/rondo-tofu-state-local/prod",
				"tofu.state.k3s":     "https://artifactory.example.local/artifactory/rondo-tofu-state-local/prod/k3s/terraform.tfstate",
				"artifactory.api":    "https://artifactory.example.local/artifactory",
				"credentials.user":   "svc-rondo",
				"credentials.secret": "CHANGEME",

				"env.ARTIFACTORY_URL":                 "https://artifactory.example.local/artifactory",
				"env.ARTIFACTORY_USER":                "svc-rondo",
				"env.ARTIFACTORY_PASSWORD":            "CHANGEME",
				"env.ARTIFACTORY_GENERIC_REPO":        "rondo-generic-local",
				"env.ARTIFACTORY_RPM_REPO":            "rondo-rpms-local",
				"env.ARTIFACTORY_DOCKER_REPO":         "rondo-docker-local",
				"env.ARTIFACTORY_HELM_REPO":           "rondo-helm-local",
				"env.REGISTRY_HOST":                   "artifactory.example.local",
				"env.TF_VAR_artifactory_url":          "https://artifactory.example.local/artifactory",
				"env.TF_VAR_artifactory_user":         "svc-rondo",
				"env.TF_VAR_artifactory_pass":         "CHANGEME",
				"env.TF_VAR_artifactory_generic_repo": "rondo-generic-local",
				"env.TF_VAR_artifactory_docker_repo":  "rondo-docker-local",
			})
		})
	}
}

func TestArtifactoryPresetDefaultsTheRepositories(t *testing.T) {
	c, err := loadArtifactory(t, func(s string) string {
		start := strings.Index(s, "    repositories:\n")
		end := strings.Index(s, "    mirrorTokenLifetimeSeconds:")
		return s[:start] + s[end:]
	})
	if err != nil {
		t.Fatal(err)
	}
	m := c.Mirror()
	for got, want := range map[string]string{
		m.OCIRepository:     "artifactory.example.local/rondo-docker-local",
		m.ChartsOCI:         "oci://artifactory.example.local/rondo-helm-local",
		m.FilesRepoURL():    "https://artifactory.example.local/artifactory/rondo-generic-local",
		m.ProviderMirrorURL: "https://artifactory.example.local/artifactory/api/terraform/rondo-tofu-local/providers/",
		m.StateBaseURL():    "https://artifactory.example.local/artifactory/rondo-tofu-state-local/prod",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if m.RPMRepo != "" {
		t.Errorf("RPM repository %q, want none", m.RPMRepo)
	}
}

func TestArtifactoryPresetTakesItsOwnRepositories(t *testing.T) {
	c, err := loadArtifactory(t, func(s string) string {
		return strings.NewReplacer("tofu: rondo-tofu-local", "tofu: site-tofu",
			"tofuState: rondo-tofu-state-local", "tofuState: site-state").Replace(s)
	})
	if err != nil {
		t.Fatal(err)
	}
	m := c.Mirror()
	if want := "https://artifactory.example.local/artifactory/api/terraform/site-tofu/providers/"; m.ProviderMirrorURL != want {
		t.Errorf("provider mirror = %q, want %q", m.ProviderMirrorURL, want)
	}
	if want := "https://artifactory.example.local/artifactory/site-state/prod/k3s/terraform.tfstate"; m.StateURL("k3s") != want {
		t.Errorf("state = %q, want %q", m.StateURL("k3s"), want)
	}
}

func TestGenericPresetResolvesTheDeclaredEndpoints(t *testing.T) {
	c, err := load(t, func(s string) string {
		return strings.Replace(s, "    repository: \"\"\n", "    repository: rpms\n", 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Mirror().IsArtifactory() {
		t.Fatal("preset generic seen as artifactory")
	}
	checkResolved(t, resolved(t, c), map[string]string{
		"files.base":         "https://files.example.local",
		"files.repo":         "https://files.example.local/rondo",
		"files.auth":         "https://svc-rondo:CHANGEME@files.example.local",
		"oci.registry":       "registry.example.local",
		"oci.images":         "registry.example.local/rondo",
		"oci.charts":         "oci://registry.example.local/charts",
		"rpm.base":           "https://files.example.local",
		"rpm.repo":           "rpms",
		"rke2.registry":      "registry.example.local",
		"tofu.mirror":        "https://files.example.local/tofu-providers/",
		"tofu.mirrorHost":    "files.example.local",
		"tofu.stateBase":     "https://files.example.local/tofu-state/prod",
		"tofu.state.k3s":     "https://files.example.local/tofu-state/prod/k3s/terraform.tfstate",
		"artifactory.api":    "",
		"credentials.user":   "svc-rondo",
		"credentials.secret": "CHANGEME",

		"env.ARTIFACTORY_URL":                 "https://files.example.local",
		"env.ARTIFACTORY_USER":                "svc-rondo",
		"env.ARTIFACTORY_PASSWORD":            "CHANGEME",
		"env.ARTIFACTORY_GENERIC_REPO":        "rondo",
		"env.ARTIFACTORY_RPM_REPO":            "rpms",
		"env.ARTIFACTORY_DOCKER_REPO":         "rondo",
		"env.ARTIFACTORY_HELM_REPO":           "charts",
		"env.REGISTRY_HOST":                   "registry.example.local",
		"env.TF_VAR_artifactory_url":          "https://files.example.local",
		"env.TF_VAR_artifactory_user":         "svc-rondo",
		"env.TF_VAR_artifactory_pass":         "CHANGEME",
		"env.TF_VAR_artifactory_generic_repo": "rondo",
		"env.TF_VAR_artifactory_docker_repo":  "rondo",
	})
}

func TestGenericPresetDefaults(t *testing.T) {
	c, err := load(t, func(s string) string {
		s = strings.Replace(s, "  preset: generic\n", "", 1)
		s = strings.Replace(s, "    repository: rondo\n    chartsRepository: charts\n", "", 1)
		s = strings.Replace(s, "    repository: rondo\n", "", 1)
		return strings.Replace(s, "  rpm:\n    url: https://files.example.local\n    repository: \"\"\n", "", 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	m := c.Mirror()
	if m.Preset != PresetGeneric || m.OCIRepository != "registry.example.local/rondo" ||
		m.ChartsOCI != "oci://registry.example.local/charts" ||
		m.FilesRepoURL() != "https://files.example.local/rondo" ||
		m.RPMBase != "https://files.example.local" || m.RPMRepo != "" {
		t.Errorf("defaults not applied: %+v", m)
	}
}

func TestMirrorTokenIsTrimmed(t *testing.T) {
	c := &Config{Secrets: Secrets{MirrorToken: "  tok  "}}
	if got := c.Mirror().MirrorToken; got != "tok" {
		t.Errorf("token = %q", got)
	}
}

func TestArtifactsValidationNamesTheKey(t *testing.T) {
	generic := map[string]struct {
		mutate func(string) string
		want   string
	}{
		"no registry": {func(s string) string {
			return strings.Replace(s, "    registry: registry.example.local\n", "", 1)
		}, "artifacts.oci.registry is required"},
		"no files URL": {func(s string) string {
			return strings.Replace(s, "  files:\n    url: https://files.example.local\n", "  files:\n", 1)
		}, "artifacts.files.url is required"},
		"no provider mirror": {func(s string) string {
			return strings.Replace(s, "    providerMirror: https://files.example.local/tofu-providers/\n", "", 1)
		}, "artifacts.tofu.providerMirror is required"},
		"no state URL": {func(s string) string {
			return strings.Replace(s, "    stateURL: https://files.example.local/tofu-state\n", "", 1)
		}, "artifacts.tofu.stateURL is required"},
		"registry with a scheme": {func(s string) string {
			return strings.Replace(s, "registry: registry.example.local", "registry: https://registry.example.local", 1)
		}, "artifacts.oci.registry"},
		"registry with a path": {func(s string) string {
			return strings.Replace(s, "registry: registry.example.local", "registry: registry.example.local/rondo", 1)
		}, "artifacts.oci.repository"},
		"RPM elsewhere": {func(s string) string {
			return strings.Replace(s, "  rpm:\n    url: https://files.example.local\n", "  rpm:\n    url: https://rpms.example.local\n", 1)
		}, "artifacts.rpm.url"},
		"artifactory block": {func(s string) string {
			return strings.Replace(s, "  oci:\n", "  artifactory:\n    url: https://a.example.local\n  oci:\n", 1)
		}, "artifacts.artifactory is only read with artifacts.preset: artifactory"},
		"unknown preset": {func(s string) string {
			return strings.Replace(s, "preset: generic", "preset: nexus", 1)
		}, "Preset"},
		"no password": {func(s string) string {
			return strings.Replace(s, "  mirrorPassword: CHANGEME\n", "", 1)
		}, "MirrorPassword"},
		"top-level artifactory": {func(s string) string {
			return strings.Replace(s, "artifacts:\n", "artifactory:\n  url: https://a.example.local\nartifacts:\n", 1)
		}, `unknown field "artifactory": the artifact mirror is declared under artifacts:`},
		"secrets.artifactoryPassword": {func(s string) string {
			return strings.Replace(s, "  mirrorPassword: CHANGEME\n", "  artifactoryPassword: CHANGEME\n", 1)
		}, "secrets.mirrorPassword"},
		"secrets.artifactoryMirrorToken": {func(s string) string {
			return strings.Replace(s, "  mirrorPassword: CHANGEME\n", "  mirrorPassword: CHANGEME\n  artifactoryMirrorToken: x\n", 1)
		}, "secrets.mirrorToken"},
	}
	for name, tc := range generic {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, tc.mutate)
			if err == nil {
				t.Fatal("manifest accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	artifactory := map[string]struct {
		mutate func(string) string
		want   string
	}{
		"no URL": {func(s string) string {
			return strings.Replace(s, "    url: https://artifactory.example.local/artifactory\n", "", 1)
		}, "artifacts.artifactory.url is required"},
		"oci endpoint": {func(s string) string {
			return strings.Replace(s, "  artifactory:\n", "  oci:\n    registry: r.example.local\n  artifactory:\n", 1)
		}, "artifacts.oci is not read with artifacts.preset: artifactory"},
		"tofu endpoint": {func(s string) string {
			return strings.Replace(s, "  artifactory:\n", "  tofu:\n    stateURL: https://s.example.local\n  artifactory:\n", 1)
		}, "artifacts.tofu is not read"},
		"token lifetime": {func(s string) string {
			return strings.Replace(s, `mirrorTokenLifetimeSeconds: ""`, "mirrorTokenLifetimeSeconds: a year", 1)
		}, "MirrorTokenLifetimeSeconds"},
	}
	for name, tc := range artifactory {
		t.Run("artifactory/"+name, func(t *testing.T) {
			_, err := loadArtifactory(t, tc.mutate)
			if err == nil {
				t.Fatal("manifest accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestTopLevelArtifactoryIsLocated(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("..", "..", "rondo.yaml.example"))
	bad := []byte(strings.Replace(string(raw), "artifacts:\n", "artifactory:\n  url: https://a.example.local\nartifacts:\n", 1))
	_, err := Parse(bad)
	if err == nil {
		t.Fatal("top-level artifactory accepted")
	}
	p := Explain(bad, err)
	if p.Path != "artifactory" || p.Line != 4 {
		t.Errorf("problem = %+v, want artifactory at line 4", p)
	}
}

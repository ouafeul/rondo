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
		"registry.rewrite":   m.RegistryRewrite,
		"tofu.mirror":        m.ProviderMirrorURL,
		"tofu.oci":           m.ProviderOCI,
		"tofu.mirrorHost":    m.ProviderMirrorHost(),
		"tofu.stateBackend":  m.StateBackend,
		"tofu.stateBase":     m.StateBaseURL(),
		"tofu.state.k3s":     m.StateURL("k3s"),
		"artifactory.api":    m.ArtifactoryURL,
		"credentials.user":   m.Username,
		"credentials.secret": m.Password,
	}
	for k, v := range c.Env() {
		if mirrorEnv[k] || strings.HasPrefix(k, "TF_VAR_mirror_") {
			out["env."+k] = v
		}
	}
	return out
}

// mirrorEnv are the keys of Env that the mirror resolves: the platform tokens and the credentials.
var mirrorEnv = map[string]bool{
	"OCI_REGISTRY": true, "CHARTS_OCI": true, "CHARTS_REPOSITORY": true, "IMAGES_REPOSITORY": true,
	"FILES_URL": true, "FILES_REPOSITORY": true, "RPM_REPOSITORY": true,
	"MIRROR_USER": true, "MIRROR_PASSWORD": true,
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
				"registry.rewrite":   "",
				"tofu.mirror":        "https://artifactory.example.local/artifactory/api/terraform/rondo-tofu-local/providers/",
				"tofu.oci":           "",
				"tofu.mirrorHost":    "artifactory.example.local",
				"tofu.stateBackend":  "http",
				"tofu.stateBase":     "https://artifactory.example.local/artifactory/rondo-tofu-state-local/prod",
				"tofu.state.k3s":     "https://artifactory.example.local/artifactory/rondo-tofu-state-local/prod/k3s/terraform.tfstate",
				"artifactory.api":    "https://artifactory.example.local/artifactory",
				"credentials.user":   "svc-rondo",
				"credentials.secret": "CHANGEME",

				"env.FILES_URL":                      "https://artifactory.example.local/artifactory",
				"env.MIRROR_USER":                    "svc-rondo",
				"env.MIRROR_PASSWORD":                "CHANGEME",
				"env.FILES_REPOSITORY":               "rondo-generic-local",
				"env.RPM_REPOSITORY":                 "rondo-rpms-local",
				"env.IMAGES_REPOSITORY":              "rondo-docker-local",
				"env.CHARTS_REPOSITORY":              "rondo-helm-local",
				"env.CHARTS_OCI":                     "oci://artifactory.example.local/rondo-helm-local",
				"env.OCI_REGISTRY":                   "artifactory.example.local",
				"env.TF_VAR_mirror_files_url":        "https://artifactory.example.local/artifactory/rondo-generic-local",
				"env.TF_VAR_mirror_registry":         "artifactory.example.local",
				"env.TF_VAR_mirror_registry_rewrite": "",
				"env.TF_VAR_mirror_username":         "svc-rondo",
				"env.TF_VAR_mirror_password":         "CHANGEME",
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
		"registry.rewrite":   "rondo",
		"tofu.mirror":        "https://files.example.local/tofu-providers/",
		"tofu.oci":           "",
		"tofu.mirrorHost":    "files.example.local",
		"tofu.stateBackend":  "http",
		"tofu.stateBase":     "https://files.example.local/tofu-state/prod",
		"tofu.state.k3s":     "https://files.example.local/tofu-state/prod/k3s/terraform.tfstate",
		"artifactory.api":    "",
		"credentials.user":   "svc-rondo",
		"credentials.secret": "CHANGEME",

		"env.FILES_URL":                      "https://files.example.local",
		"env.MIRROR_USER":                    "svc-rondo",
		"env.MIRROR_PASSWORD":                "CHANGEME",
		"env.FILES_REPOSITORY":               "rondo",
		"env.RPM_REPOSITORY":                 "rpms",
		"env.IMAGES_REPOSITORY":              "rondo",
		"env.CHARTS_REPOSITORY":              "charts",
		"env.CHARTS_OCI":                     "oci://registry.example.local/charts",
		"env.OCI_REGISTRY":                   "registry.example.local",
		"env.TF_VAR_mirror_files_url":        "https://files.example.local/rondo",
		"env.TF_VAR_mirror_registry":         "registry.example.local",
		"env.TF_VAR_mirror_registry_rewrite": "rondo",
		"env.TF_VAR_mirror_username":         "svc-rondo",
		"env.TF_VAR_mirror_password":         "CHANGEME",
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
		"no provider source": {func(s string) string {
			return strings.Replace(s, "    providerMirror: https://files.example.local/tofu-providers/\n", "", 1)
		}, "artifacts.tofu.providerMirror (a network mirror) or artifacts.tofu.providerOCI"},
		"two provider sources": {func(s string) string {
			return strings.Replace(s, "    providerMirror:", "    providerOCI: registry.example.local/providers\n    providerMirror:", 1)
		}, "are exclusive"},
		"OCI providers without a repository": {func(s string) string {
			return strings.Replace(s, "    providerMirror: https://files.example.local/tofu-providers/\n",
				"    providerOCI: registry.example.local\n", 1)
		}, "must be <registry>/<repository>"},
		"OCI providers with a scheme": {func(s string) string {
			return strings.Replace(s, "    providerMirror: https://files.example.local/tofu-providers/\n",
				"    providerOCI: https://registry.example.local/providers\n", 1)
		}, "without a scheme"},
		"OCI providers and a mirror token": {func(s string) string {
			s = strings.Replace(s, "    providerMirror: https://files.example.local/tofu-providers/\n",
				"    providerOCI: registry.example.local/providers\n", 1)
			return strings.Replace(s, "  mirrorPassword: CHANGEME\n", "  mirrorPassword: CHANGEME\n  mirrorToken: t\n", 1)
		}, "secrets.mirrorToken authenticates on artifacts.tofu.providerMirror"},
		"no state backend": {func(s string) string {
			return strings.Replace(s, "    state:\n      http:\n        url: https://files.example.local/tofu-state\n", "", 1)
		}, "artifacts.tofu.state.http or artifacts.tofu.state.s3 is required"},
		"two state backends": {func(s string) string {
			return strings.Replace(s, "    state:\n", "    state:\n      s3: {bucket: b}\n", 1)
		}, "are exclusive"},
		"http state without URL": {func(s string) string {
			return strings.Replace(s, "        url: https://files.example.local/tofu-state\n", "        url: \"\"\n", 1)
		}, "URL"},
		"s3 state without bucket": {func(s string) string {
			return strings.Replace(s, "      http:\n        url: https://files.example.local/tofu-state\n",
				"      s3:\n        endpoint: https://s3.example.local\n", 1)
		}, "Bucket"},
		"s3 state without credentials": {func(s string) string {
			s = strings.Replace(s, "      http:\n        url: https://files.example.local/tofu-state\n",
				"      s3:\n        bucket: b\n", 1)
			s = strings.Replace(s, "  username: svc-rondo\n", "", 1)
			return strings.Replace(s, "  mirrorPassword: CHANGEME\n", "", 1)
		}, "artifacts.tofu.state.s3 needs credentials"},
		"s3 access key alone": {func(s string) string {
			s = strings.Replace(s, "      http:\n        url: https://files.example.local/tofu-state\n",
				"      s3:\n        bucket: b\n", 1)
			return strings.Replace(s, "  mirrorPassword: CHANGEME\n", "  mirrorPassword: CHANGEME\n  mirrorStateAccessKey: a\n", 1)
		}, "go together"},
		"s3 keys with an http state": {func(s string) string {
			return strings.Replace(s, "  mirrorPassword: CHANGEME\n",
				"  mirrorPassword: CHANGEME\n  mirrorStateAccessKey: a\n  mirrorStateSecretKey: b\n", 1)
		}, "only read with artifacts.tofu.state.s3"},
		"username without password": {func(s string) string {
			return strings.Replace(s, "  mirrorPassword: CHANGEME\n", "", 1)
		}, "artifacts.username and secrets.mirrorPassword go together"},
		"password without username": {func(s string) string {
			return strings.Replace(s, "  username: svc-rondo\n", "", 1)
		}, "artifacts.username and secrets.mirrorPassword go together"},
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
		"unknown top-level key": {func(s string) string {
			return strings.Replace(s, "artifacts:\n", "registry:\n  url: https://a.example.local\nartifacts:\n", 1)
		}, `unknown field "registry"`},
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
			return strings.Replace(s, "  artifactory:\n", "  tofu:\n    providerOCI: r.example.local/p\n  artifactory:\n", 1)
		}, "artifacts.tofu is not read"},
		"s3 state keys": {func(s string) string {
			return strings.Replace(s, "  mirrorPassword: CHANGEME\n",
				"  mirrorPassword: CHANGEME\n  mirrorStateAccessKey: a\n  mirrorStateSecretKey: b\n", 1)
		}, "only read with artifacts.tofu.state.s3"},
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

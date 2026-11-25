package config

import (
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

// Defaults of the repositories a manifest leaves out.
const (
	DefaultRepository       = "rondo"
	DefaultChartsRepository = "charts"

	DefaultArtifactoryDocker    = "rondo-docker-local"
	DefaultArtifactoryHelm      = "rondo-helm-local"
	DefaultArtifactoryGeneric   = "rondo-generic-local"
	DefaultArtifactoryTofu      = "rondo-tofu-local"
	DefaultArtifactoryTofuState = "rondo-tofu-state-local"
)

// Mirror is the artifact mirror with every endpoint resolved, whatever the preset: what the rest
// of RONDO reads instead of the artifacts section.
type Mirror struct {
	Preset   string
	Username string
	Password string
	// CAFile is artifacts.caCertificateFile as written, relative to the manifest.
	CAFile string

	// FilesBase is the root of the file server, without a trailing slash; RONDO's own files live
	// under FilesBase/FilesRepo.
	FilesBase string
	FilesRepo string

	// OCIRegistry is host[:port]. Images are OCIRepository/<image>, OCIRepository being
	// OCIRegistry/ImagesRepo; Helm charts are ChartsOCI/<chart>, ChartsOCI being
	// oci://OCIRegistry/ChartsRepo.
	OCIRegistry   string
	ImagesRepo    string
	OCIRepository string
	ChartsRepo    string
	ChartsOCI     string

	// RPMBase is the root of the dnf repositories, <RPMBase>/<repository>/<path>; RPMRepo is the
	// site's own, empty when it declares none.
	RPMBase string
	RPMRepo string

	// RKE2SystemRegistry is the registry authority rke2 nodes pull the system images from.
	RKE2SystemRegistry string

	// ProviderMirrorURL is the OpenTofu network_mirror; MirrorToken, when set, authenticates on
	// it in place of a minted token.
	ProviderMirrorURL string
	MirrorToken       string

	// ArtifactoryURL is the API root of the preset artifactory, where tokens are minted; empty with
	// any other preset.
	ArtifactoryURL             string
	MirrorTokenLifetimeSeconds string

	stateRoot   string
	environment string
}

// Mirror resolves artifacts into endpoints, defaults filled in.
func (c *Config) Mirror() Mirror {
	a := &c.Artifacts
	m := Mirror{
		Preset:      a.preset(),
		Username:    a.Username,
		Password:    c.Secrets.MirrorPassword,
		CAFile:      a.CACertificateFile,
		MirrorToken: strings.TrimSpace(c.Secrets.MirrorToken),
		environment: c.EnvironmentName,
	}
	if m.Preset == PresetArtifactory {
		r := a.Artifactory.Repositories
		root := trimURL(a.Artifactory.URL)
		host := hostOf(root)
		m.FilesBase, m.FilesRepo = root, or(r.Generic, DefaultArtifactoryGeneric)
		m.OCIRegistry = host
		m.ImagesRepo = or(r.Docker, DefaultArtifactoryDocker)
		m.ChartsRepo = or(r.Helm, DefaultArtifactoryHelm)
		m.RPMBase, m.RPMRepo = root, repo(r.RPM)
		// Artifactory serves a docker repository at its own subdomain as well.
		m.RKE2SystemRegistry = m.ImagesRepo + "." + host
		m.ProviderMirrorURL = root + "/api/terraform/" + or(r.Tofu, DefaultArtifactoryTofu) + "/providers/"
		m.stateRoot = root + "/" + or(r.TofuState, DefaultArtifactoryTofuState)
		m.ArtifactoryURL = root
		m.MirrorTokenLifetimeSeconds = a.Artifactory.MirrorTokenLifetimeSeconds
	} else {
		m.FilesBase, m.FilesRepo = trimURL(a.Files.URL), or(a.Files.Repository, DefaultRepository)
		m.OCIRegistry = strings.TrimRight(a.OCI.Registry, "/")
		m.ImagesRepo = or(a.OCI.Repository, DefaultRepository)
		m.ChartsRepo = or(a.OCI.ChartsRepository, DefaultChartsRepository)
		m.RPMBase, m.RPMRepo = or(trimURL(a.RPM.URL), m.FilesBase), repo(a.RPM.Repository)
		// TODO: a registry served under a path prefix needs a rewrite in the rke2 registries.yaml.
		m.RKE2SystemRegistry = m.OCIRegistry
		m.ProviderMirrorURL = a.Tofu.ProviderMirror
		m.stateRoot = trimURL(a.Tofu.StateURL)
	}
	m.OCIRepository = m.OCIRegistry + "/" + m.ImagesRepo
	m.ChartsOCI = "oci://" + m.OCIRegistry + "/" + m.ChartsRepo
	return m
}

// IsArtifactory is true for the preset artifactory, the only one RONDO mints tokens on.
func (m Mirror) IsArtifactory() bool { return m.Preset == PresetArtifactory }

// FilesRepoURL is the root of RONDO's own files.
func (m Mirror) FilesRepoURL() string { return m.FilesBase + "/" + m.FilesRepo }

// ProviderMirrorHost is the host OpenTofu presents the mirror token to.
func (m Mirror) ProviderMirrorHost() string { return hostOf(m.ProviderMirrorURL) }

// StateBaseURL is the base of the http state backend of the environment.
func (m Mirror) StateBaseURL() string {
	if m.environment == "" {
		return m.stateRoot
	}
	return m.stateRoot + "/" + m.environment
}

// StateURL is the state address of the stack name.
func (m Mirror) StateURL(name string) string {
	return m.StateBaseURL() + "/" + name + "/terraform.tfstate"
}

// FilesAuthURL is FilesBase with the credentials in its userinfo, for the tools that take no
// other.
func (m Mirror) FilesAuthURL() (string, error) {
	scheme, rest, ok := strings.Cut(m.FilesBase, "://")
	if !ok {
		return "", fmt.Errorf("the files URL of the mirror (%q) has no scheme", m.FilesBase)
	}
	return scheme + "://" + escapeUserinfo(m.Username) + ":" +
		escapeUserinfo(m.Password) + "@" + rest, nil
}

func (a *Artifacts) preset() string {
	if a.Preset == "" {
		return PresetGeneric
	}
	return a.Preset
}

func trimURL(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }

func repo(r string) string { return strings.Trim(strings.TrimSpace(r), "/") }

func or(v, def string) string {
	if v := repo(v); v != "" {
		return v
	}
	return def
}

func hostOf(u string) string {
	h := schemeRe.ReplaceAllString(u, "")
	if i := strings.IndexByte(h, '/'); i >= 0 {
		h = h[:i]
	}
	return h
}

// validateArtifacts checks what the struct tags cannot: what each preset requires, and that it
// is given nothing it does not read.
func (c *Config) validateArtifacts() error {
	a := &c.Artifacts
	if a.preset() == PresetArtifactory {
		if a.Artifactory.URL == "" {
			return fmt.Errorf("artifacts.artifactory.url is required with artifacts.preset: artifactory")
		}
		for _, s := range []struct {
			key string
			set bool
		}{
			{"oci", a.OCI != OCIEndpoint{}}, {"files", a.Files != FilesEndpoint{}},
			{"rpm", a.RPM != RPMEndpoint{}}, {"tofu", a.Tofu != TofuEndpoints{}},
		} {
			if s.set {
				return fmt.Errorf("artifacts.%s is not read with artifacts.preset: artifactory, "+
					"whose endpoints derive from artifacts.artifactory.url", s.key)
			}
		}
		return nil
	}
	if a.Artifactory != (ArtifactoryMirror{}) {
		return fmt.Errorf("artifacts.artifactory is only read with artifacts.preset: artifactory")
	}
	for _, r := range []struct{ key, value string }{
		{"artifacts.oci.registry", a.OCI.Registry},
		{"artifacts.files.url", a.Files.URL},
		{"artifacts.tofu.providerMirror", a.Tofu.ProviderMirror},
		{"artifacts.tofu.stateURL", a.Tofu.StateURL},
	} {
		if strings.TrimSpace(r.value) == "" {
			return fmt.Errorf("%s is required with artifacts.preset: generic", r.key)
		}
	}
	if strings.Contains(a.OCI.Registry, "://") || strings.Contains(strings.TrimRight(a.OCI.Registry, "/"), "/") {
		return fmt.Errorf("artifacts.oci.registry (%q) must be host[:port], without a scheme or a path; "+
			"a path goes in artifacts.oci.repository", a.OCI.Registry)
	}
	// Machines resolve their RPM repositories under the root of the files.
	if a.RPM.URL != "" && trimURL(a.RPM.URL) != trimURL(a.Files.URL) {
		return fmt.Errorf("artifacts.rpm.url (%q) must be artifacts.files.url (%q) or empty: machines "+
			"reach the RPM repositories under the root of the files", a.RPM.URL, a.Files.URL)
	}
	return nil
}

// unknownKeyHint says where a key the manifest no longer reads went, empty for any other key.
func unknownKeyHint(raw []byte) string {
	var doc struct {
		Artifactory any `json:"artifactory"`
		Secrets     struct {
			Password any `json:"artifactoryPassword"`
			Token    any `json:"artifactoryMirrorToken"`
		} `json:"secrets"`
	}
	if yaml.Unmarshal(raw, &doc) != nil {
		return ""
	}
	switch {
	case doc.Artifactory != nil:
		return `unknown field "artifactory": the artifact mirror is declared under artifacts:, ` +
			`with artifacts.preset: artifactory and artifacts.artifactory.url for a JFrog Artifactory`
	case doc.Secrets.Password != nil:
		return `unknown field "artifactoryPassword": the password of the mirror is secrets.mirrorPassword`
	case doc.Secrets.Token != nil:
		return `unknown field "artifactoryMirrorToken": the token of the provider mirror is secrets.mirrorToken`
	}
	return ""
}

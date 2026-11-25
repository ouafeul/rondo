package config

import (
	"fmt"
	"path"
	"strings"

	"rondo/internal/tofustate"
)

// Defaults of the repositories a manifest leaves out.
const (
	DefaultRepository = "rondo"
	// DefaultChartsSubpath is where the charts sit under the images repository when
	// oci.chartsRepository is left out: <repository>/charts, where the mirror scripts push them
	// (their registry is <oci.registry>/<oci.repository>).
	DefaultChartsSubpath = "charts"

	DefaultArtifactoryDocker    = "rondo-docker-local"
	DefaultArtifactoryHelm      = "rondo-helm-local"
	DefaultArtifactoryGeneric   = "rondo-generic-local"
	DefaultArtifactoryTofu      = "rondo-tofu-local"
	DefaultArtifactoryTofuState = "rondo-tofu-state-local"
)

// Mirror is the artifact mirror with every endpoint resolved, whatever the preset: what the rest
// of RONDO reads instead of the artifacts section.
type Mirror struct {
	Preset string
	// Username and Password are both empty when the mirror is read anonymously.
	Username string
	Password string
	// CAFile is artifacts.caCertificateFile as written, relative to the manifest; empty, the
	// endpoints are trusted through the system trust store.
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
	// RegistryRewrite is the repository the nodes prefix to an image path asked of the registry
	// (or of a public registry) without it, so <registry>/rancher/x is pulled as
	// <registry>/<RegistryRewrite>/rancher/x; empty when the registry serves the images at its
	// root, as an Artifactory docker subdomain does.
	RegistryRewrite string

	// ProviderMirrorURL is the OpenTofu network_mirror; MirrorToken, when set, authenticates on
	// it in place of a minted token. ProviderOCI, <registry>/<repository>, is the OCI mirror used
	// instead when the network mirror is empty.
	ProviderMirrorURL string
	ProviderOCI       string
	MirrorToken       string

	// StateBackend is tofustate.HTTP (StateURL) or tofustate.S3 (StateS3, authenticated by
	// StateAccessKey and StateSecretKey).
	StateBackend   string
	StateS3        tofustate.S3Bucket
	StateAccessKey string
	StateSecretKey string

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
		Preset:       a.preset(),
		Username:     a.Username,
		Password:     c.Secrets.MirrorPassword,
		CAFile:       a.CACertificateFile,
		MirrorToken:  strings.TrimSpace(c.Secrets.MirrorToken),
		StateBackend: tofustate.HTTP,
		environment:  c.EnvironmentName,
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
		m.ChartsRepo = or(a.OCI.ChartsRepository, m.ImagesRepo+"/"+DefaultChartsSubpath)
		m.RPMBase, m.RPMRepo = or(trimURL(a.RPM.URL), m.FilesBase), repo(a.RPM.Repository)
		m.RKE2SystemRegistry = m.OCIRegistry
		m.RegistryRewrite = m.ImagesRepo
		m.ProviderMirrorURL = strings.TrimSpace(a.Tofu.ProviderMirror)
		m.ProviderOCI = repo(a.Tofu.ProviderOCI)
		m.resolveState(&a.Tofu.State, &c.Secrets)
	}
	m.OCIRepository = m.OCIRegistry + "/" + m.ImagesRepo
	m.ChartsOCI = "oci://" + m.OCIRegistry + "/" + m.ChartsRepo
	return m
}

func (m *Mirror) resolveState(s *TofuState, secrets *Secrets) {
	if s.HTTP != nil {
		m.stateRoot = trimURL(s.HTTP.URL)
	}
	if s.S3 == nil {
		return
	}
	m.StateBackend = tofustate.S3
	m.StateS3 = tofustate.S3Bucket{
		Bucket:       strings.TrimSpace(s.S3.Bucket),
		Endpoint:     trimURL(s.S3.Endpoint),
		Region:       strings.TrimSpace(s.S3.Region),
		KeyPrefix:    strings.Trim(path.Join(repo(s.S3.KeyPrefix), m.environment), "/"),
		UsePathStyle: s.S3.UsePathStyle,
	}
	if m.StateS3.Region == "" {
		m.StateS3.Region = tofustate.DefaultRegion
	}
	m.StateAccessKey, m.StateSecretKey = secrets.MirrorStateAccessKey, secrets.MirrorStateSecretKey
	if m.StateAccessKey == "" && m.StateSecretKey == "" {
		m.StateAccessKey, m.StateSecretKey = m.Username, m.Password
	}
}

// IsArtifactory is true for the preset artifactory, the only one RONDO mints tokens on.
func (m Mirror) IsArtifactory() bool { return m.Preset == PresetArtifactory }

// Anonymous is true when the mirror is read without credentials.
func (m Mirror) Anonymous() bool { return m.Username == "" && m.Password == "" }

// FilesRepoURL is the root of RONDO's own files.
func (m Mirror) FilesRepoURL() string { return m.FilesBase + "/" + m.FilesRepo }

// ProviderMirrorHost is the host OpenTofu presents its credentials to for the providers: the
// network mirror's, else the OCI registry's.
func (m Mirror) ProviderMirrorHost() string {
	if m.ProviderMirrorURL == "" && m.ProviderOCI != "" {
		host, _, _ := strings.Cut(m.ProviderOCI, "/")
		return host
	}
	return hostOf(m.ProviderMirrorURL)
}

// StateBaseURL is the base of the http state backend of the environment, empty with s3.
func (m Mirror) StateBaseURL() string {
	if m.StateBackend != tofustate.HTTP {
		return ""
	}
	if m.environment == "" {
		return m.stateRoot
	}
	return m.stateRoot + "/" + m.environment
}

// StateURL is the http state address of the stack name.
func (m Mirror) StateURL(name string) string {
	return m.StateBaseURL() + "/" + name + "/terraform.tfstate"
}

// FilesAuthURL is FilesBase with the credentials in its userinfo, for the tools that take no
// other; FilesBase itself when the mirror is read anonymously.
func (m Mirror) FilesAuthURL() (string, error) {
	scheme, rest, ok := strings.Cut(m.FilesBase, "://")
	if !ok {
		return "", fmt.Errorf("the files URL of the mirror (%q) has no scheme", m.FilesBase)
	}
	if m.Anonymous() {
		return m.FilesBase, nil
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
	if (a.Username == "") != (c.Secrets.MirrorPassword == "") {
		return fmt.Errorf("artifacts.username and secrets.mirrorPassword go together: set both, " +
			"or neither to read the mirror anonymously")
	}
	if a.preset() == PresetArtifactory {
		return c.validateArtifactory()
	}
	if a.Artifactory != (ArtifactoryMirror{}) {
		return fmt.Errorf("artifacts.artifactory is only read with artifacts.preset: artifactory")
	}
	for _, r := range []struct{ key, value string }{
		{"artifacts.oci.registry", a.OCI.Registry},
		{"artifacts.files.url", a.Files.URL},
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
	if err := c.validateProviders(); err != nil {
		return err
	}
	return c.validateState()
}

func (c *Config) validateArtifactory() error {
	a := &c.Artifacts
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
	if c.Secrets.MirrorStateAccessKey != "" || c.Secrets.MirrorStateSecretKey != "" {
		return fmt.Errorf("secrets.mirrorStateAccessKey and mirrorStateSecretKey are only read " +
			"with artifacts.tofu.state.s3")
	}
	return nil
}

func (c *Config) validateProviders() error {
	t := &c.Artifacts.Tofu
	mirror, oci := strings.TrimSpace(t.ProviderMirror) != "", repo(t.ProviderOCI) != ""
	switch {
	case mirror && oci:
		return fmt.Errorf("artifacts.tofu.providerMirror and artifacts.tofu.providerOCI are " +
			"exclusive: OpenTofu installs the providers from one mirror")
	case !mirror && !oci:
		return fmt.Errorf("artifacts.tofu.providerMirror (a network mirror) or " +
			"artifacts.tofu.providerOCI (an OCI mirror) is required with artifacts.preset: generic")
	case oci:
		r := repo(t.ProviderOCI)
		if strings.Contains(r, "://") || !strings.Contains(r, "/") {
			return fmt.Errorf("artifacts.tofu.providerOCI (%q) must be <registry>/<repository>, "+
				"without a scheme", t.ProviderOCI)
		}
		if strings.TrimSpace(c.Secrets.MirrorToken) != "" {
			return fmt.Errorf("secrets.mirrorToken authenticates on artifacts.tofu.providerMirror; " +
				"the OCI provider mirror takes artifacts.username and secrets.mirrorPassword")
		}
	}
	return nil
}

func (c *Config) validateState() error {
	s := &c.Artifacts.Tofu.State
	sec := &c.Secrets
	switch {
	case s.HTTP != nil && s.S3 != nil:
		return fmt.Errorf("artifacts.tofu.state.http and artifacts.tofu.state.s3 are exclusive")
	case s.HTTP == nil && s.S3 == nil:
		return fmt.Errorf("artifacts.tofu.state.http or artifacts.tofu.state.s3 is required " +
			"with artifacts.preset: generic")
	case s.HTTP != nil:
		if sec.MirrorStateAccessKey != "" || sec.MirrorStateSecretKey != "" {
			return fmt.Errorf("secrets.mirrorStateAccessKey and mirrorStateSecretKey are only " +
				"read with artifacts.tofu.state.s3")
		}
		return nil
	}
	if (sec.MirrorStateAccessKey == "") != (sec.MirrorStateSecretKey == "") {
		return fmt.Errorf("secrets.mirrorStateAccessKey and secrets.mirrorStateSecretKey go together")
	}
	if sec.MirrorStateAccessKey == "" && c.Artifacts.Username == "" {
		return fmt.Errorf("artifacts.tofu.state.s3 needs credentials: secrets.mirrorStateAccessKey " +
			"and mirrorStateSecretKey, or artifacts.username and secrets.mirrorPassword")
	}
	return nil
}

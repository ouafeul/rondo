// Package sandbox plays on k3d clusters what `rondo deploy`, `rondo deploy downstreams` and
// `rondo component install` play on a site, with the CLI's own code (internal/steps), and
// judges a component's deployment on its Enrollments' status. cmd/rondo-sandbox drives it;
// its script owns the infrastructure (network, PKI, registry, clusters).
//
// It lives apart from cmd/rondo: it reads the API types, which the operator's binary must not
// depend on.
package sandbox

import "path/filepath"

// The sandbox's fixed names: its artifact mirror (zot for OCI, nginx for the files, the RPM
// repositories and the states, on one host), its domain, its repositories.
const (
	registryHost      = "artifactory.sandbox.test"
	baseDomain        = "sandbox.test"
	helmRepo          = "rondo-helm-local"
	dockerRepo        = "rondo-docker-local"
	genericRepo       = "rondo-generic-local"
	rpmRepo           = "rondo-rpms-local"
	tofuStateRepo     = "rondo-tofu-state-local"
	filesURL          = "https://" + registryHost + "/artifactory"
	providerMirrorURL = filesURL + "/api/terraform/rondo-tofu-local/providers/"
	clusterPrefix     = "rondo-sbx"
	sandboxSecret     = "sandbox"
	managementRole    = "management"
)

// Settings locate a sandbox's state and the rondo tree it deploys RONDO from.
type Settings struct {
	StateDir string
	RondoDir string
}

// sitePath is the site `deploy` and `deploy downstreams` left: its Targets and clusters.
func (s Settings) sitePath() string { return filepath.Join(s.StateDir, "site.json") }

// planPath is what `component install` planned for a component, kept while it is installed.
func (s Settings) planPath(component string) string {
	return filepath.Join(s.plansDir(), component+".json")
}

func (s Settings) plansDir() string { return filepath.Join(s.StateDir, "components") }

func (s Settings) caPath() string { return filepath.Join(s.StateDir, "pki", "ca.pem") }

func (s Settings) clusterDir(name string) string {
	return filepath.Join(s.StateDir, "clusters", name)
}

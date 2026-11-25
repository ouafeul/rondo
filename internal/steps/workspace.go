package steps

import (
	"os"
	"path/filepath"
)

type Workspace struct {
	ProjectDir string
	StateDir   string
	BinDir     string
	CACert     string
}

func (w Workspace) Tool(name string) string { return filepath.Join(w.BinDir, name) }

func (w Workspace) HelmPluginsDir() string {
	return filepath.Join(filepath.Dir(w.BinDir), "helm-plugins")
}

func (w Workspace) Stack(name string) string { return filepath.Join(w.ProjectDir, "terraform", name) }

func (w Workspace) Kubeconfig() string { return filepath.Join(w.StateDir, "kubeconfig.yaml") }

func (w Workspace) StatePath() string { return filepath.Join(w.StateDir, "rondo.state.json") }

func (w Workspace) RancherCA() string {
	return filepath.Join(w.StateDir, "certs", "rancher-ca.pem")
}

func (w Workspace) TofuRC() string { return filepath.Join(w.StateDir, ".tofurc") }

// ReadCA is the mirror's CA bundle, empty when the mirror is trusted through the system store.
func (w Workspace) ReadCA() ([]byte, error) {
	if w.CACert == "" {
		return nil, nil
	}
	return os.ReadFile(w.CACert)
}

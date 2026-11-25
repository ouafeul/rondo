package steps

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspacePaths(t *testing.T) {
	w := Workspace{ProjectDir: "/proj", StateDir: "/state", BinDir: "/bin", CACert: "/ca.pem"}
	cases := map[string]string{
		w.Tool("tofu"):  filepath.Join("/bin", "tofu"),
		w.Stack("rke2"): filepath.Join("/proj", "terraform", "rke2"),
		w.Kubeconfig():  filepath.Join("/state", "kubeconfig.yaml"),
		w.StatePath():   filepath.Join("/state", "rondo.state.json"),
		w.RancherCA():   filepath.Join("/state", "certs", "rancher-ca.pem"),
		w.TofuRC():      filepath.Join("/state", ".tofurc"),
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("path %q, want %q", got, want)
		}
	}
}

func TestSiteStateNeverUnderProjectDir(t *testing.T) {
	w := Workspace{ProjectDir: "/rondo/cache/v1.2.3", StateDir: "/rondo/state", BinDir: "/bin"}
	siteState := map[string]string{
		"kubeconfig management":  w.Kubeconfig(),
		"runtime state (tokens)": w.StatePath(),
		"cluster CA":             w.RancherCA(),
		"tofurc":                 w.TofuRC(),
	}
	for what, path := range siteState {
		if path == w.ProjectDir || strings.HasPrefix(path, w.ProjectDir+string(filepath.Separator)) {
			t.Errorf("%s (%s) under ProjectDir (%s): destroyed by the next version bump",
				what, path, w.ProjectDir)
		}
		if !strings.HasPrefix(path, w.StateDir+string(filepath.Separator)) {
			t.Errorf("%s (%s) outside StateDir (%s)", what, path, w.StateDir)
		}
	}
}

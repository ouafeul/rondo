package sandbox

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewWritesAComponentCheckPasses(t *testing.T) {
	for name, c := range map[string]struct {
		archetypes            []string
		images, artifacts     int
		ports                 int
		present, absent       []string
		downstreams, vspheres int
	}{
		"a and b": {nil, 1, 1, 3, []string{"apps/hello-web/Chart.yaml", "build/hello-web/Dockerfile",
			"manifests/dependencies.yaml", "vm/payload/deploy/install.sh", "README.md"}, nil, 1, 1},
		"a": {[]string{"a"}, 1, 0, 2, []string{"platform/components/hello-podinfo.yaml"},
			[]string{"vm", "platform/components/hello-vm.yaml"}, 1, 0},
		"b": {[]string{"B"}, 0, 1, 1, []string{"platform/components/hello-vm.yaml"},
			[]string{"apps", "build", "manifests"}, 0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "hello")
			if err := RunNew(dir, c.archetypes); err != nil {
				t.Fatal(err)
			}
			if err := RunCheck(testSettings(t), dir, filepath.Join(dir, sandboxFileName)); err != nil {
				t.Errorf("check of the written component: %v", err)
			}
			sf, err := readSandboxFile(filepath.Join(dir, sandboxFileName))
			if err != nil {
				t.Fatal(err)
			}
			if sf.Name != "hello" || len(sf.Images) != c.images || len(sf.Artifacts) != c.artifacts ||
				len(sf.Ports) != c.ports || len(sf.Targets.Downstreams) != c.downstreams ||
				len(sf.Targets.VSphere) != c.vspheres {
				t.Errorf("rondo-sandbox.yaml: %+v", sf)
			}
			for _, p := range c.present {
				if !fileExists(filepath.Join(dir, p)) {
					t.Errorf("%s missing", p)
				}
			}
			for _, p := range c.absent {
				if fileExists(filepath.Join(dir, p)) {
					t.Errorf("%s written for archetypes %v", p, c.archetypes)
				}
			}
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, _ error) error {
				if d.IsDir() {
					return nil
				}
				raw, _ := os.ReadFile(p) //nolint:gosec // the test's own tree
				for _, left := range []string{scaffoldName, "CHANGEME"} {
					if strings.Contains(p, left) || strings.Contains(string(raw), left) {
						t.Errorf("%s: %s left over", p, left)
					}
				}
				info, _ := d.Info()
				if strings.HasSuffix(p, ".sh") && info.Mode().Perm() != 0o755 {
					t.Errorf("%s is %v, not executable", p, info.Mode().Perm())
				}
				return nil
			})
		})
	}
}

func TestNewRefuses(t *testing.T) {
	full := filepath.Join(t.TempDir(), "full")
	if err := os.Mkdir(full, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.Mkdir(empty, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := RunNew(empty, nil); err != nil {
		t.Errorf("empty directory: %v", err)
	}
	for name, c := range map[string]struct {
		dir        string
		archetypes []string
		want       string
	}{
		"non-empty directory": {full, nil, "is not empty"},
		"uppercase":           {filepath.Join(t.TempDir(), "Hello"), nil, "lowercase"},
		"too long":            {filepath.Join(t.TempDir(), strings.Repeat("a", 31)), nil, "30 characters"},
		"unknown archetype":   {filepath.Join(t.TempDir(), "hello"), []string{"a", "c"}, `archetype "c"`},
	} {
		t.Run(name, func(t *testing.T) {
			err := RunNew(c.dir, c.archetypes)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want %q", err, c.want)
			}
		})
	}
}

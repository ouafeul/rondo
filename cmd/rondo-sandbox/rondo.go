package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"rondo"
	"rondo/internal/version"
)

// rondoRegistry is the registry RONDO's images are published to: CI sets it at build time
// (-X main.rondoRegistry), RONDO_SANDBOX_RONDO_REGISTRY overrides it.
var rondoRegistry string

var (
	flagRondoFrom    string
	flagRondoVersion string
)

// rondoSource is the RONDO a sandbox deploys: by default this binary's own YAML and the images
// published for its version; from a clone, its YAML and images built from it.
type rondoSource struct {
	// CRDs, RONDO tree, first-party charts, dependencies manifest
	dir   string
	clone bool
	// the published images: registry/rondo-controller:version
	registry, version string
}

// env hands the source to the embedded script.
func (o rondoSource) env() []string {
	source := "registry"
	if o.clone {
		source = "clone"
	}
	return []string{"RONDO_SANDBOX_RONDO_DIR=" + o.dir, "RONDO_SANDBOX_RONDO_SOURCE=" + source,
		"RONDO_SANDBOX_RONDO_REGISTRY=" + o.registry, "RONDO_SANDBOX_RONDO_VERSION=" + o.version}
}

func resolveRondo() (rondoSource, error) {
	from := flagRondoFrom
	if from == "" {
		from = os.Getenv("RONDO_SRC_DIR")
	}
	if from != "" {
		dir, err := filepath.Abs(from)
		if err != nil {
			return rondoSource{}, err
		}
		if !isRondoSource(dir) {
			return rondoSource{}, fmt.Errorf("%s is not an RONDO clone (--rondo-from, RONDO_SRC_DIR)", dir)
		}
		return rondoSource{dir: dir, clone: true}, nil
	}
	ver := flagRondoVersion
	if ver == "" {
		ver = version.Version
	}
	if ver == "" || ver == "dev" {
		return rondoSource{}, fmt.Errorf("development rondo-sandbox, without a published RONDO version: " +
			"--rondo-from <RONDO clone> builds RONDO from its sources, --rondo-version <version> " +
			"takes a published one")
	}
	registry := os.Getenv("RONDO_SANDBOX_RONDO_REGISTRY")
	if registry == "" {
		registry = rondoRegistry
	}
	if registry == "" {
		return rondoSource{}, fmt.Errorf("unknown RONDO image registry: RONDO_SANDBOX_RONDO_REGISTRY=<host>/<repository> " +
			"(the package sets it; a hand-built binary does not)")
	}
	dir, err := extractTree()
	if err != nil {
		return rondoSource{}, err
	}
	return rondoSource{dir: dir, registry: registry, version: ver}, nil
}

func isRondoSource(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // a go.mod, read to identify the repository
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "module rondo" {
			return true
		}
	}
	return false
}

// extractTree lays this binary's RONDO YAML under the sandbox's state, once per version; a
// development build lays it again each time.
func extractTree() (string, error) {
	dir := filepath.Join(stateDir(), "rondo", version.Version)
	marker := filepath.Join(dir, ".complete")
	if _, err := os.Stat(marker); err == nil && version.Version != "dev" {
		return dir, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	err := fs.WalkDir(rondo.SandboxTree, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		raw, err := rondo.SandboxTree.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, raw, 0o644) //nolint:gosec // RONDO's public YAML
	})
	if err != nil {
		return "", fmt.Errorf("embedded RONDO tree to %s: %w", dir, err)
	}
	return dir, os.WriteFile(marker, []byte(version.Version+"\n"), 0o644) //nolint:gosec // a marker
}

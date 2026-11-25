package sandbox

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"rondo/internal/log"
	"rondo/internal/platform"
)

// scaffold is the hello-world component `component new` writes: a/ its charts (archetype A), b/
// its machine (B), README.md for both, __NAME__ standing for the component's name.
//
//go:embed all:scaffold
var scaffold embed.FS

const scaffoldName = "__NAME__"

// componentName is a name its Components, its Secret, its namespace and its machines start with.
var componentName = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,28}[a-z0-9])?$`)

// RunNew writes a hello-world component in arg, a new or empty directory it is named after: a
// first-party chart with its image and an upstream chart (archetype A), a machine (B), and the
// rondo-sandbox.yaml that installs them, its site written as `init` writes it. archetypes keeps
// a, b or both (the default).
func RunNew(arg string, archetypes []string) error {
	dir, err := filepath.Abs(arg)
	if err != nil {
		return err
	}
	name := filepath.Base(dir)
	if !componentName.MatchString(name) {
		return fmt.Errorf("%q: a component name is lowercase letters, digits and dashes, 30 characters "+
			"at most (it prefixes its Components, its Secret, its namespace, its machines)", name)
	}
	a, b, err := archetypeSet(archetypes)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	switch {
	case err == nil && len(entries) > 0:
		return fmt.Errorf("%s is not empty: `component new` writes a new component", dir)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return err
	}
	parts := []string{"README.md"}
	if a {
		parts = append(parts, "a")
	}
	if b {
		parts = append(parts, "b")
	}
	for _, part := range parts {
		if err := writeScaffold(path.Join("scaffold", part), dir, name); err != nil {
			return err
		}
	}
	tree, err := platform.Load(filepath.Join(dir, "platform"))
	if err != nil {
		return err
	}
	content := siteSection(tree, map[string]string{"helloMessage": "hello from " + name}) +
		componentSection(name, a, b)
	if err := os.WriteFile(filepath.Join(dir, sandboxFileName), []byte(content), 0o644); err != nil { //nolint:gosec // the component's own file
		return err
	}
	log.Step("component "+name+" written", "path", dir)
	log.Info("next: cd " + arg + " && rondo-sandbox deploy && rondo-sandbox deploy downstreams && " +
		"rondo-sandbox component install")
	return nil
}

func archetypeSet(archetypes []string) (a, b bool, err error) {
	if len(archetypes) == 0 {
		return true, true, nil
	}
	for _, x := range archetypes {
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "a":
			a = true
		case "b":
			b = true
		default:
			return false, false, fmt.Errorf("archetype %q: a (charts) or b (machines)", x)
		}
	}
	return a, b, nil
}

// writeScaffold copies an embedded file or tree into dir, __NAME__ replaced in paths and contents.
func writeScaffold(src, dir, name string) error {
	return fs.WalkDir(scaffold, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, src), "/")
		if rel == "" { // src is a file
			rel = path.Base(p)
		}
		dst := filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(rel, scaffoldName, name)))
		raw, err := scaffold.ReadFile(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { //nolint:gosec // the component's own tree
			return err
		}
		return os.WriteFile(dst, []byte(strings.ReplaceAll(string(raw), scaffoldName, name)), mode)
	})
}

// componentSection is rondo-sandbox.yaml's component part: what its CI publishes, the workstation
// ports to its ingresses and machine.
func componentSection(name string, a, b bool) string {
	var s strings.Builder
	s.WriteString("name: __NAME__\n")
	if a {
		s.WriteString("images:                                 # what the CI builds and pushes, with the chart's appVersion tag\n" +
			"  - name: __NAME__/web\n" +
			"    context: build/__NAME__-web\n" +
			"    tagFromChart: apps/__NAME__-web\n")
	}
	if b {
		s.WriteString("artifacts:                              # what it publishes to the generic repository\n" +
			"  - path: components/__NAME__-vm/0.1.0/payload.tar.gz\n" +
			"    dir: vm/payload                     # archived as tar.gz\n")
	}
	s.WriteString("ports:                                  # from the workstation to the ingresses and machines\n")
	if a {
		s.WriteString("  - hostPort: 8080                      # http://localhost:8080, the page\n" +
			"    host: __NAME__.apps.${INGRESS_BASE_DOMAIN}\n" +
			"  - hostPort: 8081                      # http://localhost:8081, podinfo\n" +
			"    host: __NAME__-podinfo.apps.${INGRESS_BASE_DOMAIN}\n")
	}
	if b {
		s.WriteString("  - hostPort: 8082                      # http://localhost:8082, the machine's page\n" +
			"    machine: __NAME__-vm-web-1\n" +
			"    port: 8080\n" +
			"    protocol: http                      # relayed over tcp, shown as an http:// link by status\n")
	}
	return strings.ReplaceAll(s.String(), scaffoldName, name)
}

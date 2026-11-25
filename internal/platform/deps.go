package platform

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

type Dependency struct {
	Name    string
	Version string
}

// Mirror is what manifests/dependencies.yaml makes available on a site. Artifacts are the
// paths of the `file` and `grafana_plugin` entries, relative to the file server root;
// RPMPaths the repository sub-paths of the `rpm` entries.
type Mirror struct {
	Charts    map[string]Dependency
	Artifacts map[string]bool
	RPMPaths  map[string]bool
}

type mirrorFile struct {
	HelmCharts []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"helm_charts"`
	Binaries       []mirrorArtifact `json:"binaries"`
	GrafanaPlugins []mirrorArtifact `json:"grafana_plugins"`
	RPMPackages    []struct {
		Path string `json:"path"`
	} `json:"rpm_packages"`
}

type mirrorArtifact struct {
	Version string `json:"version"`
	Path    string `json:"path"`
}

func LoadDependencies(path string) (*Mirror, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // repo manifest
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var manifest mirrorFile
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("%s unreadable: %w", path, err)
	}
	m := &Mirror{
		Charts:    map[string]Dependency{},
		Artifacts: map[string]bool{},
		RPMPaths:  map[string]bool{},
	}
	for _, c := range manifest.HelmCharts {
		m.Charts[c.Name] = Dependency{Name: c.Name, Version: c.Version}
	}
	for _, group := range [][]mirrorArtifact{manifest.Binaries, manifest.GrafanaPlugins} {
		for _, a := range group {
			if a.Path == "" {
				continue
			}
			m.Artifacts[artifactKey(expandVersion(a.Path, a.Version))] = true
		}
	}
	for _, r := range manifest.RPMPackages {
		if p := artifactKey(r.Path); p != "" {
			m.RPMPaths[p] = true
		}
	}
	if len(m.Charts) == 0 {
		return nil, fmt.Errorf("%s declares no chart (helm_charts section)", path)
	}
	return m, nil
}

func expandVersion(path, version string) string {
	return strings.ReplaceAll(path, "{version}", version)
}

func artifactKey(path string) string {
	return strings.Trim(path, "/")
}

var firstPartyCharts = map[string]bool{
	"downstream-bootstrap":   true,
	"local-path-provisioner": true,
}

func (t *Tree) ValidateDependencies(m *Mirror) []Finding {
	var out []Finding
	for _, obj := range t.Objects {
		if obj.Kind != "Component" {
			continue
		}
		if vm, ok := nestedMap(obj.Raw, "spec", "vm"); ok {
			out = append(out, validateVMMirror(obj, vm, m)...)
			continue
		}
		out = append(out, validateChartMirror(obj, m)...)
	}
	return out
}

func validateChartMirror(obj Object, m *Mirror) []Finding {
	oci, _ := nestedString(obj.Raw, "spec", "chart", "oci")
	version, _ := nestedString(obj.Raw, "spec", "chart", "version")
	if oci == "" {
		return nil
	}
	chart := chartNameFromOCI(oci)
	if chart == "" {
		return []Finding{{obj.Path, LevelError, fmt.Sprintf(
			"Component/%s: chart URL %q, chart name not found", obj.Name, oci)}}
	}
	if firstPartyCharts[chart] {
		return nil
	}
	if published, ok := nestedBool(obj.Raw, "spec", "chart", "published"); ok && published {
		return nil
	}
	dep, known := m.Charts[chart]
	if !known {
		return []Finding{{obj.Path, LevelError, fmt.Sprintf(
			"Component/%s pins chart %q, which is missing from the dependencies manifest "+
				"read: it will NOT be mirrored, and `helm pull` will fail with 404 on the "+
				"cluster. Three causes, three fixes: the chart is mirrored from "+
				"upstream => add it to the manifest; it is published by the component's "+
				"CI => set `spec.chart.published: true`; the manifest read is "+
				"the wrong one (component delivered separately) => `--deps <its "+
				"dependencies.yaml>`. Declared charts: %s",
			obj.Name, chart, strings.Join(sortedDepNames(m.Charts), ", "))}}
	}
	if !sameVersion(dep.Version, version) {
		return []Finding{{obj.Path, LevelError, fmt.Sprintf(
			"Component/%s pins %s@%s but dependencies.yaml declares %s; "+
				"only the mirrored version exists offline",
			obj.Name, chart, version, dep.Version)}}
	}
	return nil
}

func validateVMMirror(obj Object, vm map[string]any, m *Mirror) []Finding {
	var out []Finding

	artifacts, _ := nestedSlice(vm, "artifacts")
	for i, raw := range artifacts {
		a, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path, _ := a["path"].(string)
		if path == "" {
			continue
		}
		where := fmt.Sprintf("Component/%s, spec.vm.artifacts[%d]", obj.Name, i)

		if unresolved(path) {
			out = append(out, Finding{obj.Path, LevelWarning, fmt.Sprintf(
				"%s: path %q holds a placeholder, mirror coverage NOT verified",
				where, path)})
			continue
		}
		provenance, _ := a["provenance"].(string)
		if provenance == provenanceComponent {
			out = append(out, checkComponentArtifact(obj, where, path)...)
			continue
		}
		if m.Artifacts[artifactKey(path)] {
			continue
		}
		out = append(out, Finding{obj.Path, LevelError, fmt.Sprintf(
			"%s references %s with provenance `upstream` (the default), missing from "+
				"manifests/dependencies.yaml: the artifact will NOT be mirrored, and its "+
				"download will fail with 404 ON THE MACHINE, after provisioning. "+
				"Either it is an upstream COTS artifact and must be declared in the manifest "+
				"(`kind: file`, `path: %s`), or it is an artifact published by "+
				"the component's CI and must be annotated `provenance: component`.",
			where, path, path)})
	}

	repos, _ := nestedSlice(vm, "rpmRepos")
	for i, raw := range repos {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path, _ := r["path"].(string)
		if path == "" {
			continue
		}
		where := fmt.Sprintf("Component/%s, spec.vm.rpmRepos[%d]", obj.Name, i)
		if unresolved(path) {
			out = append(out, Finding{obj.Path, LevelWarning, fmt.Sprintf(
				"%s: path %q holds a placeholder, mirror coverage NOT verified",
				where, path)})
			continue
		}
		if m.RPMPaths[artifactKey(path)] {
			continue
		}
		out = append(out, Finding{obj.Path, LevelError, fmt.Sprintf(
			"%s configures package repository path %q, which no `rpm_packages` entry of "+
				"manifests/dependencies.yaml feeds: `dnf` would find nothing there. "+
				"Fed paths: %s",
			where, path, strings.Join(sortedNames(m.RPMPaths), ", "))})
	}
	return out
}

const componentArtifactPrefix = "components/"

func checkComponentArtifact(obj Object, where, path string) []Finding {
	var out []Finding
	want := componentArtifactPrefix + obj.Name + "/"
	if !strings.HasPrefix(strings.TrimLeft(path, "/"), want) {
		out = append(out, Finding{obj.Path, LevelError, fmt.Sprintf(
			"%s is annotated `provenance: component` but its path %q does not follow the "+
				"publishing convention %q; a path off the convention is either a "+
				"typo or another component's artifact. This artifact is "+
				"published by the component's CI, NOT by the rondo mirror: check that "+
				"its pipeline did publish this version.",
			where, path, want+"")})
	}
	return out
}

func unresolved(s string) bool { return strings.Contains(s, "${") }

// chartNameFromOCI is the chart of an oci:// reference, or of one under ${CHARTS_OCI}.
func chartNameFromOCI(oci string) string {
	rest, ok := strings.CutPrefix(oci, "oci://")
	if !ok {
		rest, ok = strings.CutPrefix(oci, "${CHARTS_OCI}")
	}
	if !ok {
		return ""
	}
	idx := strings.LastIndex(rest, "/")
	if idx < 0 || idx == len(rest)-1 {
		return ""
	}
	return rest[idx+1:]
}

func sameVersion(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

func sortedDepNames(deps map[string]Dependency) []string {
	out := make([]string, 0, len(deps))
	for k := range deps {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

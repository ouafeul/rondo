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

type Mirror struct {
	Charts       map[string]Dependency
	Artifacts    map[string]bool
	GenericRepos map[string]bool
	RPMRepos     map[string]bool
}

type mirrorFile struct {
	HelmCharts []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"helm_charts"`
	Binaries       []mirrorArtifact `json:"binaries"`
	GrafanaPlugins []mirrorArtifact `json:"grafana_plugins"`
	RPMPackages    []struct {
		TargetRepo string `json:"target_repo"`
	} `json:"rpm_packages"`
}

type mirrorArtifact struct {
	Version    string `json:"version"`
	TargetRepo string `json:"target_repo"`
	TargetPath string `json:"target_path"`
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
		Charts:       map[string]Dependency{},
		Artifacts:    map[string]bool{},
		GenericRepos: map[string]bool{},
		RPMRepos:     map[string]bool{},
	}
	for _, c := range manifest.HelmCharts {
		m.Charts[c.Name] = Dependency{Name: c.Name, Version: c.Version}
	}
	for _, group := range [][]mirrorArtifact{manifest.Binaries, manifest.GrafanaPlugins} {
		for _, a := range group {
			if a.TargetRepo == "" || a.TargetPath == "" {
				continue
			}
			m.GenericRepos[a.TargetRepo] = true
			m.Artifacts[artifactKey(a.TargetRepo, expandVersion(a.TargetPath, a.Version))] = true
		}
	}
	for _, r := range manifest.RPMPackages {
		if r.TargetRepo != "" {
			m.RPMRepos[r.TargetRepo] = true
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

func artifactKey(repo, path string) string {
	return strings.Trim(repo, "/") + "/" + strings.Trim(path, "/")
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
		repo, _ := a["repo"].(string)
		path, _ := a["path"].(string)
		if repo == "" || path == "" {
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
			out = append(out, checkComponentArtifact(obj, where, repo, path, m)...)
			continue
		}
		if unresolved(repo) {
			out = append(out, Finding{obj.Path, LevelWarning, fmt.Sprintf(
				"%s: repository %q holds a placeholder, mirror coverage NOT verified",
				where, repo)})
			continue
		}
		if m.Artifacts[artifactKey(repo, path)] {
			continue
		}
		out = append(out, Finding{obj.Path, LevelError, fmt.Sprintf(
			"%s references %s/%s with provenance `upstream` (the default), missing from "+
				"manifests/dependencies.yaml: the artifact will NOT be mirrored, and its "+
				"download will fail with 404 ON THE MACHINE, after provisioning. "+
				"Either it is an upstream COTS artifact and must be declared in the manifest "+
				"(target repository %s, target path %s), or it is an artifact published by "+
				"the component's CI and must be annotated `provenance: component`.",
			where, repo, path, repo, path)})
	}

	repos, _ := nestedSlice(vm, "rpmRepos")
	for i, raw := range repos {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		repo, _ := r["repo"].(string)
		if repo == "" {
			continue
		}
		where := fmt.Sprintf("Component/%s, spec.vm.rpmRepos[%d]", obj.Name, i)
		if unresolved(repo) {
			out = append(out, Finding{obj.Path, LevelWarning, fmt.Sprintf(
				"%s: repository %q holds a placeholder, mirror coverage NOT verified",
				where, repo)})
			continue
		}
		if m.RPMRepos[repo] {
			continue
		}
		out = append(out, Finding{obj.Path, LevelError, fmt.Sprintf(
			"%s configures package repository %q, which no `rpm_packages` entry of "+
				"manifests/dependencies.yaml feeds: `dnf` would find nothing there. "+
				"Fed repositories: %s",
			where, repo, strings.Join(sortedNames(m.RPMRepos), ", "))})
	}
	return out
}

const componentArtifactPrefix = "components/"

func checkComponentArtifact(obj Object, where, repo, path string, m *Mirror) []Finding {
	var out []Finding
	if !unresolved(repo) && len(m.GenericRepos) > 0 && !m.GenericRepos[repo] {
		out = append(out, Finding{obj.Path, LevelError, fmt.Sprintf(
			"%s publishes to repository %q, which no entry of manifests/dependencies.yaml "+
				"uses: wrong repository name? Known repositories: %s",
			where, repo, strings.Join(sortedNames(m.GenericRepos), ", "))})
	}
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

func chartNameFromOCI(oci string) string {
	rest := strings.TrimPrefix(oci, "oci://")
	if rest == oci {
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

package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"rondo/api/v1alpha1"
	"rondo/internal/log"
	"rondo/internal/platform"
)

// componentKeys are the keys of rondo-sandbox.yaml that are the component's, kept as they are when
// init writes the site's again.
type componentKeys struct {
	Name         string            `json:"name,omitempty"`
	Platform     string            `json:"platform,omitempty"`
	Apps         string            `json:"apps,omitempty"`
	Dependencies string            `json:"dependencies,omitempty"`
	Images       []imageBuild      `json:"images,omitempty"`
	Ports        []portSpec        `json:"ports,omitempty"`
	Artifacts    []artifactBuild   `json:"artifacts,omitempty"`
	RPMRepos     map[string]string `json:"rpmRepos,omitempty"`
}

// RunInit writes rondo-sandbox.yaml at a component's root, as `rondo init` writes rondo.yaml: a
// downstream per role its chart Enrollments name, a vSphere Target per role its machine ones
// name, the componentValues and Secrets its tree reads. An existing file keeps the component's
// keys; only --force writes its targets and secrets again.
func RunInit(dir string, force bool) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, sandboxFileName)
	existing := fileExists(path)
	if existing && !force {
		return fmt.Errorf("%s already exists (--force rewrites its targets and secrets, and keeps the rest)", path)
	}
	sf, err := readSandboxFile(path)
	if err != nil {
		return err
	}
	tree, err := platform.Load(filepath.Join(dir, orDefault(sf.Platform, "platform")))
	if err != nil {
		return err
	}
	if _, enrolls, vmEnrolls := index(tree); len(enrolls)+len(vmEnrolls) == 0 {
		return fmt.Errorf("%s: no Enrollment, nothing to describe", dir)
	}
	var b strings.Builder
	b.WriteString(siteSection(tree, nil))
	rest, err := yaml.Marshal(componentKeys{Name: sf.Name, Platform: sf.Platform, Apps: sf.Apps,
		Dependencies: sf.Dependencies, Images: sf.Images, Ports: sf.Ports, Artifacts: sf.Artifacts,
		RPMRepos: sf.RPMRepos})
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(string(rest)); s != "{}" {
		b.WriteString(s + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil { //nolint:gosec // the component's own file
		return err
	}
	log.Step(sandboxFileName+" written, to be completed", "path", path)
	return nil
}

// siteSection is rondo-sandbox.yaml's site, from a component's tree: a downstream per role its
// chart Enrollments name, a vSphere Target per role its machine ones name, the componentValues
// and Secrets the tree reads. values gives componentValues; the others are left to fill in.
func siteSection(tree *platform.Tree, values map[string]string) string {
	_, enrolls, vmEnrolls := index(tree)
	reads := valuesRead(tree)
	var b strings.Builder
	b.WriteString("# The sandbox site, with the keys of rondo.yaml (targets, secrets), and what the component's CI\n" +
		"# publishes (images, artifacts...): `rondo-sandbox deploy`, `deploy downstreams`, then\n" +
		"# `component install`. Docs: components/sandbox.\n")
	b.WriteString("targets:\n")
	b.WriteString("  # management:\n" +
		"  #   roles: [<role>]   # sandbox only: this role on the management cluster, one cluster fewer\n")
	writeTargets(&b, "downstreams", roleTargets(enrolls), reads, values)
	writeTargets(&b, "vsphere", roleTargets(vmEnrolls), reads, values)
	if secrets := secretsFor(tree, nil, nil); len(secrets) > 0 {
		b.WriteString("secrets:\n  components:\n")
		for _, name := range sortedKeys(secrets) {
			fmt.Fprintf(&b, "    %s:\n", name)
			for _, k := range sortedKeys(secrets[name]) {
				fmt.Fprintf(&b, "      %s: %s   # otherwise `%s`\n", k, sandboxSecret, sandboxSecret)
			}
		}
	}
	return b.String()
}

// roleTargets are the roles Enrollments name, management aside, each the Target of the same name.
func roleTargets(enrolls []enrollmentInfo) map[string][]string {
	out := map[string][]string{}
	for _, e := range enrolls {
		if e.role != managementRole {
			out[e.role] = append(out[e.role], e.name)
		}
	}
	return out
}

func writeTargets(b *strings.Builder, kind string, targets, reads map[string][]string, values map[string]string) {
	if len(targets) == 0 {
		return
	}
	fmt.Fprintf(b, "  %s:\n", kind)
	for _, role := range sortedKeys(targets) {
		name := strings.TrimRight(role[:min(len(role), 24)], "-")
		fmt.Fprintf(b, "    - name: %s   # Enrollments %s\n", name, strings.Join(targets[role], ", "))
		fmt.Fprintf(b, "      roles: [%s]\n", role)
		var keys []string
		for _, e := range targets[role] {
			keys = append(keys, reads[e]...)
		}
		slices.Sort(keys)
		keys = slices.Compact(keys)
		if len(keys) == 0 {
			continue
		}
		b.WriteString("      componentValues:\n")
		for _, k := range keys {
			v, ok := values[k]
			if !ok {
				fmt.Fprintf(b, "        %s: CHANGEME   # to fill in\n", k)
				continue
			}
			raw, _ := yaml.Marshal(v)
			fmt.Fprintf(b, "        %s: %s\n", k, strings.TrimSpace(string(raw)))
		}
	}
}

// valuesRead are the componentValues each Enrollment reads, through itself or its Component:
// what a site's Target must give, standard and machine values aside.
func valuesRead(tree *platform.Tree) map[string][]string {
	objects := map[string]platform.Object{}
	for _, o := range tree.Objects {
		objects[o.Kind+"/"+o.Name] = o
	}
	out := map[string][]string{}
	for _, o := range tree.Objects {
		if o.Kind != "Enrollment" {
			continue
		}
		spec, _ := o.Raw["spec"].(map[string]any)
		comp, _ := spec["component"].(string)
		for _, obj := range []platform.Object{o, objects["Component/"+comp]} {
			raw, _ := json.Marshal(obj.Raw)
			for _, m := range targetValueToken.FindAllStringSubmatch(string(raw), -1) {
				k := m[1]
				if slices.Contains(standardTargetValues, k) || k == v1alpha1.ValueVMTemplate ||
					k == v1alpha1.ValueSSHUser || strings.HasPrefix(k, "vmIPs.") {
					continue
				}
				out[o.Name] = append(out[o.Name], k)
			}
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

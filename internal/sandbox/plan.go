package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"rondo/api/v1alpha1"
	"rondo/internal/kobj"
	"rondo/internal/log"
	"rondo/internal/platform"
)

// imageBuild is an image the component's CI publishes itself, built here from its sources.
type imageBuild struct {
	Name       string `json:"name"`
	Context    string `json:"context"`
	Dockerfile string `json:"dockerfile,omitempty"`
	Tag        string `json:"tag,omitempty"`
	// a chart directory whose appVersion is the tag, as the component's CI does
	TagFromChart string `json:"tagFromChart,omitempty"`
}

// plan is what `component install` made of a component against the site, kept while it is
// installed: status and the debug commands read every installed component's.
type plan struct {
	Component    string                       `json:"component"`
	Dir          string                       `json:"dir"`
	Platform     string                       `json:"platform"`
	Apps         string                       `json:"apps"`
	Dependencies string                       `json:"dependencies,omitempty"`
	Images       []imageBuild                 `json:"images,omitempty"`
	Secrets      map[string]map[string]string `json:"secrets"`
	Ports        []forward                    `json:"ports,omitempty"`
	Enrollments  []string                     `json:"enrollments"`
	// its Components, and the Target each Enrollment resolves to, and the release a chart one
	// delivers there
	Components []string          `json:"components"`
	Targets    map[string]string `json:"targets"`
	Releases   map[string]string `json:"releases,omitempty"`
	Namespaces []string          `json:"namespaces"`
	// machine components (archetypes B and C), and the values their vSphere Target gets for
	// them (vmIPs.<component>.<group>), by Target
	Machines   []machine                    `json:"machines,omitempty"`
	VMValues   map[string]map[string]string `json:"vmValues,omitempty"`
	Artifacts  []artifactBuild              `json:"artifacts,omitempty"`
	RPMProxies []rpmProxy                   `json:"rpmProxies,omitempty"`
	// filled by publish
	Tree     string `json:"tree,omitempty"`
	Registry string `json:"registry,omitempty"`
	NoBuild  bool   `json:"noBuild,omitempty"`
}

func loadPlan(s Settings, component string) (*plan, error) {
	raw, err := os.ReadFile(s.planPath(component))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("component %q is not installed on the sandbox (installed: %s)", component,
			orDefault(strings.Join(Components(s), ", "), "none"))
	}
	if err != nil {
		return nil, err
	}
	var p plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%s unreadable: %w", s.planPath(component), err)
	}
	return &p, nil
}

// loadPlans are the installed components' plans, by name.
func loadPlans(s Settings) ([]*plan, error) {
	var out []*plan
	for _, name := range Components(s) {
		p, err := loadPlan(s, name)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func savePlan(s Settings, p *plan) error {
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.plansDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.planPath(p.Component), append(raw, '\n'), 0o600)
}

// Components are the components installed on the sandbox, for status and completion.
func Components(s Settings) []string {
	entries, err := os.ReadDir(s.plansDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".json"); ok && !e.IsDir() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Enrollments are the installed components', for completion; none without a sandbox.
func Enrollments(s Settings) []string {
	plans, err := loadPlans(s)
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range plans {
		out = append(out, p.Enrollments...)
	}
	sort.Strings(out)
	return out
}

// Values every Kubernetes Target of the sandbox provides, as `rondo deploy` does on a site.
var standardTargetValues = []string{"ingressIP", "imageRegistry", "mirrorCA", "storageClass"}

var targetValueToken = regexp.MustCompile(`\$\{target\.values\.([A-Za-z0-9_.-]+)\}`)

// RunPlan plans a component against the deployed site, and writes what the script reconciles for
// every installed component (writeInstalled).
func RunPlan(s Settings, dir, ipPrefix string) error {
	st, err := loadSite(s)
	if err != nil {
		return err
	}
	p, err := planComponent(s, st, dir, ipPrefix)
	if err != nil {
		return err
	}
	if err := savePlan(s, p); err != nil {
		return err
	}
	if err := writeInstalled(s); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.StateDir, "installing"), []byte(p.Component+"\n"), 0o600)
}

func planComponent(s Settings, st *site, dir, ipPrefix string) (*plan, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if !fileExists(dir) {
		return nil, fmt.Errorf("%s: directory not found", dir)
	}
	sf, err := readSandboxFile(filepath.Join(dir, sandboxFileName))
	if err != nil {
		return nil, err
	}
	p := &plan{
		Component: orDefault(sf.Name, filepath.Base(dir)),
		Dir:       dir,
		Platform:  filepath.Join(dir, orDefault(sf.Platform, "platform")),
		Apps:      filepath.Join(dir, orDefault(sf.Apps, "apps")),
		Images:    sf.Images,
		Artifacts: sf.Artifacts,
	}
	if deps := filepath.Join(dir, orDefault(sf.Dependencies, "manifests/dependencies.yaml")); fileExists(deps) {
		p.Dependencies = deps
	}
	log.Step("validating the tree", "component", p.Component, "tree", p.Platform)
	if err := lint(s, p); err != nil {
		return nil, err
	}
	tree, err := platform.Load(p.Platform)
	if err != nil {
		return nil, err
	}
	comps, enrolls, vmEnrolls := index(tree)
	if len(enrolls)+len(vmEnrolls) == 0 {
		return nil, fmt.Errorf("%s: no Enrollment to deploy", p.Platform)
	}
	all, err := loadPlans(s)
	if err != nil {
		return nil, err
	}
	var others []*plan
	var previous *plan
	for _, o := range all {
		if o.Component == p.Component {
			previous = o
			continue
		}
		others = append(others, o)
	}
	for name := range comps {
		p.Components = append(p.Components, name)
	}
	sort.Strings(p.Components)
	if err := checkOwnership(p, enrolls, vmEnrolls, others); err != nil {
		return nil, err
	}
	base, err := baseReleases(s)
	if err != nil {
		return nil, err
	}
	log.Step("Targets of the Enrollments")
	if err := placeEnrollments(st, p, enrolls, vmEnrolls, base, others); err != nil {
		return nil, err
	}
	if err := planMachines(tree, st, p, vmEnrolls, ipPrefix, others, previous); err != nil {
		return nil, err
	}
	if err := checkTargetValues(tree, st, p); err != nil {
		return nil, err
	}
	if err := checkMachineSources(tree, p, sf); err != nil {
		return nil, err
	}
	declared := map[string]map[string]string{}
	for _, src := range []map[string]map[string]string{st.Secrets, sf.Secrets.Components} {
		for name, data := range src {
			if declared[name] == nil {
				declared[name] = map[string]string{}
			}
			for k, v := range data {
				declared[name][k] = v
			}
		}
	}
	p.Secrets = secretsFor(tree, declared, sf.Secrets.Components)
	logContract(tree, append(enrolls, vmEnrolls...), p)
	for _, e := range append(enrolls, vmEnrolls...) {
		p.Enrollments = append(p.Enrollments, e.name)
	}
	sort.Strings(p.Enrollments)
	nss := map[string]bool{}
	for _, c := range comps {
		if !c.vm {
			nss[c.release.namespace] = true
		}
	}
	p.Namespaces = sortedSet(nss)
	if p.Ports, err = resolvePorts(st, p, sf.Ports); err != nil {
		return nil, err
	}
	for _, o := range others {
		for _, f := range o.Ports {
			for _, mine := range p.Ports {
				if f.listen() == mine.listen() {
					return nil, fmt.Errorf("workstation port %s already forwarded by component %s", mine.listen(), o.Component)
				}
			}
		}
	}
	return p, nil
}

// lint is `rondo component lint`, with the component's dependencies manifest when it has one.
func lint(s Settings, p *plan) error {
	findings, err := platform.ValidateDir(p.Platform, filepath.Join(s.RondoDir, "manifests", "crds"),
		p.Dependencies)
	if err != nil {
		return err
	}
	var errs int
	for _, f := range findings {
		if f.Level == platform.LevelError {
			errs++
			log.Error(f.Path + ": " + f.Message)
			continue
		}
		log.Warn(f.Path + ": " + f.Message)
	}
	if errs > 0 {
		return fmt.Errorf("%d error(s) in %s: the site would reject this tree", errs, p.Platform)
	}
	log.Info("tree valid", "warnings", len(findings))
	return nil
}

type release struct{ name, namespace string }

func (r release) String() string { return r.name + " (" + r.namespace + ")" }

type componentInfo struct {
	name    string
	release release
	vm      bool
	oci     string
	version string
}

type enrollmentInfo struct {
	name, component, role string
	release               release
}

func componentOf(o platform.Object) componentInfo {
	spec, _ := o.Raw["spec"].(map[string]any)
	c := componentInfo{name: o.Name}
	if _, ok := spec["vm"]; ok {
		c.vm = true
		return c
	}
	c.release.namespace, _ = spec["namespace"].(string)
	c.release.name, _ = spec["releaseName"].(string)
	if c.release.name == "" {
		c.release.name = o.Name
	}
	if chart, ok := spec["chart"].(map[string]any); ok {
		c.oci, _ = chart["oci"].(string)
		c.version, _ = chart["version"].(string)
	}
	return c
}

// index reads the tree's Components and Enrollments: the chart Enrollments (archetype A), which
// go on clusters, and the machine ones (B and C), which go on vSphere Targets.
func index(tree *platform.Tree) (map[string]componentInfo, []enrollmentInfo, []enrollmentInfo) {
	comps := map[string]componentInfo{}
	for _, o := range tree.Objects {
		if o.Kind == "Component" {
			comps[o.Name] = componentOf(o)
		}
	}
	var charts, machines []enrollmentInfo
	for _, o := range tree.Objects {
		if o.Kind != "Enrollment" {
			continue
		}
		spec, _ := o.Raw["spec"].(map[string]any)
		comp, _ := spec["component"].(string)
		role, _ := spec["target"].(string)
		e := enrollmentInfo{name: o.Name, component: comp, role: role, release: comps[comp].release}
		if comps[comp].vm {
			machines = append(machines, e)
			continue
		}
		charts = append(charts, e)
	}
	byName := func(l []enrollmentInfo) {
		sort.Slice(l, func(i, j int) bool { return l[i].name < l[j].name })
	}
	byName(charts)
	byName(machines)
	return comps, charts, machines
}

// checkOwnership refuses a Component or an Enrollment another installed component delivers:
// their names are the cluster's, on a site too the second tree would overwrite the first.
func checkOwnership(p *plan, enrolls, vmEnrolls []enrollmentInfo, others []*plan) error {
	for _, o := range others {
		for _, c := range p.Components {
			if slices.Contains(o.Components, c) {
				return fmt.Errorf("Component/%s already installed by component %s: on a site too, the second "+
					"tree would overwrite the first", c, o.Component)
			}
		}
		for _, e := range append(slices.Clone(enrolls), vmEnrolls...) {
			if _, ok := o.Targets[e.name]; ok {
				return fmt.Errorf("Enrollment/%s already installed by component %s: on a site too, the second "+
					"tree would overwrite the first", e.name, o.Component)
			}
		}
	}
	return nil
}

// placeEnrollments resolves each Enrollment's target, a Target's name or a role, on the site as
// the controller will: a chart Enrollment on a cluster, a machine one on a vSphere Target. It
// refuses what a site would leave pending or break: no Target for it, a Target of the other
// kind, a release another Enrollment already delivers there.
func placeEnrollments(st *site, p *plan, enrolls, vmEnrolls []enrollmentInfo, base map[release]string, others []*plan) error {
	p.Targets, p.Releases = map[string]string{}, map[string]string{}
	occupied := map[string]map[string]string{}
	for _, o := range others {
		for e, r := range o.Releases {
			t := o.Targets[e]
			if occupied[t] == nil {
				occupied[t] = map[string]string{}
			}
			occupied[t][r] = e
		}
	}
	for _, e := range enrolls {
		if comp, ok := base[e.release]; ok {
			return fmt.Errorf("Enrollment %s: release %s is the one of Component %s of RONDO's "+
				"base layer, present on every target: a collision on a site too",
				e.name, e.release, comp)
		}
		c := st.clusterOf(e.role)
		if c == nil {
			return unplaced(st, e, false)
		}
		if occupied[c.Target] == nil {
			occupied[c.Target] = map[string]string{}
		}
		if other, ok := occupied[c.Target][e.release.String()]; ok {
			return fmt.Errorf("Enrollments %s and %s: same release %s on Target %s: a collision on a "+
				"site too; give their roles to two Targets of %s", other, e.name, e.release, c.Target,
				sandboxFileName)
		}
		occupied[c.Target][e.release.String()] = e.name
		p.Targets[e.name], p.Releases[e.name] = c.Target, e.release.String()
		log.Info("Enrollment", "name", e.name, "role", e.role, "target", c.Target, "cluster", c.Name)
	}
	for _, e := range vmEnrolls {
		t := st.vsphereOf(e.role)
		if t == nil {
			return unplaced(st, e, true)
		}
		p.Targets[e.name] = t.Name
		log.Info("Enrollment", "name", e.name, "role", e.role, "target", t.Name, "machines", "containers")
	}
	return nil
}

// unplaced says why no Target of the site takes an Enrollment, and what to change.
func unplaced(st *site, e enrollmentInfo, vm bool) error {
	if vm {
		if c := st.clusterOf(e.role); c != nil {
			return fmt.Errorf("Enrollment %s (machine component) targets %q, Target %s, a cluster: "+
				"a machine goes on a vSphere Target (targets.vsphere of %s)", e.name, e.role, c.Target, sandboxFileName)
		}
		return fmt.Errorf("Enrollment %s targets %q, which no vSphere Target holds: add it to the roles "+
			"of a targets.vsphere entry of %s, then run `rondo-sandbox deploy`", e.name, e.role, sandboxFileName)
	}
	if t := st.vsphereOf(e.role); t != nil {
		return fmt.Errorf("Enrollment %s (chart) targets %q, vSphere Target %s: a chart goes on a cluster "+
			"(targets.downstreams of %s)", e.name, e.role, t.Name, sandboxFileName)
	}
	for _, d := range st.Downstreams {
		if d.Name == e.role || slices.Contains(d.Roles, e.role) {
			return fmt.Errorf("Enrollment %s targets %q, held by downstream %s, not deployed yet: "+
				"`rondo-sandbox deploy downstreams %s`", e.name, e.role, d.Name, d.Name)
		}
	}
	return fmt.Errorf("Enrollment %s targets %q, which no Target holds: add it to the roles of a "+
		"targets.downstreams entry of %s (or of targets.management, for a one-cluster sandbox), then run "+
		"`rondo-sandbox deploy downstreams` (or `deploy`)", e.name, e.role, sandboxFileName)
}

// checkTargetValues refuses a token of a value the Target an Enrollment resolves to does not
// have: on a site, the controller would leave it unresolved.
func checkTargetValues(tree *platform.Tree, st *site, p *plan) error {
	objects := map[string]platform.Object{}
	for _, o := range tree.Objects {
		objects[o.Kind+"/"+o.Name] = o
	}
	var missing []string
	for _, o := range tree.Objects {
		if o.Kind != "Enrollment" {
			continue
		}
		target := p.Targets[o.Name]
		have := targetValueKeys(st, p, target)
		spec, _ := o.Raw["spec"].(map[string]any)
		comp, _ := spec["component"].(string)
		for _, obj := range []platform.Object{o, objects["Component/"+comp]} {
			raw, err := json.Marshal(obj.Raw)
			if err != nil {
				return err
			}
			for _, m := range targetValueToken.FindAllStringSubmatch(string(raw), -1) {
				if !have(m[1]) {
					missing = append(missing, fmt.Sprintf("%s/%s reads ${target.values.%s} on Target %s",
						obj.Kind, obj.Name, m[1], target))
				}
			}
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		missing = slices.Compact(missing)
		return fmt.Errorf("missing Target values: %s; set them in the componentValues of these "+
			"Targets (%s), then run `rondo-sandbox deploy` or `deploy downstreams`, which apply them",
			strings.Join(missing, "; "), sandboxFileName)
	}
	return nil
}

// targetValueKeys says whether a Target of the site gives a value: the standard ones of a
// cluster, a vSphere Target's template, SSH user and machine IPs, and rondo-sandbox.yaml's
// componentValues.
func targetValueKeys(st *site, p *plan, target string) func(string) bool {
	if c := st.cluster(target); c != nil {
		values := st.values(c)
		return func(k string) bool {
			_, ok := values[k]
			return slices.Contains(standardTargetValues, k) || ok
		}
	}
	t := st.vsphereOf(target)
	return func(k string) bool {
		if k == v1alpha1.ValueVMTemplate || k == v1alpha1.ValueSSHUser {
			return true
		}
		if _, ok := p.VMValues[target][k]; ok {
			return true
		}
		if t == nil {
			return false
		}
		_, ok := t.ComponentValues[k]
		return ok
	}
}

// secretsFor gives every Secret the tree reads, through valuesFrom or a machine installer's
// secretsFrom: the declared value when there is one, `sandbox` otherwise; and every Secret of
// own, the component's rondo-sandbox.yaml, read or not. rondo-platform-values is RONDO's, never
// the component's.
func secretsFor(tree *platform.Tree, declared, own map[string]map[string]string) map[string]map[string]string {
	out := map[string]map[string]string{}
	var generated []string
	for _, o := range tree.Objects {
		spec, _ := o.Raw["spec"].(map[string]any)
		sources, _ := spec["valuesFrom"].([]any)
		if vm, _ := spec["vm"].(map[string]any); vm != nil {
			installer, _ := vm["installer"].(map[string]any)
			from, _ := installer["secretsFrom"].([]any)
			sources = append(sources, from...)
		}
		for _, s := range sources {
			src, _ := s.(map[string]any)
			ref, _ := src["secretRef"].(map[string]any)
			name, _ := ref["name"].(string)
			if name == "" || name == kobj.PlatformValuesSecret {
				continue
			}
			keys, _ := src["keys"].([]any)
			for _, k := range keys {
				key, _ := k.(string)
				if out[name] == nil {
					out[name] = map[string]string{}
				}
				if v := declared[name][key]; v != "" {
					out[name][key] = v
					continue
				}
				out[name][key] = sandboxSecret
				generated = append(generated, name+"."+key)
			}
		}
	}
	for name, data := range own {
		for k, v := range data {
			if out[name] == nil {
				out[name] = map[string]string{}
			}
			out[name][k] = v
		}
	}
	if len(generated) > 0 {
		slices.Sort(generated)
		log.Info("component secrets set to `"+sandboxSecret+"` ("+sandboxFileName+", secrets.components, sets them)",
			"keys", strings.Join(slices.Compact(generated), ", "))
	}
	return out
}

// logContract says what a site must give the component: a Target per role, the Secrets of
// rondo.yaml, the componentValues read.
func logContract(tree *platform.Tree, enrolls []enrollmentInfo, p *plan) {
	roles := map[string]bool{}
	for _, e := range enrolls {
		roles[e.role] = true
	}
	var secrets []string
	for name, data := range p.Secrets {
		keys := make([]string, 0, len(data))
		for k := range data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		secrets = append(secrets, "secrets.components."+name+".{"+strings.Join(keys, ",")+"}")
	}
	sort.Strings(secrets)
	values := map[string]bool{}
	for _, o := range tree.Objects {
		raw, _ := json.Marshal(o.Raw)
		for _, m := range targetValueToken.FindAllStringSubmatch(string(raw), -1) {
			k := m[1]
			if slices.Contains(standardTargetValues, k) || k == v1alpha1.ValueVMTemplate ||
				k == v1alpha1.ValueSSHUser || strings.HasPrefix(k, "vmIPs.") {
				continue
			}
			values[k] = true
		}
	}
	log.Info("site contract: what a site's rondo.yaml must hold for this component",
		"roles", strings.Join(sortedSet(roles), ","),
		"secrets", orDefault(strings.Join(secrets, " "), "none"),
		"componentValues", orDefault(strings.Join(sortedSet(values), ","), "none"))
	if len(p.Machines) > 0 {
		var ips []string
		for _, values := range p.VMValues {
			for k, v := range values {
				var list []string
				_ = json.Unmarshal([]byte(v), &list)
				ips = append(ips, fmt.Sprintf("%s (%d IP)", k, len(list)))
			}
		}
		sort.Strings(ips)
		log.Info("site contract, machines: a vSphere Target (targets.vsphere of rondo.yaml) with a "+
			"vmTemplate EL9", "componentValues", strings.Join(ips, ", "))
	}
}

// baseReleases are the releases RONDO's base layer delivers on every cluster target.
func baseReleases(s Settings) (map[release]string, error) {
	tree, err := platform.Load(filepath.Join(s.RondoDir, "platform"))
	if err != nil {
		return nil, err
	}
	byName := map[string]componentInfo{}
	for _, o := range tree.Objects {
		if o.Kind == "Component" {
			byName[o.Name] = componentOf(o)
		}
	}
	out := map[release]string{}
	for _, o := range tree.Objects {
		if o.Kind != "Enrollment" {
			continue
		}
		spec, _ := o.Raw["spec"].(map[string]any)
		comp, _ := spec["component"].(string)
		out[byName[comp].release] = comp
	}
	return out, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

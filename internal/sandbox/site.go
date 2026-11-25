package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"rondo/internal/log"
)

// sandboxFile is rondo-sandbox.yaml, at the component's root. targets and secrets are rondo.yaml's
// keys, without what the workstation provides (artifact mirror, vCenter, nodes, versions): the site
// the sandbox stands for. The other keys are the component's, what its CI publishes and what the
// workstation reaches. Every field is optional.
type sandboxFile struct {
	Targets struct {
		Management  managementSpec `json:"management,omitempty"`
		Downstreams []targetSpec   `json:"downstreams,omitempty"`
		VSphere     []targetSpec   `json:"vsphere,omitempty"`
	} `json:"targets,omitempty"`
	Secrets struct {
		Components map[string]map[string]string `json:"components,omitempty"`
	} `json:"secrets,omitempty"`

	Name         string       `json:"name,omitempty"`
	Platform     string       `json:"platform,omitempty"`
	Apps         string       `json:"apps,omitempty"`
	Dependencies string       `json:"dependencies,omitempty"`
	Images       []imageBuild `json:"images,omitempty"`
	Ports        []portSpec   `json:"ports,omitempty"`
	// machine components: the files their CI publishes, and the upstream of each RPM
	// repository the site mirrors, by <repo>/<path>
	Artifacts []artifactBuild   `json:"artifacts,omitempty"`
	RPMRepos  map[string]string `json:"rpmRepos,omitempty"`
}

// targetSpec is a Target of rondo.yaml as the sandbox reads it: a downstream (a k3d cluster) or a
// vSphere Target (whose machines are containers).
type targetSpec struct {
	Name            string            `json:"name"`
	Roles           []string          `json:"roles,omitempty"`
	ComponentValues map[string]string `json:"componentValues,omitempty"`
}

// managementSpec is targets.management. Its roles are the sandbox's own: a site gives them to
// downstreams, the sandbox may keep them on its management to spare a cluster.
type managementSpec struct {
	Roles           []string          `json:"roles,omitempty"`
	ComponentValues map[string]string `json:"componentValues,omitempty"`
}

const sandboxFileName = "rondo-sandbox.yaml"

func readSandboxFile(path string) (*sandboxFile, error) {
	var sf sandboxFile
	raw, err := os.ReadFile(path) //nolint:gosec // the component's own file
	if os.IsNotExist(err) {
		return &sf, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.UnmarshalStrict(raw, &sf); err != nil {
		return nil, fmt.Errorf("%s: %w (of rondo.yaml, the sandbox reads targets.management, "+
			"targets.downstreams and targets.vsphere, each {name, roles, componentValues}, and "+
			"secrets.components)", path, err)
	}
	return &sf, nil
}

// site is the sandbox's site as `rondo-sandbox deploy` and `deploy downstreams` left it: the
// Targets rondo-sandbox.yaml declares, and the clusters deployed for them.
type site struct {
	Config      string                       `json:"config"`
	Management  managementSpec               `json:"management"`
	Downstreams []targetSpec                 `json:"downstreams,omitempty"`
	VSphere     []targetSpec                 `json:"vsphere,omitempty"`
	Secrets     map[string]map[string]string `json:"secrets,omitempty"`
	// management first, then the downstreams deployed
	Clusters []cluster `json:"clusters"`
}

type cluster struct {
	Name       string   `json:"name"`
	Target     string   `json:"target"`
	Roles      []string `json:"roles"`
	IngressIP  string   `json:"ingressIP"`
	Management bool     `json:"management,omitempty"`
}

// Ingress IPs: .100 for the management, .101 to .127 for the downstreams, below Docker's own
// (.128 and above).
const (
	managementIngressIP = 100
	lastIngressIP       = 127
)

func (st *site) management() *cluster {
	for i := range st.Clusters {
		if st.Clusters[i].Management {
			return &st.Clusters[i]
		}
	}
	return nil
}

func (st *site) cluster(target string) *cluster {
	for i := range st.Clusters {
		if st.Clusters[i].Target == target {
			return &st.Clusters[i]
		}
	}
	return nil
}

// clusterOf is the cluster whose Target is named ref or carries the role ref.
func (st *site) clusterOf(ref string) *cluster {
	for i := range st.Clusters {
		if st.Clusters[i].Target == ref || slices.Contains(st.Clusters[i].Roles, ref) {
			return &st.Clusters[i]
		}
	}
	return nil
}

func (st *site) downstream(name string) *targetSpec {
	i := slices.IndexFunc(st.Downstreams, func(t targetSpec) bool { return t.Name == name })
	if i < 0 {
		return nil
	}
	return &st.Downstreams[i]
}

// vsphereOf is the vSphere Target named ref or carrying the role ref.
func (st *site) vsphereOf(ref string) *targetSpec {
	for i := range st.VSphere {
		if st.VSphere[i].Name == ref || slices.Contains(st.VSphere[i].Roles, ref) {
			return &st.VSphere[i]
		}
	}
	return nil
}

// values are the componentValues rondo-sandbox.yaml gives a cluster's Target.
func (st *site) values(c *cluster) map[string]string {
	if c.Management {
		return st.Management.ComponentValues
	}
	if d := st.downstream(c.Target); d != nil {
		return d.ComponentValues
	}
	return nil
}

// subnetIP is an address of the sandbox's /24 network, the one its clusters' ingress IPs are in.
func (st *site) subnetIP(host int) string {
	if m := st.management(); m != nil {
		if i := strings.LastIndex(m.IngressIP, "."); i > 0 {
			return fmt.Sprintf("%s.%d", m.IngressIP[:i], host)
		}
	}
	return fmt.Sprintf("172.28.0.%d", host)
}

func loadSite(s Settings) (*site, error) {
	raw, err := os.ReadFile(s.sitePath())
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("no sandbox (%s): run `rondo-sandbox deploy` first", s.StateDir)
	}
	if err != nil {
		return nil, err
	}
	var st site
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("%s unreadable: %w", s.sitePath(), err)
	}
	return &st, nil
}

func saveSite(s Settings, st *site) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.sitePath(), append(raw, '\n'), 0o600)
}

// RunSite reads rondo-sandbox.yaml for `deploy` (the management) or `deploy downstreams` (the
// downstreams named, all by default), and writes what the script creates: clusters.txt, and the
// images of RONDO the site needs.
func RunSite(s Settings, config, ipPrefix string, downstreams bool, names []string) error {
	sf, err := readSandboxFile(config)
	if err != nil {
		return err
	}
	if _, err := os.Stat(config); os.IsNotExist(err) {
		log.Info("no " + sandboxFileName + ": the management cluster only (`rondo-sandbox init` writes one " +
			"from the component's tree)")
	}
	var cur *site
	if downstreams {
		if cur, err = loadSite(s); err != nil {
			return err
		}
	} else if fileExists(s.sitePath()) {
		if cur, err = loadSite(s); err != nil {
			return err
		}
	}
	st, wanted, err := updateSite(cur, sf, config, ipPrefix, downstreams, names)
	if err != nil {
		return err
	}
	if err := saveSite(s, st); err != nil {
		return err
	}
	images := "controller\ntoolbox\n"
	if len(st.VSphere) > 0 {
		images += "sandbox-machine\n"
	}
	if err := os.WriteFile(filepath.Join(s.StateDir, "rondo-images.txt"), []byte(images), 0o600); err != nil {
		return err
	}
	return writeLines(filepath.Join(s.StateDir, "clusters.txt"), wanted)
}

var targetName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// updateSite is the site cur becomes with rondo-sandbox.yaml: its Targets declared again, and the
// management, or the downstreams named, deployed. A cluster keeps its ingress IP.
func updateSite(cur *site, sf *sandboxFile, config, ipPrefix string, downstreams bool, names []string) (*site, []string, error) {
	if err := checkTargets(sf); err != nil {
		return nil, nil, err
	}
	st := &site{Config: config, Management: sf.Targets.Management, Downstreams: sf.Targets.Downstreams,
		VSphere: sf.Targets.VSphere, Secrets: sf.Secrets.Components}
	if cur != nil {
		st.Clusters = cur.Clusters
	}
	taken := map[string]bool{}
	for _, c := range st.Clusters {
		taken[c.IngressIP] = true
	}
	if !downstreams {
		mgmt := cluster{Name: clusterPrefix, Target: managementRole, Management: true,
			Roles:     append([]string{managementRole}, sf.Targets.Management.Roles...),
			IngressIP: fmt.Sprintf("%s.%d", ipPrefix, managementIngressIP)}
		if m := st.management(); m != nil {
			*m = mgmt
		} else {
			st.Clusters = append([]cluster{mgmt}, st.Clusters...)
		}
		for _, c := range st.Clusters {
			if !c.Management && st.downstream(c.Target) == nil {
				log.Warn("downstream deployed but missing from "+sandboxFileName, "target", c.Target,
					"remove", "rondo-sandbox destroy downstreams "+c.Target)
			}
		}
		return st, []string{clusterPrefix}, nil
	}
	if st.management() == nil {
		return nil, nil, fmt.Errorf("no management cluster: run `rondo-sandbox deploy` first")
	}
	selected, err := selectTargets(st.Downstreams, names)
	if err != nil {
		return nil, nil, err
	}
	var wanted []string
	for _, t := range selected {
		c := st.cluster(t.Name)
		if c == nil {
			ip, err := freeIngressIP(ipPrefix, taken)
			if err != nil {
				return nil, nil, err
			}
			taken[ip] = true
			st.Clusters = append(st.Clusters, cluster{Name: clusterPrefix + "-" + t.Name, Target: t.Name, IngressIP: ip})
			c = &st.Clusters[len(st.Clusters)-1]
		}
		c.Roles = slices.Clone(t.Roles)
		wanted = append(wanted, c.Name)
		log.Info("downstream", "target", t.Name, "cluster", c.Name, "roles", strings.Join(t.Roles, ","),
			"ingress", c.IngressIP)
	}
	return st, wanted, nil
}

func freeIngressIP(ipPrefix string, taken map[string]bool) (string, error) {
	for host := managementIngressIP + 1; host <= lastIngressIP; host++ {
		if ip := fmt.Sprintf("%s.%d", ipPrefix, host); !taken[ip] {
			return ip, nil
		}
	}
	return "", fmt.Errorf("more than %d downstreams: the sandbox gives them ingress IPs .%d to .%d",
		lastIngressIP-managementIngressIP, managementIngressIP+1, lastIngressIP)
}

// selectTargets are the Targets named, or all of them when none is, as rondo deploy downstreams
// selects them.
func selectTargets(all []targetSpec, names []string) ([]targetSpec, error) {
	if len(names) == 0 {
		if len(all) == 0 {
			return nil, fmt.Errorf("no downstream in %s (targets.downstreams)", sandboxFileName)
		}
		return all, nil
	}
	known := make([]string, len(all))
	for i, t := range all {
		known[i] = t.Name
	}
	var out []targetSpec
	for _, n := range names {
		i := slices.Index(known, n)
		if i < 0 {
			return nil, fmt.Errorf("unknown downstream %q (%s: %s)", n, sandboxFileName,
				orDefault(strings.Join(known, ", "), "none"))
		}
		out = append(out, all[i])
	}
	return out, nil
}

// checkTargets refuses what a site would refuse, or what makes a role ambiguous: a name used
// twice, a role carried by two Targets.
func checkTargets(sf *sandboxFile) error {
	names := map[string]string{managementRole: "targets.management"}
	carried := map[string]string{}
	carry := func(where string, roles []string) error {
		for _, r := range roles {
			if !targetName.MatchString(r) || len(r) > 63 {
				return fmt.Errorf("%s, role %q: must be a DNS name (RFC 1123)", where, r)
			}
			if r == managementRole {
				return fmt.Errorf("%s: the management role belongs to the management cluster only", where)
			}
			if other, ok := carried[r]; ok {
				return fmt.Errorf("%s and %s both hold role %q: an Enrollment targeting it "+
					"would have no single Target", other, where, r)
			}
			carried[r] = where
		}
		return nil
	}
	if err := carry("targets.management", sf.Targets.Management.Roles); err != nil {
		return err
	}
	for kind, list := range map[string][]targetSpec{"downstreams": sf.Targets.Downstreams, "vsphere": sf.Targets.VSphere} {
		for i, t := range list {
			where := fmt.Sprintf("targets.%s[%d]", kind, i)
			if !targetName.MatchString(t.Name) || len(t.Name) > 24 {
				return fmt.Errorf("%s: name %q must be a DNS name (RFC 1123) of 24 characters at most "+
					"(the k3d cluster is named %s-<name>)", where, t.Name, clusterPrefix)
			}
			if other, ok := names[t.Name]; ok {
				return fmt.Errorf("%s: name %q already taken by %s", where, t.Name, other)
			}
			names[t.Name] = where
			if len(t.Roles) == 0 {
				return fmt.Errorf("%s (%s): empty roles, no Enrollment would target it", where, t.Name)
			}
			if err := carry(where+" ("+t.Name+")", t.Roles); err != nil {
				return err
			}
		}
	}
	return nil
}

// selectDeployed are the deployed downstreams named, all of them when none is.
func selectDeployed(st *site, names []string) ([]cluster, error) {
	var deployed []cluster
	for _, c := range st.Clusters {
		if !c.Management {
			deployed = append(deployed, c)
		}
	}
	if len(names) == 0 {
		return deployed, nil
	}
	var out []cluster
	for _, n := range names {
		i := slices.IndexFunc(deployed, func(c cluster) bool { return c.Target == n })
		if i < 0 {
			known := make([]string, len(deployed))
			for j, c := range deployed {
				known[j] = c.Target
			}
			return nil, fmt.Errorf("downstream %q is not deployed (deployed: %s)", n,
				orDefault(strings.Join(known, ", "), "none"))
		}
		out = append(out, deployed[i])
	}
	return out, nil
}

// ClusterRef is a cluster of the sandbox and the roles its Target carries.
type ClusterRef struct {
	Name   string
	Target string
	Roles  []string
}

// Clusters are the sandbox's, for `rondo-sandbox kubeconfig` and its completion.
func Clusters(s Settings) []ClusterRef {
	st, err := loadSite(s)
	if err != nil {
		return nil
	}
	out := make([]ClusterRef, len(st.Clusters))
	for i, c := range st.Clusters {
		out[i] = ClusterRef{Name: c.Name, Target: c.Target, Roles: c.Roles}
	}
	return out
}

// Downstreams are the downstreams rondo-sandbox.yaml declared at the last deploy, and those
// deployed, for completion.
func Downstreams(s Settings) (declared, deployed []string) {
	st, err := loadSite(s)
	if err != nil {
		return nil, nil
	}
	for _, t := range st.Downstreams {
		declared = append(declared, t.Name)
	}
	for _, c := range st.Clusters {
		if !c.Management {
			deployed = append(deployed, c.Target)
		}
	}
	return declared, deployed
}

// Kubeconfig is the workstation kubeconfig of the cluster whose Target is named ref or carries
// the role ref; the management's when ref is empty.
func Kubeconfig(s Settings, ref string) (string, error) {
	if ref == "" {
		ref = managementRole
	}
	clusters := Clusters(s)
	if len(clusters) == 0 && ref == managementRole {
		return filepath.Join(s.clusterDir(clusterPrefix), "kubeconfig.yaml"), nil
	}
	var known []string
	for _, c := range clusters {
		if c.Name == ref || c.Target == ref || slices.Contains(c.Roles, ref) {
			return filepath.Join(s.clusterDir(c.Name), "kubeconfig.yaml"), nil
		}
		known = append(known, c.Target)
	}
	if len(clusters) == 0 {
		return "", fmt.Errorf("no sandbox (%s): run `rondo-sandbox deploy` first", s.StateDir)
	}
	return "", fmt.Errorf("no sandbox cluster is Target %q or holds that role (Targets: %s)",
		ref, strings.Join(known, ", "))
}

func writeLines(path string, lines []string) error {
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

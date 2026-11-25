package sandbox

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"golang.org/x/crypto/ssh"

	"rondo/api/v1alpha1"
	"rondo/internal/exec"
	"rondo/internal/kobj"
	"rondo/internal/log"
	"rondo/internal/platform"
	"rondo/internal/runner"
)

// The sandbox's vSphere Targets are those rondo-sandbox.yaml declares under targets.vsphere, for
// the machine components (archetypes B and C). Their machines are containers.
const (
	// the vSphere Target the sandbox's rondo.yaml names when rondo-sandbox.yaml declares none: the
	// management's placement reads one, nothing applies it
	vsphereStub     = "sandbox-vsphere"
	machineTemplate = "rondo-sandbox-machine"
	// machines take .10 to .99 of the sandbox's network: below are zot and nginx, above the
	// ingress IPs (.100) and Docker's own (.128)
	firstMachineIP = 10
	lastMachineIP  = 99
)

// machine is a VM of a component as the sandbox runs it: a container named and addressed as
// terraform/vm names and addresses the VM.
type machine struct {
	Name       string `json:"name"`
	Enrollment string `json:"enrollment"`
	Component  string `json:"component"`
	Target     string `json:"target"`
	Group      string `json:"group"`
	IP         string `json:"ip"`
	CPU        int    `json:"cpu"`
	MemoryMB   int    `json:"memoryMb"`
}

// container is the Docker name of a machine.
func (m machine) container() string { return clusterPrefix + "-machine-" + m.Name }

// vmSpecOf reads a Component's spec.vm as the API server stores it, defaults included.
func vmSpecOf(o platform.Object) (*v1alpha1.VMSpec, error) {
	spec, _ := o.Raw["spec"].(map[string]any)
	raw, err := json.Marshal(spec["vm"])
	if err != nil {
		return nil, err
	}
	var vm v1alpha1.VMSpec
	if err := json.Unmarshal(raw, &vm); err != nil {
		return nil, fmt.Errorf("Component/%s, spec.vm: %w", o.Name, err)
	}
	groups, _ := spec["vm"].(map[string]any)["groups"].([]any)
	for i, g := range groups {
		if fields, _ := g.(map[string]any); fields != nil && i < len(vm.Groups) {
			if _, set := fields["replicas"]; !set {
				vm.Groups[i].Replicas = 1
			}
		}
	}
	return &vm, nil
}

// planMachines gives each machine Enrollment its machines: one container per replica, at an IP
// of the sandbox's network, which its vSphere Target carries as a site's does. A machine keeps
// its IP from one install to the next, and none takes another component's.
func planMachines(tree *platform.Tree, st *site, p *plan, vmEnrolls []enrollmentInfo, ipPrefix string,
	others []*plan, previous *plan) error {
	if len(vmEnrolls) == 0 {
		return nil
	}
	specs := map[string]*v1alpha1.VMSpec{}
	for _, o := range tree.Objects {
		if o.Kind != "Component" {
			continue
		}
		if spec, _ := o.Raw["spec"].(map[string]any); spec["vm"] == nil {
			continue
		}
		vm, err := vmSpecOf(o)
		if err != nil {
			return err
		}
		specs[o.Name] = vm
	}
	taken := map[string]string{}
	for _, o := range others {
		for _, m := range o.Machines {
			taken[m.IP] = m.Name
		}
	}
	kept := map[string]string{}
	if previous != nil {
		for _, m := range previous.Machines {
			if _, ok := taken[m.IP]; !ok {
				kept[m.Name] = m.IP
			}
		}
	}
	for _, ip := range kept {
		taken[ip] = "kept"
	}
	next := firstMachineIP
	free := func() (string, error) {
		for ; next <= lastMachineIP; next++ {
			if ip := fmt.Sprintf("%s.%d", ipPrefix, next); taken[ip] == "" {
				taken[ip] = "new"
				return ip, nil
			}
		}
		return "", fmt.Errorf("more than %d machines: the sandbox places them from .%d to .%d of its network",
			lastMachineIP-firstMachineIP+1, firstMachineIP, lastMachineIP)
	}
	p.VMValues = map[string]map[string]string{}
	byComponent := map[string]string{}
	for _, e := range vmEnrolls {
		if other, ok := byComponent[e.component]; ok {
			return fmt.Errorf("Enrollments %s and %s: Component %s twice on a vSphere Target, "+
				"its machines would have the same names (terraform/vm names them <component>-<group>-<n>)",
				other, e.name, e.component)
		}
		byComponent[e.component] = e.name
		targetName := p.Targets[e.name]
		values := map[string]string{
			v1alpha1.ValueVMTemplate: machineTemplate,
			v1alpha1.ValueSSHUser:    kobj.VMSSHUser,
		}
		if t := st.vsphereOf(targetName); t != nil {
			for k, v := range t.ComponentValues {
				values[k] = v
			}
		}
		vm := specs[e.component]
		for _, g := range vm.Groups {
			var ips []string
			for i := range g.Replicas {
				ip, ok := kept[fmt.Sprintf("%s-%s-%d", e.component, g.Name, i+1)]
				if !ok {
					var err error
					if ip, err = free(); err != nil {
						return err
					}
				}
				ips = append(ips, ip)
			}
			if len(ips) > 0 {
				raw, _ := json.Marshal(ips)
				values[g.IPsKey(e.component)] = string(raw)
				if p.VMValues[targetName] == nil {
					p.VMValues[targetName] = map[string]string{}
				}
				p.VMValues[targetName][g.IPsKey(e.component)] = string(raw)
			}
		}
		target := &v1alpha1.Target{Spec: v1alpha1.TargetSpec{Values: values}}
		target.Name = targetName
		groups, err := exec.VMGroupVars(vm, e.component, target)
		if err != nil {
			return fmt.Errorf("Enrollment %s: %w", e.name, err)
		}
		for _, g := range groups {
			for i, ip := range g.IPs {
				p.Machines = append(p.Machines, machine{
					Name:       fmt.Sprintf("%s-%s-%d", e.component, g.Name, i+1),
					Enrollment: e.name, Component: e.component, Target: targetName, Group: g.Name, IP: ip,
					CPU: g.CPU, MemoryMB: g.MemoryMB,
				})
			}
		}
	}
	for _, m := range p.Machines {
		log.Info("machine", "name", m.Name, "ip", m.IP, "enrollment", m.Enrollment, "target", m.Target,
			"size", fmt.Sprintf("%d CPU / %d MB", m.CPU, m.MemoryMB))
	}
	return nil
}

// writeInstalled writes what the script reconciles for every installed component: the machines,
// the workstation ports, the stand-in's RPM proxies, the key the machines let in.
func writeInstalled(s Settings) error {
	plans, err := loadPlans(s)
	if err != nil {
		return err
	}
	var machines []machine
	var forwards []forward
	var proxies []rpmProxy
	upstreams := map[string]string{}
	for _, p := range plans {
		machines = append(machines, p.Machines...)
		forwards = append(forwards, p.Ports...)
		for _, r := range p.RPMProxies {
			if up, ok := upstreams[r.Prefix]; ok {
				if up != r.Upstream {
					log.Warn("RPM repository relayed to two upstreams: the first installed component wins",
						"repository", r.Prefix, "upstream", up, "dropped", r.Upstream, "component", p.Component)
				}
				continue
			}
			upstreams[r.Prefix] = r.Upstream
			proxies = append(proxies, r)
		}
	}
	sort.Slice(forwards, func(i, j int) bool { return forwards[i].listen() < forwards[j].listen() })
	sort.Slice(proxies, func(i, j int) bool { return proxies[i].Prefix < proxies[j].Prefix })
	if err := writePorts(s, forwards); err != nil {
		return err
	}
	return writeMachineFiles(s, machines, proxies)
}

// writeMachines is what the script reconciles: one line per machine, its name, IP, CPUs and
// memory; empty without machine components.
func writeMachines(s Settings, machines []machine) error {
	var b strings.Builder
	for _, m := range machines {
		fmt.Fprintf(&b, "%s %s %s %d %d %s %s\n", m.container(), m.Name, m.IP, m.CPU, m.MemoryMB,
			m.Component, m.Group)
	}
	return os.WriteFile(filepath.Join(s.StateDir, "machines.txt"), []byte(b.String()), 0o600)
}

// checkMachineSources fails early on a file or a repository of a machine component the sandbox
// could not serve: `check` says so before any cluster exists.
func checkMachineSources(tree *platform.Tree, p *plan, sf *sandboxFile) error {
	if len(p.Machines) == 0 {
		return nil
	}
	deps, err := readDeps(p.Dependencies)
	if err != nil {
		return err
	}
	if _, err := vmSources(tree, sf.Artifacts, deps); err != nil {
		return err
	}
	p.RPMProxies, err = rpmProxies(tree, sf.RPMRepos)
	return err
}

// writeMachineFiles are what the script reads for the machines: their list, the key they let
// in, the stand-in's RPM proxies.
func writeMachineFiles(s Settings, machines []machine, proxies []rpmProxy) error {
	if err := writeMachines(s, machines); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.StateDir, "rpm-proxy.conf"), []byte(rpmConf(proxies)), 0o600); err != nil {
		return err
	}
	if len(machines) == 0 {
		return nil
	}
	_, pub, err := machineKey(s)
	if err != nil {
		return err
	}
	dir := filepath.Join(s.StateDir, "machines")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "authorized_key"), []byte(pub+"\n"), 0o644) //nolint:gosec // a public key
}

// machineKey is the pair RONDO reaches the machines with (Secret rondo-vm-ssh), made once per
// sandbox: the machines get its public half when they start, before RONDO runs.
func machineKey(s Settings) (priv, pub string, err error) {
	path := filepath.Join(s.StateDir, "pki", "machines_ed25519")
	if raw, err := os.ReadFile(path); err == nil {
		pubRaw, err := os.ReadFile(path + ".pub")
		if err != nil {
			return "", "", err
		}
		return string(raw), strings.TrimSpace(string(pubRaw)), nil
	}
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(sk, "rondo-vm@rondo-sandbox")
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pk)
	if err != nil {
		return "", "", err
	}
	priv = string(pem.EncodeToMemory(block))
	pub = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, []byte(priv), 0o600); err != nil {
		return "", "", err
	}
	return priv, pub, os.WriteFile(path+".pub", []byte(pub+"\n"), 0o644) //nolint:gosec // a public key
}

// tokens are the platform placeholders a machine spec may name, as the sandbox's site resolves
// them (internal/subst, siteConfig).
var tokens = strings.NewReplacer(
	"${ARTIFACTORY_URL}", artifactoryURL,
	"${ARTIFACTORY_GENERIC_REPO}", genericRepo,
	"${ARTIFACTORY_RPM_REPO}", rpmRepo,
)

// artifactBuild is a file of a machine component that its CI publishes on Artifactory (an
// installer archive, a playbook), made here from its sources.
type artifactBuild struct {
	// the path the Component names (spec.vm.artifacts[].path, spec.vm.ansible.path)
	Path string `json:"path"`
	// a directory archived as tar.gz, or a file the build leaves
	Dir  string `json:"dir,omitempty"`
	File string `json:"file,omitempty"`
	// a command run in the component's directory before File is read
	Build string `json:"build,omitempty"`
}

// vmSource is where a machine component's file comes from on the stand-in.
type vmSource struct {
	component, repo, path string
	ansible               bool
	build                 *artifactBuild
}

// vmSources lists every file the machine components download, each with its build when the
// component's CI makes it; an upstream file comes from dependencies.yaml instead.
func vmSources(tree *platform.Tree, builds []artifactBuild, deps *depsFile) ([]vmSource, error) {
	byPath := map[string]*artifactBuild{}
	for i := range builds {
		b := &builds[i]
		if (b.Dir == "") == (b.File == "") {
			return nil, fmt.Errorf("rondo-sandbox.yaml, artifacts %q: dir (an archived directory) or file "+
				"(a built file), exactly one of the two", b.Path)
		}
		byPath[strings.Trim(b.Path, "/")] = b
	}
	upstream := map[string]bool{}
	for _, d := range append(deps.Binaries, deps.GrafanaPlugins...) {
		upstream[d.TargetRepo+"/"+strings.ReplaceAll(d.TargetPath, "{version}", d.Version)] = true
	}
	var out []vmSource
	var missing []string
	for _, o := range tree.Objects {
		if o.Kind != "Component" {
			continue
		}
		if spec, _ := o.Raw["spec"].(map[string]any); spec["vm"] == nil {
			continue
		}
		vm, err := vmSpecOf(o)
		if err != nil {
			return nil, err
		}
		add := func(repo, path string, ansible bool, provenance v1alpha1.ArtifactProvenance) {
			src := vmSource{component: o.Name, repo: tokens.Replace(repo), path: strings.Trim(path, "/"),
				ansible: ansible, build: byPath[strings.Trim(path, "/")]}
			if src.build == nil && (provenance == v1alpha1.ProvenanceComponent || !upstream[src.repo+"/"+src.path]) {
				what := "artifact " + path
				if ansible {
					what = "Ansible content " + path
				}
				missing = append(missing, fmt.Sprintf("Component/%s, %s", o.Name, what))
				return
			}
			out = append(out, src)
		}
		for _, a := range vm.Artifacts {
			add(a.Repo, a.Path, false, a.Provenance)
		}
		if a := vm.Ansible; a != nil {
			add(a.Repo, a.Path, true, v1alpha1.ProvenanceComponent)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("files the component's CI publishes, with no source for the sandbox: %s; "+
			"the artifacts section of rondo-sandbox.yaml says how to make them (dir or file, and build), or "+
			"dependencies.yaml brings them (binaries)", strings.Join(missing, "; "))
	}
	return out, nil
}

// rpmProxy sends a repository of a machine component to its upstream, under the path the
// site's Artifactory serves it at.
type rpmProxy struct {
	Prefix   string `json:"prefix"`
	Upstream string `json:"upstream"`
}

// rpmProxies are the machine components' RPM repositories, each with the upstream
// rondo-sandbox.yaml gives it: the site mirrors them, the sandbox proxies them.
func rpmProxies(tree *platform.Tree, upstreams map[string]string) ([]rpmProxy, error) {
	known := map[string]string{}
	for k, v := range upstreams {
		known[strings.Trim(tokens.Replace(k), "/")] = v
	}
	seen := map[string]bool{}
	var out []rpmProxy
	var missing []string
	for _, o := range tree.Objects {
		if o.Kind != "Component" {
			continue
		}
		if spec, _ := o.Raw["spec"].(map[string]any); spec["vm"] == nil {
			continue
		}
		vm, err := vmSpecOf(o)
		if err != nil {
			return nil, err
		}
		for _, r := range vm.RPMRepos {
			key := strings.Trim(tokens.Replace(r.Repo)+"/"+strings.Trim(r.Path, "/"), "/")
			up, ok := known[key]
			if !ok {
				missing = append(missing, fmt.Sprintf("Component/%s, repository %s (%s)", o.Name, r.Name, key))
				continue
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, rpmProxy{Prefix: "/artifactory/" + key + "/", Upstream: strings.TrimRight(up, "/") + "/"})
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("RPM repositories the site mirrors, with no upstream for the sandbox: %s; "+
			"the rpmRepos section of rondo-sandbox.yaml gives the upstream URL of each "+
			"(<repo>/<path>: https://...)", strings.Join(missing, "; "))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out, nil
}

// rpmConf is the stand-in's nginx locations for rpmProxies; Docker's DNS resolves upstreams
// when a machine asks, not when nginx starts.
func rpmConf(proxies []rpmProxy) string {
	var b strings.Builder
	for _, r := range proxies {
		fmt.Fprintf(&b, "    location ~ ^%s(.*)$ {\n", regexpQuote(r.Prefix))
		b.WriteString("      resolver 127.0.0.11 valid=60s;\n")
		fmt.Fprintf(&b, "      proxy_pass %s$1;\n", r.Upstream)
		b.WriteString("      proxy_ssl_server_name on;\n")
		b.WriteString("      proxy_set_header Authorization \"\";\n")
		b.WriteString("    }\n")
	}
	return b.String()
}

func regexpQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`.+*?()|[]{}^$\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Machines are the installed components' machines, for `rondo-sandbox ssh` and its completion.
func Machines(s Settings) []string {
	plans, err := loadPlans(s)
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range plans {
		out = append(out, machineList(p.Machines)...)
	}
	return out
}

// machineOf is the machine of that name among the plans.
func machineOf(plans []*plan, name string) *machine {
	for _, p := range plans {
		if i := slices.IndexFunc(p.Machines, func(m machine) bool { return m.Name == name }); i >= 0 {
			return &p.Machines[i]
		}
	}
	return nil
}

// rondoMachineFiles lays on the stand-in what the machine Jobs download of RONDO: its machine
// module (the sandbox's, vmModule) and its playbook, under this RONDO version as the CI publishes
// them.
func rondoMachineFiles(s Settings, rondoVersion string) error {
	if rondoVersion == "" {
		return fmt.Errorf("--rondo-version required: the machine module and RONDO's playbook are " +
			"published under the RONDO version")
	}
	root := filepath.Join(s.StateDir, "generic", genericRepo)
	module, err := vmModule(s.RondoDir)
	if err != nil {
		return err
	}
	if err := layFile(filepath.Join(root, "tofu-modules", "vm", rondoVersion, "vm.tar.gz"), module); err != nil {
		return err
	}
	playbook, err := tarGzDir(filepath.Join(s.RondoDir, "ansible"))
	if err != nil {
		return fmt.Errorf("RONDO playbook: %w", err)
	}
	if err := layFile(filepath.Join(root, "ansible", "rondo", rondoVersion, "rondo-ansible.tar.gz"), playbook); err != nil {
		return err
	}
	log.Info("sandbox machine module and RONDO playbook published", "version", rondoVersion)
	return nil
}

// machineFiles lays on the stand-in the files a machine component's CI publishes, built from its
// sources, and notes their digests for the tree the sandbox applies.
func (pub *publisher) machineFiles(ctx context.Context, s Settings, p *plan, tree *platform.Tree,
	deps *depsFile, rw *treeRewrites) error {
	sources, err := vmSources(tree, p.Artifacts, deps)
	if err != nil {
		return err
	}
	for _, src := range sources {
		if src.build == nil {
			continue
		}
		raw, err := pub.make(ctx, p.Dir, src.build)
		if err != nil {
			return fmt.Errorf("rondo-sandbox.yaml, artifacts %q: %w", src.build.Path, err)
		}
		if err := layFile(filepath.Join(s.StateDir, "generic", src.repo, filepath.FromSlash(src.path)), raw); err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		digest := hex.EncodeToString(sum[:])
		if src.ansible {
			rw.ansible[src.component] = digest[:8]
		} else {
			rw.digests[src.component+"/"+src.path] = "sha256:" + digest
		}
		log.Info("component file published", "path", src.repo+"/"+src.path, "sha256", digest[:12])
	}
	return nil
}

// make builds a file of rondo-sandbox.yaml's artifacts in the component's directory.
func (pub *publisher) make(ctx context.Context, dir string, b *artifactBuild) ([]byte, error) {
	if b.Dir != "" {
		return tarGzDir(filepath.Join(dir, b.Dir))
	}
	if b.Build != "" {
		log.Info("build", "command", b.Build)
		if err := pub.run.Run(ctx, runner.Cmd{Name: "bash", Args: []string{"-c", b.Build}, Dir: dir}); err != nil {
			return nil, err
		}
	}
	return os.ReadFile(filepath.Join(dir, b.File)) //nolint:gosec // the component's build output
}

func layFile(dest string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".part"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil { //nolint:gosec // served by the stand-in
		return err
	}
	return os.Rename(tmp, dest)
}

// rewriteVM points a machine component at what the sandbox built: each file's digest, which
// vm-install checks and which replays it when the file changes, and its playbook's version.
func rewriteVM(component string, vm map[string]any, rw treeRewrites) {
	artifacts, _ := vm["artifacts"].([]any)
	for _, a := range artifacts {
		art, _ := a.(map[string]any)
		p, _ := art["path"].(string)
		if d, ok := rw.digests[component+"/"+strings.Trim(p, "/")]; ok {
			art["digest"] = d
		}
	}
	if a, _ := vm["ansible"].(map[string]any); a != nil {
		if h, ok := rw.ansible[component]; ok {
			v, _ := a["version"].(string)
			a["version"] = sandboxVersion(v, h)
		}
	}
}

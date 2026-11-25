package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"sigs.k8s.io/yaml"

	"rondo/api/v1alpha1"
	"rondo/internal/log"
	"rondo/internal/platform"
	"rondo/internal/runner"
	"rondo/internal/values"
)

// The commands a developer debugs a component with, between two deploys.

// RunCheck is what `component install` checks before touching the sandbox, in seconds and
// without it: the tree against the site rondo-sandbox.yaml describes, every Target deployed (its
// roles, componentValues, collisions, site contract), its ports, where its charts come from, the
// images it builds.
func RunCheck(s Settings, dir, config string) error {
	tmp, err := os.MkdirTemp("", "rondo-sandbox-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	cs := Settings{StateDir: tmp, RondoDir: s.RondoDir}
	sf, err := readSandboxFile(config)
	if err != nil {
		return err
	}
	st, _, err := updateSite(nil, sf, config, "172.28.0", false, nil)
	if err != nil {
		return err
	}
	if len(st.Downstreams) > 0 {
		if st, _, err = updateSite(st, sf, config, "172.28.0", true, nil); err != nil {
			return err
		}
	}
	p, err := planComponent(cs, st, dir, "172.28.0")
	if err != nil {
		return err
	}
	log.Step("sources of the component's charts and images")
	apps, err := firstPartyCharts(p.Apps)
	if err != nil {
		return err
	}
	deps, err := readDeps(p.Dependencies)
	if err != nil {
		return err
	}
	tree, err := platform.Load(p.Platform)
	if err != nil {
		return err
	}
	var errs []string
	charts := chartsOf(tree)
	for _, c := range charts {
		if _, err := sourceOf(c, apps, deps); err != nil {
			errs = append(errs, err.Error())
		}
	}
	for _, img := range p.Images {
		if _, err := imageTag(p.Dir, img); err != nil {
			errs = append(errs, err.Error())
		}
		if df := dockerfileOf(p.Dir, img); !fileExists(df) {
			errs = append(errs, fmt.Sprintf("image %s: %s not found", img.Name, df))
		}
	}
	for _, e := range errs {
		log.Error(e)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d error(s): `rondo-sandbox component install` would fail, and so would the site", len(errs))
	}
	log.Info("the component passes the static checks; `rondo-sandbox component install` installs it",
		"enrollments", len(p.Enrollments), "charts", len(charts), "images", len(p.Images),
		"machines", len(p.Machines), "targets", len(st.Clusters)+len(st.VSphere))
	return nil
}

// RunWhy says what blocks, now: the operator, the nodes, the Enrollments not Ready and their
// last Job, the pods that do not run. It fails when something does.
func RunWhy(ctx context.Context, s Settings, name string) error {
	sb, err := openSandbox(s, "")
	if err != nil {
		return err
	}
	return sb.why(ctx, name)
}

func (sb *sandbox) why(ctx context.Context, name string) error {
	findings := 0
	for _, check := range []func(context.Context) error{sb.operatorCrash, sb.nodePressure} {
		if err := check(ctx); err != nil {
			log.Error(err.Error())
			findings++
		}
	}
	list, err := sb.enrollments(ctx)
	if err != nil {
		return err
	}
	w, err := sb.world(ctx)
	if err != nil {
		return err
	}
	watched, err := sb.watched(sb.enrollmentNames())
	if err != nil {
		return err
	}
	if err := sb.adoptedJob(ctx, list, watched); err != nil {
		log.Error(err.Error())
		findings++
	}
	var blocked []v1alpha1.Enrollment
	found := name == ""
	for _, e := range list {
		if name != "" && e.Name != name || name == "" && !watched[e.Name] {
			continue
		}
		found = true
		if judge(&e, w, time.Time{}, 2) != ready {
			blocked = append(blocked, e)
		}
	}
	if !found {
		return fmt.Errorf("no Enrollment %q on the sandbox (those of the installed components: %s)", name,
			orDefault(strings.Join(sb.enrollmentNames(), ", "), "none"))
	}
	if len(blocked) > 0 {
		sb.explain(ctx, blocked)
		sb.stuckPods(ctx)
		findings += len(blocked)
	}
	if findings == 0 {
		log.Info("nothing is blocking: RONDO judges Ready everything the installed components wait for",
			"components", orDefault(strings.Join(sb.components(), ","), "none"))
		return nil
	}
	return fmt.Errorf("%d blocking issue(s)", findings)
}

// located is an Enrollment with where its workload lives.
type located struct {
	enr        *v1alpha1.Enrollment
	comp       *v1alpha1.Component
	target     *v1alpha1.Target
	cluster    cluster
	kubeconfig string
}

func (l *located) namespace() string { return l.comp.Spec.Namespace }

func (l *located) release() string {
	if l.comp.Spec.ReleaseName != "" {
		return l.comp.Spec.ReleaseName
	}
	return l.comp.Name
}

func (sb *sandbox) locate(ctx context.Context, name string) (*located, error) {
	list, err := sb.enrollments(ctx)
	if err != nil {
		return nil, err
	}
	w, err := sb.world(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		e := &list[i]
		if e.Name != name {
			continue
		}
		l := &located{enr: e, comp: w.components[e.Spec.Component], target: w.targets[e.Status.ResolvedTarget]}
		if l.comp != nil && l.comp.Spec.VM != nil {
			return nil, fmt.Errorf("Enrollment %s: machine component (%s); `rondo-sandbox pods %s` shows "+
				"their state, `rondo-sandbox ssh <machine>` opens a shell on one", name,
				strings.Join(sb.machinesOf(name), ", "), name)
		}
		if l.comp == nil || l.comp.Spec.Chart == nil {
			return nil, fmt.Errorf("Enrollment %s: Component %s missing or without a chart", name, e.Spec.Component)
		}
		if c := sb.site.cluster(e.Status.ResolvedTarget); c != nil {
			l.cluster, l.kubeconfig = *c, filepath.Join(sb.s.clusterDir(c.Name), "kubeconfig.yaml")
			return l, nil
		}
		return nil, fmt.Errorf("Enrollment %s: Target %q resolved outside the sandbox, or not yet", name,
			e.Status.ResolvedTarget)
	}
	return nil, fmt.Errorf("no Enrollment %q on the sandbox (those of the installed components: %s)", name,
		orDefault(strings.Join(sb.enrollmentNames(), ", "), "none"))
}

func (sb *sandbox) resolvedValues(l *located) ([]byte, error) {
	if l.target == nil {
		return nil, fmt.Errorf("Enrollment %s: Target not resolved yet", l.enr.Name)
	}
	v, err := values.ForEnrollment(l.comp, l.enr, l.target)
	if err != nil {
		return nil, err
	}
	if v == nil || len(v.Raw) == 0 {
		return []byte("{}"), nil
	}
	return v.Raw, nil
}

// RunValues prints the values RONDO applies for an Enrollment: the Component's and its own,
// ${target.*} resolved; ${secrets.*} stay tokens, the Job resolves them.
func RunValues(ctx context.Context, s Settings, name string, out io.Writer) error {
	sb, err := openSandbox(s, "")
	if err != nil {
		return err
	}
	return sb.values(ctx, name, out)
}

func (sb *sandbox) values(ctx context.Context, name string, out io.Writer) error {
	l, err := sb.locate(ctx, name)
	if err != nil {
		return err
	}
	raw, err := sb.resolvedValues(l)
	if err != nil {
		return err
	}
	doc, err := yaml.JSONToYAML(raw)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "# %s: %s %s, release %s in %s, Target %s (cluster %s)\n"+
		"# ${secrets.*}: resolved in the Job, from valuesFrom\n%s",
		l.enr.Name, l.comp.Spec.Chart.OCI, l.comp.Spec.Chart.Version, l.release(), l.namespace(),
		l.enr.Status.ResolvedTarget, l.cluster.Name, doc)
	return nil
}

const toolboxImage = "rondo-sandbox/rondo-toolbox:build"

// RunTemplate renders an Enrollment's chart with its values, by the toolbox's helm when the
// sandbox built it: the helm that installs it.
func RunTemplate(ctx context.Context, s Settings, name string, out io.Writer) error {
	sb, err := openSandbox(s, "")
	if err != nil {
		return err
	}
	return sb.template(ctx, name, out)
}

func (sb *sandbox) template(ctx context.Context, name string, out io.Writer) error {
	l, err := sb.locate(ctx, name)
	if err != nil {
		return err
	}
	raw, err := sb.resolvedValues(l)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "rondo-sandbox-template-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// the toolbox runs as uid 65532: it must enter the directory it reads the values from
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // values only, no secret resolved
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "values.json"), raw, 0o644); err != nil { //nolint:gosec // read by the toolbox user
		return err
	}
	_, err = sb.run.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"image", "inspect", toolboxImage}})
	toolbox := err == nil
	if !toolbox {
		log.Warn("toolbox image missing: rendered by the workstation's helm, not RONDO's")
	}
	manifest, err := sb.run.Output(ctx, sb.templateCmd(l, dir, toolbox))
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, manifest)
	return err
}

// templateCmd renders from zot, as seen from the workstation: by the toolbox's helm in a
// container on the host network, or by the workstation's.
func (sb *sandbox) templateCmd(l *located, dir string, toolbox bool) runner.Cmd {
	registry := "127.0.0.1:5100"
	if p := sb.planOf(l.enr.Name); p != nil && p.Registry != "" {
		registry = p.Registry
	}
	chart := strings.Replace(l.comp.Spec.Chart.OCI, "oci://"+registryHost+"/", "oci://"+registry+"/", 1)
	args := []string{"template", l.release(), chart, "--version", l.comp.Spec.Chart.Version,
		"--namespace", l.namespace(), "--plain-http"}
	if !toolbox {
		return runner.Cmd{Name: "helm", Args: append(args, "-f", filepath.Join(dir, "values.json"))}
	}
	return runner.Cmd{Name: "docker", Args: append(append([]string{"run", "--rm", "--network", "host",
		"--entrypoint", "helm", "-e", "HOME=/tmp", "-v", dir + ":/w:ro", toolboxImage}, args...),
		"-f", "/w/values.json")}
}

// workloads are the controllers an Enrollment delivered, as Kind/Name.
func workloads(e *v1alpha1.Enrollment) map[string]bool {
	out := map[string]bool{}
	for _, it := range e.Status.Inventory {
		switch it.Kind {
		case "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob":
			out[it.Kind+"/"+it.Name] = true
		}
	}
	return out
}

// belongs says a pod is the release's: owned by a workload it delivered, labelled with it, or,
// for what an operator creates from its resources (a Prometheus), named after it.
func belongs(p pod, ws map[string]bool, release string) bool {
	if p.Metadata.Labels["app.kubernetes.io/instance"] == release || p.Metadata.Labels["release"] == release {
		return true
	}
	for _, o := range p.Metadata.OwnerReferences {
		name := o.Name
		switch o.Kind {
		case "ReplicaSet":
			if i := strings.LastIndex(name, "-"); i > 0 && ws["Deployment/"+name[:i]] {
				return true
			}
		case "Job":
			if i := strings.LastIndex(name, "-"); ws["Job/"+name] || i > 0 && ws["CronJob/"+name[:i]] {
				return true
			}
		default:
			if ws[o.Kind+"/"+name] {
				return true
			}
		}
	}
	return strings.HasPrefix(p.Metadata.Name, release+"-") || strings.Contains(p.Metadata.Name, "-"+release+"-")
}

func (sb *sandbox) podsOf(ctx context.Context, l *located) ([]pod, error) {
	all, err := sb.pods(ctx, l.kubeconfig, l.namespace())
	if err != nil {
		return nil, err
	}
	ws := workloads(l.enr)
	var out []pod
	for _, p := range all {
		if belongs(p, ws, l.release()) {
			out = append(out, p)
		}
	}
	return out, nil
}

// RunPods lists the pods of an Enrollment, or of every Enrollment of the installed components,
// on the cluster each runs on.
func RunPods(ctx context.Context, s Settings, name string, out io.Writer) error {
	sb, err := openSandbox(s, "")
	if err != nil {
		return err
	}
	return sb.listPods(ctx, name, out)
}

func (sb *sandbox) listPods(ctx context.Context, name string, out io.Writer) error {
	names := sb.enrollmentNames()
	if name != "" {
		names = []string{name}
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ENROLLMENT\tCLUSTER\tPOD\tREADY\tSTATE\tRESTARTS")
	for _, n := range names {
		if machines := sb.machinesOf(n); len(machines) > 0 {
			for _, m := range sb.machines() {
				if m.Enrollment == n {
					system, state := sb.machineState(ctx, m)
					fmt.Fprintf(w, "%s\t%s\t%s (%s)\t%s\t%s\t—\n", n, m.Target, m.Name, m.IP, system, state)
				}
			}
			continue
		}
		l, err := sb.locate(ctx, n)
		if err != nil {
			if name != "" {
				return err
			}
			fmt.Fprintf(w, "%s\t—\t—\t—\t%s\t—\n", n, truncate(err.Error(), 60))
			continue
		}
		pods, err := sb.podsOf(ctx, l)
		if err != nil {
			return err
		}
		if len(pods) == 0 {
			fmt.Fprintf(w, "%s\t%s\t—\t—\tno pod (objects without workloads, or not created yet)\t—\n", n, l.cluster.Name)
		}
		for _, p := range pods {
			readyCount, restarts := 0, int32(0)
			for _, c := range p.Status.ContainerStatuses {
				if c.Ready {
					readyCount++
				}
				restarts += c.RestartCount
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t%s\t%d\n", n, l.cluster.Name, p.Metadata.Name, readyCount,
				len(p.Status.ContainerStatuses), orDefault(p.waiting(), p.Status.Phase), restarts)
		}
	}
	return w.Flush()
}

// machinesOf are the machines of a machine Enrollment of an installed component.
func (sb *sandbox) machinesOf(enrollment string) []string {
	var out []string
	for _, m := range sb.machines() {
		if m.Enrollment == enrollment {
			out = append(out, m.Name)
		}
	}
	return out
}

// machineState is a machine's systemd state (running, degraded…) and its container's, with the
// units that failed: what an installer that broke a service leaves.
func (sb *sandbox) machineState(ctx context.Context, m machine) (system, state string) {
	state, err := sb.run.Output(ctx, runner.Cmd{Name: "docker",
		Args: []string{"inspect", "-f", "{{.State.Status}}", m.container()}})
	if err != nil {
		return "—", "missing (rondo-sandbox component install recreates it)"
	}
	state = strings.TrimSpace(state)
	if state != "running" {
		return "—", state
	}
	system, _ = sb.run.Output(ctx, runner.Cmd{Name: "docker",
		Args: []string{"exec", m.container(), "systemctl", "is-system-running"}})
	failed, _ := sb.run.Output(ctx, runner.Cmd{Name: "docker",
		Args: []string{"exec", m.container(), "systemctl", "--failed", "--no-legend", "--plain"}})
	var units []string
	for _, line := range strings.Split(failed, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			units = append(units, f[0])
		}
	}
	if len(units) > 0 {
		state += ", failed: " + strings.Join(units, " ")
	}
	return orDefault(strings.TrimSpace(system), "—"), state
}

// RunSSH opens a root shell on a machine of an installed component, or runs a command there, through
// Docker: what vm-install reaches over SSH, without the workstation needing a route to it. Like
// ssh, it gives back the command's exit code.
func RunSSH(ctx context.Context, s Settings, name string, command []string, tty bool) (int, error) {
	plans, err := loadPlans(s)
	if err != nil {
		return 0, err
	}
	m := machineOf(plans, name)
	if m == nil {
		return 0, fmt.Errorf("no machine %q (machines: %s)", name, orDefault(strings.Join(Machines(s), ", "), "none"))
	}
	args := []string{"exec", "-i"}
	if tty {
		args = append(args, "-t")
	}
	args = append(args, m.container())
	if len(command) == 0 {
		command = []string{"bash", "-l"}
	}
	// Not through the runner, which reads output by lines: a shell's prompt has none.
	c := exec.CommandContext(ctx, "docker", append(args, command...)...) //nolint:gosec // a machine of the plan
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = c.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}

// RunTail streams the logs of an Enrollment's pods, every container, each line prefixed by its
// pod and container.
func RunTail(ctx context.Context, s Settings, name, since string, follow bool) error {
	sb, err := openSandbox(s, "")
	if err != nil {
		return err
	}
	return sb.tail(ctx, name, since, follow)
}

func (sb *sandbox) tail(ctx context.Context, name, since string, follow bool) error {
	l, err := sb.locate(ctx, name)
	if err != nil {
		return err
	}
	pods, err := sb.podsOf(ctx, l)
	if err != nil {
		return err
	}
	if len(pods) == 0 {
		return fmt.Errorf("Enrollment %s: no pod on %s", name, l.cluster.Name)
	}
	var wg sync.WaitGroup
	for _, p := range pods {
		args := []string{"--kubeconfig", l.kubeconfig, "-n", l.namespace(), "logs", "pod/" + p.Metadata.Name,
			"--all-containers", "--prefix", "--since=" + since}
		if follow {
			args = append(args, "-f")
		}
		cmd := exec.CommandContext(ctx, sb.ws.Tool("kubectl"), args...) //nolint:gosec // kubectl, the sandbox's arguments
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cmd.Wait()
		}()
	}
	wg.Wait()
	return nil
}

// RunRestart restarts an Enrollment's workloads, runs its CronJobs now, and first drops from
// the node the images rondo-sandbox.yaml builds: rebuilt under the same tag, the node would
// keep the old one.
func RunRestart(ctx context.Context, s Settings, name string) error {
	sb, err := openSandbox(s, "")
	if err != nil {
		return err
	}
	return sb.restart(ctx, name)
}

func (sb *sandbox) restart(ctx context.Context, name string) error {
	l, err := sb.locate(ctx, name)
	if err != nil {
		return err
	}
	node := "k3d-" + l.cluster.Name + "-server-0"
	var images []imageBuild
	dir := ""
	if p := sb.planOf(name); p != nil {
		images, dir = p.Images, p.Dir
	}
	for _, img := range images {
		tag, err := imageTag(dir, img)
		if err != nil {
			continue
		}
		ref := registryHost + "/" + img.Name + ":" + tag
		if err := sb.run.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"exec", node, "crictl", "rmi", ref}}); err == nil {
			log.Info("image removed from the node, the next pod will pull it again", "image", ref, "node", node)
		}
	}
	restarted := 0
	for _, it := range l.enr.Status.Inventory {
		var args []string
		switch it.Kind {
		case "Deployment", "StatefulSet", "DaemonSet":
			args = []string{"rollout", "restart", strings.ToLower(it.Kind) + "/" + it.Name}
		case "CronJob":
			job := fmt.Sprintf("%s-sbx-%d", it.Name, time.Now().Unix())
			if len(job) > 63 {
				job = job[len(job)-63:]
			}
			args = []string{"create", "job", job, "--from=cronjob/" + it.Name}
		default:
			continue
		}
		if err := sb.kube.Run(ctx, l.kubeconfig, append([]string{"-n", orDefault(it.Namespace, l.namespace())}, args...)...); err != nil {
			return err
		}
		restarted++
	}
	if restarted == 0 {
		log.Warn("nothing to restart: the Enrollment ships no Deployment, StatefulSet, DaemonSet or CronJob", "enrollment", name)
		return nil
	}
	log.Info("restarted; `rondo-sandbox pods "+name+"` follows the pods", "workloads", restarted, "cluster", l.cluster.Name)
	return nil
}

package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"

	"rondo/api/v1alpha1"
	"rondo/internal/kobj"
	"rondo/internal/log"
	"rondo/internal/platform"
	"rondo/internal/values"
)

func (sb *sandbox) enrollments(ctx context.Context) ([]v1alpha1.Enrollment, error) {
	out, err := sb.kube.Output(ctx, sb.ws.Kubeconfig(), "-n", kobj.PlatformNamespace, "get",
		"enrollments.rondo.ouafeul.github.io", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list v1alpha1.EnrollmentList
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("Enrollment list: %w", err)
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list.Items, nil
}

// watched are the Enrollments named and the base layer's on every cluster that has one, which
// they wait for.
func (sb *sandbox) watched(names []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, e := range names {
		out[e] = true
	}
	base, err := sb.baseEnrollments(sb.site.Clusters)
	if err != nil {
		return nil, err
	}
	for _, e := range base {
		out[e] = true
	}
	return out, nil
}

// baseEnrollments are the base layer's Enrollments on those clusters, as ApplyBaseEnrollments
// names them: <template>-<target>.
func (sb *sandbox) baseEnrollments(clusters []cluster) ([]string, error) {
	templates, err := platform.Load(filepath.Join(sb.s.RondoDir, "platform"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, o := range templates.Objects {
		if o.Kind != "Enrollment" {
			continue
		}
		for _, c := range clusters {
			if sb.hasBase(c) {
				out = append(out, o.Name+"-"+c.Target)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

type verdict int

const (
	pending verdict = iota
	ready
	failed
)

// world is what the controller reads to decide on a Job, fetched at each poll.
type world struct {
	components map[string]*v1alpha1.Component
	targets    map[string]*v1alpha1.Target
	// the Secrets the sandbox applied, which a machine installation reads (secretsFrom)
	secrets map[string]map[string]string
}

func (sb *sandbox) world(ctx context.Context) (*world, error) {
	w := &world{components: map[string]*v1alpha1.Component{}, targets: map[string]*v1alpha1.Target{},
		secrets: sb.secrets()}
	var comps v1alpha1.ComponentList
	if err := sb.getJSON(ctx, "components.rondo.ouafeul.github.io", &comps); err != nil {
		return nil, err
	}
	for i := range comps.Items {
		w.components[comps.Items[i].Name] = &comps.Items[i]
	}
	var targets v1alpha1.TargetList
	if err := sb.getJSON(ctx, "targets.rondo.ouafeul.github.io", &targets); err != nil {
		return nil, err
	}
	for i := range targets.Items {
		w.targets[targets.Items[i].Name] = &targets.Items[i]
	}
	return w, nil
}

func (sb *sandbox) getJSON(ctx context.Context, resource string, into any) error {
	out, err := sb.kube.Output(ctx, sb.ws.Kubeconfig(), "-n", kobj.PlatformNamespace, "get",
		resource, "-o", "json")
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(out), into)
}

// converged says the last applied Job delivered what the controller now wants: the chart
// version and values hash it compares (internal/controllers, nextAction). Before that, a
// Ready condition is the previous deploy's.
func (w *world) converged(e *v1alpha1.Enrollment) bool {
	comp, target := w.components[e.Spec.Component], w.targets[e.Status.ResolvedTarget]
	if comp == nil || target == nil || e.Status.ActiveJobRef != nil {
		return false
	}
	if comp.Spec.VM != nil {
		return w.machinesConverged(e, comp, target)
	}
	if comp.Spec.Chart == nil {
		return false
	}
	resolved, err := values.ForEnrollment(comp, e, target)
	if err != nil {
		return false
	}
	hash, err := values.Hash(resolved)
	if err != nil {
		return false
	}
	return e.Status.AppliedChartVersion == comp.Spec.Chart.Version && e.Status.AppliedValuesHash == hash
}

// machinesConverged is converged for a machine component: the provisioning and the
// installation the controller last applied are the ones it now wants (values.VMInfraHash,
// VMInstallHash, VMInstallerVersion).
func (w *world) machinesConverged(e *v1alpha1.Enrollment, comp *v1alpha1.Component, target *v1alpha1.Target) bool {
	vm, err := values.ResolveVM(comp.Spec.VM, values.TargetVars(target))
	if err != nil {
		return false
	}
	var secrets map[string]string
	if vm.Installer != nil {
		for _, src := range vm.Installer.SecretsFrom {
			if src.SecretRef == nil {
				continue
			}
			for _, k := range src.Keys {
				if v, ok := w.secrets[src.SecretRef.Name][k]; ok {
					if secrets == nil {
						secrets = map[string]string{}
					}
					secrets[k] = v
				}
			}
		}
	}
	return e.Status.AppliedValuesHash == values.VMInfraHash(vm, comp.Name, target) &&
		e.Status.AppliedInstallerVersion == values.VMInstallerVersion(vm) &&
		e.Status.AppliedChartVersion == values.VMInstallHash(vm, secrets)
}

// judge counts only what happened since this deploy began: a failure or a stall left by an
// earlier run is not this run's verdict.
func judge(e *v1alpha1.Enrollment, w *world, since time.Time, maxFailures int32) verdict {
	if c := meta.FindStatusCondition(e.Status.Conditions, v1alpha1.ConditionStalled); c != nil &&
		c.Status == "True" && c.Reason != v1alpha1.ReasonDependencyPending && c.LastTransitionTime.After(since) {
		return failed
	}
	if a := e.Status.LastAttempt; a != nil && a.FinishedAt != nil && a.FinishedAt.After(since) &&
		a.Result != "Succeeded" && e.Status.ConsecutiveFailures >= maxFailures {
		return failed
	}
	if w.converged(e) && meta.IsStatusConditionTrue(e.Status.Conditions, v1alpha1.ConditionReady) {
		return ready
	}
	return pending
}

func readyMessage(e *v1alpha1.Enrollment) string {
	c := meta.FindStatusCondition(e.Status.Conditions, v1alpha1.ConditionReady)
	if c == nil {
		return "waiting for the controller"
	}
	return c.Message
}

// await follows the Enrollments wanted until RONDO's own verdict: all Ready, one failing, or time
// out. For a component, its images are checked once it is Ready.
func (sb *sandbox) await(ctx context.Context, o DeployOptions, want []string, what string) error {
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Minute
	}
	if o.MaxFailures == 0 {
		o.MaxFailures = 2
	}
	log.Step("RONDO verdict", "for", what, "timeout", o.Timeout.String())
	watched, err := sb.watched(want)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(o.Timeout)
	seen := map[string]string{}
	lastChange := time.Now()
	for {
		list, err := sb.enrollments(ctx)
		if err != nil {
			return err
		}
		w, err := sb.world(ctx)
		if err != nil {
			return err
		}
		var mine, waiting, broken []v1alpha1.Enrollment
		for _, e := range list {
			if !watched[e.Name] {
				continue
			}
			if slices.Contains(want, e.Name) {
				mine = append(mine, e)
			}
			state := fmt.Sprintf("%s · %s", orDefault(e.Status.Health, "—"), readyMessage(&e))
			if seen[e.Name] != state {
				seen[e.Name] = state
				lastChange = time.Now()
				log.Info(e.Name, "state", truncate(state, 160))
			}
			switch judge(&e, w, o.since, o.MaxFailures) {
			case failed:
				broken = append(broken, e)
			case pending:
				waiting = append(waiting, e)
			}
		}
		switch {
		case len(broken) > 0:
			sb.explain(ctx, broken)
			sb.report(ctx, mine)
			return fmt.Errorf("%s failed: %s", what, names(broken))
		case len(mine) == len(want) && len(waiting) == 0:
			if sb.p == nil {
				log.Info("RONDO judges all awaited Enrollments Ready", "for", what, "enrollments", len(want))
				return nil
			}
			sb.report(ctx, mine)
			if err := sb.checkImages(ctx); err != nil {
				return err
			}
			log.Info("the component is deployed: RONDO judges all its Enrollments Ready", "component", sb.p.Component)
			return nil
		case time.Now().After(deadline):
			sb.explain(ctx, waiting)
			sb.report(ctx, mine)
			return fmt.Errorf("no verdict after %s, waiting: %s", o.Timeout, names(waiting))
		}
		if err := sb.operatorCrash(ctx); err != nil {
			return err
		}
		if err := sb.nodePressure(ctx); err != nil {
			return err
		}
		if err := sb.adoptedJob(ctx, list, watched); err != nil {
			return err
		}
		if time.Since(lastChange) > stallAfter {
			runningJobs(waiting)
			sb.stuckPods(ctx)
			lastChange = time.Now()
		}
		if err := sleep(ctx, 5*time.Second); err != nil {
			return err
		}
	}
}

// runningJobs says which Enrollments wait on a Job of theirs that still runs: a vm-install
// playing its phases, a helm --wait on pods that do not get ready.
func runningJobs(waiting []v1alpha1.Enrollment) {
	for _, e := range waiting {
		if e.Status.ActiveJobRef == nil {
			continue
		}
		log.Warn(e.Name, "running Job", e.Status.ActiveJobRef.Name, "phase", orDefault(string(e.Status.Phase), "—"),
			"logs", "rondo-sandbox logs "+e.Name)
	}
}

// After this long without any Enrollment changing, the wait says which pods do not run.
const stallAfter = 3 * time.Minute

// explain says, for each Enrollment that blocks, what RONDO says of it and what its last
// Job printed.
func (sb *sandbox) explain(ctx context.Context, list []v1alpha1.Enrollment) {
	for _, e := range list {
		log.Error(e.Name, "message", readyMessage(&e))
		if e.Status.HealthMessage != "" {
			log.Error(e.Name, "health", e.Status.Health, "detail", e.Status.HealthMessage)
		}
		a := e.Status.LastAttempt
		if a == nil {
			continue
		}
		if a.Message != "" {
			log.Error(e.Name, "last Job", a.JobName, "result", a.Result, "cause", a.Message)
		}
		logs, err := sb.kube.Output(ctx, sb.ws.Kubeconfig(), "-n", kobj.PlatformNamespace,
			"logs", "job/"+a.JobName, "--tail=40")
		if err != nil {
			continue
		}
		fmt.Fprintf(os.Stderr, "\n── job/%s (last 40 lines) ──\n%s\n", a.JobName, logs)
		if strings.Contains(logs, "cannot be imported into the current release") {
			log.Warn("a cluster resource is already shipped by another release on this cluster: " +
				"give these roles to two downstreams of " + sandboxFileName + ", as on a site")
		}
	}
}

func (sb *sandbox) report(ctx context.Context, list []v1alpha1.Enrollment) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "\nENROLLMENT\tTARGET\tVERSION\tHEALTH\tREADY\tMESSAGE")
	for _, e := range list {
		readyState := "False"
		if meta.IsStatusConditionTrue(e.Status.Conditions, v1alpha1.ConditionReady) {
			readyState = "True"
		}
		version := e.Status.AppliedChartVersion
		if e.Status.AppliedInstallerVersion != "" || e.Status.MachinesSecretRef != nil {
			version = orDefault(e.Status.AppliedInstallerVersion, "—")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, e.Status.ResolvedTarget,
			version, orDefault(e.Status.Health, "—"), readyState, truncate(readyMessage(&e), 70))
	}
	_ = w.Flush()
	sb.endpoints(ctx)
}

// endpoints lists the component's ingress hosts per cluster, and the hosts lines that make
// them reachable from the workstation.
func (sb *sandbox) endpoints(ctx context.Context) {
	fmt.Println()
	hosts := map[string][]string{}
	mgmt := sb.site.management()
	if sb.hasBase(*mgmt) {
		fmt.Printf("RONDO dashboard: https://rondo.%s\n", baseDomain)
		hosts[mgmt.IngressIP] = []string{"rondo." + baseDomain}
	} else {
		fmt.Println("RONDO dashboard: rondo-sandbox dashboard (port-forward to the management cluster, which has no ingress)")
	}
	namespaces := map[string]bool{}
	var ports []forward
	for _, p := range sb.plans {
		for _, ns := range p.Namespaces {
			namespaces[ns] = true
		}
		ports = append(ports, p.Ports...)
	}
	for _, c := range sb.site.Clusters {
		out, err := sb.kube.Output(ctx, filepath.Join(sb.s.clusterDir(c.Name), "kubeconfig.yaml"),
			"get", "ingress", "-A", "-o", "json")
		if err != nil {
			continue
		}
		var list struct {
			Items []struct {
				Metadata struct {
					Namespace string `json:"namespace"`
				} `json:"metadata"`
				Spec struct {
					Rules []struct {
						Host string `json:"host"`
					} `json:"rules"`
				} `json:"spec"`
			} `json:"items"`
		}
		if json.Unmarshal([]byte(out), &list) != nil {
			continue
		}
		for _, ing := range list.Items {
			if !namespaces[ing.Metadata.Namespace] {
				continue
			}
			for _, r := range ing.Spec.Rules {
				if r.Host != "" && !slices.Contains(hosts[c.IngressIP], r.Host) {
					fmt.Printf("%-14s https://%s  (cluster %s)\n", ing.Metadata.Namespace, r.Host, c.Name)
					hosts[c.IngressIP] = append(hosts[c.IngressIP], r.Host)
				}
			}
		}
	}
	if len(hosts) > 0 {
		fmt.Println("\nFrom the workstation, these names resolve through /etc/hosts:")
	}
	ips := make([]string, 0, len(hosts))
	for ip := range hosts {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	for _, ip := range ips {
		fmt.Printf("  %s %s\n", ip, strings.Join(hosts[ip], " "))
	}
	if machines := sb.machines(); len(machines) > 0 {
		fmt.Println("\nMachines (containers; rondo-sandbox ssh <machine> opens a shell on one):")
		for _, m := range machines {
			fmt.Printf("  %-28s %-15s %s, group %s, target %s\n", m.Name, m.IP, m.Enrollment, m.Group, m.Target)
		}
	}
	if len(ports) > 0 {
		fmt.Println("\nWorkstation ports (rondo-sandbox.yaml, ports):")
		for _, f := range ports {
			where := "cluster " + f.Cluster
			if f.Machine != "" {
				where = "machine " + f.Machine
			}
			fmt.Printf("  %-60s (%s)\n", f.String(), where)
			if f.Protocol == "http" && !slices.Contains(hosts[f.IP], f.Host) {
				log.Warn("port to a host that no ingress of the component serves", "port", f.HostPort, "host", f.Host)
			}
		}
	}
	fmt.Printf("Management kubeconfig: %s\n", sb.ws.Kubeconfig())
}

// checkImages fails on any image of the component that does not come through
// ${target.values.imageRegistry} (the sandbox pulled it from the Internet, an air-gapped site
// could not), or that its registry cannot serve: a CronJob counts as healthy once applied,
// its image is only pulled at its first run.
func (sb *sandbox) checkImages(ctx context.Context) error {
	prefix := registryHost + "/"
	var outside, missing []string
	checked := map[string]bool{}
	for _, c := range sb.site.Clusters {
		out, err := sb.kube.Output(ctx, filepath.Join(sb.s.clusterDir(c.Name), "kubeconfig.yaml"),
			"get", "pods,cronjobs", "-A", "-o", "json")
		if err != nil {
			return err
		}
		var list struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal([]byte(out), &list); err != nil {
			return err
		}
		for _, item := range list.Items {
			md, _ := item["metadata"].(map[string]any)
			ns, _ := md["namespace"].(string)
			if !slices.Contains(sb.p.Namespaces, ns) {
				continue
			}
			name, _ := md["name"].(string)
			kind, _ := item["kind"].(string)
			for _, img := range containerImages(item["spec"]) {
				if !strings.HasPrefix(img, prefix) {
					outside = append(outside, fmt.Sprintf("%s/%s/%s: %s", ns, kind, name, img))
					continue
				}
				if checked[img] {
					continue
				}
				checked[img] = true
				if !sb.served(ctx, strings.TrimPrefix(img, prefix)) {
					missing = append(missing, fmt.Sprintf("%s/%s/%s: %s", ns, kind, name, img))
				}
			}
		}
	}
	slices.Sort(outside)
	outside = slices.Compact(outside)
	for _, o := range outside {
		log.Error("image outside ${target.values.imageRegistry}", "object", o)
	}
	if len(outside) > 0 {
		return fmt.Errorf("%d image(s) pulled from outside the site's Artifactory: unreachable when air-gapped, "+
			"their registry must come from ${target.values.imageRegistry}", len(outside))
	}
	log.Info("all the component's images go through ${target.values.imageRegistry}")
	return sb.reportMissing(missing)
}

// reportMissing fails on an image nobody publishes; one the developer chose not to build
// (--no-build) is only a warning.
func (sb *sandbox) reportMissing(missing []string) error {
	var fatal int
	for _, m := range missing {
		if sb.p.NoBuild && sb.declaredImage(m) {
			log.Warn("image not built (--no-build): its pods will not start", "object", m)
			continue
		}
		fatal++
		log.Error("image not found on the sandbox's Artifactory", "object", m)
	}
	if fatal > 0 {
		return fmt.Errorf("%d image(s) nobody publishes: neither built by rondo-sandbox.yaml "+
			"(images) nor upstream; on a site, ImagePullBackOff", fatal)
	}
	return nil
}

func (sb *sandbox) declaredImage(entry string) bool {
	for _, img := range sb.p.Images {
		if strings.Contains(entry, registryHost+"/"+img.Name+":") {
			return true
		}
	}
	return false
}

// served asks zot for a manifest, as containerd would: a miss syncs it from upstream first.
func (sb *sandbox) served(ctx context.Context, ref string) bool {
	repo, tag := ref, "latest"
	if i := strings.LastIndex(ref, "@"); i > 0 {
		repo, tag = ref[:i], ref[i+1:]
	} else if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		repo, tag = ref[:i], ref[i+1:]
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead,
		"http://"+sb.p.Registry+"/v2/"+repo+"/manifests/"+tag, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json", "application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json"}, ", "))
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// containerImages walks a spec for the images of its containers, init and ephemeral
// containers, pod templates included.
func containerImages(node any) []string {
	var out []string
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			switch k {
			case "containers", "initContainers", "ephemeralContainers":
				items, _ := v.([]any)
				for _, it := range items {
					c, _ := it.(map[string]any)
					if img, ok := c["image"].(string); ok {
						out = append(out, img)
					}
				}
			default:
				out = append(out, containerImages(v)...)
			}
		}
	case []any:
		for _, v := range n {
			out = append(out, containerImages(v)...)
		}
	}
	return out
}

// RunStatus shows the site's clusters, then every installed component's Enrollments, with the
// URLs, machines and ports they give.
func RunStatus(ctx context.Context, s Settings) error {
	sb, err := openSandbox(s, "")
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TARGET\tCLUSTER\tROLES\tINGRESS")
	for _, c := range sb.site.Clusters {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.Target, c.Name, strings.Join(c.Roles, ","), c.IngressIP)
	}
	for _, t := range sb.site.VSphere {
		fmt.Fprintf(w, "%s\t(vSphere: containers)\t%s\t—\n", t.Name, strings.Join(t.Roles, ","))
	}
	for _, d := range sb.site.Downstreams {
		if sb.site.cluster(d.Name) == nil {
			fmt.Fprintf(w, "%s\t(not deployed: rondo-sandbox deploy downstreams %s)\t%s\t—\n", d.Name, d.Name,
				strings.Join(d.Roles, ","))
		}
	}
	_ = w.Flush()
	if len(sb.plans) == 0 {
		fmt.Println("\nNo component installed: `rondo-sandbox component install <directory>`")
		return nil
	}
	fmt.Println("\nInstalled components:", strings.Join(sb.components(), ", "))
	want := sb.enrollmentNames()
	list, err := sb.enrollments(ctx)
	if err != nil {
		return err
	}
	var mine []v1alpha1.Enrollment
	for _, e := range list {
		if slices.Contains(want, e.Name) {
			mine = append(mine, e)
		}
	}
	sb.report(ctx, mine)
	return nil
}

func names(list []v1alpha1.Enrollment) string {
	out := make([]string, len(list))
	for i, e := range list {
		out[i] = e.Name
	}
	return strings.Join(out, ", ")
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

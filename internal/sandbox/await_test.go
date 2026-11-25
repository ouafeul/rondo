package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/yaml"

	"rondo/api/v1alpha1"
	"rondo/internal/runner"
	"rondo/internal/steps"
)

const kubectl = "/bin/kubectl"

// fakeSandbox is a sandbox whose kubectl answers what a test programs, keyed by command line;
// p, when set, is the component it installs.
func fakeSandbox(t *testing.T, st *site, p *plan) (*sandbox, *runner.Fake) {
	t.Helper()
	fake := &runner.Fake{Outputs: map[string]string{}, Errs: map[string]error{}}
	ws := steps.Workspace{ProjectDir: "../..", StateDir: t.TempDir(), BinDir: "/bin"}
	sb := &sandbox{s: Settings{StateDir: t.TempDir(), RondoDir: "../.."}, site: st, p: p, ws: ws,
		kube: steps.NewKubeClient(ws, fake), run: fake}
	if p != nil {
		sb.plans = []*plan{p}
	}
	return sb, fake
}

// oneCluster is a sandbox whose management carries the monitoring role, and the component it
// installs there.
func oneCluster(t *testing.T, registry string) (*sandbox, *runner.Fake) {
	t.Helper()
	return fakeSandbox(t, &site{Clusters: []cluster{{Name: "rondo-sbx", Target: managementRole,
		IngressIP: "10.0.0.100", Roles: []string{managementRole, "monitoring"}, Management: true}}},
		&plan{Component: "acme", Enrollments: []string{"web"}, Namespaces: []string{"web"}, Registry: registry,
			Targets: map[string]string{"web": managementRole}})
}

// awaitComponent is component install's wait.
func awaitComponent(sb *sandbox, o DeployOptions) error {
	return sb.await(context.Background(), o, sb.p.Enrollments, "component "+sb.p.Component)
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// program answers the verdict loop's reads with the world of verdictWorld.
func program(t *testing.T, fake *runner.Fake, enr *v1alpha1.Enrollment, w *world, pods string) {
	t.Helper()
	fake.Outputs[kubectl+" -n rondo-system get enrollments.rondo.ouafeul.github.io -o json"] =
		asJSON(t, v1alpha1.EnrollmentList{Items: []v1alpha1.Enrollment{*enr}})
	fake.Outputs[kubectl+" -n rondo-system get components.rondo.ouafeul.github.io -o json"] =
		asJSON(t, v1alpha1.ComponentList{Items: []v1alpha1.Component{*w.components["web"]}})
	fake.Outputs[kubectl+" -n rondo-system get targets.rondo.ouafeul.github.io -o json"] =
		asJSON(t, v1alpha1.TargetList{Items: []v1alpha1.Target{*w.targets["management"]}})
	fake.Outputs[kubectl+" get ingress -A -o json"] =
		`{"items":[{"metadata":{"namespace":"web"},"spec":{"rules":[{"host":"web.monitoring.sandbox.test"}]}}]}`
	fake.Outputs[kubectl+" get pods,cronjobs -A -o json"] = pods
	fake.Outputs[kubectl+" get pods -o json -n rondo-system"] = `{"items":[]}`
	fake.Outputs[kubectl+" -n rondo-system get jobs -o json"] = `{"items":[]}`
}

func registry(t *testing.T, served ...string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, s := range served {
			if r.URL.Path == "/v2/"+s {
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

const webPods = `{"items":[{"kind":"Pod","metadata":{"namespace":"web","name":"web-1"},
	"spec":{"containers":[{"image":"mirror.sandbox.test/acme/web:2.1.0"}]}}]}`

func TestAwaitConcludesOnceEveryEnrollmentIsReady(t *testing.T) {
	w, enr := verdictWorld(t)
	sb, fake := oneCluster(t, registry(t, "acme/web/manifests/2.1.0"))
	program(t, fake, enr, w, webPods)
	opts := DeployOptions{Timeout: time.Minute, MaxFailures: 2, since: time.Now()}
	if err := awaitComponent(sb, opts); err != nil {
		t.Fatalf("verdict: %v", err)
	}
}

func TestAwaitFailsOnAnImageNobodyPublishes(t *testing.T) {
	w, enr := verdictWorld(t)
	sb, fake := oneCluster(t, registry(t))
	program(t, fake, enr, w, webPods)
	err := awaitComponent(sb, DeployOptions{Timeout: time.Minute, MaxFailures: 2, since: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "nobody publishes") {
		t.Errorf("image missing from the registry: %v", err)
	}

	sb.p.NoBuild = true
	sb.p.Images = []imageBuild{{Name: "acme/web"}}
	if err := awaitComponent(sb, DeployOptions{Timeout: time.Minute, MaxFailures: 2,
		since: time.Now()}); err != nil {
		t.Errorf("rondo-sandbox.yaml image under --no-build: %v, want a warning", err)
	}
}

func TestAwaitFailsOnAnImageOutsideTheRegistry(t *testing.T) {
	w, enr := verdictWorld(t)
	sb, fake := oneCluster(t, registry(t))
	program(t, fake, enr, w, `{"items":[{"kind":"Pod","metadata":{"namespace":"web","name":"web-1"},
		"spec":{"initContainers":[{"image":"docker.io/library/busybox:1.36"}]}}]}`)
	err := awaitComponent(sb, DeployOptions{Timeout: time.Minute, MaxFailures: 2, since: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "outside the site's mirror") {
		t.Errorf("image pulled from the Internet: %v", err)
	}
}

func TestAwaitExplainsAFailingEnrollment(t *testing.T) {
	since := time.Now()
	w, enr := verdictWorld(t)
	done := metav1.NewTime(since.Add(time.Minute))
	enr.Status.LastAttempt = &v1alpha1.AttemptInfo{JobName: "job-web", Result: "Failed",
		Message: "helm upgrade: exit 1", FinishedAt: &done}
	enr.Status.ConsecutiveFailures = 2
	sb, fake := oneCluster(t, registry(t))
	program(t, fake, enr, w, `{"items":[]}`)
	fake.Outputs[kubectl+" -n rondo-system logs job/job-web --tail=40"] =
		"Error: rendered manifests contain a resource that already exists. cannot be imported into the current release"
	err := awaitComponent(sb, DeployOptions{Timeout: time.Minute, MaxFailures: 2, since: since})
	if err == nil || !strings.Contains(err.Error(), "component acme failed: web") {
		t.Errorf("failed Enrollment: %v", err)
	}
}

func TestAwaitStopsOnACrashingOperator(t *testing.T) {
	w, enr := verdictWorld(t)
	enr.Status.Conditions = nil
	sb, fake := oneCluster(t, registry(t))
	program(t, fake, enr, w, `{"items":[]}`)
	fake.Outputs[kubectl+" get pods -o json -n rondo-system"] = `{"items":[{"metadata":{"name":"rondo-probe-1",
		"labels":{"app.kubernetes.io/name":"rondo-probe"}},"status":{"phase":"Running",
		"containerStatuses":[{"ready":false,"restartCount":5}]}}]}`
	fake.Outputs[kubectl+" -n rondo-system logs rondo-probe-1 --previous --tail=25"] = "panic: assignment to entry in nil map"
	err := awaitComponent(sb, DeployOptions{Timeout: time.Minute, MaxFailures: 2, since: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "rondo-probe in CrashLoopBackOff") {
		t.Errorf("operator crash-looping: %v", err)
	}
}

func TestAwaitStopsOnAnAdoptedJob(t *testing.T) {
	w, enr := verdictWorld(t)
	enr.Status.Conditions = nil
	enr.Status.LastAttempt = &v1alpha1.AttemptInfo{JobName: "enrollment-web-helmapply-1", Result: "Succeeded"}
	sb, fake := oneCluster(t, registry(t))
	program(t, fake, enr, w, `{"items":[]}`)
	fake.Outputs[kubectl+" -n rondo-system get jobs -o json"] = `{"items":[{"metadata":{
		"name":"enrollment-web-helmapply-1","ownerReferences":[{"name":"web-other"}]}}]}`
	err := awaitComponent(sb, DeployOptions{Timeout: time.Minute, MaxFailures: 2, since: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "took over Job") {
		t.Errorf("Job taken over from another Enrollment: %v", err)
	}
}

func TestStuckPodsNamesWhyAPodDoesNotRun(t *testing.T) {
	sb, fake := oneCluster(t, "")
	fake.Outputs[kubectl+" get pods -o json -A"] = `{"items":[
		{"metadata":{"namespace":"logging","name":"fluent-bit-1"},"status":{"phase":"Pending",
		 "containerStatuses":[{"state":{"waiting":{"reason":"ContainerCreating"}}}]}},
		{"metadata":{"namespace":"kube-system","name":"coredns"},"status":{"phase":"Pending"}}]}`
	fake.Outputs[kubectl+" get events -A --field-selector type=Warning -o json"] = `{"items":[
		{"lastTimestamp":"2026-11-05T10:00:00Z","message":"hostPath type check failed: /etc/machine-id is not a file",
		 "involvedObject":{"namespace":"logging","name":"fluent-bit-1"}}]}`
	sb.stuckPods(context.Background())
	if w := sb.warnings(context.Background(), "kc")["logging/fluent-bit-1"]; !strings.Contains(w, "machine-id") {
		t.Errorf("last event = %q", w)
	}
}

func TestBootstrapStepsApplyWhatASiteWouldHave(t *testing.T) {
	s, p := planned(t)
	st, err := loadSite(s)
	if err != nil {
		t.Fatal(err)
	}
	sb, fake := fakeSandbox(t, st, p)
	sb.s.StateDir = t.TempDir()
	sb.cfg = siteConfig(st, sb.plans)
	if err := writeFile(sb.s.caPath(), "CA"); err != nil {
		t.Fatal(err)
	}
	if err := sb.applySite(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sb.probeGrant(context.Background()); err != nil {
		t.Fatal(err)
	}
	var applied []string
	for _, c := range fake.Calls {
		if !strings.HasSuffix(c.String(), "apply -f -") {
			continue
		}
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(c.Stdin), &obj); err != nil {
			t.Fatal(err)
		}
		applied = append(applied, obj.Kind+"/"+obj.Metadata.Name)
		switch {
		case obj.Kind == "Target" && obj.Metadata.Name == managementRole && !strings.Contains(c.Stdin, "inCluster: true"):
			t.Errorf("management Target without inCluster:\n%s", c.Stdin)
		case obj.Kind == "Target" && obj.Metadata.Name == "dc1" &&
			(!strings.Contains(c.Stdin, "credentialsSecretRef") || !strings.Contains(c.Stdin, "172.28.0.10") ||
				!strings.Contains(c.Stdin, "helloMessage: hello")):
			t.Errorf("vSphere Target without its Secret, ConfigMap, componentValues or machine IPs:\n%s", c.Stdin)
		}
	}
	slices.Sort(applied)
	want := []string{"ConfigMap/rondo-vsphere-dc1", "Secret/acme", "Secret/rondo-platform-values",
		"Secret/rondo-tofurc", "Secret/rondo-vm-ssh", "Secret/rondo-vsphere-dc1", "Secret/vm-secrets",
		"Target/dc1", "Target/management"}
	if !slices.Equal(applied, want) {
		t.Errorf("applied objects = %v, want %v", applied, want)
	}
	if last := fake.Calls[len(fake.Calls)-1].String(); !strings.Contains(last, "delete clusterrole,clusterrolebinding "+probeGrantName) {
		t.Errorf("management without base layer: the probe must not keep extra rights (%s)", last)
	}
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestAwaitStopsOnANodeUnderDiskPressure(t *testing.T) {
	w, enr := verdictWorld(t)
	enr.Status.Conditions = nil
	sb, fake := oneCluster(t, registry(t))
	program(t, fake, enr, w, `{"items":[]}`)
	fake.Outputs[kubectl+" get nodes -o json"] = `{"items":[{"metadata":{"name":"k3d-rondo-sbx-server-0"},
		"status":{"conditions":[{"type":"MemoryPressure","status":"False"},
		{"type":"DiskPressure","status":"True","message":"kubelet has disk pressure"},{"type":"Ready","status":"True"}]}}]}`
	err := awaitComponent(sb, DeployOptions{Timeout: time.Minute, MaxFailures: 2, since: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "DiskPressure") || !strings.Contains(err.Error(), "docker builder prune") {
		t.Errorf("node under DiskPressure: %v", err)
	}
}

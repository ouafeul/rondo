package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"rondo"
	"rondo/api/v1alpha1"
	"rondo/internal/dashapi"
	"rondo/internal/dashboard"
	"rondo/internal/jobs"
	"rondo/internal/kobj"
)

const ns = kobj.PlatformNamespace

func meta(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: ns} }

func ready(status metav1.ConditionStatus, msg string) []metav1.Condition {
	return []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: status, Reason: "Test", Message: msg}}
}

func fixture() []client.Object {
	started := metav1.NewTime(time.Now().Add(-3 * time.Minute))
	replicas := int32(1)
	sel := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "rondo-controller"}}
	return []client.Object{
		&v1alpha1.Target{
			ObjectMeta: meta("management"),
			Spec: v1alpha1.TargetSpec{
				Roles:      []string{"management"},
				Kubernetes: &v1alpha1.KubernetesCluster{InCluster: true},
				Values:     map[string]string{"clusterDomain": "cluster.example.local"},
			},
			Status: v1alpha1.TargetStatus{Conditions: ready(metav1.ConditionTrue, "")},
		},
		&v1alpha1.Target{
			ObjectMeta: meta("rke2-a"),
			Spec: v1alpha1.TargetSpec{Kubernetes: &v1alpha1.KubernetesCluster{
				Server: "https://10.0.0.50:6443",
			}},
			Status: v1alpha1.TargetStatus{Conditions: ready(metav1.ConditionFalse, "apiserver unreachable")},
		},
		&v1alpha1.Component{
			ObjectMeta: meta("traefik"),
			Spec: v1alpha1.ComponentSpec{
				Chart:     &v1alpha1.ChartSource{OCI: "oci://r/traefik", Version: "41.0.2"},
				Namespace: "traefik",
				DependsOn: []v1alpha1.LocalObjectReference{{Name: "metallb"}},
			},
		},
		&v1alpha1.Component{
			ObjectMeta: meta("demo-mft"),
			Spec:       v1alpha1.ComponentSpec{VM: &v1alpha1.VMSpec{Groups: []v1alpha1.VMGroup{{Name: "front"}, {Name: "back"}}}},
		},
		&v1alpha1.Enrollment{
			ObjectMeta: meta("traefik-management"),
			Spec:       v1alpha1.EnrollmentSpec{Component: "traefik", Target: "management"},
			Status: v1alpha1.EnrollmentStatus{
				ResolvedTarget: "management", Conditions: ready(metav1.ConditionTrue, "ok"),
				Health: v1alpha1.HealthHealthy, AppliedChartVersion: "41.0.2", InventoryCount: 12, AttemptCount: 2,
				LastAttempt: &v1alpha1.AttemptInfo{JobName: "helm-traefik", Result: "Succeeded", StartedAt: started},
				History:     []v1alpha1.AttemptInfo{{JobName: "helm-traefik-0", Result: "Failed", StartedAt: started}},
			},
		},
		&v1alpha1.Enrollment{
			ObjectMeta: meta("mft-rke2-a"),
			Spec:       v1alpha1.EnrollmentSpec{Component: "demo-mft", Target: "rke2-a"},
			Status: v1alpha1.EnrollmentStatus{
				ResolvedTarget: "rke2-a", Conditions: ready(metav1.ConditionFalse, "tofu failed"),
				Health: "Degraded", ConsecutiveFailures: 3,
			},
		},
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "helm-traefik", Namespace: ns, Labels: map[string]string{
				jobs.LabelManagedBy: jobs.ManagedByValue, jobs.LabelVerb: string(jobs.VerbHelmApply),
				jobs.LabelOwnerKind: "Enrollment", jobs.LabelOwnerName: "traefik-management",
			}},
			Status: batchv1.JobStatus{Succeeded: 1, StartTime: &started},
		},
		&appsv1.Deployment{
			ObjectMeta: meta("rondo-controller"),
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas, Selector: sel,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "r/rondo:v1.0.0"}}}},
			},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "rondo-controller-abc", Namespace: ns, Labels: map[string]string{"app": "rondo-controller"}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}},
		},
		&corev1.ConfigMap{
			ObjectMeta: meta(kobj.RecordName("acme")),
			Data:       map[string]string{"version": "1.4.0", "objects": "Component/demo-mft\nEnrollment/mft-rke2-a\n"},
		},
		&corev1.ConfigMap{
			ObjectMeta: meta(kobj.RecordName("rondo")),
			Data:       map[string]string{"version": "v1.0.0", "objects": "Component/traefik\n"},
		},
		&corev1.ConfigMap{
			ObjectMeta: meta(kobj.RecordName("base-management")),
			Data:       map[string]string{"version": "v1.0.0", "objects": "Enrollment/traefik-management\n"},
		},
		&corev1.ConfigMap{ObjectMeta: meta("rondo-component-no-objects")},
	}
}

// newDashboard serves the real dashboard over a fake cluster, and writes the kubectl that reaches
// it: contexts rondo-default and rondo-prod, a port-forward to the server, the record ConfigMaps.
func newDashboard(t *testing.T, dir string) string {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	objs := fixture()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	cs := k8sfake.NewClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "helm-traefik-x", Namespace: ns, Labels: map[string]string{"job-name": "helm-traefik"}},
	})
	srv := httptest.NewServer((&dashboard.Server{
		Client: c, Namespace: ns, Logs: &dashboard.PodLogs{Clientset: cs, Namespace: ns},
	}).Handler())
	t.Cleanup(srv.Close)

	var cms corev1.ConfigMapList
	for _, o := range objs {
		if cm, ok := o.(*corev1.ConfigMap); ok {
			cms.Items = append(cms.Items, *cm)
		}
	}
	raw, err := json.Marshal(cms)
	if err != nil {
		t.Fatal(err)
	}
	cmFile := filepath.Join(dir, "configmaps.json")
	if err := os.WriteFile(cmFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return writeKubectl(t, dir, fmt.Sprintf(`case "$*" in
"config get-contexts -o name") printf 'rondo-default\nrondo-prod\n' ;;
*" port-forward svc/rondo-dashboard :80") echo "Forwarding from 127.0.0.1:%s -> 8080"; exec sleep 600 ;;
*" get configmaps -o json") cat %q ;;
*) echo "unexpected: $*" >&2; exit 1 ;;
esac`, srv.URL[strings.LastIndex(srv.URL, ":")+1:], cmFile))
}

func writeKubectl(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeRondo stands in for the rondo binary: it prints what internal/log prints off a terminal,
// asks the way confirm does, and echoes its arguments. deploy runs until interrupted.
const fakeRondo = `#!/bin/sh
echo "args: $*"
case "$1 $2" in
"deploy "*)
	trap 'echo "ERROR failed  err=context canceled"; exit 1' INT
	echo "▶ provisioning VMs k3s (tofu)"
	while :; do sleep 0.05; done ;;
"init --env")
	mkdir -p "$FAKE_HOME/envs/$3"
	printf 'environmentName: %s\n' "$3" > "$FAKE_HOME/envs/$3/rondo.yaml"
	echo "▶ rondo.yaml written, to fill in path=$FAKE_HOME/envs/$3/rondo.yaml" ;;
"env select")
	echo "▶ environment selected env=$3 kubectl=rondo-$3"
	echo "✓ environment selected env=$3 kubectl=rondo-$3 (0s)" ;;
*)
	echo "▶ applying the tree tree=acme"
	echo "│ tool output"
	echo " INFO object applied  object=Component/acme"
	printf "Apply version 1.4.0? (y/N) " >&2
	read ans
	echo "✓ applying the tree tree=acme (1.2s)"
	[ "$ans" = "y" ] || { echo "ERROR failed  err=cancelled"; exit 1; } ;;
esac
`

type harness struct {
	t        *testing.T
	m        *Model
	home     string
	connects []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	tick = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }
	t.Cleanup(func() { tick = tea.Tick })
	h := &harness{t: t, home: t.TempDir()}
	t.Setenv("FAKE_HOME", h.home)
	exe := filepath.Join(h.home, "rondo")
	if err := os.WriteFile(exe, []byte(fakeRondo), 0o755); err != nil {
		t.Fatal(err)
	}
	prod := filepath.Join(h.home, "envs", "prod", "rondo.yaml")
	if err := os.MkdirAll(filepath.Dir(prod), 0o700); err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(filepath.Dir(prod), "certs", "artifactory-ca.pem")
	if err := os.MkdirAll(filepath.Dir(ca), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ca, []byte("ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prod, rondo.ExampleConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	kubectl := newDashboard(t, h.home)
	h.m = New(Options{
		Executable: exe,
		Version:    "v1.0.0-test",
		Env:        "",
		Connect: func(env string) (Source, string, error) {
			h.connects = append(h.connects, env)
			kctx := "rondo-" + envLabel(env)
			src, err := ConnectRemote(kubectl, kctx)
			if err != nil {
				return nil, kctx, err
			}
			return src, kctx, nil
		},
		Envs: func() []string { return []string{"default", "prod"} },
		ConfigPath: func(env string) string {
			if env == "" {
				return filepath.Join(h.home, "rondo.yaml")
			}
			return filepath.Join(h.home, "envs", env, "rondo.yaml")
		},
	})
	t.Cleanup(func() {
		if h.m.src != nil {
			h.m.src.Close()
		}
	})
	h.do(h.m.Init())
	h.send(tea.WindowSizeMsg{Width: 140, Height: 40})
	return h
}

// do runs cmd and what follows from it; the clock is stopped, finish pumps the commands.
func (h *harness) do(cmd tea.Cmd) {
	h.t.Helper()
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case nil, tea.QuitMsg:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		default:
			_, next := h.m.Update(msg)
			queue = append(queue, next)
		}
	}
}

func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.m.Update(msg)
	h.do(cmd)
}

func (h *harness) keys(keys ...string) {
	h.t.Helper()
	for _, k := range keys {
		h.send(keyPress(k))
	}
}

func keyPress(k string) tea.KeyPressMsg { return keyMsg(k) }

func (h *harness) screen() string {
	return ansi.Strip(h.m.View().Content)
}

func (h *harness) see(want ...string) {
	h.t.Helper()
	s := h.screen()
	for _, w := range want {
		if !strings.Contains(s, w) {
			h.t.Fatalf("%q not on the screen:\n%s", w, s)
		}
	}
}

func (h *harness) dontSee(unwanted ...string) {
	h.t.Helper()
	s := h.screen()
	for _, w := range unwanted {
		if strings.Contains(s, w) {
			h.t.Fatalf("%q shown by mistake:\n%s", w, s)
		}
	}
}

// finish takes the running command's events until it ends, answering its question with answer.
func (h *harness) finish(answer string) *run {
	h.t.Helper()
	r := h.m.runs[len(h.m.runs)-1]
	deadline := time.Now().Add(10 * time.Second)
	for r.state == runRunning {
		if time.Now().After(deadline) {
			h.t.Fatalf("command still running: %v", r.lines)
		}
		time.Sleep(5 * time.Millisecond)
		h.send(runMsg{id: r.id})
		if r.prompt != "" {
			h.see("Confirmation asked", r.prompt+"?")
			h.keys(answer)
		}
	}
	return r
}

func TestScreenFitsTheTerminal(t *testing.T) {
	h := newHarness(t)
	for _, size := range [][2]int{{140, 40}, {80, 24}, {60, 15}} {
		h.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for i := range views {
			h.do(h.m.switchView(i))
			lines := strings.Split(h.m.View().Content, "\n")
			if len(lines) != size[1] {
				t.Errorf("%dx%d, view %s: %d lines", size[0], size[1], views[i].id, len(lines))
			}
			for _, l := range lines {
				if w := ansi.StringWidth(l); w > size[0] {
					t.Errorf("%dx%d, view %s: line of %d columns: %q", size[0], size[1], views[i].id, w, ansi.Strip(l))
				}
			}
		}
	}
}

func TestDashboardViewsShowTheCluster(t *testing.T) {
	h := newHarness(t)
	h.see("RONDO", "env default", "ctx rondo-default", "● connected", "Targets", "1 not ready",
		"Enrollments", "Needs attention", "rke2-a", "apiserver unreachable", "mft-rke2-a")

	h.keys("2")
	h.see("Components", "traefik", "A · Helm", "41.0.2", "1/1 ready", "metallb", "demo-mft", "C · multi-VM", "0/1 ready")
	h.keys("3")
	h.see("Healthy 1", "Degraded 1", "Failing 1", "traefik-management", "Succeeded", "Ready", "Not ready",
		"Component", "Target", "A Component on a Target")
	h.keys("4")
	h.see("management", "local", "remote", "https://10.0.0.50:6443")
	h.keys("5")
	h.see("helm-traefik", "helm-apply", "Enrollment/traefik-management", "3 min ago")
	h.keys("6")
	h.see("rondo-controller", "Healthy", "v1.0.0", "1/1", "rondo-controller-abc", "Running")
	h.keys("7")
	h.see("acme", "1.4.0")
	h.dontSee("no-objects")
}

func TestNavigationDetailAndBack(t *testing.T) {
	h := newHarness(t)
	h.keys("3", "down", "enter")
	h.see("Enrollments › mft-rke2-a", "Consecutive failures", "3", "tofu failed")

	h.keys("esc", "down", "down", "enter")
	h.see("Enrollments › traefik-management", "Last attempt", "helm-traefik", "History", "helm-traefik-0")

	h.keys("l")
	h.see("Enrollments › traefik-management › Job helm-traefik", "fake logs", "follow on")
	h.keys("f")
	h.see("follow paused")

	h.keys("esc", "esc")
	h.see("Healthy 1")
	h.dontSee("History")

	h.keys("1", "enter")
	if h.m.cur().id != vTargets || h.m.sel[vTargets] != "rke2-a" {
		t.Fatalf("⏎ on the overview leads to %s/%s", h.m.cur().id, h.m.sel[vTargets])
	}
	h.keys("enter")
	h.see("Targets › rke2-a", "apiserver unreachable")

	h.keys("tab", "down")
	if h.m.cur().id != vJobs || h.m.top() != nil {
		t.Fatalf("tab then ↓: view %s, panel %v", h.m.cur().id, h.m.top())
	}
	h.keys("up", "up", "up", "up", "up")
	if h.m.cur().id != vConsole {
		t.Fatalf("↑ from the first view: %s", h.m.cur().id)
	}
}

func TestMenuChangesTheViewWithArrows(t *testing.T) {
	h := newHarness(t)
	h.keys("tab")
	if !h.m.inMenu() {
		t.Fatal("tab does not give the menu")
	}
	h.see("change view", "go to content")
	h.keys("down", "j", "k")
	if h.m.cur().id != vComponents || !h.m.inMenu() {
		t.Fatalf("↓ j k in the menu: view %s, menu %v", h.m.cur().id, h.m.inMenu())
	}
	h.keys("enter")
	if h.m.inMenu() || h.m.cur().id != vComponents || h.m.top() != nil {
		t.Fatalf("⏎ in the menu: menu %v, view %s, panel %v", h.m.inMenu(), h.m.cur().id, h.m.top())
	}
	h.keys("4", "left")
	if !h.m.inMenu() {
		t.Fatal("← in a view without a tree does not give the menu")
	}
	h.keys("right", "down")
	if h.m.inMenu() || h.m.sel[vTargets] != "rke2-a" {
		t.Fatalf("→ then ↓: menu %v, selection %q", h.m.inMenu(), h.m.sel[vTargets])
	}
	h.keys("tab", "3")
	if h.m.inMenu() || h.m.cur().id != vEnrollments {
		t.Fatalf("a digit from the menu: menu %v, view %s", h.m.inMenu(), h.m.cur().id)
	}

	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.keys("tab")
	if h.m.inMenu() || h.m.cur().id != vTargets {
		t.Fatalf("tab without a menu shown: menu %v, view %s", h.m.inMenu(), h.m.cur().id)
	}
}

func TestInstallationsGroupComponentsAndEnrollments(t *testing.T) {
	h := newHarness(t)
	h.keys("2")
	h.see("▾ acme", "└ demo-mft", "▾ rondo", "└ traefik", "1 Component", "1.4.0", "0/1 ready", "1/1 ready")
	if s := h.screen(); strings.Index(s, "acme") > strings.Index(s, "▾ rondo") {
		t.Errorf("the installed components come after rondo:\n%s", s)
	}
	h.keys("down", "-")
	h.see("▸ acme", "▸ rondo")
	h.dontSee("demo-mft", "└ traefik")
	if h.m.sel[vComponents] != rootKey("acme") {
		t.Fatalf("- on a Component: selection %q", h.m.sel[vComponents])
	}
	h.keys("+")
	h.see("▾ acme", "└ demo-mft", "▾ rondo", "└ traefik")
	h.keys("space")
	h.see("▸ acme")
	h.dontSee("demo-mft")
	h.keys("space")
	h.see("▾ acme", "└ demo-mft")
	h.keys("down", "right")
	if h.m.sel[vComponents] != "demo-mft" || h.m.inMenu() || len(h.m.folded[vComponents]) != 1 {
		t.Fatalf("→ on a Component: selection %q, menu %v, folded %v", h.m.sel[vComponents], h.m.inMenu(), h.m.folded[vComponents])
	}
	h.keys("space")
	if h.m.sel[vComponents] != rootKey("acme") || !h.m.folded[vComponents][rootKey("acme")] {
		t.Fatalf("space on a Component: selection %q, folded %v", h.m.sel[vComponents], h.m.folded[vComponents])
	}
	h.keys("left")
	if !h.m.inMenu() || !h.m.folded[vComponents][rootKey("acme")] {
		t.Fatalf("← on a root: menu %v, folded %v", h.m.inMenu(), h.m.folded[vComponents])
	}
	h.keys("right", "space", "enter")
	h.see("Components › acme", "configmap/rondo-component-acme", "1.4.0", "charts and machines",
		"demo-mft", "C · multi-VM", "mft-rke2-a", "rke2-a", "Not ready")
	h.dontSee("traefik")
	h.keys("l")
	h.dontSee("no attempt")

	h.keys("esc", "3")
	h.see("▾ acme", "└ mft-rke2-a", "▾ rondo", "└ traefik-management", "1 Enrollment", "Degraded")
	h.keys("/", "t", "r", "a", "e")
	h.see("▾ rondo", "traefik-management")
	h.dontSee("acme", "mft-rke2-a")
	h.keys("enter", "l")
	h.dontSee("no attempt")
}

func TestServiceAndJobLogs(t *testing.T) {
	h := newHarness(t)
	h.keys("5", "enter")
	h.see("Jobs & logs › Job helm-traefik", "fake logs")
	h.keys("esc", "6", "enter")
	h.see("Services › Pod rondo-controller-abc", "fake logs")
}

func TestFilterKeepsMatchingRows(t *testing.T) {
	h := newHarness(t)
	h.keys("2", "/", "m", "f", "t")
	h.see("/mft", "demo-mft")
	h.dontSee("traefik")
	h.keys("enter")
	h.see("filter: mft")
	h.keys("esc")
	h.see("traefik", "demo-mft")
}

func TestInstallRunsTheCLIAndAnswersItsQuestion(t *testing.T) {
	h := newHarness(t)
	h.keys("D", "7", "i")
	h.see("Install a component", "Component", "Version", "$ rondo component install <component> --env default --dry-run")
	h.keys("enter")
	h.see("Component: value required")

	for _, k := range "acme" {
		h.keys(string(k))
	}
	h.keys("tab", "1", ".", "4", "enter")
	if h.m.cur().id != vConsole {
		t.Fatalf("the console must open, view %s", h.m.cur().id)
	}
	r := h.finish("y")
	if r.state != runSucceeded {
		t.Fatalf("state %v, lines %v", r.state, r.lines)
	}
	h.see("Install a component", "finished", "applying the tree tree=acme", "1.2s",
		"object applied", "args: component install acme --version 1.4 --env default --dry-run")
	if len(r.steps) != 1 || r.steps[0].state != runSucceeded || r.steps[0].dur != "1.2s" {
		t.Errorf("steps = %+v", r.steps)
	}
	if !strings.Contains(h.m.flash, "finished") || h.m.flashBad {
		t.Errorf("flash = %q", h.m.flash)
	}
}

func TestReadOnlyLaunchesNothing(t *testing.T) {
	h := newHarness(t)
	h.m.opts.ReadOnly = true
	h.keys("7", "i")
	if h.m.form != nil || !strings.Contains(h.m.flash, "read-only") {
		t.Errorf("form opened in read-only mode (flash %q)", h.m.flash)
	}
	// prod has an rondo.yaml: outside read-only mode, these keys open it (TestEditorSavesTheManifest)
	h.m.flash = ""
	h.keys("8", "down", "e")
	if h.m.editor != nil || !strings.Contains(h.m.flash, "read-only") {
		t.Errorf("rondo.yaml editor opened in read-only mode (flash %q)", h.m.flash)
	}
	if cmd := h.m.launch(opDestroy, []string{"destroy"}); cmd != nil || len(h.m.runs) != 0 {
		t.Error("command launched in read-only mode")
	}
	h.keys("3")
	h.see("traefik-management", "Succeeded")
}

func TestReadOnlySaysSoAndShadesWhatItRefuses(t *testing.T) {
	h := newHarness(t)
	h.dontSee("READ-ONLY")
	h.m.opts.ReadOnly = true
	h.see("READ-ONLY", "env default")

	h.keys("7")
	footer := h.m.renderFooter()
	for _, k := range []string{"i", "m", "u"} {
		if !strings.Contains(footer, stDim.Render(k)) || strings.Contains(footer, stKey.Render(k)) {
			t.Errorf("key %q not shaded in the bar: %q", k, footer)
		}
	}
	if !strings.Contains(footer, stKey.Render("⏎")) {
		t.Errorf("⏎ (objects) shaded though it launches nothing: %q", footer)
	}
	for i, row := range h.m.table(vOps).rows {
		if row[0].style.Render("x") != stDim.Render("x") || row[1].style.Render("x") != stDim.Render("x") {
			t.Errorf("operation %s not shaded", h.m.table(vOps).keys[i])
		}
	}
	h.keys("?")
	h.see("Commands — refused in read-only mode")
}

func TestRefusedQuestionFailsTheCommand(t *testing.T) {
	h := newHarness(t)
	h.keys("7", "u")
	h.see("Uninstall a component", "destructive operation", "$ rondo component uninstall acme --env default")
	h.keys("enter")
	r := h.finish("n")
	if r.state != runFailed || !strings.Contains(h.m.flash, "cancelled") {
		t.Fatalf("state %v, flash %q", r.state, h.m.flash)
	}
	h.see("failed", "ERROR failed")
}

func TestOneCommandAtATimeAndQuitAsks(t *testing.T) {
	h := newHarness(t)
	h.keys("9", "enter", "enter")
	if h.m.active() == nil {
		t.Fatal("deploy must be running")
	}
	h.keys("9", "down", "enter", "enter")
	if len(h.m.runs) != 1 || !strings.Contains(h.m.flash, "is running") {
		t.Fatalf("%d runs, flash %q", len(h.m.runs), h.m.flash)
	}
	h.keys("q")
	h.see("Quit?", "quitting interrupts it")
	h.keys("n")
	h.dontSee("Quit?")

	h.keys("0", "x")
	r := h.finish("y")
	if !r.stopped || r.state != runFailed {
		t.Fatalf("stopped=%v state=%v", r.stopped, r.state)
	}
	h.see("interrupted")
}

func TestSelectingAnEnvironmentReconnects(t *testing.T) {
	h := newHarness(t)
	h.keys("8")
	h.see("Environments", "●  default", "prod", "rondo-prod", "Manifest", "read manifest")
	h.keys("down")
	h.see("environmentName", "cluster.example.local", "k3s v1.31.4+k3s1 on dc1, 3 nodes", "rke2 rke2-a")
	h.dontSee("CHANGEME")
	h.keys("enter")
	if h.m.cur().id != vEnvs {
		t.Fatalf("env select must not open the console, view %s", h.m.cur().id)
	}
	h.finish("y")
	if h.m.env != "prod" || h.connects[len(h.connects)-1] != "prod" {
		t.Fatalf("env %q, connections %v", h.m.env, h.connects)
	}
	h.see("env prod", "ctx rondo-prod")

	h.keys("4", "d")
	h.see("Deploy downstreams", "[ ] rke2-a", "none checked => all")
	h.keys("space")
	h.see("[x] rke2-a", "$ rondo deploy downstreams rke2-a --env prod")
}

func TestUnreachableClusterStillOffersTheCLI(t *testing.T) {
	h := newHarness(t)
	h.do(h.m.switchEnv("absent"))
	h.see("● disconnected", "Cannot read the management cluster", "kubectl context rondo-absent not found")
	h.keys("9")
	h.see("Deploy management", "rondo deploy")
}

func TestHelpAndToggles(t *testing.T) {
	h := newHarness(t)
	h.keys("?")
	h.see("Help", "Navigation", "toggle --dry-run")
	h.keys("z")
	h.dontSee("toggle --dry-run")
	h.keys("D", "S", "a")
	h.see("DRY-RUN", "SKIP-DL", "paused")
	h.keys("9")
	h.see("[x] dry-run", "[x] skip-download")
	h.keys("ctrl+c")
}

// clickOn clicks the first place the screen shows text, as a terminal reports it; dx columns
// further in, when given.
func (h *harness) clickOn(text string, dx ...int) {
	h.t.Helper()
	for y, l := range strings.Split(h.screen(), "\n") {
		if i := strings.Index(l, text); i >= 0 {
			x := ansi.StringWidth(l[:i])
			for _, d := range dx {
				x += d
			}
			h.send(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
			return
		}
	}
	h.t.Fatalf("%q not on the screen:\n%s", text, h.screen())
}

func TestMouseClicksWhereTheScreenDrawsIt(t *testing.T) {
	h := newHarness(t)
	if h.m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("the mouse is not captured")
	}
	h.clickOn("4 Targets")
	if h.m.cur().id != vTargets || !h.m.inMenu() {
		t.Fatalf("click on the menu: view %s, menu %v", h.m.cur().id, h.m.inMenu())
	}
	h.clickOn("rke2-a")
	if h.m.sel[vTargets] != "rke2-a" || h.m.top() != nil || h.m.inMenu() {
		t.Fatalf("click on a row: selection %q, panel %v, menu %v", h.m.sel[vTargets], h.m.top(), h.m.inMenu())
	}
	y := strings.Count(h.screen()[:strings.Index(h.screen(), "0 Console")], "\n") + 3
	h.send(tea.MouseClickMsg{X: 3, Y: y, Button: tea.MouseLeft})
	if !h.m.inMenu() || h.m.cur().id != vTargets {
		t.Fatalf("click in the menu's empty space: menu %v, view %s", h.m.inMenu(), h.m.cur().id)
	}
	h.send(tea.MouseClickMsg{X: sidebarWidth + 10, Y: y, Button: tea.MouseLeft})
	if h.m.inMenu() || h.m.sel[vTargets] != "rke2-a" {
		t.Fatalf("click in the content's empty space: menu %v, selection %q", h.m.inMenu(), h.m.sel[vTargets])
	}

	h.clickOn("2 Components")
	h.clickOn("demo-mft")
	h.clickOn("demo-mft")
	h.see("Components › demo-mft", "VM groups")
	h.keys("esc")

	h.clickOn("▾ rondo")
	h.see("▸ rondo")
	h.dontSee("└ traefik")
	if h.m.sel[vComponents] != rootKey("rondo") {
		t.Fatalf("click on ▾: selection %q", h.m.sel[vComponents])
	}
	h.clickOn("▸ rondo", 2)
	if !h.m.folded[vComponents][rootKey("rondo")] || h.m.sel[vComponents] != rootKey("rondo") {
		t.Errorf("click on a root's name: folded %v, selection %q", h.m.folded[vComponents], h.m.sel[vComponents])
	}

	h.send(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if h.m.sel[vComponents] != "demo-mft" {
		t.Fatalf("wheel: selection %q", h.m.sel[vComponents])
	}

	h.keys("tab")
	h.clickOn("acme")
	if h.m.inMenu() || h.m.sel[vComponents] != rootKey("acme") {
		t.Fatalf("click in the content from the menu: menu %v, selection %q", h.m.inMenu(), h.m.sel[vComponents])
	}

	h.keys("M")
	if h.m.View().MouseMode != tea.MouseModeNone {
		t.Fatal("M does not give the mouse back to the terminal")
	}
	h.see("mouse off")
	h.keys("M")
	if h.m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("M does not take the mouse back")
	}
}

func TestFooterHintsSendTheirKey(t *testing.T) {
	h := newHarness(t)
	h.keys("3", "down", "enter")
	h.clickOn("esc back")
	if h.m.top() != nil {
		t.Fatal(`click on "esc back": the panel stays`)
	}
	h.clickOn("tab menu")
	if !h.m.inMenu() {
		t.Fatal(`click on "tab menu": no menu`)
	}
	h.clickOn("↑↓ change", 1)
	if h.m.cur().id != vTargets {
		t.Fatalf("click on ↓: view %s", h.m.cur().id)
	}
	h.clickOn("↑↓ change")
	if h.m.cur().id != vEnrollments {
		t.Fatalf("click on ↑: view %s", h.m.cur().id)
	}
	h.clickOn("1…0 go")
	if !h.m.inMenu() || h.m.cur().id != vEnrollments {
		t.Fatalf("click on 1…0: menu %v, view %s", h.m.inMenu(), h.m.cur().id)
	}
	h.clickOn("→ ⏎ go", 2)
	if h.m.inMenu() {
		t.Fatal("click on ⏎: the menu keeps the focus")
	}

	h.keys("2")
	h.clickOn("collapse all")
	h.see("▸ acme", "▸ rondo")
	h.clickOn("expand all")
	h.see("▾ acme", "▾ rondo")
	h.clickOn("- collapse all")
	h.see("▸ acme", "▸ rondo")
	h.clickOn("+ expand all")
	h.see("▾ acme", "▾ rondo")
	h.clickOn("/ filter")
	if !h.m.filtering {
		t.Fatal(`click on "/ filter": no filter`)
	}
	h.clickOn("esc clear")
	if h.m.filtering {
		t.Fatal(`click on "esc clear": the filter stays`)
	}
	h.clickOn("? help")
	h.see("Navigation", "any key close")
	h.clickOn("any key close")
	h.dontSee("Navigation")
}

func TestBreadcrumbGoesBackToALevel(t *testing.T) {
	h := newHarness(t)
	h.keys("3", "down", "down", "down", "enter", "l")
	h.see("Enrollments › traefik-management › Job helm-traefik", "fake logs")
	h.clickOn("Job helm-traefik")
	if len(h.m.stack) != 2 {
		t.Fatalf("click on the current level: %d panels", len(h.m.stack))
	}
	h.clickOn("traefik-management ›")
	if len(h.m.stack) != 1 || h.m.top().key != "traefik-management" {
		t.Fatalf("click on the detail: panels %d", len(h.m.stack))
	}
	h.see("Enrollments › traefik-management", "Last attempt")
	h.dontSee("fake logs")
	h.keys("l")
	h.clickOn("Enrollments ›")
	if h.m.top() != nil || h.m.cur().id != vEnrollments || h.m.inMenu() {
		t.Fatalf("click on the view: panel %v, view %s, menu %v", h.m.top(), h.m.cur().id, h.m.inMenu())
	}
	h.see("Healthy 1", "▾ rondo")
}

func TestDetailNamesOpenTheirDetail(t *testing.T) {
	h := newHarness(t)
	h.keys("2", "enter")
	h.see("Components › acme", "charts and machines")
	h.clickOn("mft-rke2-a")
	h.see("Components › acme › mft-rke2-a", "Consecutive failures", "tofu failed")
	h.clickOn("acme ›")
	h.clickOn("demo-mft")
	h.see("Components › acme › demo-mft", "VM groups", "Placements")
	h.clickOn("mft-rke2-a")
	h.see("Components › acme › demo-mft › mft-rke2-a", "Consecutive failures")
	h.keys("l")
	h.see("no attempt for mft-rke2-a")

	h.keys("esc", "esc")
	h.see("Components › acme", "charts and machines")
	h.send(tea.WindowSizeMsg{Width: 140, Height: 12})
	h.keys("G")
	h.dontSee("charts and machines")
	h.clickOn("mft-rke2-a")
	if p := h.m.top(); p == nil || p.key != "mft-rke2-a" {
		t.Fatalf("click on a name after scrolling: panel %+v", p)
	}
}

func TestFilterMatchesARootByItsName(t *testing.T) {
	h := newHarness(t)
	h.keys("2", "/")
	h.keys(strings.Split("component", "")...)
	h.see("No items.")
	h.dontSee("demo-mft", "traefik")
	h.keys("esc", "/")
	h.keys(strings.Split("acme", "")...)
	h.see("▾ acme", "└ demo-mft")
	h.dontSee("traefik")
}

func TestTreeViewsSayWhatTheyCouldNotRead(t *testing.T) {
	h := newHarness(t)
	h.keys("2")
	h.send(dataMsg{gen: h.m.gen, view: vTrees, err: errors.New("configmaps forbidden")})
	h.see(`Cannot read, view "Installations": configmaps forbidden`)
	delete(h.m.data, vEnrollments)
	h.send(dataMsg{gen: h.m.gen, view: vEnrollments, err: errors.New("dashboard unreachable")})
	h.see(`Cannot read, view "Enrollments": dashboard unreachable`)
	h.keys("enter")
	h.see("Components › acme", "cannot read: dashboard unreachable")
	h.dontSee("loading…")
}

func TestLogsKeyIgnoresAnInstallation(t *testing.T) {
	h := newHarness(t)
	h.keys("3", "enter")
	h.see("Enrollments › acme", "mft-rke2-a")
	h.dontSee("l logs")
	h.keys("l")
	if len(h.m.stack) != 1 {
		t.Fatalf("l on an installation: %d panels", len(h.m.stack))
	}
	h.dontSee("no attempt")
}

func TestHeaderFollowsTheShownViewsRead(t *testing.T) {
	h := newHarness(t)
	h.keys("2")
	h.send(dataMsg{gen: h.m.gen, view: vEnrollments, data: h.m.data[vEnrollments]})
	h.send(dataMsg{gen: h.m.gen, view: vTrees, err: errors.New("configmaps forbidden")})
	h.see("● connected", `Cannot read, view "Installations": configmaps forbidden`)
	h.send(dataMsg{gen: h.m.gen, view: vComponents, err: errors.New("dashboard unreachable")})
	h.send(dataMsg{gen: h.m.gen, view: vTrees, data: h.m.data[vTrees]})
	h.see("● unreachable")

	h.keys("8")
	h.send(dataMsg{gen: h.m.gen, view: vTrees, data: h.m.data[vTrees]})
	h.see("● connected")
}

// A long line of words before a link: the viewport cuts it by width, which wrapLinks counts; cut
// by words, its six 40-column words would take two lines more in 72 columns.
func TestLinksAfterAWrappedLineOfWords(t *testing.T) {
	h := newHarness(t)
	h.send(tea.WindowSizeMsg{Width: 100, Height: 40})
	h.keys("2")
	var deps []string
	for i := range 6 {
		deps = append(deps, fmt.Sprintf("dependencywithreallyverylongnamenumber%02d", i))
	}
	line, w := strings.TrimSuffix(kv("Depends on", strings.Join(deps, ", ")), "\n"), h.m.contentWidth()
	byWidth, byWords := (ansi.StringWidth(line)+w-1)/w, strings.Count(ansi.Wordwrap(line, w, ""), "\n")+1
	if byWords <= byWidth {
		t.Fatalf("%d lines by width, %d by words: the test cannot tell the two wraps apart", byWidth, byWords)
	}
	h.send(dataMsg{gen: h.m.gen, view: vComponents, data: []dashapi.ComponentView{{
		Name: "demo-mft", Archetype: "B", DependsOn: deps,
		Enrollments: []dashapi.PlacementView{{Name: "mft-rke2-a", Target: "rke2-a", Ready: "False"}},
	}}})
	h.keys("down", "enter")
	h.see("Components › demo-mft", "Placements")
	h.clickOn("mft-rke2-a")
	if p := h.m.top(); p == nil || p.key != "mft-rke2-a" {
		t.Fatalf("click under a wrapped line: panel %+v", p)
	}
	h.see("Components › demo-mft › mft-rke2-a")
}

func TestWheelActsOnThePaneUnderIt(t *testing.T) {
	h := newHarness(t)
	h.keys("2")
	h.see("▾ acme")
	menu := tea.MouseWheelMsg{X: 3, Y: bodyTop + 1, Button: tea.MouseWheelDown}
	h.send(menu)
	if h.m.cur().id != vEnrollments || !h.m.inMenu() {
		t.Fatalf("wheel on the menu: view %s, menu %v", h.m.cur().id, h.m.inMenu())
	}
	menu.Button = tea.MouseWheelUp
	h.send(menu)
	if h.m.cur().id != vComponents || h.m.sel[vComponents] != rootKey("acme") {
		t.Fatalf("wheel up on the menu: view %s, selection %q", h.m.cur().id, h.m.sel[vComponents])
	}
	h.send(tea.MouseWheelMsg{X: sidebarWidth + 10, Y: bodyTop + 2, Button: tea.MouseWheelDown})
	if h.m.inMenu() || h.m.cur().id != vComponents || h.m.sel[vComponents] != "demo-mft" {
		t.Fatalf("wheel on the content: menu %v, view %s, selection %q", h.m.inMenu(), h.m.cur().id, h.m.sel[vComponents])
	}
	h.keys("tab")
	h.send(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if h.m.cur().id != vEnrollments || h.m.sel[vComponents] != "demo-mft" {
		t.Fatalf("wheel outside the panes, menu focused: view %s, selection %q", h.m.cur().id, h.m.sel[vComponents])
	}
}

func TestOverviewJumpShowsTheObjectThroughAFilter(t *testing.T) {
	h := newHarness(t)
	h.keys("3", "/", "t", "r", "a", "e", "enter", "space")
	h.see("filter: trae")
	h.dontSee("└ mft-rke2-a")
	h.keys("1")
	h.m.sel[vOverview] = "Enrollment/mft-rke2-a"
	h.keys("enter")
	h.see("└ mft-rke2-a")
	h.dontSee("filter: trae")
	if h.m.cur().id != vEnrollments || h.m.sel[vEnrollments] != "mft-rke2-a" {
		t.Fatalf("⏎ from the overview: view %s, selection %q", h.m.cur().id, h.m.sel[vEnrollments])
	}
}

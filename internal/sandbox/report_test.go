package sandbox

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"rondo/api/v1alpha1"
	"rondo/internal/values"
)

// verdictWorld is a Component on a Target, and an Enrollment that applied what it wants.
func verdictWorld(t *testing.T) (*world, *v1alpha1.Enrollment) {
	t.Helper()
	comp := &v1alpha1.Component{ObjectMeta: metav1.ObjectMeta{Name: "web"}, Spec: v1alpha1.ComponentSpec{
		Chart:     &v1alpha1.ChartSource{OCI: "oci://r/web", Version: "1.0.0"},
		Namespace: "web",
		Values:    &apiextensionsv1.JSON{Raw: []byte(`{"image":"${target.values.imageRegistry}/web"}`)},
	}}
	target := &v1alpha1.Target{ObjectMeta: metav1.ObjectMeta{Name: "management"},
		Spec: v1alpha1.TargetSpec{Values: map[string]string{"imageRegistry": "reg"}}}
	enr := &v1alpha1.Enrollment{ObjectMeta: metav1.ObjectMeta{Name: "web"},
		Spec: v1alpha1.EnrollmentSpec{Component: "web", Target: "management"}}
	enr.Status.ResolvedTarget = "management"
	resolved, err := values.ForEnrollment(comp, enr, target)
	if err != nil {
		t.Fatal(err)
	}
	enr.Status.AppliedValuesHash, _ = values.Hash(resolved)
	enr.Status.AppliedChartVersion = "1.0.0"
	enr.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: "True"}}
	return &world{components: map[string]*v1alpha1.Component{"web": comp},
		targets: map[string]*v1alpha1.Target{"management": target}}, enr
}

func TestJudgeTrustsReadyOnlyOnceConverged(t *testing.T) {
	since := time.Now()
	w, enr := verdictWorld(t)
	if v := judge(enr, w, since, 2); v != ready {
		t.Fatalf("converged and Ready Enrollment judged %v", v)
	}

	stale := enr.DeepCopy()
	w.components["web"].Spec.Chart.Version = "1.1.0"
	if v := judge(stale, w, since, 2); v != pending {
		t.Errorf("Ready from an earlier version judged %v: a false green after a redeploy", v)
	}
	w.components["web"].Spec.Chart.Version = "1.0.0"

	running := enr.DeepCopy()
	running.Status.ActiveJobRef = &v1alpha1.LocalObjectReference{Name: "job"}
	if v := judge(running, w, since, 2); v != pending {
		t.Errorf("running Job judged %v", v)
	}
}

func TestJudgeCountsOnlyThisDeploysFailures(t *testing.T) {
	since := time.Now()
	w, enr := verdictWorld(t)
	enr.Status.Conditions = nil

	old := enr.DeepCopy()
	old.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionStalled, Status: "True",
		Reason: v1alpha1.ReasonFailed, LastTransitionTime: metav1.NewTime(since.Add(-time.Hour))}}
	if v := judge(old, w, since, 2); v == failed {
		t.Error("blocker from an earlier deploy counted")
	}

	stalled := enr.DeepCopy()
	stalled.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionStalled, Status: "True",
		Reason: v1alpha1.ReasonFailed, LastTransitionTime: metav1.NewTime(since.Add(time.Minute))}}
	if v := judge(stalled, w, since, 2); v != failed {
		t.Errorf("blocker from this deploy judged %v", v)
	}

	failing := enr.DeepCopy()
	done := metav1.NewTime(since.Add(time.Minute))
	failing.Status.LastAttempt = &v1alpha1.AttemptInfo{JobName: "j", Result: "Failed", FinishedAt: &done}
	failing.Status.ConsecutiveFailures = 2
	if v := judge(failing, w, since, 2); v != failed {
		t.Errorf("two Job failures judged %v", v)
	}
	failing.Status.ConsecutiveFailures = 1
	if v := judge(failing, w, since, 2); v == failed {
		t.Error("a tolerated failure counted")
	}
}

func TestContainerImagesReadsCronJobsToo(t *testing.T) {
	var cronjob map[string]any
	if err := json.Unmarshal([]byte(`{"jobTemplate":{"spec":{"template":{"spec":{
		"initContainers":[{"image":"reg/init:1"}],
		"containers":[{"image":"reg/app:2"},{"name":"no-image"}]}}}}}`), &cronjob); err != nil {
		t.Fatal(err)
	}
	got := containerImages(cronjob)
	slices.Sort(got)
	if !slices.Equal(got, []string{"reg/app:2", "reg/init:1"}) {
		t.Errorf("images = %v", got)
	}
}

func TestPodWaitingAndCrashing(t *testing.T) {
	var p pod
	if err := json.Unmarshal([]byte(`{"status":{"phase":"Running","containerStatuses":[
		{"ready":false,"restartCount":4,"state":{"running":{}}}]}}`), &p); err != nil {
		t.Fatal(err)
	}
	if !p.crashing() || p.waiting() != "NotReady" {
		t.Errorf("pod restarted 4 times, between two back-offs: crashing=%v waiting=%q", p.crashing(), p.waiting())
	}
	var pull pod
	_ = json.Unmarshal([]byte(`{"status":{"phase":"Pending","containerStatuses":[
		{"state":{"waiting":{"reason":"ImagePullBackOff"}}}]}}`), &pull)
	if pull.waiting() != "ImagePullBackOff" || pull.crashing() {
		t.Errorf("pull: waiting=%q crashing=%v", pull.waiting(), pull.crashing())
	}
	var done pod
	_ = json.Unmarshal([]byte(`{"status":{"phase":"Succeeded"}}`), &done)
	if done.waiting() != "" {
		t.Errorf("completed pod: waiting=%q", done.waiting())
	}
}

func TestSiteConfigServesImagesFromTheStandIn(t *testing.T) {
	s, p := planned(t)
	st, _ := loadSite(s)
	cfg := siteConfig(st, []*plan{p})
	if cfg.Mirror().OCIRegistry != registryHost || cfg.Targets.Management.IngressLoadBalancerIP != "172.28.0.100" {
		t.Errorf("site = %+v", cfg.Targets.Management)
	}
	if len(cfg.Targets.Downstreams) != 1 || cfg.RegistryHostFor(&cfg.Targets.Downstreams[0].RKE2) != registryHost ||
		cfg.Targets.Downstreams[0].Name != "obs" || cfg.Targets.Downstreams[0].RKE2.IngressLoadBalancerIP != "172.28.0.101" {
		t.Errorf("downstreams = %+v", cfg.Targets.Downstreams)
	}
	if len(cfg.Targets.VSphere) != 1 || cfg.Targets.Management.VSphereTarget != "dc1" ||
		cfg.Targets.VSphere[0].ComponentValues["vmIPs.vm.app"] != `["172.28.0.10"]` {
		t.Errorf("vSphere = %+v, management placed on %q", cfg.Targets.VSphere, cfg.Targets.Management.VSphereTarget)
	}
	if bare := siteConfig(&site{Clusters: st.Clusters[:1]}, nil); len(bare.Targets.VSphere) != 1 ||
		bare.Targets.VSphere[0].Name != vsphereStub {
		t.Errorf("no vSphere Target declared: %+v, want a stub for the management placement", bare.Targets.VSphere)
	}
	if env := cfg.Env(); env["REGISTRY_HOST"] != registryHost || env["INGRESS_BASE_DOMAIN"] != baseDomain {
		t.Errorf("platform tokens: %v %v", env["REGISTRY_HOST"], env["INGRESS_BASE_DOMAIN"])
	}
	sb := &sandbox{site: st}
	if sb.hasBase(st.Clusters[0]) || !sb.hasBase(st.Clusters[1]) {
		t.Error("base layer: never on a management-only cluster, always on a downstream")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("a  b\nc", 10); got != "a b c" {
		t.Errorf("%q", got)
	}
	if got := truncate("abcdefghij", 5); got != "abcd…" {
		t.Errorf("%q", got)
	}
}

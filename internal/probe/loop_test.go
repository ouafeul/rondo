package probe

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"rondo/api/v1alpha1"
)

func probeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func newFake(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(probeScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Enrollment{}).
		Build()
}

type fakeReader struct {
	client.Reader
	staleErr   error
	stopped    int
	namespaces map[string]bool
}

func (f *fakeReader) stale() error { return f.staleErr }
func (f *fakeReader) watches() int { return 1 }
func (f *fakeReader) stop()        { f.stopped++ }

func (f *fakeReader) covers(namespaces map[string]bool) bool {
	for ns := range namespaces {
		if !f.namespaces[ns] {
			return false
		}
	}
	return true
}

func newProber(c client.Client, r *fakeReader) (*Prober, *[]*fakeReader) {
	var built []*fakeReader
	p := &Prober{Client: c, Namespace: "rondo-system"}
	p.newReader = func(_ context.Context, _ client.Client, _ *v1alpha1.Target,
		ns map[string]bool) (targetReader, error) {
		f := r
		if f == nil {
			f = &fakeReader{Reader: c}
		}
		f.namespaces = ns
		built = append(built, f)
		return f, nil
	}
	return p, &built
}

func release(name string, mutate func(*v1alpha1.Enrollment)) *v1alpha1.Enrollment {
	r := &v1alpha1.Enrollment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "rondo-system"},
		Spec: v1alpha1.EnrollmentSpec{
			Component: "victoria-logs",
			Target:    "acme",
		},
		Status: v1alpha1.EnrollmentStatus{
			ResolvedTarget: "acme",
			Inventory: []v1alpha1.InventoryEntry{
				{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "logging", Name: "vl"},
			},
		},
	}
	if mutate != nil {
		mutate(r)
	}
	return r
}

func inClusterTarget(mutate func(*v1alpha1.Target)) *v1alpha1.Target {
	tg := &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "acme", Namespace: "rondo-system"},
		Spec:       v1alpha1.TargetSpec{Kubernetes: &v1alpha1.KubernetesCluster{InCluster: true}},
	}
	if mutate != nil {
		mutate(tg)
	}
	return tg
}

func healthyDeployment() *appsv1.Deployment {
	two := int32(2)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "vl", Namespace: "logging"},
		Spec:       appsv1.DeploymentSpec{Replicas: &two},
		Status:     appsv1.DeploymentStatus{Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2},
	}
}

func TestAssessInventoryAbsentObjectIsDegraded(t *testing.T) {
	c := newFake(t)
	health, msg := AssessInventory(context.Background(), c, []v1alpha1.InventoryEntry{
		{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "logging", Name: "absent"},
	})
	if health != v1alpha1.HealthDegraded {
		t.Errorf("health = %q, want Degraded", health)
	}
	if !strings.Contains(msg, "absent") {
		t.Errorf("message = %q, must name the object or say it is missing", msg)
	}
}

func TestAssessInventoryHealthy(t *testing.T) {
	c := newFake(t, healthyDeployment())
	health, _ := AssessInventory(context.Background(), c, []v1alpha1.InventoryEntry{
		{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "logging", Name: "vl"},
	})
	if health != v1alpha1.HealthHealthy {
		t.Errorf("health = %q", health)
	}
}

func TestAssessInventoryEmptyIsNotHealthy(t *testing.T) {
	c := newFake(t)
	health, _ := AssessInventory(context.Background(), c, nil)
	if health == v1alpha1.HealthHealthy {
		t.Error("empty inventory judged Healthy")
	}
}

func TestAssessInventoryMixedTakesWorst(t *testing.T) {
	c := newFake(t, healthyDeployment(),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "conf", Namespace: "logging"}})
	health, _ := AssessInventory(context.Background(), c, []v1alpha1.InventoryEntry{
		{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "logging", Name: "vl"},
		{Version: "v1", Kind: "ConfigMap", Namespace: "logging", Name: "conf"},
		{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "logging", Name: "gone"},
	})
	if health != v1alpha1.HealthDegraded {
		t.Errorf("health = %q, want Degraded (one object missing)", health)
	}
}

func TestProberPublishesHealthOnly(t *testing.T) {
	rel := release("victoria-logs-acme", func(r *v1alpha1.Enrollment) {
		r.Status.Health = v1alpha1.HealthProgressing
		r.Status.Conditions = []metav1.Condition{{
			Type: "Ready", Status: metav1.ConditionFalse, Reason: "Progressing",
			LastTransitionTime: metav1.Now(),
		}}
	})
	c := newFake(t, rel, inClusterTarget(nil), healthyDeployment())
	p, _ := newProber(c, nil)

	if err := p.once(context.Background()); err != nil {
		t.Fatalf("once: %v", err)
	}
	var got v1alpha1.Enrollment
	if err := c.Get(context.Background(),
		types.NamespacedName{Namespace: "rondo-system", Name: "victoria-logs-acme"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Health != v1alpha1.HealthHealthy {
		t.Errorf("health = %q, want Healthy after reassessment", got.Status.Health)
	}
	if len(got.Status.Inventory) != 1 {
		t.Errorf("inventory modified: %+v", got.Status.Inventory)
	}
	if len(got.Status.Conditions) != 1 {
		t.Errorf("conditions modified: %+v", got.Status.Conditions)
	}
}

func TestProberSkipsReleaseWithoutInventory(t *testing.T) {
	rel := release("empty", func(r *v1alpha1.Enrollment) { r.Status.Inventory = nil })
	c := newFake(t, rel, inClusterTarget(nil))
	p, _ := newProber(c, nil)
	if err := p.once(context.Background()); err != nil {
		t.Fatalf("once: %v", err)
	}
	var got v1alpha1.Enrollment
	if err := c.Get(context.Background(),
		types.NamespacedName{Namespace: "rondo-system", Name: "empty"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Health != "" {
		t.Errorf("health written on a Release without inventory: %q", got.Status.Health)
	}
}

func TestProberDanglingTargetDoesNotStopThePass(t *testing.T) {
	orphan := release("orphan", func(r *v1alpha1.Enrollment) {
		r.Status.ResolvedTarget = "gone"
	})
	healthy := release("healthy", nil)
	c := newFake(t, orphan, healthy, inClusterTarget(nil), healthyDeployment())
	p, _ := newProber(c, nil)

	if err := p.once(context.Background()); err != nil {
		t.Fatalf("once: %v", err)
	}
	var got v1alpha1.Enrollment
	if err := c.Get(context.Background(),
		types.NamespacedName{Namespace: "rondo-system", Name: "healthy"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Health != v1alpha1.HealthHealthy {
		t.Errorf("the healthy Release was not probed: %q", got.Status.Health)
	}
}

func TestProberDoesNotRewriteUnchangedHealth(t *testing.T) {
	c := newFake(t, inClusterTarget(nil), healthyDeployment())
	p, _ := newProber(c, nil)

	_, msg := AssessInventory(context.Background(), c, release("x", nil).Status.Inventory)
	rel := release("stable", func(r *v1alpha1.Enrollment) {
		r.Status.Health = v1alpha1.HealthHealthy
		r.Status.HealthMessage = msg
	})
	if err := c.Create(context.Background(), rel); err != nil {
		t.Fatal(err)
	}
	if err := c.Status().Update(context.Background(), rel); err != nil {
		t.Fatal(err)
	}

	rv := resourceVersion(t, c, "stable")
	if err := p.once(context.Background()); err != nil {
		t.Fatalf("once: %v", err)
	}
	if after := resourceVersion(t, c, "stable"); after != rv {
		t.Errorf("identical health rewritten (rv %s -> %s)", rv, after)
	}
}

func resourceVersion(t *testing.T, c client.Client, name string) string {
	t.Helper()
	var rel v1alpha1.Enrollment
	if err := c.Get(context.Background(),
		types.NamespacedName{Namespace: "rondo-system", Name: name}, &rel); err != nil {
		t.Fatal(err)
	}
	return rel.ResourceVersion
}

func TestProberRunStopsOnCancel(t *testing.T) {
	c := newFake(t, release("r", nil), inClusterTarget(nil), healthyDeployment())
	p, _ := newProber(c, nil)
	p.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run does not return on cancellation")
	}
	var got v1alpha1.Enrollment
	if err := c.Get(context.Background(),
		types.NamespacedName{Namespace: "rondo-system", Name: "r"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Health == "" {
		t.Error("no pass done before cancellation")
	}
}

func TestTruncateBoundsMessage(t *testing.T) {
	if got := truncate("short", 2048); got != "short" {
		t.Errorf("truncate = %q", got)
	}
	got := truncate(strings.Repeat("x", 5000), 2048)
	if len(got) != 2048 || !strings.HasSuffix(got, "...") {
		t.Errorf("length = %d, suffix = %q", len(got), got[len(got)-3:])
	}
}

func TestJitterIsBoundedAndSymmetric(t *testing.T) {
	const interval = 30 * time.Second
	p := &Prober{Interval: interval}

	low := interval - time.Duration(float64(interval)*jitterFraction)
	high := interval + time.Duration(float64(interval)*jitterFraction)

	var min, max time.Duration = high, low
	for i := 0; i < 2000; i++ {
		d := p.nextDelay(interval)
		if d < low || d > high {
			t.Fatalf("wait = %v outside [%v, %v]: `RONDO_PROBE_INTERVAL` says "+
				"nothing about the real cadence", d, low, high)
		}
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}
	if min >= interval {
		t.Errorf("no wait SHORTER than %v in 2000 draws (min %v): the "+
			"jitter is not symmetric and the cadence drifts", interval, min)
	}
	if max <= interval {
		t.Errorf("no wait LONGER than %v in 2000 draws (max %v)",
			interval, max)
	}
}

func TestVeryShortIntervalDoesNotProduceAHotLoop(t *testing.T) {
	p := &Prober{}
	for i := 0; i < 500; i++ {
		if d := p.nextDelay(10 * time.Millisecond); d < time.Second {
			t.Fatalf("wait = %v: a very short interval plus a negative jitter "+
				"would produce a hot loop on the Rancher proxy", d)
		}
	}
}

func TestProberReusesTheCacheAcrossPasses(t *testing.T) {
	c := newFake(t, release("r", nil), inClusterTarget(nil), healthyDeployment())
	p, built := newProber(c, nil)

	for i := 0; i < 3; i++ {
		if err := p.once(context.Background()); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	if len(*built) != 1 {
		t.Errorf("%d caches built for 3 passes: the cache does not outlive the pass, "+
			"each one rebuilds an informer per GVK", len(*built))
	}
}

func TestProberDisconnectedCacheIsUnknownWithItsCause(t *testing.T) {
	rel := release("cut-off", func(r *v1alpha1.Enrollment) {
		r.Status.Health = v1alpha1.HealthHealthy
	})
	c := newFake(t, rel, inClusterTarget(nil), healthyDeployment())
	p, _ := newProber(c, &fakeReader{Reader: c, staleErr: errors.New("watch closed by the proxy")})

	if err := p.once(context.Background()); err != nil {
		t.Fatalf("once: %v", err)
	}
	var got v1alpha1.Enrollment
	if err := c.Get(context.Background(),
		types.NamespacedName{Namespace: "rondo-system", Name: "cut-off"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Health != v1alpha1.HealthUnknown {
		t.Errorf("health = %q: the cache is disconnected, its Healthy is stale", got.Status.Health)
	}
	if !strings.Contains(got.Status.HealthMessage, "watch closed by the proxy") {
		t.Errorf("message = %q, must carry the cause of the disconnection", got.Status.HealthMessage)
	}
}

func TestProberStopsTheCacheOfATargetWithoutEnrollment(t *testing.T) {
	rel := release("r", nil)
	tg := inClusterTarget(nil)
	c := newFake(t, rel, tg, healthyDeployment())
	r := &fakeReader{Reader: c}
	p, _ := newProber(c, r)

	if err := p.once(context.Background()); err != nil {
		t.Fatalf("once: %v", err)
	}
	if r.stopped != 0 {
		t.Fatalf("cache stopped while the target is being probed")
	}

	if err := c.Delete(context.Background(), rel); err != nil {
		t.Fatal(err)
	}
	if err := p.once(context.Background()); err != nil {
		t.Fatalf("once: %v", err)
	}
	if r.stopped != 1 {
		t.Errorf("cache of an unprobed target stopped %d times: its watches leak", r.stopped)
	}
	if len(p.readers) != 0 {
		t.Errorf("%d caches still registered", len(p.readers))
	}
}

func TestProberStopsCachesOnCancellation(t *testing.T) {
	c := newFake(t, release("r", nil), inClusterTarget(nil), healthyDeployment())
	r := &fakeReader{Reader: c}
	p, _ := newProber(c, r)
	p.Interval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run does not return on cancellation")
	}
	if r.stopped != 1 {
		t.Errorf("cache stopped %d times after cancellation: the informers outlive the service", r.stopped)
	}
}

func TestPassCountsReadsAndWatches(t *testing.T) {
	two := release("two", func(r *v1alpha1.Enrollment) {
		r.Status.Inventory = append(r.Status.Inventory,
			v1alpha1.InventoryEntry{Version: "v1", Kind: "ConfigMap", Namespace: "logging", Name: "conf"})
	})
	c := newFake(t, two, inClusterTarget(nil), healthyDeployment(),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "conf", Namespace: "logging"}})
	p, _ := newProber(c, nil)

	st, err := p.pass(context.Background())
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if st.enrollments != 1 || st.targets != 1 {
		t.Errorf("releases=%d targets=%d", st.enrollments, st.targets)
	}
	if st.reads != 2 {
		t.Errorf("reads = %d, want 2 (the inventory size)", st.reads)
	}
	if st.watches != 1 {
		t.Errorf("watches = %d, want 1", st.watches)
	}
	if st.dur <= 0 {
		t.Error("duration not measured: the pass cannot be compared on site")
	}
}

func TestCacheIsBoundToTheInventoryNamespaces(t *testing.T) {
	rel := release("bounded", func(r *v1alpha1.Enrollment) {
		r.Status.Inventory = append(r.Status.Inventory,
			v1alpha1.InventoryEntry{Version: "v1", Kind: "Secret", Namespace: "monitoring", Name: "ca"},
			v1alpha1.InventoryEntry{Group: "apiextensions.k8s.io", Version: "v1",
				Kind: "CustomResourceDefinition", Name: "certificates.cert-manager.io"})
	})
	c := newFake(t, rel, inClusterTarget(nil), healthyDeployment())
	p, built := newProber(c, nil)

	if err := p.once(context.Background()); err != nil {
		t.Fatalf("once: %v", err)
	}
	if len(*built) != 1 {
		t.Fatalf("%d caches built", len(*built))
	}
	got := (*built)[0].namespaces
	want := map[string]bool{"logging": true, "monitoring": true}
	if len(got) != len(want) {
		t.Fatalf("namespaces = %v, want %v", got, want)
	}
	for ns := range want {
		if !got[ns] {
			t.Errorf("namespace %q missing from the cache: its objects would be unreadable", ns)
		}
	}
	if got[""] {
		t.Error("empty namespace in scope: a cluster-scoped entry is not one")
	}
}

func TestNewNamespaceRebuildsTheCache(t *testing.T) {
	c := newFake(t, release("first", nil), inClusterTarget(nil), healthyDeployment())
	p, built := newProber(c, nil)

	if err := p.once(context.Background()); err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	if len(*built) != 1 {
		t.Fatalf("%d caches after the first pass", len(*built))
	}

	elsewhere := release("elsewhere", func(r *v1alpha1.Enrollment) {
		r.Status.Inventory = []v1alpha1.InventoryEntry{
			{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "monitoring", Name: "vm"},
		}
	})
	if err := c.Create(context.Background(), elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := p.once(context.Background()); err != nil {
		t.Fatalf("pass 2: %v", err)
	}

	if len(*built) != 2 {
		t.Fatalf("%d caches built: the cache bound to `logging` served a read "+
			"in `monitoring`", len(*built))
	}
	if (*built)[0].stopped != 1 {
		t.Errorf("first cache stopped %d times: its watches leak", (*built)[0].stopped)
	}
	if !(*built)[1].namespaces["monitoring"] || !(*built)[1].namespaces["logging"] {
		t.Errorf("cache rebuilt on %v, want logging + monitoring", (*built)[1].namespaces)
	}
}

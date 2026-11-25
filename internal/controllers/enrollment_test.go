package controllers

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"rondo/api/v1alpha1"
	"rondo/internal/jobs"
)

func setupRelease(t *testing.T, h *testHarness, mutate func(*v1alpha1.Enrollment)) string {
	t.Helper()
	h.createTarget(t, "acme", []string{"monitoring"}, nil)
	h.createComponent(t, "kps", "83.3.0", `{"grafana":{"enabled":true}}`)
	h.createEnrollment(t, "kps-monitoring", "kps", "monitoring", "", mutate)
	return "kps-monitoring"
}

func TestEnrollmentLaunchesExactlyOneJob(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := setupRelease(t, h, nil)

	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	js := h.jobsFor(t, name)
	if len(js) != 1 {
		t.Fatalf("%d Jobs created, want 1", len(js))
	}
	rel := h.getEnrollment(t, name)
	if rel.Status.ActiveJobRef == nil || rel.Status.ActiveJobRef.Name != js[0].Name {
		t.Fatalf("activeJobRef = %v, want %s", rel.Status.ActiveJobRef, js[0].Name)
	}
	if rel.Status.AttemptCount != 1 {
		t.Errorf("attemptCount = %d, want 1", rel.Status.AttemptCount)
	}
	requireCondition(t, rel.Status.Conditions, v1alpha1.ConditionReconciling, "True")

	for i := 0; i < 3; i++ {
		if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
			t.Fatalf("Reconcile while running %d: %v", i, err)
		}
	}
	if got := len(h.jobsFor(t, name)); got != 1 {
		t.Fatalf("%d Jobs while running, want 1: activeJobRef did not guard", got)
	}
	rel = h.getEnrollment(t, name)
	msg := requireCondition(t, rel.Status.Conditions, v1alpha1.ConditionReconciling, "True")
	if !strings.Contains(msg, "kubectl logs") {
		t.Errorf("message = %q, must point to the Job logs", msg)
	}
}

func TestEnrollmentConcurrentReconcileYieldsOneJob(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := setupRelease(t, h, nil)

	other := &EnrollmentReconciler{
		Client: h.client, Scheme: h.scheme, Jobs: h.enrollment.Jobs,
		Lock: &jobs.Lock{Client: h.client, Namespace: h.ns, Holder: "controller-other", Duration: time.Minute},
	}

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		r := h.enrollment
		if i%2 == 1 {
			r = other
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = r.Reconcile(ctx, h.req(name))
		}()
	}
	close(start)
	wg.Wait()

	js := h.jobsFor(t, name)
	if len(js) != 1 {
		names := make([]string, 0, len(js))
		for _, j := range js {
			names = append(names, j.Name)
		}
		t.Fatalf("%d Jobs after %d concurrent reconciles (%v), want 1", len(js), n, names)
	}
}

func TestEnrollmentRetryAfterFailureUsesFreshJob(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := setupRelease(t, h, nil)

	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	first := h.jobsFor(t, name)[0].Name
	h.finishJob(t, first, false)

	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("Reconcile (reap): %v", err)
	}
	rel := h.getEnrollment(t, name)
	if rel.Status.ActiveJobRef != nil {
		t.Errorf("activeJobRef = %v, want nil after reap", rel.Status.ActiveJobRef)
	}
	requireCondition(t, rel.Status.Conditions, v1alpha1.ConditionApplied, "False")
	if rel.Status.ConsecutiveFailures != 1 {
		t.Errorf("consecutiveFailures = %d, want 1", rel.Status.ConsecutiveFailures)
	}
	if len(rel.Status.History) != 1 || rel.Status.History[0].Result != "Failed" {
		t.Errorf("history = %v, want one Failed attempt", rel.Status.History)
	}

	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("Reconcile (retry): %v", err)
	}
	js := h.jobsFor(t, name)
	if len(js) != 2 {
		t.Fatalf("%d Jobs after retry, want 2 (the first one keeps its logs)", len(js))
	}
	rel = h.getEnrollment(t, name)
	if rel.Status.ActiveJobRef == nil || rel.Status.ActiveJobRef.Name == first {
		t.Fatalf("the retry reused the failed Job (%v), no retry possible",
			rel.Status.ActiveJobRef)
	}
}

func TestEnrollmentSuccessRecordsAppliedState(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := setupRelease(t, h, nil)

	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatal(err)
	}
	job := h.jobsFor(t, name)[0].Name
	h.finishJob(t, job, true)
	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatal(err)
	}

	rel := h.getEnrollment(t, name)
	requireCondition(t, rel.Status.Conditions, v1alpha1.ConditionApplied, "True")
	if rel.Status.AppliedChartVersion != "83.3.0" {
		t.Errorf("appliedChartVersion = %q, want 83.3.0", rel.Status.AppliedChartVersion)
	}
	if rel.Status.AppliedValuesHash == "" {
		t.Error("appliedValuesHash empty: no configuration drift would be detectable")
	}
	if rel.Status.ConsecutiveFailures != 0 {
		t.Errorf("consecutiveFailures = %d, want 0 after success", rel.Status.ConsecutiveFailures)
	}
	msg := requireCondition(t, rel.Status.Conditions, v1alpha1.ConditionReady, "False")
	if !strings.Contains(msg, "health") {
		t.Errorf("message = %q, must tell applied from healthy", msg)
	}

	for i := 0; i < 3; i++ {
		if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(h.jobsFor(t, name)); got != 1 {
		t.Errorf("%d Jobs in steady state, want 1: useless apply loop", got)
	}

	rel = h.getEnrollment(t, name)
	rel.Status.Health = v1alpha1.HealthHealthy
	if err := h.client.Status().Update(ctx, rel); err != nil {
		t.Fatal(err)
	}
	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatal(err)
	}
	requireCondition(t, h.getEnrollment(t, name).Status.Conditions, v1alpha1.ConditionReady, "True")
}

func TestEnrollmentGatedByDependency(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.createTarget(t, "acme", []string{"monitoring"}, nil)
	h.createComponent(t, "cert-manager", "v1.16.2", "")
	h.createComponent(t, "kps", "83.3.0", "", "cert-manager")
	h.createEnrollment(t, "cm", "cert-manager", "monitoring", "", nil)
	h.createEnrollment(t, "kps-monitoring", "kps", "monitoring", "", nil)
	for _, e := range []string{"cm", "kps-monitoring"} {
		if _, err := h.enrollment.Reconcile(ctx, h.req(e)); err != nil {
			t.Fatal(err)
		}
	}
	dependent := "kps-monitoring"
	dependency := "cm"

	if _, err := h.enrollment.Reconcile(ctx, h.req(dependent)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(h.jobsFor(t, dependent)); got != 0 {
		t.Fatalf("%d Jobs despite an unmet dependency, want 0", got)
	}
	c := conditionOf(h.getEnrollment(t, dependent).Status.Conditions, v1alpha1.ConditionReady)
	if c == nil || c.Reason != v1alpha1.ReasonDependencyPending {
		t.Fatalf("condition Ready = %v, want reason=DependencyPending", c)
	}
	if !strings.Contains(c.Message, "cert-manager") {
		t.Errorf("message = %q, must name the awaited dependency", c.Message)
	}

	dep := h.getEnrollment(t, dependency)
	dep.Status.Health = v1alpha1.HealthHealthy
	dep.Status.Conditions = []metav1.Condition{{
		Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue,
		Reason: v1alpha1.ReasonSucceeded, LastTransitionTime: metav1.Now(),
	}}
	if err := h.client.Status().Update(ctx, dep); err != nil {
		t.Fatal(err)
	}
	if _, err := h.enrollment.Reconcile(ctx, h.req(dependent)); err != nil {
		t.Fatalf("Reconcile after the dependency is met: %v", err)
	}
	if got := len(h.jobsFor(t, dependent)); got != 1 {
		t.Fatalf("%d Jobs after the dependency is met, want 1", got)
	}
}

func TestEnrollmentAlwaysCarriesItsFinalizer(t *testing.T) {
	h := newHarness(t)
	name := setupRelease(t, h, nil)
	if _, err := h.enrollment.Reconcile(context.Background(), h.req(name)); err != nil {
		t.Fatal(err)
	}
	if !hasFinalizer(h.getEnrollment(t, name), v1alpha1.EnrollmentFinalizer) {
		t.Error("finalizer missing: deleting the Enrollment would leave the workload on its target")
	}
}

func TestEnrollmentResolvesExactlyOneTarget(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		targets map[string][]string
		want    string
	}{
		{"no target", map[string][]string{"acme": {"logs"}}, "no Target"},
		{"two targets", map[string][]string{"a": {"monitoring"}, "b": {"monitoring"}}, "several Targets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			for n, roles := range tc.targets {
				h.createTarget(t, n, roles, nil)
			}
			h.createComponent(t, "kps", "83.3.0", "")
			h.createEnrollment(t, "kps", "kps", "monitoring", "", nil)
			if _, err := h.enrollment.Reconcile(ctx, h.req("kps")); err != nil {
				t.Fatal(err)
			}
			msg := requireCondition(t, h.getEnrollment(t, "kps").Status.Conditions,
				v1alpha1.ConditionStalled, "True")
			if !strings.Contains(msg, tc.want) {
				t.Errorf("message = %q, want %q", msg, tc.want)
			}
			if n := len(h.jobsFor(t, "kps")); n != 0 {
				t.Errorf("%d Jobs started without a unique target", n)
			}
		})
	}
}

func TestEnrollmentRefusesToMoveToAnotherTarget(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := setupRelease(t, h, nil)
	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatal(err)
	}
	if got := h.getEnrollment(t, name).Status.ResolvedTarget; got != "acme" {
		t.Fatalf("resolvedTarget = %q, want acme", got)
	}

	var old v1alpha1.Target
	if err := h.client.Get(ctx, h.key("acme"), &old); err != nil {
		t.Fatal(err)
	}
	old.Spec.Roles = nil
	if err := h.client.Update(ctx, &old); err != nil {
		t.Fatal(err)
	}
	h.createTarget(t, "other", []string{"monitoring"}, nil)
	enr := h.getEnrollment(t, name)
	enr.Status.ActiveJobRef = nil
	if err := h.client.Status().Update(ctx, enr); err != nil {
		t.Fatal(err)
	}
	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatal(err)
	}
	msg := requireCondition(t, h.getEnrollment(t, name).Status.Conditions, v1alpha1.ConditionStalled, "True")
	if !strings.Contains(msg, "other") {
		t.Errorf("message = %q, must name the new target", msg)
	}
}

func TestEnrollmentStallsOnMissingComponent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := setupRelease(t, h, nil)

	var comp v1alpha1.Component
	if err := h.client.Get(ctx, types.NamespacedName{Namespace: h.ns, Name: "kps"}, &comp); err != nil {
		t.Fatal(err)
	}
	if err := h.client.Delete(ctx, &comp); err != nil {
		t.Fatal(err)
	}
	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	msg := requireCondition(t, h.getEnrollment(t, name).Status.Conditions, v1alpha1.ConditionStalled, "True")
	if !strings.Contains(msg, "kps") {
		t.Errorf("message = %q, must name the missing Component", msg)
	}
	if got := len(h.jobsFor(t, name)); got != 0 {
		t.Errorf("%d Jobs with a missing Component, want 0", got)
	}
}

func TestEnrollmentBackoffGrows(t *testing.T) {
	rel := &v1alpha1.Enrollment{}
	var last time.Duration
	for i := int32(1); i <= 6; i++ {
		rel.Status.ConsecutiveFailures = i
		d := failureBackoff(rel.Status.ConsecutiveFailures)
		if i > 1 && d < last {
			t.Errorf("backoff decreasing: %v after %v", d, last)
		}
		if d > 10*time.Minute {
			t.Errorf("backoff = %v, exceeds the 10 min ceiling", d)
		}
		last = d
	}
	if last <= requeueWhenBlocked {
		t.Errorf("final backoff = %v, must have grown beyond %v", last, requeueWhenBlocked)
	}
}

func TestEnrollmentSpecChangeRaisesTheCeiling(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := setupRelease(t, h, nil)

	rel := h.getEnrollment(t, name)
	rel.Status.ConsecutiveFailures = maxConsecutiveFailures
	rel.Status.ObservedGeneration = rel.Generation
	if err := h.client.Status().Update(ctx, rel); err != nil {
		t.Fatalf("status: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
			t.Fatalf("Reconcile at the ceiling: %v", err)
		}
	}
	if n := len(h.jobsFor(t, name)); n != 0 {
		t.Fatalf("%d Job(s) at the ceiling, want 0", n)
	}
	requireCondition(t, h.getEnrollment(t, name).Status.Conditions, v1alpha1.ConditionStalled, "True")

	fresh := h.getEnrollment(t, name)
	fresh.Spec.Values = &apiextensionsv1.JSON{Raw: []byte(`{"grafana":{"enabled":false}}`)}
	if err := h.client.Update(ctx, fresh); err != nil {
		t.Fatalf("fixing the spec: %v", err)
	}
	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("Reconcile after the fix: %v", err)
	}

	after := h.getEnrollment(t, name)
	if after.Status.ConsecutiveFailures != 0 {
		t.Errorf("consecutiveFailures = %d after fixing the spec: the ceiling is a "+
			"dead end, and the halt message promises a remedy that does nothing",
			after.Status.ConsecutiveFailures)
	}
	if conditionOf(after.Status.Conditions, v1alpha1.ConditionStalled) != nil {
		t.Error("the Stalled condition survives the fix: the Enrollment would read as stuck " +
			"while it resumes")
	}
	if n := len(h.jobsFor(t, name)); n != 1 {
		t.Errorf("%d Job(s) after the fix, want 1: it did not resume", n)
	}
}

package controllers

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"

	"rondo/api/v1alpha1"
)

func burstEnrollment(rec *EnrollmentReconciler, req ctrl.Request, n int) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = rec.Reconcile(context.Background(), req)
		}()
	}
	close(start)
	wg.Wait()
}

func TestEnrollmentLaunchesOnlyOneJobUnderConcurrentReconciles(t *testing.T) {
	h := newHarness(t)
	name := setupRelease(t, h, nil)

	hidden := &atomic.Bool{}
	rec := &EnrollmentReconciler{
		Client: &laggingJobCache{Client: h.client, hidden: hidden},
		Reader: h.client,
		Scheme: h.scheme,
		Jobs:   h.enrollment.Jobs,
		Lock:   h.enrollment.Lock,
	}

	burstEnrollment(rec, h.req(name), 8)
	list := h.jobsFor(t, name)
	if len(list) != 1 {
		t.Fatalf("%d Jobs after 8 concurrent reconciles, want 1: %v",
			len(list), namesOf(list))
	}
	running := list[0].Name

	hidden.Store(true)

	deleteLease(t, h, EnrollmentGVK.Kind, name)

	for i := 0; i < 4; i++ {
		burstEnrollment(rec, h.req(name), 8)
	}

	list = h.jobsFor(t, name)
	if len(list) != 1 {
		t.Fatalf("%d Jobs while %q is still RUNNING: %v.\n"+
			"A Job missing from a cache is not a vanished Job, and a 2-minute "+
			"lease does not cover the Job it protects. Two concurrent `helm upgrade` "+
			"runs leave the release in `pending-upgrade`, which only manual "+
			"intervention clears on an air-gapped downstream cluster",
			len(list), running, namesOf(list))
	}
	if list[0].Name != running {
		t.Fatalf("live Job %q was replaced by %q", running, list[0].Name)
	}

	rel := h.getEnrollment(t, name)
	if rel.Status.ActiveJobRef == nil || rel.Status.ActiveJobRef.Name != running {
		t.Errorf("activeJobRef = %v, want %q: the reservation was cleared on a "+
			"Job that is still running", rel.Status.ActiveJobRef, running)
	}
	if rel.Status.AttemptCount != 1 {
		t.Errorf("attemptCount = %d after a single Job: each increment is a "+
			"new fingerprint, hence a new Job NAME, hence one more `helm upgrade`",
			rel.Status.AttemptCount)
	}
}

func TestEnrollmentKeepsReservationForJobMissingFromCache(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := setupRelease(t, h, nil)

	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("launching the apply: %v", err)
	}
	before := h.getEnrollment(t, name)
	if before.Status.ActiveJobRef == nil {
		t.Fatal("no reservation after launch: the test would measure nothing")
	}
	running := before.Status.ActiveJobRef.Name

	hidden := &atomic.Bool{}
	hidden.Store(true)
	rec := &EnrollmentReconciler{
		Client: &laggingJobCache{Client: h.client, hidden: hidden},
		Reader: h.client,
		Scheme: h.scheme,
		Jobs:   h.enrollment.Jobs,
		Lock:   h.enrollment.Lock,
	}
	if _, err := rec.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("reaping with a lagging cache: %v", err)
	}

	rel := h.getEnrollment(t, name)
	if rel.Status.ActiveJobRef == nil || rel.Status.ActiveJobRef.Name != running {
		t.Fatalf("activeJobRef = %v while %q is running: the controller concluded the Job "+
			"vanished on the word of a lagging cache alone, and the uniqueness guard "+
			"was destroyed exactly when it is needed", rel.Status.ActiveJobRef, running)
	}
	if rel.Status.ConsecutiveFailures != 0 {
		t.Errorf("consecutiveFailures = %d: a running Job was counted as a "+
			"failure, so the retry ceiling is used up by a stale read",
			rel.Status.ConsecutiveFailures)
	}
}

func TestEnrollmentRelaunchesNothingWhenReservationLost(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := setupRelease(t, h, nil)

	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("launching the apply: %v", err)
	}
	list := h.jobsFor(t, name)
	if len(list) != 1 {
		t.Fatalf("%d Jobs at launch, want 1", len(list))
	}
	running := list[0].Name

	rel := h.getEnrollment(t, name)
	rel.Status.ActiveJobRef = nil
	if err := h.client.Status().Update(ctx, rel); err != nil {
		t.Fatalf("dropping the reservation: %v", err)
	}
	deleteLease(t, h, EnrollmentGVK.Kind, name)

	for i := 0; i < 3; i++ {
		if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}

	if n := len(h.jobsFor(t, name)); n != 1 {
		t.Fatalf("%d Jobs while %q is still RUNNING and only the RESERVATION was "+
			"lost: the controller does not check the world before relaunching, "+
			"it trusts only its own memory", n, running)
	}
	got := h.getEnrollment(t, name)
	if got.Status.ActiveJobRef == nil || got.Status.ActiveJobRef.Name != running {
		t.Errorf("activeJobRef = %v, want %q: the live Job must be RE-ADOPTED, "+
			"otherwise its outcome is never reaped and the Enrollment stays stuck "+
			"until the TTL", got.Status.ActiveJobRef, running)
	}
	if c := conditionOf(got.Status.Conditions, v1alpha1.ConditionReconciling); c == nil ||
		string(c.Status) != "True" {
		t.Errorf("condition Reconciling = %v: an in-flight Job must SHOW on the object", c)
	}
}

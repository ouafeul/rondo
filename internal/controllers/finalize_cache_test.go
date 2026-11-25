package controllers

import (
	"context"
	"sync/atomic"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"rondo/api/v1alpha1"
	"rondo/internal/jobs"
)

func TestEnrollmentReleasesFinalizerOnlyOnConfirmedDisappearance(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	name := setupRelease(t, h, nil)
	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("launching the apply: %v", err)
	}
	js := h.jobsFor(t, name)
	if len(js) != 1 {
		t.Fatalf("%d Jobs at launch, want 1: the test would measure nothing", len(js))
	}
	running := js[0].Name

	rel := h.getEnrollment(t, name)
	if !hasFinalizer(rel, v1alpha1.EnrollmentFinalizer) {
		t.Fatalf("no finalizer on the Enrollment: the test would measure nothing")
	}
	if err := h.client.Delete(ctx, rel); err != nil {
		t.Fatalf("deleting the Enrollment: %v", err)
	}

	hidden := &atomic.Bool{}
	hidden.Store(true)
	rec := &EnrollmentReconciler{
		Client: &laggingJobCache{Client: h.client, hidden: hidden},
		Reader: h.client,
		Scheme: h.scheme,
		Jobs:   h.enrollment.Jobs,
		Lock:   h.enrollment.Lock,
	}
	for i := 0; i < 3; i++ {
		_, _ = rec.Reconcile(ctx, h.req(name))
	}

	live, err := jobs.LiveFor(ctx, h.client, h.ns, EnrollmentGVK.Kind, name)
	if err != nil {
		t.Fatalf("Job inventory: %v", err)
	}
	if live == nil {
		t.Fatalf("no live Job: the test would measure nothing (want %q)", running)
	}

	var got v1alpha1.Enrollment
	err = h.client.Get(ctx, h.key(name), &got)
	if apierrors.IsNotFound(err) {
		t.Fatalf("the Enrollment VANISHED while %q is still running: the finalizer was "+
			"lifted on the word of a lagging cache alone. On a machine component the "+
			"in-flight Job is a `vm-destroy`: VMs being destroyed and no object "+
			"left to follow the outcome", running)
	}
	if err != nil {
		t.Fatalf("reading the Enrollment: %v", err)
	}
	if !hasFinalizer(&got, v1alpha1.EnrollmentFinalizer) {
		t.Fatalf("finalizers = %v while %q is running: the object is no longer held",
			got.Finalizers, running)
	}
	if n := len(h.jobsFor(t, name)); n != 1 {
		t.Errorf("%d Jobs: a `helm uninstall` was started during a `helm upgrade`, "+
			"leaving the release in `pending-upgrade`, which only manual intervention clears", n)
	}
}

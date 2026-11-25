package controllers

import (
	"context"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"rondo/api/v1alpha1"
)

type conflictingStatus struct {
	client.Client
}

func (c *conflictingStatus) Status() client.SubResourceWriter {
	return &conflictingStatusWriter{SubResourceWriter: c.Client.Status()}
}

type conflictingStatusWriter struct {
	client.SubResourceWriter
}

var enrollmentsResource = schema.GroupResource{Group: "rondo.ouafeul.github.io", Resource: "enrollments"}

func (w *conflictingStatusWriter) Update(ctx context.Context, obj client.Object,
	opts ...client.SubResourceUpdateOption) error {
	if rel, ok := obj.(*v1alpha1.Enrollment); ok && rel.Status.ActiveJobRef != nil {
		return apierrors.NewConflict(enrollmentsResource, rel.Name,
			fmt.Errorf("the object has been modified; please apply your changes to the "+
				"latest version and try again"))
	}
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}

func TestEnrollmentLeavesNoJobWithoutReservation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := setupRelease(t, h, nil)

	rec := &EnrollmentReconciler{
		Client: &conflictingStatus{Client: h.client},
		Reader: h.client,
		Scheme: h.scheme,
		Jobs:   h.enrollment.Jobs,
		Lock:   h.enrollment.Lock,
	}
	if _, err := rec.Reconcile(ctx, h.req(name)); err == nil {
		t.Fatal("the reconcile should have failed on the reservation Conflict: " +
			"the test would measure nothing")
	}

	js := h.jobsFor(t, name)
	if len(js) != 0 {
		t.Fatalf("%d Job(s) created although the RESERVATION failed: %v.\n"+
			"The Job runs, a `helm upgrade` on a real cluster, and nothing in "+
			"the apiserver points to it anymore: `status.activeJobRef` is empty, `attemptCount` "+
			"did not move, and the only thing still preventing a second `helm "+
			"upgrade` is the uncached inventory",
			len(js), namesOf(js))
	}
	rel := h.getEnrollment(t, name)
	if rel.Status.ActiveJobRef != nil {
		t.Errorf("activeJobRef = %v although the reservation failed",
			rel.Status.ActiveJobRef)
	}

	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("next pass: %v", err)
	}
	if n := len(h.jobsFor(t, name)); n != 1 {
		t.Fatalf("%d Jobs on the next pass, want 1: a lost reservation must not "+
			"block the Enrollment", n)
	}
}

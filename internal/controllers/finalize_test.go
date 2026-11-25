package controllers

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"rondo/api/v1alpha1"
	"rondo/internal/jobs"
)

func TestDeletingAnEnrollmentUninstallsThenReleasesTheFinalizer(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*testing.T, *testHarness) string
		verb  jobs.Verb
	}{
		{"chart", func(t *testing.T, h *testHarness) string { return setupRelease(t, h, nil) }, jobs.VerbHelmUninstall},
		{"vm", func(t *testing.T, h *testHarness) string { return setupVMRelease(t, h, "") }, jobs.VerbVMDestroy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			name := tc.setup(t, h)

			if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
				t.Fatal(err)
			}
			applyJob := h.getEnrollment(t, name).Status.ActiveJobRef.Name
			if err := h.client.Delete(ctx, h.getEnrollment(t, name)); err != nil {
				t.Fatal(err)
			}

			if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
				t.Fatal(err)
			}
			if got := len(h.jobsFor(t, name)); got != 1 {
				t.Fatalf("%d Jobs during the apply, want 1: the uninstall must not run in parallel", got)
			}
			h.finishJob(t, applyJob, true)

			if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
				t.Fatal(err)
			}
			uninstall := h.getEnrollment(t, name).Status.ActiveJobRef
			if uninstall == nil || uninstall.Name == applyJob {
				t.Fatalf("activeJobRef = %v: the end of the apply was taken for the end of the uninstall", uninstall)
			}
			var job batchv1.Job
			if err := h.client.Get(ctx, h.key(uninstall.Name), &job); err != nil {
				t.Fatal(err)
			}
			if v := jobs.Verb(job.Labels[jobs.LabelVerb]); v != tc.verb {
				t.Fatalf("verb = %q, want %q", v, tc.verb)
			}

			if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
				t.Fatal(err)
			}
			if !hasFinalizer(h.getEnrollment(t, name), v1alpha1.EnrollmentFinalizer) {
				t.Fatal("finalizer released before the uninstall finished")
			}
			h.finishJob(t, uninstall.Name, true)
			if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
				t.Fatal(err)
			}
			err := h.client.Get(ctx, h.key(name), &v1alpha1.Enrollment{})
			if !apierrors.IsNotFound(err) {
				t.Errorf("Enrollment still present after a successful uninstall (err=%v)", err)
			}
		})
	}
}

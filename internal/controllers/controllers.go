package controllers

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"rondo/api/v1alpha1"
	"rondo/internal/jobs"
)

const (
	requeueWhileJobRuns = 15 * time.Second
	requeueWhenLocked   = 20 * time.Second
	requeueWhenBlocked  = 30 * time.Second
	requeueSteadyState  = 5 * time.Minute
	maxFailureBackoff   = 10 * time.Minute
)

const maxConsecutiveFailures = 10

func failureBackoff(consecutiveFailures int32) time.Duration {
	d := requeueWhenBlocked
	for i := int32(1); i < consecutiveFailures && d < maxFailureBackoff; i++ {
		d *= 2
	}
	if d > maxFailureBackoff {
		d = maxFailureBackoff
	}
	return d
}

func haltedByFailureCeiling(conds *[]metav1.Condition, generation int64, kind, name string,
	failures int32, lastJob string) (halted bool, changed bool) {
	if failures < maxConsecutiveFailures {
		return false, false
	}
	msg := fmt.Sprintf("%d consecutive failures, retries stopped so as not to flood the "+
		"cluster with Jobs. Cause: `kubectl logs job/%s`. To resume: fix the cause, then "+
		"`kubectl patch %s %s --subresource=status --type=merge -p "+
		"'{\"status\":{\"consecutiveFailures\":0}}'`, or change the spec in any way.",
		failures, lastJob, kind, name)
	changed = setCondition(conds, generation, v1alpha1.ConditionStalled, "True",
		v1alpha1.ReasonFailed, msg)
	if markReady(conds, generation, false, v1alpha1.ReasonFailed, msg) {
		changed = true
	}
	return true, changed
}

func noteLeaseLost(conds *[]metav1.Condition, generation int64, what, holder, job string) bool {
	if holder == "" {
		holder = "<unknown>"
	}
	msg := fmt.Sprintf("mutual-exclusion lease LOST to %q while Job %s is still "+
		"running: nothing prevents a second %s from starting on the same "+
		"resource anymore, and two concurrent runs CORRUPT it. Check that only one "+
		"controller is active (`kubectl -n rondo-system get lease`, `kubectl -n rondo-system "+
		"get pods -l app.kubernetes.io/name=rondo-controller`) and follow the running Job with "+
		"`kubectl logs job/%s`", holder, job, what, job)
	return setCondition(conds, generation, v1alpha1.ConditionStalled, "True",
		v1alpha1.ReasonLeaseLost, msg)
}

func clearLeaseLostStall(conds *[]metav1.Condition) bool {
	c := apimeta.FindStatusCondition(*conds, v1alpha1.ConditionStalled)
	if c == nil || c.Reason != v1alpha1.ReasonLeaseLost {
		return false
	}
	clearCondition(conds, v1alpha1.ConditionStalled)
	return true
}

func lastJobName(last *v1alpha1.AttemptInfo) string {
	if last == nil || last.JobName == "" {
		return "<none>"
	}
	return last.JobName
}

var EnrollmentGVK = v1alpha1.GroupVersion.WithKind("Enrollment")

func setCondition(conds *[]metav1.Condition, generation int64, condType, status, reason, msg string) bool {
	return apimeta.SetStatusCondition(conds, metav1.Condition{
		Type:               condType,
		Status:             metav1.ConditionStatus(status),
		Reason:             reason,
		Message:            truncate(msg, 32*1024),
		ObservedGeneration: generation,
	})
}

func markReady(conds *[]metav1.Condition, generation int64, ready bool, reason, msg string) bool {
	status := string(metav1.ConditionFalse)
	if ready {
		status = string(metav1.ConditionTrue)
	}
	return setCondition(conds, generation, v1alpha1.ConditionReady, status, reason, msg)
}

func clearCondition(conds *[]metav1.Condition, condType string) {
	apimeta.RemoveStatusCondition(conds, condType)
}

func isConditionTrue(conds []metav1.Condition, condType string) bool {
	return apimeta.IsStatusConditionTrue(conds, condType)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func hasFinalizer(obj client.Object, f string) bool {
	for _, existing := range obj.GetFinalizers() {
		if existing == f {
			return true
		}
	}
	return false
}

func addFinalizer(obj client.Object, f string) bool {
	if hasFinalizer(obj, f) {
		return false
	}
	obj.SetFinalizers(append(obj.GetFinalizers(), f))
	return true
}

func removeFinalizer(obj client.Object, f string) bool {
	out := make([]string, 0, len(obj.GetFinalizers()))
	found := false
	for _, existing := range obj.GetFinalizers() {
		if existing == f {
			found = true
			continue
		}
		out = append(out, existing)
	}
	if found {
		obj.SetFinalizers(out)
	}
	return found
}

func appendHistory(history []v1alpha1.AttemptInfo, attempt v1alpha1.AttemptInfo) []v1alpha1.AttemptInfo {
	out := append([]v1alpha1.AttemptInfo{attempt}, history...)
	if len(out) > v1alpha1.MaxHistory {
		out = out[:v1alpha1.MaxHistory]
	}
	return out
}

func ignoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func releaseLock(ctx context.Context, lock *jobs.Lock, kind, name string) {
	if err := lock.Release(ctx, kind, name); err != nil {
		log.FromContext(ctx).Error(err, "releasing the mutual-exclusion lease failed, "+
			"the resource stays locked until the lease expires",
			"kind", kind, "name", name)
	}
}

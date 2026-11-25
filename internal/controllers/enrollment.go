package controllers

import (
	"context"
	"fmt"
	"sort"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"rondo/api/v1alpha1"
	"rondo/internal/jobs"
	"rondo/internal/values"
)

type EnrollmentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Jobs   jobs.Config
	Lock   *jobs.Lock
	// Uncached (mgr.GetAPIReader()): for reads whose freshness is a safety invariant.
	Reader client.Reader
}

func (r *EnrollmentReconciler) reader() client.Reader {
	if r.Reader != nil {
		return r.Reader
	}
	return r.Client
}

// +kubebuilder:rbac:groups=rondo.ouafeul.github.io,resources=enrollments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=rondo.ouafeul.github.io,resources=enrollments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rondo.ouafeul.github.io,resources=enrollments/finalizers,verbs=update
// +kubebuilder:rbac:groups=rondo.ouafeul.github.io,resources=components;targets,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;delete

func (r *EnrollmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var enr v1alpha1.Enrollment
	if err := r.Get(ctx, req.NamespacedName, &enr); err != nil {
		return ctrl.Result{}, ignoreNotFound(err)
	}

	if !enr.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &enr)
	}
	if err := r.setFinalizer(ctx, &enr, true); err != nil {
		return ctrl.Result{}, err
	}

	if enr.Status.ActiveJobRef != nil {
		return r.reapActiveJob(ctx, &enr)
	}

	comp, target, res, err := r.resolveRefs(ctx, &enr)
	if err != nil || comp == nil {
		return res, err
	}

	if blocked, res, err := r.checkGates(ctx, &enr, comp, target); blocked {
		return res, err
	}

	return r.applyIfNeeded(ctx, &enr, comp, target)
}

func (r *EnrollmentReconciler) finalize(ctx context.Context, enr *v1alpha1.Enrollment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	if !hasFinalizer(enr, v1alpha1.EnrollmentFinalizer) {
		return ctrl.Result{}, nil
	}
	if enr.Status.ResolvedTarget == "" {
		return ctrl.Result{}, r.setFinalizer(ctx, enr, false)
	}

	if live, lErr := jobs.LiveFor(ctx, r.reader(), r.Jobs.Namespace, EnrollmentGVK.Kind, enr.Name); lErr != nil {
		return ctrl.Result{}, lErr
	} else if live != nil {
		logger.Info("live Job found by inventory, deletion waits for it "+
			"to finish, the finalizer is KEPT", "enrollment", enr.Name, "job", live.Name)
		if enr.Status.ActiveJobRef != nil && enr.Status.ActiveJobRef.Name == live.Name {
			return ctrl.Result{RequeueAfter: requeueWhileJobRuns}, nil
		}
		enr.Status.ActiveJobRef = &v1alpha1.LocalObjectReference{Name: live.Name}
		return ctrl.Result{RequeueAfter: requeueWhileJobRuns}, r.Status().Update(ctx, enr)
	}

	if enr.Status.ActiveJobRef != nil {
		var job batchv1.Job
		key := types.NamespacedName{Namespace: r.Jobs.Namespace, Name: enr.Status.ActiveJobRef.Name}
		err := r.Get(ctx, key, &job)
		if apierrors.IsNotFound(err) {
			err = r.reader().Get(ctx, key, &job)
			if err == nil {
				logger.Info("Job missing from the cache but present in the apiserver, "+
					"the finalizer is kept", "enrollment", enr.Name, "job", key.Name)
			}
		}
		switch {
		case apierrors.IsNotFound(err):
			logger.Info("uninstall Job not found, releasing the finalizer", "enrollment", enr.Name)
		case err != nil:
			return ctrl.Result{}, err
		case !isUninstall(&job):
			// The apply that was running when deletion arrived is over: the uninstall is still owed.
			releaseLock(ctx, r.Lock, EnrollmentGVK.Kind, enr.Name)
			enr.Status.ActiveJobRef = nil
			return r.finalize(ctx, enr)
		default:
			if done, ok := jobs.Finished(&job); !done {
				return ctrl.Result{RequeueAfter: requeueWhileJobRuns}, nil
			} else if !ok {
				logger.Error(fmt.Errorf("%s", jobs.FailureMessage(&job)),
					"uninstall failed, the finalizer is released anyway, the helm release may remain",
					"enrollment", enr.Name, "job", job.Name)
			}
		}
		releaseLock(ctx, r.Lock, EnrollmentGVK.Kind, enr.Name)
		return ctrl.Result{}, r.setFinalizer(ctx, enr, false)
	}

	ok, err := r.Lock.AcquireFor(ctx, EnrollmentGVK.Kind, enr.Name,
		r.Jobs.LeaseTTLFor(enr.Spec.Policy.TimeoutOr(0)))
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		return ctrl.Result{RequeueAfter: requeueWhenLocked}, nil
	}
	verb := jobs.VerbHelmUninstall
	if comp, cerr := r.componentOf(ctx, enr); cerr == nil && comp.Spec.VM != nil {
		verb = jobs.VerbVMDestroy
		log.FromContext(ctx).Info("pruning a machine component, its VMs will be destroyed",
			"enrollment", enr.Name, "component", comp.Name)
	}
	job, err := r.launch(ctx, enr, verb)
	if err != nil {
		releaseLock(ctx, r.Lock, EnrollmentGVK.Kind, enr.Name)
		return ctrl.Result{}, err
	}
	logger.Info("uninstall started", "enrollment", enr.Name, "job", job.Name)
	return ctrl.Result{RequeueAfter: requeueWhileJobRuns}, nil
}

func isUninstall(job *batchv1.Job) bool {
	v := jobs.Verb(job.Labels[jobs.LabelVerb])
	return v == jobs.VerbHelmUninstall || v == jobs.VerbVMDestroy
}

// setFinalizer patches metadata only: a full Update would re-serialize the spec and bump its generation.
func (r *EnrollmentReconciler) setFinalizer(ctx context.Context, enr *v1alpha1.Enrollment, want bool) error {
	base := enr.DeepCopy()
	var changed bool
	if want {
		changed = addFinalizer(enr, v1alpha1.EnrollmentFinalizer)
	} else {
		changed = removeFinalizer(enr, v1alpha1.EnrollmentFinalizer)
	}
	if !changed {
		return nil
	}
	return r.Patch(ctx, enr, client.MergeFrom(base))
}

func (r *EnrollmentReconciler) resolveRefs(ctx context.Context, enr *v1alpha1.Enrollment) (
	*v1alpha1.Component, *v1alpha1.Target, ctrl.Result, error) {
	gen := enr.Generation

	var comp v1alpha1.Component
	if err := r.Get(ctx, types.NamespacedName{Namespace: enr.Namespace, Name: enr.Spec.Component}, &comp); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, nil, ctrl.Result{}, err
		}
		res, err := r.stall(ctx, enr, gen,
			fmt.Sprintf("Component %q not found", enr.Spec.Component))
		return nil, nil, res, err
	}
	target, msg, err := r.resolveTarget(ctx, enr)
	if err != nil {
		return nil, nil, ctrl.Result{}, err
	}
	if msg != "" {
		res, err := r.stall(ctx, enr, gen, msg)
		return nil, nil, res, err
	}
	if enr.Status.ResolvedTarget != target.Name {
		enr.Status.ResolvedTarget = target.Name
		if err := r.Status().Update(ctx, enr); err != nil {
			return nil, nil, ctrl.Result{}, err
		}
	}
	return &comp, target, ctrl.Result{}, nil
}

func (r *EnrollmentReconciler) resolveTarget(ctx context.Context, enr *v1alpha1.Enrollment) (
	*v1alpha1.Target, string, error) {
	var list v1alpha1.TargetList
	if err := r.List(ctx, &list, client.InNamespace(enr.Namespace)); err != nil {
		return nil, "", fmt.Errorf("listing Targets: %w", err)
	}
	var matches []*v1alpha1.Target
	for i := range list.Items {
		if list.Items[i].Answers(enr.Spec.Target) {
			matches = append(matches, &list.Items[i])
		}
	}
	switch {
	case len(matches) == 0:
		return nil, fmt.Sprintf("no Target has the name or role %q", enr.Spec.Target), nil
	case len(matches) > 1:
		names := make([]string, len(matches))
		for i, t := range matches {
			names[i] = t.Name
		}
		sort.Strings(names)
		return nil, fmt.Sprintf("%q matches several Targets (%v), an Enrollment targets exactly one",
			enr.Spec.Target, names), nil
	}
	t := matches[0]
	if prev := enr.Status.ResolvedTarget; prev != "" && prev != t.Name {
		return nil, fmt.Sprintf("%q resolved to %s and now resolves to %s: delete and "+
			"recreate the Enrollment to move the component", enr.Spec.Target, prev, t.Name), nil
	}
	return t, "", nil
}

func (r *EnrollmentReconciler) checkGates(ctx context.Context, enr *v1alpha1.Enrollment,
	comp *v1alpha1.Component, target *v1alpha1.Target) (bool, ctrl.Result, error) {
	gen := enr.Generation

	if enr.Status.ObservedGeneration != gen {
		enr.Status.ObservedGeneration = gen
		if enr.Status.ConsecutiveFailures > 0 {
			enr.Status.ConsecutiveFailures = 0
			clearCondition(&enr.Status.Conditions, v1alpha1.ConditionStalled)
		}
		if err := r.Status().Update(ctx, enr); err != nil {
			return true, ctrl.Result{}, err
		}
	}

	if halted, changed := haltedByFailureCeiling(&enr.Status.Conditions, gen, "enrollment", enr.Name,
		enr.Status.ConsecutiveFailures, lastJobName(enr.Status.LastAttempt)); halted {
		if !changed {
			return true, ctrl.Result{}, nil
		}
		return true, ctrl.Result{}, r.Status().Update(ctx, enr)
	}

	if comp.Spec.Chart == nil && comp.Spec.VM == nil {
		res, err := r.stall(ctx, enr, gen, fmt.Sprintf(
			"Component %s declares neither spec.chart nor spec.vm, nothing to apply "+
				"(CRD replaced by a more permissive one?)", comp.Name))
		return true, res, err
	}

	if !target.Spec.AcceptsArchetype(comp.Spec.Archetype()) {
		res, err := r.stall(ctx, enr, gen, fmt.Sprintf(
			"target %s does not accept a component of archetype %q", target.Name, comp.Spec.Archetype()))
		return true, res, err
	}

	if !target.Spec.InCluster() && !isConditionTrue(target.Status.Conditions, v1alpha1.ConditionReady) {
		markReady(&enr.Status.Conditions, gen, false, v1alpha1.ReasonTargetUnreachable,
			fmt.Sprintf("target %s not ready, see `kubectl describe target %s`", target.Name, target.Name))
		return true, ctrl.Result{RequeueAfter: requeueWhenBlocked}, r.Status().Update(ctx, enr)
	}

	if comp.Spec.VM != nil {
		if _, err := values.ResolveVM(comp.Spec.VM, values.TargetVars(target)); err != nil {
			res, serr := r.stall(ctx, enr, gen,
				fmt.Sprintf("Component %s on target %s: %v", comp.Name, target.Name, err))
			return true, res, serr
		}
	}

	if pending, err := r.pendingDependencies(ctx, enr, comp); err != nil {
		return true, ctrl.Result{}, err
	} else if len(pending) > 0 {
		markReady(&enr.Status.Conditions, gen, false, v1alpha1.ReasonDependencyPending,
			fmt.Sprintf("waiting for %v on %s", pending, target.Name))
		return true, ctrl.Result{RequeueAfter: requeueWhenBlocked}, r.Status().Update(ctx, enr)
	}

	return false, ctrl.Result{}, nil
}

func (r *EnrollmentReconciler) pendingDependencies(ctx context.Context, enr *v1alpha1.Enrollment,
	comp *v1alpha1.Component) ([]string, error) {
	if len(comp.Spec.DependsOn) == 0 {
		return nil, nil
	}
	var list v1alpha1.EnrollmentList
	if err := r.List(ctx, &list, client.InNamespace(enr.Namespace)); err != nil {
		return nil, fmt.Errorf("listing Enrollments: %w", err)
	}
	ready := map[string]bool{}
	for i := range list.Items {
		dep := &list.Items[i]
		if dep.Status.ResolvedTarget != enr.Status.ResolvedTarget {
			continue
		}
		ready[dep.Spec.Component] = isConditionTrue(dep.Status.Conditions,
			v1alpha1.ConditionReady)
	}
	var pending []string
	for _, d := range comp.Spec.DependsOn {
		if !ready[d.Name] {
			pending = append(pending, d.Name)
		}
	}
	return pending, nil
}

func (r *EnrollmentReconciler) reapActiveJob(ctx context.Context, enr *v1alpha1.Enrollment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	gen := enr.Generation
	name := enr.Status.ActiveJobRef.Name

	key := types.NamespacedName{Namespace: r.Jobs.Namespace, Name: name}
	var job batchv1.Job
	err := r.Get(ctx, key, &job)
	if apierrors.IsNotFound(err) {
		err = r.reader().Get(ctx, key, &job)
		if err == nil {
			logger.Info("Job missing from the cache but present in the apiserver, "+
				"reservation kept", "enrollment", enr.Name, "job", name)
		}
	}
	switch {
	case apierrors.IsNotFound(err):
		enr.Status.History = appendHistory(enr.Status.History, v1alpha1.AttemptInfo{
			JobName: name, StartedAt: metav1.Now(), Result: "Aborted",
			Message: "Job not found, outcome unknown (TTL expired?)",
		})
		enr.Status.ActiveJobRef = nil
		releaseLock(ctx, r.Lock, EnrollmentGVK.Kind, enr.Name)
		enr.Status.ConsecutiveFailures++
		setCondition(&enr.Status.Conditions, gen, v1alpha1.ConditionReconciling, "False",
			v1alpha1.ReasonFailed, "Job outcome unknown, re-evaluating")
		if err := r.Status().Update(ctx, enr); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: failureBackoff(enr.Status.ConsecutiveFailures)}, nil
	case err != nil:
		return ctrl.Result{}, err
	}

	done, succeeded := jobs.Finished(&job)
	if !done {
		held, aErr := r.Lock.AcquireFor(ctx, EnrollmentGVK.Kind, enr.Name,
			r.Jobs.LeaseTTLFor(enr.Spec.Policy.TimeoutOr(0)))
		if aErr != nil {
			logger.Error(aErr, "renewing the lease", "enrollment", enr.Name)
		} else if !held {
			holder, _, _ := r.Lock.HeldBy(ctx, EnrollmentGVK.Kind, enr.Name)
			logger.Error(fmt.Errorf("lease held by %q", holder),
				"lease lost while running, mutual exclusion not guaranteed",
				"enrollment", enr.Name, "job", name)
			noteLeaseLost(&enr.Status.Conditions, gen, "`helm upgrade`", holder, name)
		} else {
			clearLeaseLostStall(&enr.Status.Conditions)
		}
		setCondition(&enr.Status.Conditions, gen, v1alpha1.ConditionReconciling, "True",
			v1alpha1.ReasonJobRunning, "Job "+name+" running, `kubectl logs job/"+name+"`")
		return ctrl.Result{RequeueAfter: requeueWhileJobRuns}, r.Status().Update(ctx, enr)
	}
	clearLeaseLostStall(&enr.Status.Conditions)

	attempt := v1alpha1.AttemptInfo{
		JobName: name,
		Version: enr.Status.AppliedChartVersion,
		Result:  "Succeeded",
	}
	if job.Status.StartTime != nil {
		attempt.StartedAt = *job.Status.StartTime
	} else {
		attempt.StartedAt = job.CreationTimestamp
	}
	attempt.FinishedAt = job.Status.CompletionTime
	if attempt.FinishedAt == nil {
		now := metav1.Now()
		attempt.FinishedAt = &now
	}

	var dp *desired
	if succeeded {
		desired, err := r.desiredState(ctx, enr)
		if err != nil {
			return ctrl.Result{}, err
		}
		dp = &desired
		enr.Status.ConsecutiveFailures = 0
		clearCondition(&enr.Status.Conditions, v1alpha1.ConditionStalled)

		if desired.archetype == v1alpha1.ArchetypeVM {
			switch enr.Status.Phase {
			case v1alpha1.PhaseProvision:
				attempt.Version = "machines:" + desired.infraHash
				enr.Status.AppliedValuesHash = desired.infraHash
				setCondition(&enr.Status.Conditions, gen, v1alpha1.ConditionApplied, "True",
					v1alpha1.ReasonSucceeded, "machines provisioned")
			case v1alpha1.PhaseInstall:
				attempt.Version = desired.installerVersion
				enr.Status.AppliedInstallerVersion = desired.installerVersion
				enr.Status.AppliedChartVersion = desired.installHash
				setCondition(&enr.Status.Conditions, gen, v1alpha1.ConditionApplied, "True",
					v1alpha1.ReasonSucceeded, "installed at version "+desired.installerVersion)
			}
		} else {
			attempt.Version = desired.chartVersion
			enr.Status.AppliedChartVersion = desired.chartVersion
			enr.Status.AppliedValuesHash = desired.valuesHash
			setCondition(&enr.Status.Conditions, gen, v1alpha1.ConditionApplied, "True",
				v1alpha1.ReasonSucceeded, "applied at version "+desired.chartVersion)
		}
	} else {
		attempt.Result = "Failed"
		attempt.Message = jobs.FailureMessage(&job)
		enr.Status.ConsecutiveFailures++
		setCondition(&enr.Status.Conditions, gen, v1alpha1.ConditionApplied, "False",
			v1alpha1.ReasonFailed, attempt.Message+", `kubectl logs job/"+name+"`")
	}

	enr.Status.History = appendHistory(enr.Status.History, attempt)
	enr.Status.LastAttempt = &attempt
	enr.Status.ActiveJobRef = nil
	setCondition(&enr.Status.Conditions, gen, v1alpha1.ConditionReconciling, "False",
		v1alpha1.ReasonSucceeded, "Job finished")
	r.refreshReady(enr, dp)

	releaseLock(ctx, r.Lock, EnrollmentGVK.Kind, enr.Name)
	if err := r.Status().Update(ctx, enr); err != nil {
		return ctrl.Result{}, err
	}
	if !succeeded {
		return ctrl.Result{RequeueAfter: failureBackoff(enr.Status.ConsecutiveFailures)}, nil
	}
	return ctrl.Result{RequeueAfter: requeueWhileJobRuns}, nil
}

type desired struct {
	archetype        v1alpha1.Archetype
	chartOCI         string
	chartVersion     string
	valuesHash       string
	infraHash        string
	installHash      string
	installerVersion string
	fingerprint      string
	timeout          time.Duration
}

func (r *EnrollmentReconciler) desiredState(ctx context.Context, enr *v1alpha1.Enrollment) (desired, error) {
	var comp v1alpha1.Component
	if err := r.Get(ctx, types.NamespacedName{Namespace: enr.Namespace, Name: enr.Spec.Component}, &comp); err != nil {
		return desired{}, err
	}
	target, err := r.targetOf(ctx, enr)
	if err != nil {
		return desired{}, err
	}
	resolved, err := values.ForEnrollment(&comp, enr, target)
	if err != nil {
		return desired{}, err
	}
	hash, err := values.Hash(resolved)
	if err != nil {
		return desired{}, err
	}
	d := desired{valuesHash: hash, archetype: comp.Spec.Archetype()}
	switch d.archetype {
	case v1alpha1.ArchetypeVM:
		vm, verr := values.ResolveVM(comp.Spec.VM, values.TargetVars(target))
		if verr != nil {
			return desired{}, verr
		}
		secrets, serr := r.installSecrets(ctx, enr.Namespace, vm)
		if serr != nil {
			return desired{}, serr
		}
		d.infraHash = values.VMInfraHash(vm, comp.Name, target)
		d.installerVersion = values.VMInstallerVersion(vm)
		d.installHash = values.VMInstallHash(vm, secrets)
	default:
		if comp.Spec.Chart == nil {
			return desired{}, fmt.Errorf("Component %s declares neither spec.chart nor "+
				"spec.vm, nothing to apply", comp.Name)
		}
		d.chartVersion = comp.Spec.Chart.Version
		d.chartOCI = comp.Spec.Chart.OCI
	}
	d.fingerprint = fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d", d.archetype, d.chartOCI, d.chartVersion,
		d.valuesHash, d.infraHash, d.installHash, enr.Status.AttemptCount)
	d.timeout = enr.Spec.Policy.TimeoutOr(0)
	return d, nil
}

func (r *EnrollmentReconciler) componentOf(ctx context.Context, enr *v1alpha1.Enrollment) (*v1alpha1.Component, error) {
	var comp v1alpha1.Component
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: enr.Namespace, Name: enr.Spec.Component,
	}, &comp); err != nil {
		return nil, err
	}
	return &comp, nil
}

func (r *EnrollmentReconciler) targetOf(ctx context.Context, enr *v1alpha1.Enrollment) (*v1alpha1.Target, error) {
	var target v1alpha1.Target
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: enr.Namespace, Name: enr.Status.ResolvedTarget,
	}, &target); err != nil {
		return nil, err
	}
	return &target, nil
}

func (r *EnrollmentReconciler) installSecrets(ctx context.Context, ns string,
	vm *v1alpha1.VMSpec) (map[string]string, error) {
	if vm.Installer == nil || len(vm.Installer.SecretsFrom) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, src := range vm.Installer.SecretsFrom {
		switch {
		case src.SecretRef != nil:
			var s corev1.Secret
			if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: src.SecretRef.Name}, &s); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return nil, fmt.Errorf("secretsFrom Secret %s: %w", src.SecretRef.Name, err)
			}
			for _, k := range src.Keys {
				if v, ok := s.Data[k]; ok {
					out[k] = string(v)
				}
			}
		case src.ConfigMapRef != nil:
			var cm corev1.ConfigMap
			if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: src.ConfigMapRef.Name}, &cm); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return nil, fmt.Errorf("secretsFrom ConfigMap %s: %w", src.ConfigMapRef.Name, err)
			}
			for _, k := range src.Keys {
				if v, ok := cm.Data[k]; ok {
					out[k] = v
				}
			}
		}
	}
	return out, nil
}

func (r *EnrollmentReconciler) applyIfNeeded(ctx context.Context, enr *v1alpha1.Enrollment,
	comp *v1alpha1.Component, target *v1alpha1.Target) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	gen := enr.Generation

	d, err := r.desiredState(ctx, enr)
	if err != nil {
		return ctrl.Result{}, err
	}

	verb, phase, why := nextAction(enr, d)

	if verb == "" {
		r.refreshReady(enr, &d)
		return ctrl.Result{RequeueAfter: requeueSteadyState}, r.Status().Update(ctx, enr)
	}

	if live, lErr := jobs.LiveFor(ctx, r.reader(), r.Jobs.Namespace, EnrollmentGVK.Kind, enr.Name); lErr != nil {
		return ctrl.Result{}, lErr
	} else if live != nil {
		if enr.Status.ActiveJobRef == nil || enr.Status.ActiveJobRef.Name != live.Name {
			logger.Info("live Job found by inventory, the reservation had been "+
				"lost, no second Job will be started", "enrollment", enr.Name, "job", live.Name)
			enr.Status.ActiveJobRef = &v1alpha1.LocalObjectReference{Name: live.Name}
		}
		setCondition(&enr.Status.Conditions, gen, v1alpha1.ConditionReconciling, "True",
			v1alpha1.ReasonJobRunning, "Job "+live.Name+" running, `kubectl logs job/"+live.Name+"`")
		return ctrl.Result{RequeueAfter: requeueWhileJobRuns}, r.Status().Update(ctx, enr)
	}

	ok, err := r.Lock.AcquireFor(ctx, EnrollmentGVK.Kind, enr.Name, r.Jobs.LeaseTTLFor(d.timeout))
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		holder, _, _ := r.Lock.HeldBy(ctx, EnrollmentGVK.Kind, enr.Name)
		setCondition(&enr.Status.Conditions, gen, v1alpha1.ConditionReconciling, "True",
			v1alpha1.ReasonJobRunning, "lock held by "+holder+", retry deferred")
		return ctrl.Result{RequeueAfter: requeueWhenLocked}, r.Status().Update(ctx, enr)
	}

	enr.Status.Phase = phase
	job, err := r.launch(ctx, enr, verb)
	if err != nil {
		releaseLock(ctx, r.Lock, EnrollmentGVK.Kind, enr.Name)
		return ctrl.Result{}, err
	}
	logger.Info("action started", "enrollment", enr.Name, "target", target.Name,
		"archetype", d.archetype, "verb", verb, "phase", phase,
		"reason", why, "job", job.Name)
	return ctrl.Result{RequeueAfter: requeueWhileJobRuns}, nil
}

func nextAction(enr *v1alpha1.Enrollment, d desired) (jobs.Verb, v1alpha1.Phase, string) {
	if d.archetype == v1alpha1.ArchetypeVM {
		switch {
		case enr.Status.AppliedValuesHash != d.infraHash:
			return jobs.VerbVMApply, v1alpha1.PhaseProvision, "machine infrastructure to apply"
		case enr.Status.MachinesSecretRef == nil:
			return jobs.VerbVMApply, v1alpha1.PhaseProvision, "machine inventory missing"
		case d.installerVersion == "":
			return "", "", "no installer declared"
		case enr.Status.AppliedInstallerVersion != d.installerVersion:
			return jobs.VerbVMInstall, v1alpha1.PhaseInstall, "installer version to run"
		case enr.Status.AppliedChartVersion != d.installHash:
			return jobs.VerbVMInstall, v1alpha1.PhaseInstall, "install configuration changed"
		default:
			return "", "", ""
		}
	}

	if enr.Status.AppliedChartVersion != d.chartVersion ||
		enr.Status.AppliedValuesHash != d.valuesHash {
		return jobs.VerbHelmApply, "", "chart or values to apply"
	}
	return "", "", ""
}

func (r *EnrollmentReconciler) launch(ctx context.Context, enr *v1alpha1.Enrollment, verb jobs.Verb) (*batchv1.Job, error) {
	d, err := r.desiredState(ctx, enr)
	if err != nil {
		return nil, err
	}
	job, err := r.Jobs.Build(jobs.Request{
		Verb:        verb,
		Owner:       enr,
		OwnerGVK:    EnrollmentGVK,
		Fingerprint: d.fingerprint + "|" + string(verb),
		Timeout:     d.timeout,
	})
	if err != nil {
		return nil, err
	}

	enr.Status.ActiveJobRef = &v1alpha1.LocalObjectReference{Name: job.Name}
	enr.Status.AttemptCount++
	setCondition(&enr.Status.Conditions, enr.Generation, v1alpha1.ConditionReconciling, "True",
		v1alpha1.ReasonJobRunning, "Job "+job.Name+" started")
	if err := r.Status().Update(ctx, enr); err != nil {
		return nil, fmt.Errorf("reserving Job %s: %w", job.Name, err)
	}

	if err := r.Create(ctx, job); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating Job %s: %w", job.Name, err)
		}
		if err := r.Get(ctx, types.NamespacedName{Namespace: job.Namespace, Name: job.Name}, job); err != nil {
			return nil, err
		}
	}
	return job, nil
}

func (r *EnrollmentReconciler) refreshReady(enr *v1alpha1.Enrollment, d *desired) {
	gen := enr.Generation
	applied := isConditionTrue(enr.Status.Conditions, v1alpha1.ConditionApplied)
	switch {
	case !applied:
		markReady(&enr.Status.Conditions, gen, false, v1alpha1.ReasonProgressing, "not applied yet")
	case d != nil && d.archetype == v1alpha1.ArchetypeVM:
		r.markVMReady(enr, *d)
	case enr.Status.Health == v1alpha1.HealthHealthy:
		markReady(&enr.Status.Conditions, gen, true, v1alpha1.ReasonSucceeded,
			"applied at "+enr.Status.AppliedChartVersion+" and healthy")
	case enr.Status.Health == "":
		markReady(&enr.Status.Conditions, gen, false, v1alpha1.ReasonProgressing,
			"applied, health not evaluated yet")
	default:
		markReady(&enr.Status.Conditions, gen, false, v1alpha1.ReasonProgressing,
			"applied but "+enr.Status.Health+": "+enr.Status.HealthMessage)
	}
}

const notProbed = "health not probed (no Kubernetes object to assess)"

func (r *EnrollmentReconciler) markVMReady(enr *v1alpha1.Enrollment, d desired) {
	gen := enr.Generation
	switch {
	case enr.Status.MachinesSecretRef == nil:
		markReady(&enr.Status.Conditions, gen, false, v1alpha1.ReasonProgressing,
			"machines applied but inventory missing, provisioning to rerun")
	case d.installerVersion == "":
		markReady(&enr.Status.Conditions, gen, true, v1alpha1.ReasonSucceeded,
			"machines provisioned; no installer declared, "+notProbed)
	case enr.Status.AppliedInstallerVersion != d.installerVersion:
		markReady(&enr.Status.Conditions, gen, false, v1alpha1.ReasonProgressing,
			"machines provisioned, install "+installedAs(enr)+
				", wanted version "+d.installerVersion)
	case enr.Status.AppliedChartVersion != d.installHash:
		markReady(&enr.Status.Conditions, gen, false, v1alpha1.ReasonProgressing,
			"install configuration changed, install to rerun")
	default:
		markReady(&enr.Status.Conditions, gen, true, v1alpha1.ReasonSucceeded,
			"machines provisioned and installed at "+d.installerVersion+", "+notProbed)
	}
}

func installedAs(enr *v1alpha1.Enrollment) string {
	if enr.Status.AppliedInstallerVersion == "" {
		return "never run"
	}
	return "at " + enr.Status.AppliedInstallerVersion
}

func (r *EnrollmentReconciler) stall(ctx context.Context, enr *v1alpha1.Enrollment, gen int64, msg string) (ctrl.Result, error) {
	setCondition(&enr.Status.Conditions, gen, v1alpha1.ConditionStalled, "True", v1alpha1.ReasonFailed, msg)
	markReady(&enr.Status.Conditions, gen, false, v1alpha1.ReasonFailed, msg)
	return ctrl.Result{RequeueAfter: requeueWhenBlocked}, r.Status().Update(ctx, enr)
}

func (r *EnrollmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Enrollment{}).
		Owns(&batchv1.Job{}).
		Watches(&v1alpha1.Target{}, handler.EnqueueRequestsFromMapFunc(r.enrollmentsFor)).
		Watches(&v1alpha1.Component{}, handler.EnqueueRequestsFromMapFunc(r.enrollmentsFor)).
		Named("enrollment").
		Complete(r)
}

func (r *EnrollmentReconciler) enrollmentsFor(ctx context.Context, obj client.Object) []reconcile.Request {
	var list v1alpha1.EnrollmentList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		log.FromContext(ctx).Error(err, "listing Enrollments for a wakeup failed, no "+
			"Enrollment will be woken", "object", obj.GetName())
		return nil
	}
	var out []reconcile.Request
	for i := range list.Items {
		enr := &list.Items[i]
		switch o := obj.(type) {
		case *v1alpha1.Component:
			if enr.Spec.Component != o.Name {
				continue
			}
		case *v1alpha1.Target:
			if enr.Status.ResolvedTarget != o.Name && !o.Answers(enr.Spec.Target) {
				continue
			}
		}
		out = append(out, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: enr.Namespace, Name: enr.Name},
		})
	}
	return out
}

package controllers

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"rondo/api/v1alpha1"
	"rondo/internal/kobj"
	"rondo/internal/kubeconfig"
)

type TargetReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=rondo.ouafeul.github.io,resources=targets,verbs=get;list;watch
// +kubebuilder:rbac:groups=rondo.ouafeul.github.io,resources=targets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets;configmaps,verbs=get;list;watch

func (r *TargetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var target v1alpha1.Target
	if err := r.Get(ctx, req.NamespacedName, &target); err != nil {
		return ctrl.Result{}, ignoreNotFound(err)
	}
	gen := target.Generation

	target.Status.ObservedGeneration = gen

	if target.Spec.VSphere != nil {
		if err := r.validateVSphere(ctx, &target); err != nil {
			setCondition(&target.Status.Conditions, gen, v1alpha1.ConditionStalled, "True",
				v1alpha1.ReasonFailed, err.Error())
			markReady(&target.Status.Conditions, gen, false, v1alpha1.ReasonFailed, err.Error())
			return ctrl.Result{RequeueAfter: requeueWhenBlocked}, r.Status().Update(ctx, &target)
		}
		clearCondition(&target.Status.Conditions, v1alpha1.ConditionStalled)
		markReady(&target.Status.Conditions, gen, true, v1alpha1.ReasonSucceeded,
			"vSphere placement usable, only the Jobs contact vCenter")
		return ctrl.Result{RequeueAfter: requeueSteadyState}, r.Status().Update(ctx, &target)
	}

	if target.Spec.InCluster() {
		clearCondition(&target.Status.Conditions, v1alpha1.ConditionStalled)
		markReady(&target.Status.Conditions, gen, true, v1alpha1.ReasonSucceeded,
			"management cluster (controller ServiceAccount)")
		return ctrl.Result{RequeueAfter: requeueSteadyState}, r.Status().Update(ctx, &target)
	}

	if err := r.validateRemote(ctx, &target); err != nil {
		setCondition(&target.Status.Conditions, gen, v1alpha1.ConditionStalled, "True",
			v1alpha1.ReasonFailed, err.Error())
		markReady(&target.Status.Conditions, gen, false, v1alpha1.ReasonFailed, err.Error())
		return ctrl.Result{RequeueAfter: requeueWhenBlocked}, r.Status().Update(ctx, &target)
	}

	clearCondition(&target.Status.Conditions, v1alpha1.ConditionStalled)
	markReady(&target.Status.Conditions, gen, true, v1alpha1.ReasonSucceeded,
		"kubeconfig usable, reachability checked by rondo-probe")
	return ctrl.Result{RequeueAfter: requeueSteadyState}, r.Status().Update(ctx, &target)
}

// Kept in sync by hand with the defaultless variables of terraform/vm/variables.tf.
var vmRequiredSettings = []string{
	"vsphere_server", "vsphere_datacenter", "vsphere_cluster",
	"vsphere_datastore", "vsphere_network",
	"network_gateway", "network_dns",
}

var vmRequiredCredentials = []string{kobj.VSphereUserKey, kobj.VSpherePasswordKey}

func (r *TargetReconciler) validateVSphere(ctx context.Context, target *v1alpha1.Target) error {
	place := target.Spec.VSphere
	if place.CredentialsSecretRef == nil {
		return fmt.Errorf("vSphere target without vsphere.credentialsSecretRef: the Jobs " +
			"would have no vCenter credentials")
	}
	var creds corev1.Secret
	key := types.NamespacedName{Namespace: target.Namespace, Name: place.CredentialsSecretRef.Name}
	if err := r.Get(ctx, key, &creds); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("Secret %q not found in %s (does `rondo deploy` project "+
				"the vSphere credentials?)", place.CredentialsSecretRef.Name, target.Namespace)
		}
		return err
	}
	for _, k := range vmRequiredCredentials {
		if _, ok := creds.Data[k]; !ok {
			return fmt.Errorf("key %q missing from Secret %q (keys present: %v)",
				k, place.CredentialsSecretRef.Name, secretKeys(&creds))
		}
	}

	if place.SettingsConfigMapRef == nil {
		return fmt.Errorf("vSphere target without vsphere.settingsConfigMapRef: the machine " +
			"module would have no datacenter, datastore or network")
	}
	var settings corev1.ConfigMap
	key = types.NamespacedName{Namespace: target.Namespace, Name: place.SettingsConfigMapRef.Name}
	if err := r.Get(ctx, key, &settings); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("ConfigMap %q not found in %s (does `rondo deploy` project "+
				"the site values?)", place.SettingsConfigMapRef.Name, target.Namespace)
		}
		return err
	}
	var missing []string
	for _, k := range vmRequiredSettings {
		if v, ok := settings.Data[k]; !ok || v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("placement setting(s) missing from ConfigMap %q: %s; "+
			"these `terraform/vm` variables have no default, so `tofu apply` "+
			"would fail with \"No value for required variable\"",
			place.SettingsConfigMapRef.Name, strings.Join(missing, ", "))
	}
	return nil
}

func (r *TargetReconciler) validateRemote(ctx context.Context, target *v1alpha1.Target) error {
	ref := target.Spec.Kubernetes.KubeconfigSecretRef
	var secret corev1.Secret
	key := types.NamespacedName{Namespace: target.Namespace, Name: ref.Name}
	if err := r.Get(ctx, key, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("Secret %q not found in %s (did `rondo deploy downstreams` succeed?)",
				ref.Name, target.Namespace)
		}
		return err
	}
	raw, ok := secret.Data[ref.Key]
	if !ok {
		return fmt.Errorf("key %q missing from Secret %q (keys present: %v)",
			ref.Key, ref.Name, secretKeys(&secret))
	}
	info, err := kubeconfig.Parse(raw)
	if err != nil {
		return fmt.Errorf("Secret %q, key %q: %w", ref.Name, ref.Key, err)
	}
	if s := target.Spec.Kubernetes.Server; s != "" && s != info.Server {
		return fmt.Errorf("kubernetes.server (%s) differs from the kubeconfig endpoint (%s): "+
			"the Jobs will use the kubeconfig one", s, info.Server)
	}
	return nil
}

func secretKeys(s *corev1.Secret) []string {
	out := make([]string, 0, len(s.Data))
	for k := range s.Data {
		out = append(out, k)
	}
	return out
}

func (r *TargetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Target{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.targetsForSecret)).
		Named("target").
		Complete(r)
}

func (r *TargetReconciler) targetsForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	var list v1alpha1.TargetList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		log.FromContext(ctx).Error(err, "listing Targets for a wakeup failed, NO "+
			"target will be woken by this Secret", "secret", obj.GetName())
		return nil
	}
	var out []reconcile.Request
	for i := range list.Items {
		t := &list.Items[i]
		k := t.Spec.Kubernetes
		if k == nil || k.KubeconfigSecretRef == nil || k.KubeconfigSecretRef.Name != obj.GetName() {
			continue
		}
		out = append(out, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: t.Namespace, Name: t.Name},
		})
	}
	return out
}

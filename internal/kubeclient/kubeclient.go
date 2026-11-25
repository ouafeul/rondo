package kubeclient

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	"rondo/api/v1alpha1"
	"rondo/internal/kubeconfig"
)

func ForTarget(ctx context.Context, local client.Client, target *v1alpha1.Target) (client.Client, error) {
	if target.Spec.InCluster() {
		return local, nil
	}
	cfg, err := RESTConfigForTarget(ctx, local, target)
	if err != nil {
		return nil, err
	}
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		return nil, fmt.Errorf("client for target %s: %w", target.Name, err)
	}
	return c, nil
}

func RESTConfigForTarget(ctx context.Context, local client.Client, target *v1alpha1.Target) (*rest.Config, error) {
	if target.Spec.InCluster() {
		cfg, err := ctrlconfig.GetConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster configuration for Target %s: %w", target.Name, err)
		}
		return cfg, nil
	}
	raw, err := KubeconfigBytes(ctx, local, target)
	if err != nil {
		return nil, err
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("rest.Config from the kubeconfig of Target %s: %w", target.Name, err)
	}
	return cfg, nil
}

func KubeconfigBytes(ctx context.Context, local client.Client, target *v1alpha1.Target) ([]byte, error) {
	if target.Spec.Kubernetes == nil || target.Spec.Kubernetes.KubeconfigSecretRef == nil {
		return nil, fmt.Errorf("Target %s has no kubernetes.kubeconfigSecretRef", target.Name)
	}
	ref := target.Spec.Kubernetes.KubeconfigSecretRef
	var secret corev1.Secret
	if err := local.Get(ctx, types.NamespacedName{
		Namespace: target.Namespace, Name: ref.Name,
	}, &secret); err != nil {
		return nil, fmt.Errorf("read Secret %s: %w", ref.Name, err)
	}
	raw, ok := secret.Data[ref.Key]
	if !ok {
		return nil, fmt.Errorf("key %q missing from Secret %s", ref.Key, ref.Name)
	}
	if _, err := kubeconfig.Parse(raw); err != nil {
		return nil, fmt.Errorf("kubeconfig of Target %s: %w", target.Name, err)
	}
	return raw, nil
}

package probe

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"rondo/api/v1alpha1"
)

func remoteTarget(name string) *v1alpha1.Target {
	return &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "rondo-system"},
		Spec: v1alpha1.TargetSpec{Kubernetes: &v1alpha1.KubernetesCluster{
			KubeconfigSecretRef: &v1alpha1.SecretKeySelector{Name: "kubeconfig-" + name, Key: "value"},
		}},
	}
}

func kubeconfigSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kubeconfig-" + name, Namespace: "rondo-system"},
		Data: map[string][]byte{"value": []byte("apiVersion: v1\n" +
			"kind: Config\n" +
			"clusters:\n" +
			"- name: c\n" +
			"  cluster:\n" +
			"    server: https://127.0.0.1:6443\n" +
			"    insecure-skip-tls-verify: true\n" +
			"users:\n" +
			"- name: u\n" +
			"  user:\n" +
			"    token: t\n" +
			"contexts:\n" +
			"- name: c\n" +
			"  context:\n" +
			"    cluster: c\n" +
			"    user: u\n" +
			"current-context: c\n")},
	}
}

func TestNewTargetCacheRemembersItsNamespaces(t *testing.T) {
	tg := remoteTarget("downstream")
	c := newFake(t, tg, kubeconfigSecret("downstream"))
	ns := map[string]bool{"monitoring": true, "logging": true}

	r, err := newTargetCache(context.Background(), c, tg, ns)
	if err != nil {
		t.Fatalf("newTargetCache: %v", err)
	}
	defer r.stop()

	if !r.covers(ns) {
		t.Error("the cache does not cover the namespaces it was built for")
	}
	if r.covers(map[string]bool{"elsewhere": true}) {
		t.Error("the cache claims coverage outside its bound: a read there would be " +
			"served by an informer that does not watch it")
	}
	if err := r.stale(); err != nil {
		t.Errorf("fresh cache already reported disconnected: %v", err)
	}
}

func TestNewTargetCacheRejectsMissingKubeconfig(t *testing.T) {
	tg := remoteTarget("no-secret")
	c := newFake(t, tg)

	if _, err := newTargetCache(context.Background(), c, tg, nil); err == nil {
		t.Fatal("cache built without a kubeconfig")
	}
}

func TestCacheCountsOneWatchPerGVKNotPerObject(t *testing.T) {
	tc := &targetCache{
		reader: newFake(t, healthyDeployment()),
		gvks:   map[schema.GroupVersionKind]bool{},
		denied: map[string]bool{},
	}
	dep := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}

	for _, name := range []string{"a", "b", "c"} {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(dep)
		_ = tc.Get(context.Background(),
			types.NamespacedName{Namespace: "logging", Name: name}, u)
	}
	if got := tc.watches(); got != 1 {
		t.Errorf("watches = %d for 3 reads of the same GVK, want 1", got)
	}

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Service"})
	_ = tc.Get(context.Background(), types.NamespacedName{Namespace: "logging", Name: "s"}, u)
	if got := tc.watches(); got != 2 {
		t.Errorf("watches = %d after a second GVK, want 2", got)
	}
}

func TestCacheDelegatesItsReads(t *testing.T) {
	tc := &targetCache{
		reader: newFake(t, healthyDeployment()),
		gvks:   map[schema.GroupVersionKind]bool{},
		denied: map[string]bool{},
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"})
	if err := tc.Get(context.Background(),
		types.NamespacedName{Namespace: "logging", Name: "vl"}, u); err != nil {
		t.Fatalf("Get: %v", err)
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "apps", Version: "v1", Kind: "DeploymentList"})
	if err := tc.List(context.Background(), list); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Items) == 0 {
		t.Error("List returned nothing: the read is not delegated to the store")
	}
}

func TestStopCancelsTheCacheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tc := &targetCache{cancel: cancel}
	tc.stop()
	if ctx.Err() == nil {
		t.Error("stop() leaves the cache context alive: its watches survive")
	}
}

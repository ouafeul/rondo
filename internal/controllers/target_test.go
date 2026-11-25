package controllers

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"rondo/api/v1alpha1"
)

func (h *testHarness) vmSiteObjects(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	creds := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "rondo-vsphere-credentials", Namespace: h.ns},
		StringData: map[string]string{"vsphere_user": "adm@vsphere.local", "vsphere_password": "pw"},
	}
	if err := h.client.Create(ctx, creds); err != nil {
		t.Fatalf("vSphere credentials Secret: %v", err)
	}
	data := map[string]string{}
	for _, k := range vmRequiredSettings {
		data[k] = "site-value"
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "rondo-site", Namespace: h.ns},
		Data:       data,
	}
	if err := h.client.Create(ctx, cm); err != nil {
		t.Fatalf("site ConfigMap: %v", err)
	}
}

func (h *testHarness) createVSphereTarget(t *testing.T, name string,
	roles []string, values map[string]string) *v1alpha1.Target {
	t.Helper()
	h.vmSiteObjects(t)
	target := &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns},
		Spec: v1alpha1.TargetSpec{
			Roles: roles,
			VSphere: &v1alpha1.VSpherePlacement{
				CredentialsSecretRef: &v1alpha1.LocalObjectReference{Name: "rondo-vsphere-credentials"},
				SettingsConfigMapRef: &v1alpha1.LocalObjectReference{Name: "rondo-site"},
			},
			Values: values,
		},
	}
	if err := h.client.Create(context.Background(), target); err != nil {
		t.Fatalf("creating vSphere Target %s: %v", name, err)
	}
	if _, err := h.target.Reconcile(context.Background(), h.req(name)); err != nil {
		t.Fatalf("reconciling Target %s: %v", name, err)
	}
	var got v1alpha1.Target
	if err := h.client.Get(context.Background(), h.key(name), &got); err != nil {
		t.Fatal(err)
	}
	requireCondition(t, got.Status.Conditions, v1alpha1.ConditionReady, "True")
	return target
}

func TestVSphereTargetIsReadyWithoutKubeconfig(t *testing.T) {
	h := newHarness(t)
	h.createVSphereTarget(t, "dmz", nil, nil)
	var target v1alpha1.Target
	if err := h.client.Get(context.Background(), h.key("dmz"), &target); err != nil {
		t.Fatal(err)
	}
	if c := conditionOf(target.Status.Conditions, v1alpha1.ConditionStalled); c != nil &&
		string(c.Status) == "True" {
		t.Fatalf("vSphere target Stalled: %s", c.Message)
	}
}

func TestVSphereTargetMissingSettingNamesIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.createVSphereTarget(t, "dmz", nil, nil)

	var cm corev1.ConfigMap
	if err := h.client.Get(ctx, h.key("rondo-site"), &cm); err != nil {
		t.Fatal(err)
	}
	delete(cm.Data, "vsphere_datastore")
	if err := h.client.Update(ctx, &cm); err != nil {
		t.Fatal(err)
	}
	if _, err := h.target.Reconcile(ctx, h.req("dmz")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var target v1alpha1.Target
	if err := h.client.Get(ctx, h.key("dmz"), &target); err != nil {
		t.Fatal(err)
	}
	msg := requireCondition(t, target.Status.Conditions, v1alpha1.ConditionReady, "False")
	if !strings.Contains(msg, "vsphere_datastore") {
		t.Errorf("message = %q, must name the missing setting", msg)
	}
}

func TestRemoteTargetNamesWhatIsWrongWithItsKubeconfig(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	target := &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "rke2-a", Namespace: h.ns},
		Spec: v1alpha1.TargetSpec{Kubernetes: &v1alpha1.KubernetesCluster{
			Server:              "https://a:6443",
			KubeconfigSecretRef: &v1alpha1.SecretKeySelector{Name: "kc", Key: "kubeconfig"},
		}},
	}
	if err := h.client.Create(ctx, target); err != nil {
		t.Fatal(err)
	}
	kubeconfig := func(server string) []byte {
		return []byte("apiVersion: v1\nkind: Config\ncurrent-context: c\n" +
			"clusters: [{name: c, cluster: {server: '" + server + "', certificate-authority-data: Y2E=}}]\n" +
			"contexts: [{name: c, context: {cluster: c, user: u}}]\n" +
			"users: [{name: u, user: {token: t}}]\n")
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kc", Namespace: h.ns}}

	for _, step := range []struct {
		data      map[string][]byte
		wantReady string
		wantMsg   string
	}{
		{nil, "False", `"kc" not found`},
		{map[string][]byte{"other": []byte("x")}, "False", "[other]"},
		{map[string][]byte{"kubeconfig": []byte("::")}, "False", `key "kubeconfig"`},
		{map[string][]byte{"kubeconfig": kubeconfig("https://b:6443")}, "False", "differs"},
		{map[string][]byte{"kubeconfig": kubeconfig("https://a:6443")}, "True", "kubeconfig usable"},
	} {
		if step.data != nil {
			secret.Data = step.data
			var err error
			if secret.ResourceVersion == "" {
				err = h.client.Create(ctx, secret)
			} else {
				err = h.client.Update(ctx, secret)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := h.target.Reconcile(ctx, h.req("rke2-a")); err != nil {
			t.Fatal(err)
		}
		var got v1alpha1.Target
		if err := h.client.Get(ctx, h.key("rke2-a"), &got); err != nil {
			t.Fatal(err)
		}
		if msg := requireCondition(t, got.Status.Conditions, v1alpha1.ConditionReady, step.wantReady); !strings.Contains(msg, step.wantMsg) {
			t.Errorf("message = %q, want %q", msg, step.wantMsg)
		}
	}
}

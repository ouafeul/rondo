package probe

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"rondo/api/v1alpha1"
)

func TestAdmissionWebhooksAreHealthyOnceApplied(t *testing.T) {
	for _, kind := range []string{"ValidatingWebhookConfiguration", "MutatingWebhookConfiguration"} {
		o := &unstructured.Unstructured{}
		o.SetAPIVersion("admissionregistration.k8s.io/v1")
		o.SetKind(kind)
		o.SetName("a-webhook")

		got := Assess(o)
		if got.Health != v1alpha1.HealthHealthy {
			t.Errorf("%s judged %q (%s): a status-less object applied successfully is HEALTHY, "+
				"otherwise dependsOn blocks the whole downstream chain", kind, got.Health, got.Message)
		}
	}
}

func TestDeclarativeChartKindsAreHealthyOnceApplied(t *testing.T) {
	for _, tc := range []struct{ apiVersion, kind string }{
		{"networking.k8s.io/v1", "IngressClass"},
		{"traefik.io/v1alpha1", "IngressRoute"},
		{"traefik.io/v1alpha1", "IngressRouteTCP"},
		{"traefik.io/v1alpha1", "Middleware"},
		{"traefik.io/v1alpha1", "TLSOption"},
		{"traefik.io/v1alpha1", "TLSStore"},
		{"traefik.io/v1alpha1", "ServersTransport"},
		{"storage.k8s.io/v1", "StorageClass"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			o := &unstructured.Unstructured{}
			o.SetAPIVersion(tc.apiVersion)
			o.SetKind(tc.kind)
			o.SetName("an-object")

			got := Assess(o)
			if got.Health != v1alpha1.HealthHealthy {
				t.Errorf("%s/%s = %s (%q): the Release would stay Unknown and everything that "+
					"depends on it would be blocked", tc.apiVersion, tc.kind, got.Health, got.Message)
			}
		})
	}
}

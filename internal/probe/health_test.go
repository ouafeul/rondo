package probe

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sjson "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/yaml"

	"rondo/api/v1alpha1"
)

func obj(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("invalid test YAML: %v", err)
	}
	return &unstructured.Unstructured{Object: m}
}

func assess(t *testing.T, doc, wantHealth, wantMsgFragment string) Assessment {
	t.Helper()
	a := Assess(obj(t, doc))
	if a.Health != wantHealth {
		t.Errorf("health = %q, want %q (message: %q)", a.Health, wantHealth, a.Message)
	}
	if wantMsgFragment != "" && !strings.Contains(a.Message, wantMsgFragment) {
		t.Errorf("message = %q, must contain %q", a.Message, wantMsgFragment)
	}
	return a
}

func TestDeploymentNeedsObservedGeneration(t *testing.T) {
	assess(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: grafana, namespace: monitoring, generation: 4}
spec: {replicas: 2}
status: {readyReplicas: 2, observedGeneration: 4}`, v1alpha1.HealthHealthy, "")

	assess(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: grafana, namespace: monitoring, generation: 5}
spec: {replicas: 2}
status: {readyReplicas: 2, observedGeneration: 4}`, v1alpha1.HealthProgressing, "generation not observed")

	assess(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: grafana, namespace: monitoring, generation: 1}
spec: {replicas: 3}
status: {readyReplicas: 1, observedGeneration: 1}`, v1alpha1.HealthProgressing, "1/3 replicas")

	assess(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: paused, namespace: default, generation: 1}
spec: {replicas: 0}
status: {observedGeneration: 1}`, v1alpha1.HealthHealthy, "zero")
}

func TestDaemonSetUsesStatusDesired(t *testing.T) {
	assess(t, `
apiVersion: apps/v1
kind: DaemonSet
metadata: {name: speaker, namespace: metallb-system}
status: {desiredNumberScheduled: 3, numberReady: 3}`, v1alpha1.HealthHealthy, "")

	assess(t, `
apiVersion: apps/v1
kind: DaemonSet
metadata: {name: speaker, namespace: metallb-system}
status: {desiredNumberScheduled: 3, numberReady: 1}`, v1alpha1.HealthProgressing, "1/3")

	assess(t, `
apiVersion: apps/v1
kind: DaemonSet
metadata: {name: speaker, namespace: metallb-system}
status: {desiredNumberScheduled: 0, numberReady: 0}`, v1alpha1.HealthProgressing, "no pod scheduled")
}

func TestPrometheusViaDeclaredRules(t *testing.T) {
	declared := declaredTable([]v1alpha1.HealthRule{
		{Group: "monitoring.coreos.com", Kind: "Prometheus", Condition: "Available"},
		{Group: "monitoring.coreos.com", Kind: "Alertmanager", Condition: "Available"},
		{Group: "monitoring.coreos.com", Kind: "PodMonitor"},
	})

	a := AssessWith(obj(t, `
apiVersion: monitoring.coreos.com/v1
kind: Prometheus
metadata: {name: kps, namespace: monitoring}
status:
  conditions:
    - {type: Available, status: "True"}`), declared)
	if a.Health != v1alpha1.HealthHealthy {
		t.Errorf("Prometheus = %q, want Healthy (%s)", a.Health, a.Message)
	}

	a = AssessWith(obj(t, `
apiVersion: monitoring.coreos.com/v1
kind: Alertmanager
metadata: {name: kps, namespace: monitoring}
status:
  conditions:
    - {type: Available, status: "False", reason: NoPodReady, message: "0/1 pods ready"}`),
		declared)
	if a.Health != v1alpha1.HealthDegraded || !strings.Contains(a.Message, "NoPodReady") {
		t.Errorf("Alertmanager = %q / %q, want Degraded and NoPodReady", a.Health, a.Message)
	}

	a = AssessWith(obj(t, `
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata: {name: kps, namespace: monitoring}`), declared)
	if a.Health != v1alpha1.HealthHealthy {
		t.Errorf("PodMonitor = %q, want Healthy (%s)", a.Health, a.Message)
	}

	a = Assess(obj(t, `
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata: {name: kps, namespace: monitoring}`))
	if a.Health != v1alpha1.HealthUnknown {
		t.Errorf("without a declared rule, PodMonitor = %q, want Unknown", a.Health)
	}
}

func TestDeclaredRulesCannotShadowTheBuiltInTable(t *testing.T) {
	declared := declaredTable([]v1alpha1.HealthRule{
		{Group: "apps", Kind: "Deployment"},
		{Group: "cert-manager.io", Kind: "Certificate"},
	})

	a := AssessWith(obj(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: d, namespace: n, generation: 1}
status: {observedGeneration: 1, readyReplicas: 0}
spec: {replicas: 2}`), declared)
	if a.Health == v1alpha1.HealthHealthy {
		t.Error("a Deployment with no ready replica judged healthy: the declared rule shadowed the table")
	}

	a = AssessWith(obj(t, `
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: c, namespace: n}
status:
  conditions:
    - {type: Ready, status: "False", reason: Failed}`), declared)
	if a.Health == v1alpha1.HealthHealthy {
		t.Error("a failed Certificate judged healthy: the declared rule shadowed the table")
	}
}

func TestCertManagerReady(t *testing.T) {
	assess(t, `
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: cluster-ca, namespace: cert-manager}
status:
  conditions:
    - {type: Ready, status: "True", reason: Ready, message: "Certificate is up to date"}`,
		v1alpha1.HealthHealthy, "")

	assess(t, `
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata: {name: cluster-ca-issuer}
status:
  conditions:
    - {type: Ready, status: "False", reason: Pending, message: "secret not found"}`,
		v1alpha1.HealthDegraded, "secret not found")
}

func TestStatelessKindsAreHealthyOnceApplied(t *testing.T) {
	for _, doc := range []string{`
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata: {name: downstream-pool, namespace: metallb-system}
spec: {addresses: ["10.0.0.50/32"]}`, `
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata: {name: downstream-l2, namespace: metallb-system}`, `
apiVersion: v1
kind: ConfigMap
metadata: {name: dashboards, namespace: monitoring}`, `
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata: {name: grafana, namespace: monitoring}`} {
		a := Assess(obj(t, doc))
		if a.Health != v1alpha1.HealthHealthy {
			t.Errorf("%s judged %q, want Healthy (kind without status)", a.GVK, a.Health)
		}
	}
}

func TestUnknownKindIsNeverHealthy(t *testing.T) {
	a := assess(t, `
apiVersion: acme.example.com/v1
kind: WidgetFactory
metadata: {name: mystery, namespace: default}
spec: {}`, v1alpha1.HealthUnknown, "not modelled")
	if !strings.Contains(a.Message, "internal/probe") {
		t.Errorf("the message must say where to add the kind: %q", a.Message)
	}

	a = assess(t, `
apiVersion: acme.example.com/v1
kind: WidgetFactory
metadata: {name: with-ready, namespace: default}
status:
  conditions:
    - {type: Ready, status: "True"}`, v1alpha1.HealthHealthy, "not modelled")
}

func TestPodFailureReasonIsUseful(t *testing.T) {
	assess(t, `
apiVersion: v1
kind: Pod
metadata: {name: grafana-0, namespace: monitoring}
status:
  phase: Pending
  containerStatuses:
    - name: grafana
      state:
        waiting: {reason: ImagePullBackOff, message: "back-off pulling image"}`,
		v1alpha1.HealthProgressing, "grafana: ImagePullBackOff")

	assess(t, `
apiVersion: v1
kind: Pod
metadata: {name: crash-0, namespace: default}
status:
  phase: Failed
  containerStatuses:
    - name: app
      state:
        terminated: {reason: Error}`, v1alpha1.HealthDegraded, "app: Error")

	assess(t, `
apiVersion: v1
kind: Pod
metadata: {name: ok, namespace: default}
status: {phase: Running}`, v1alpha1.HealthHealthy, "")
}

func TestPVCPendingIsCalledOut(t *testing.T) {
	assess(t, `
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: prometheus-db, namespace: monitoring}
status: {phase: Pending}`, v1alpha1.HealthProgressing, "storageClass")

	assess(t, `
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: prometheus-db, namespace: monitoring}
status: {phase: Bound}`, v1alpha1.HealthHealthy, "")
}

func TestJobAssessment(t *testing.T) {
	assess(t, `
apiVersion: batch/v1
kind: Job
metadata: {name: db-init, namespace: default}
status:
  conditions: [{type: Complete, status: "True"}]`, v1alpha1.HealthHealthy, "")

	assess(t, `
apiVersion: batch/v1
kind: Job
metadata: {name: db-init, namespace: default}
status:
  conditions: [{type: Failed, status: "True"}]`, v1alpha1.HealthDegraded, "pod logs")

	assess(t, `
apiVersion: batch/v1
kind: Job
metadata: {name: db-init, namespace: default}
status: {}`, v1alpha1.HealthProgressing, "running")
}

func TestAggregateOrdersBySeverity(t *testing.T) {
	cases := []struct {
		name string
		in   []Assessment
		want Health
	}{
		{"all healthy", []Assessment{
			{GVK: "Deployment", Name: "a", Health: v1alpha1.HealthHealthy},
			{GVK: "Service", Name: "b", Health: v1alpha1.HealthHealthy},
		}, v1alpha1.HealthHealthy},
		{"one degraded dominates", []Assessment{
			{GVK: "Deployment", Name: "a", Health: v1alpha1.HealthProgressing},
			{GVK: "Certificate", Name: "b", Health: v1alpha1.HealthDegraded, Message: "expired"},
			{GVK: "Widget", Name: "c", Health: v1alpha1.HealthUnknown},
		}, v1alpha1.HealthDegraded},
		{"unknown ranks above progressing", []Assessment{
			{GVK: "Deployment", Name: "a", Health: v1alpha1.HealthProgressing},
			{GVK: "Widget", Name: "c", Health: v1alpha1.HealthUnknown, Message: "not modelled"},
		}, v1alpha1.HealthUnknown},
		{"progressing alone", []Assessment{
			{GVK: "Deployment", Name: "a", Health: v1alpha1.HealthProgressing, Message: "1/3"},
		}, v1alpha1.HealthProgressing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := Aggregate(tc.in)
			if got != tc.want {
				t.Errorf("Aggregate = %q, want %q (msg %q)", got, tc.want, msg)
			}
			if tc.want != v1alpha1.HealthHealthy && msg == "" {
				t.Error("an unhealthy verdict must carry a message naming the culprit object")
			}
		})
	}
}

func TestAggregateEmptyIsUnknown(t *testing.T) {
	got, msg := Aggregate(nil)
	if got != v1alpha1.HealthUnknown {
		t.Errorf("Aggregate(empty) = %q, want Unknown", got)
	}
	if !strings.Contains(msg, "no object") {
		t.Errorf("message = %q, must report that there is no object", msg)
	}
}

func TestAggregateMessageNamesTheCulprit(t *testing.T) {
	_, msg := Aggregate([]Assessment{
		{GVK: "Deployment", Namespace: "monitoring", Name: "grafana", Health: v1alpha1.HealthHealthy},
		{GVK: "PersistentVolumeClaim", Namespace: "monitoring", Name: "prometheus-db",
			Health: v1alpha1.HealthProgressing, Message: "volume not provisioned"},
	})
	for _, want := range []string{"PersistentVolumeClaim", "monitoring/prometheus-db", "volume not provisioned"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message = %q, must contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "grafana") {
		t.Errorf("healthy objects must not clutter the message: %q", msg)
	}
}

func TestBothDecodersAgree(t *testing.T) {
	doc := []byte(`
apiVersion: apps/v1
kind: Deployment
metadata: {name: grafana, namespace: monitoring, generation: 4}
spec: {replicas: 3}
status: {readyReplicas: 3, observedGeneration: 4}`)

	var viaYAML map[string]any
	if err := yaml.Unmarshal(doc, &viaYAML); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	jsonDoc, err := yaml.YAMLToJSON(doc)
	if err != nil {
		t.Fatalf("YAMLToJSON: %v", err)
	}
	var viaAPI map[string]any
	if err := k8sjson.Unmarshal(jsonDoc, &viaAPI); err != nil {
		t.Fatalf("k8s json: %v", err)
	}

	yamlType := typeOf(t, viaYAML, "spec", "replicas")
	apiType := typeOf(t, viaAPI, "spec", "replicas")
	if yamlType == apiType {
		t.Fatalf("both decoders return %s: the test covers nothing", yamlType)
	}
	t.Logf("spec.replicas types, yaml:%s apiserver:%s", yamlType, apiType)

	a := Assess(&unstructured.Unstructured{Object: viaYAML})
	b := Assess(&unstructured.Unstructured{Object: viaAPI})
	if a.Health != b.Health {
		t.Errorf("verdicts differ by decoder: yaml=%q (%s) vs apiserver=%q (%s)",
			a.Health, a.Message, b.Health, b.Message)
	}
	if a.Health != v1alpha1.HealthHealthy {
		t.Errorf("3/3 replicas and generation observed => Healthy, got %q (%s)", a.Health, a.Message)
	}
}

func typeOf(t *testing.T, m map[string]any, fields ...string) string {
	t.Helper()
	raw, found, err := unstructured.NestedFieldNoCopy(m, fields...)
	if err != nil || !found {
		t.Fatalf("field %v missing", fields)
	}
	return fmt.Sprintf("%T", raw)
}

func TestOurOwnCRsUseTheCommonVocabulary(t *testing.T) {
	for _, kind := range []string{"Target", "Enrollment"} {
		doc := `
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: ` + kind + `
metadata: {name: x, namespace: rondo-system}
status:
  conditions: [{type: Ready, status: "True"}]`
		if a := Assess(obj(t, doc)); a.Health != v1alpha1.HealthHealthy {
			t.Errorf("%s judged %q, want Healthy", kind, a.Health)
		}
	}
}

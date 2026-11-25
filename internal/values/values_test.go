package values

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"rondo/api/v1alpha1"
)

func j(t *testing.T, s string) *apiextensionsv1.JSON {
	t.Helper()
	if s == "" {
		return nil
	}
	var probe any
	if err := json.Unmarshal([]byte(s), &probe); err != nil {
		t.Fatalf("invalid test JSON: %v", err)
	}
	return &apiextensionsv1.JSON{Raw: []byte(s)}
}

func decode(t *testing.T, in *apiextensionsv1.JSON) map[string]any {
	t.Helper()
	if in == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(in.Raw, &m); err != nil {
		t.Fatalf("unreadable result: %v", err)
	}
	return m
}

func TestMergeIsDeepForObjects(t *testing.T) {
	base := j(t, `{"service":{"type":"LoadBalancer","annotations":{"a":"1"}},"image":{"repository":"arti/metallb"}}`)
	overlay := j(t, `{"service":{"annotations":{"metallb.universe.tf/loadBalancerIPs":"10.0.0.50"}}}`)

	got, err := Merge(base, overlay)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	m := decode(t, got)
	svc := m["service"].(map[string]any)
	if svc["type"] != "LoadBalancer" {
		t.Errorf("service.type lost by the merge: %v", svc["type"])
	}
	ann := svc["annotations"].(map[string]any)
	if ann["a"] != "1" {
		t.Error("sibling annotation overwritten by the deep merge")
	}
	if ann["metallb.universe.tf/loadBalancerIPs"] != "10.0.0.50" {
		t.Error("overlay annotation missing")
	}
	if m["image"].(map[string]any)["repository"] != "arti/metallb" {
		t.Error("top-level key untouched by the overlay was lost")
	}
}

func TestMergeReplacesLists(t *testing.T) {
	got, err := Merge(
		j(t, `{"ingress":{"hosts":["a.local","b.local","c.local"]}}`),
		j(t, `{"ingress":{"hosts":["only.local"]}}`),
	)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	hosts := decode(t, got)["ingress"].(map[string]any)["hosts"].([]any)
	if len(hosts) != 1 || hosts[0] != "only.local" {
		t.Errorf("hosts = %v, want full replacement", hosts)
	}
}

func TestMergeIgnoresEmptyLayers(t *testing.T) {
	got, err := Merge(nil, j(t, `{"a":1}`), nil, &apiextensionsv1.JSON{})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if decode(t, got)["a"] != float64(1) {
		t.Errorf("non-empty layer lost: %v", decode(t, got))
	}
	got, err = Merge(nil, nil)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got != nil {
		t.Errorf("Merge(empty) = %v, want nil", got)
	}
}

func testTarget() *v1alpha1.Target {
	return &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "acme"},
		Spec: v1alpha1.TargetSpec{
			Kubernetes: &v1alpha1.KubernetesCluster{Server: "https://10.0.0.31:6443"},
			Values: map[string]string{
				"ingressIP":    "10.0.0.50",
				"storageClass": "longhorn",
			},
		},
	}
}

func TestResolveSubstitutesTargetTokens(t *testing.T) {
	in := j(t, `{
      "service":{"annotations":{"metallb.universe.tf/loadBalancerIPs":"${target.values.ingressIP}"}},
      "persistence":{"storageClassName":"${target.values.storageClass}"},
      "nameOverride":"kps-${target.name}",
      "externalUrl":"${target.server}/proxy"
    }`)
	got, err := Resolve(in, TargetVars(testTarget()))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	m := decode(t, got)
	if v := m["service"].(map[string]any)["annotations"].(map[string]any)["metallb.universe.tf/loadBalancerIPs"]; v != "10.0.0.50" {
		t.Errorf("ingressIP = %v, want 10.0.0.50", v)
	}
	if v := m["persistence"].(map[string]any)["storageClassName"]; v != "longhorn" {
		t.Errorf("storageClass = %v, want longhorn", v)
	}
	if v := m["nameOverride"]; v != "kps-acme" {
		t.Errorf("nameOverride = %v, want kps-acme", v)
	}
	if v := m["externalUrl"]; v != "https://10.0.0.31:6443/proxy" {
		t.Errorf("externalUrl = %v", v)
	}
}

func TestResolveFailsClosedOnUnknownToken(t *testing.T) {
	in := j(t, `{"a":"${target.values.missing}","b":"${target.unknown}"}`)
	_, err := Resolve(in, TargetVars(testTarget()))
	if err == nil {
		t.Fatal("unknown token accepted: the configuration would reach the cluster with holes")
	}
	var mv *MissingVarsError
	if !errors.As(err, &mv) {
		t.Fatalf("error of type %T, want *MissingVarsError", err)
	}
	if len(mv.Tokens) != 2 {
		t.Errorf("reported tokens = %v, want both", mv.Tokens)
	}
	msg := err.Error()
	for _, want := range []string{"target.values.missing", "target.unknown", "target.values.storageClass"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message = %q, must contain %q", msg, want)
		}
	}
}

func TestResolveLeavesForeignTokensIntact(t *testing.T) {
	in := j(t, `{
      "dashboard":{"expr":"sum(rate(x[${__interval}])) by (${namespace})","ds":"${DS_PROMETHEUS}"},
      "mixed":"${target.name}-${namespace}"
    }`)
	got, err := Resolve(in, TargetVars(testTarget()))
	if err != nil {
		t.Fatalf("Resolve failed on foreign tokens: %v", err)
	}
	m := decode(t, got)
	d := m["dashboard"].(map[string]any)
	if !strings.Contains(d["expr"].(string), "${__interval}") || !strings.Contains(d["expr"].(string), "${namespace}") {
		t.Errorf("Grafana tokens altered: %v", d["expr"])
	}
	if d["ds"] != "${DS_PROMETHEUS}" {
		t.Errorf("datasource altered: %v", d["ds"])
	}
	if m["mixed"] != "acme-${namespace}" {
		t.Errorf("mixed string = %v, want acme-${namespace}", m["mixed"])
	}
}

func TestResolveLeavesSecretTokensToTheJob(t *testing.T) {
	in := j(t, `{"adminPassword":"${secrets.grafanaAdminPassword}","name":"${target.name}"}`)
	got, err := Resolve(in, TargetVars(testTarget()))
	if err != nil {
		t.Fatalf("Resolve refused a ${secrets.} in chart values: %v", err)
	}
	m := decode(t, got)
	if m["adminPassword"] != "${secrets.grafanaAdminPassword}" {
		t.Errorf("adminPassword = %v, the token must reach the Job intact", m["adminPassword"])
	}
	if m["name"] != "acme" {
		t.Errorf("name = %v, target substitution must still happen", m["name"])
	}
}

func TestResolveHandlesKeysAndLists(t *testing.T) {
	in := j(t, `{
      "hosts":["${target.name}.local","fixed.local"],
      "perHost":{"${target.name}.local":{"tls":true}},
      "nested":[{"url":"${target.server}"}]
    }`)
	got, err := Resolve(in, TargetVars(testTarget()))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	m := decode(t, got)
	hosts := m["hosts"].([]any)
	if hosts[0] != "acme.local" || hosts[1] != "fixed.local" {
		t.Errorf("hosts = %v", hosts)
	}
	if _, ok := m["perHost"].(map[string]any)["acme.local"]; !ok {
		t.Errorf("object key not substituted: %v", m["perHost"])
	}
	if u := m["nested"].([]any)[0].(map[string]any)["url"]; u != "https://10.0.0.31:6443" {
		t.Errorf("value nested in a list not substituted: %v", u)
	}
}

func TestResolveTolerateMalformedTokens(t *testing.T) {
	in := j(t, `{"a":"price ${ 100","b":"${target.name}"}`)
	got, err := Resolve(in, TargetVars(testTarget()))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	m := decode(t, got)
	if m["a"] != "price ${ 100" {
		t.Errorf("a = %v, want unchanged", m["a"])
	}
	if m["b"] != "acme" {
		t.Errorf("b = %v, the rest must still be substituted", m["b"])
	}
}

func TestResolveEmptyIsNoop(t *testing.T) {
	for _, in := range []*apiextensionsv1.JSON{nil, {}} {
		got, err := Resolve(in, TargetVars(testTarget()))
		if err != nil {
			t.Fatalf("Resolve(empty): %v", err)
		}
		if got != in {
			t.Errorf("Resolve(empty) must return the input as is")
		}
	}
}

func TestHashIsOrderIndependent(t *testing.T) {
	a, err := Hash(j(t, `{"b":2,"a":{"y":1,"x":2}}`))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	b, err := Hash(j(t, `{"a":{"x":2,"y":1},"b":2}`))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if a != b {
		t.Errorf("different hashes for equivalent values: %s vs %s", a, b)
	}
	c, err := Hash(j(t, `{"a":{"x":3,"y":1},"b":2}`))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if c == a {
		t.Error("value change not reflected in the hash")
	}
	e1, _ := Hash(nil)
	e2, _ := Hash(&apiextensionsv1.JSON{})
	if e1 != e2 {
		t.Errorf("nil and empty give different hashes: %s vs %s", e1, e2)
	}
}

func TestMergeRejectsMalformedValues(t *testing.T) {
	bad := &apiextensionsv1.JSON{Raw: []byte(`{"a":`)}
	if _, err := Merge(bad); err == nil {
		t.Error("malformed values accepted by Merge")
	}
	if _, err := Resolve(bad, nil); err == nil {
		t.Error("malformed values accepted by Resolve")
	}
	if _, err := Hash(bad); err == nil {
		t.Error("malformed values accepted by Hash")
	}
}

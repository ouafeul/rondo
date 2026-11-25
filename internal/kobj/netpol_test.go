package kobj

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

func TestEveryServiceHasAnIngressRule(t *testing.T) {
	objs := testInstall().Objects()
	services := servicesOf(t, objs)
	if len(services) < 1 {
		t.Fatalf("%d Service(s) rendered: the guard compares nothing (did the manifests "+
			"change shape?)", len(services))
	}
	policies := ingressPolicies(t, objs)
	if len(policies) == 0 {
		t.Fatal("no ingress NetworkPolicy rendered: the guard compares nothing")
	}

	for name, svc := range services {
		var covered bool
		for _, pol := range policies {
			if selectorMatches(pol.selector, svc.selector) && pol.ports[svc.targetPort] {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("Service %s (selector %v, port %q) is allowed by NO "+
				"ingress rule: under the default deny it becomes unreachable and the "+
				"symptom will be a TIMEOUT, not a readable refusal.\nrendered policies: %s",
				name, svc.selector, svc.targetPort, describePolicies(policies))
		}
	}
}

func TestDefaultDenyExists(t *testing.T) {
	var found bool
	for _, obj := range testInstall().Objects() {
		if kind, _ := obj["kind"].(string); kind != "NetworkPolicy" {
			continue
		}
		spec, _ := obj["spec"].(Object)
		sel, _ := spec["podSelector"].(Object)
		if _, hasIngress := spec["ingress"]; hasIngress || len(sel) != 0 {
			continue
		}
		found = true
		types := strsOf(t, spec["policyTypes"])
		if len(types) != 1 || types[0] != "Ingress" {
			t.Errorf("default-deny policyTypes = %v: filtering EGRESS would cut "+
				"the Jobs off from their dependencies (mirror, vCenter, targets)", types)
		}
	}
	if !found {
		t.Error("no default-deny NetworkPolicy (empty podSelector, no ingress " +
			"rule): the named exceptions then exclude nothing at all")
	}
}

func TestNoIngressRuleAllowsEveryone(t *testing.T) {
	for _, pol := range ingressPolicies(t, testInstall().Objects()) {
		if !pol.hasFrom {
			t.Errorf("NetworkPolicy %q has an ingress rule without `from`: it "+
				"allows any pod in the cluster, so it excludes nothing", pol.name)
		}
	}
}

func TestJobsHaveNoRedundantRole(t *testing.T) {
	var admin bool
	for _, obj := range testInstall().Objects() {
		kind, _ := obj["kind"].(string)
		meta, _ := obj["metadata"].(Object)
		name, _ := meta["name"].(string)
		if kind == "Role" && name == ExecServiceAccount {
			t.Error("a Role is rendered for rondo-exec although its ServiceAccount holds " +
				"cluster-admin: it grants nothing more and suggests a least " +
				"privilege that does not exist")
		}
		if kind == "ClusterRoleBinding" {
			roleRef, _ := obj["roleRef"].(Object)
			if rn, _ := roleRef["name"].(string); rn == "cluster-admin" {
				admin = true
			}
		}
	}
	if !admin {
		t.Fatal("no cluster-admin binding on rondo-exec: installing a " +
			"chart that creates CRDs or ClusterRoles would fail. The check above " +
			"would then be meaningless")
	}
}

type svcInfo struct {
	selector   map[string]string
	targetPort string
}

type polInfo struct {
	name           string
	selector       map[string]string
	ports          map[string]bool
	hasFrom        bool
	fromApps       map[string]bool
	fromNamespaces map[string]bool
}

func servicesOf(t *testing.T, objs []Object) map[string]svcInfo {
	t.Helper()
	out := map[string]svcInfo{}
	for _, obj := range objs {
		if kind, _ := obj["kind"].(string); kind != "Service" {
			continue
		}
		meta, _ := obj["metadata"].(Object)
		name, _ := meta["name"].(string)
		spec, _ := obj["spec"].(Object)
		sel, _ := spec["selector"].(Object)
		ports, _ := spec["ports"].([]any)
		if len(ports) == 0 {
			t.Fatalf("Service %s has no port: unexpected shape", name)
		}
		p, _ := ports[0].(Object)
		target := fmt.Sprintf("%v", p["targetPort"])
		out[name] = svcInfo{selector: toStrMap(sel), targetPort: target}
	}
	return out
}

func ingressPolicies(t *testing.T, objs []Object) []polInfo {
	t.Helper()
	var out []polInfo
	for _, obj := range objs {
		if kind, _ := obj["kind"].(string); kind != "NetworkPolicy" {
			continue
		}
		meta, _ := obj["metadata"].(Object)
		name, _ := meta["name"].(string)
		spec, _ := obj["spec"].(Object)
		rules, _ := spec["ingress"].([]any)
		if len(rules) == 0 {
			continue
		}
		sel, _ := spec["podSelector"].(Object)
		info := polInfo{
			name:           name,
			selector:       toStrMap(matchLabelsOf(sel)),
			ports:          map[string]bool{},
			hasFrom:        true,
			fromApps:       map[string]bool{},
			fromNamespaces: map[string]bool{},
		}
		for _, raw := range rules {
			rule, _ := raw.(Object)
			from, _ := rule["from"].([]any)
			if len(from) == 0 {
				info.hasFrom = false
			}
			for _, rawPeer := range from {
				peer, _ := rawPeer.(Object)
				if ps, ok := peer["podSelector"].(Object); ok {
					if app, found := toStrMap(matchLabelsOf(ps))["app.kubernetes.io/name"]; found {
						info.fromApps[app] = true
					}
				}
				if nsSel, ok := peer["namespaceSelector"].(Object); ok {
					if n, found := toStrMap(matchLabelsOf(nsSel))["kubernetes.io/metadata.name"]; found {
						info.fromNamespaces[n] = true
					}
				}
			}
			ports, _ := rule["ports"].([]any)
			for _, rp := range ports {
				port, _ := rp.(Object)
				info.ports[fmt.Sprintf("%v", port["port"])] = true
			}
		}
		out = append(out, info)
	}
	return out
}

func selectorMatches(policy, service map[string]string) bool {
	if len(policy) == 0 {
		return false
	}
	for k, v := range policy {
		if service[k] != v {
			return false
		}
	}
	return true
}

func matchLabelsOf(sel Object) Object {
	ml, _ := sel["matchLabels"].(Object)
	return ml
}

func toStrMap(o Object) map[string]string {
	out := map[string]string{}
	for k, v := range o {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func describePolicies(pols []polInfo) string {
	var out []string
	for _, p := range pols {
		var ports []string
		for k := range p.ports {
			ports = append(ports, k)
		}
		sort.Strings(ports)
		out = append(out, fmt.Sprintf("%s -> %v on %v", p.name, p.selector, ports))
	}
	sort.Strings(out)
	return strings.Join(out, "\n  ")
}

func strsOf(t *testing.T, v any) []string {
	t.Helper()
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

// testInstall renders every object, the dashboard Ingress included.
func testInstall() PlatformInstall {
	return PlatformInstall{ControllerImage: "c:1", ToolboxImage: "t:1", DashboardHostname: "rondo.example.local"}
}

func TestNamespacedObjectsLiveInRondoSystem(t *testing.T) {
	install := PlatformInstall{ControllerImage: "c:1", ToolboxImage: "t:1"}
	for _, obj := range install.Objects() {
		meta, _ := obj["metadata"].(Object)
		kind, _ := obj["kind"].(string)
		ns, _ := meta["namespace"].(string)
		if kind == "Namespace" || kind == "ClusterRole" || kind == "ClusterRoleBinding" {
			continue
		}
		if ns != PlatformNamespace {
			t.Errorf("%s/%v: namespace = %q, want %q", kind, meta["name"], ns, PlatformNamespace)
		}
	}
}

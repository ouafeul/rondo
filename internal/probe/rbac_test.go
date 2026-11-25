package probe

import (
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"rondo/internal/kobj"
)

var statelessRBAC = map[string]struct {
	resource string
	inView   bool
}{
	"/ConfigMap":      {},
	"/Secret":         {},
	"/Service":        {},
	"/ServiceAccount": {},
	"/Namespace":      {resource: "namespaces", inView: true},

	"metallb.io/IPAddressPool":    {},
	"metallb.io/L2Advertisement":  {},
	"metallb.io/BGPAdvertisement": {},

	"rbac.authorization.k8s.io/Role":               {},
	"rbac.authorization.k8s.io/RoleBinding":        {},
	"rbac.authorization.k8s.io/ClusterRole":        {resource: "clusterroles"},
	"rbac.authorization.k8s.io/ClusterRoleBinding": {resource: "clusterrolebindings"},

	"networking.k8s.io/Ingress":       {},
	"networking.k8s.io/NetworkPolicy": {},
	"networking.k8s.io/IngressClass":  {resource: "ingressclasses"},
	"policy/PodDisruptionBudget":      {},

	"scheduling.k8s.io/PriorityClass":                             {resource: "priorityclasses"},
	"apiextensions.k8s.io/CustomResourceDefinition":               {resource: "customresourcedefinitions"},
	"admissionregistration.k8s.io/ValidatingWebhookConfiguration": {resource: "validatingwebhookconfigurations"},
	"admissionregistration.k8s.io/MutatingWebhookConfiguration":   {resource: "mutatingwebhookconfigurations"},

	"traefik.io/IngressRoute":     {},
	"traefik.io/IngressRouteTCP":  {},
	"traefik.io/Middleware":       {},
	"traefik.io/TLSOption":        {},
	"traefik.io/TLSStore":         {},
	"traefik.io/ServersTransport": {},

	"storage.k8s.io/StorageClass": {resource: "storageclasses"},
}

func TestEveryStatelessKindIsClassifiedForRBAC(t *testing.T) {
	for k := range statelessKinds {
		if _, ok := statelessRBAC[k]; !ok {
			t.Errorf("%s added to statelessKinds without being classified here: if it is "+
				"cluster-scoped and outside the built-in `view`, its informer will be denied", k)
		}
	}
	for k := range statelessRBAC {
		if !statelessKinds[k] {
			t.Errorf("%s classified here but missing from statelessKinds", k)
		}
	}
}

func TestProbeReadsClusterScopedKindsThatViewIgnores(t *testing.T) {
	granted := probeClusterReadRules(t)
	for k, want := range statelessRBAC {
		if want.resource == "" || want.inView {
			continue
		}
		group := k[:strings.Index(k, "/")]
		if !granted[group+"/"+want.resource] {
			t.Errorf("the probe ClusterRole does not grant %s/%s: the built-in `view` does not "+
				"cover this group, so the informer for %s will be denied",
				group, want.resource, k)
		}
	}
}

func probeClusterReadRules(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, o := range (kobj.PlatformInstall{}).Objects() {
		if o["kind"] != "ClusterRole" {
			continue
		}
		meta, _ := o["metadata"].(map[string]any)
		if meta["name"] != kobj.ProbeName {
			continue
		}
		rules, _ := o["rules"].([]any)
		for _, r := range rules {
			rule, _ := r.(map[string]any)
			groups, _ := rule["apiGroups"].([]any)
			resources, _ := rule["resources"].([]any)
			verbs, _ := rule["verbs"].([]any)
			if !hasAll(verbs, "get", "list", "watch") {
				continue
			}
			for _, g := range groups {
				for _, res := range resources {
					out[g.(string)+"/"+res.(string)] = true
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("no read rule found on ClusterRole %s", kobj.ProbeName)
	}
	return out
}

func hasAll(got []any, want ...string) bool {
	have := map[string]bool{}
	for _, v := range got {
		have[v.(string)] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}

func TestA403DoesNotMarkTheTargetDisconnected(t *testing.T) {
	tc := &targetCache{denied: map[string]bool{}}
	tc.onWatchError("mgmt", "CustomResourceDefinition", apierrors.NewForbidden(
		schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
		"", errors.New("forbidden")))

	if err := tc.stale(); err != nil {
		t.Errorf("target marked disconnected on a 403 (%v): all its Releases "+
			"would turn Unknown because of a single unreadable kind", err)
	}
}

func TestAnOutageDoesMarkTheTargetDisconnected(t *testing.T) {
	tc := &targetCache{denied: map[string]bool{}}
	tc.onWatchError("mgmt", "Deployment", errors.New("connection reset"))

	err := tc.stale()
	if err == nil {
		t.Fatal("watch outage not recorded: the cache would serve stale values")
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("cause = %q, must carry the watch error", err)
	}
}

func TestA403IsReportedOnlyOnce(t *testing.T) {
	tc := &targetCache{denied: map[string]bool{}}
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "storageclasses"}, "",
		errors.New("forbidden"))

	if !tc.noteDenied("StorageClass") {
		t.Fatal("first occurrence not reported")
	}
	tc.onWatchError("mgmt", "StorageClass", forbidden)
	if tc.noteDenied("StorageClass") {
		t.Error("403 reported twice for the same kind")
	}
}

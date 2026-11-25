package kobj

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var rbacMarker = regexp.MustCompile(
	`\+kubebuilder:rbac:groups=([^,]*),resources=([^,]*),verbs=([^\s]+)`)

type rbacRule struct {
	group    string
	resource string
	verb     string
	where    string
}

func (r rbacRule) String() string {
	g := r.group
	if g == "" {
		g = "core"
	}
	return fmt.Sprintf("%s/%s:%s", g, r.resource, r.verb)
}

func TestRenderedRBACCoversEveryMarker(t *testing.T) {
	markers := parseRBACMarkers(t, filepath.Join("..", "controllers"))
	if len(markers) == 0 {
		t.Fatal("no +kubebuilder:rbac marker found in internal/controllers: " +
			"the guard checks nothing (package moved?)")
	}

	granted := grantedRules(t)
	if len(granted) == 0 {
		t.Fatal("no RBAC rule rendered: the guard compares nothing")
	}

	var missing []string
	for key, rule := range markers {
		if !granted[key] {
			missing = append(missing, fmt.Sprintf("%s (declared in %s)", rule, rule.where))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the RBAC rendered by internal/bootstrap does not cover %d marker(s):\n  %s\n\n"+
			"An uncovered marker is a `Forbidden` on a real cluster, and envtest "+
			"runs as admin: NO behaviour test can catch it. Extend "+
			"controllerRole, or remove the marker if it "+
			"describes a permission the controller does not need.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

func parseRBACMarkers(t *testing.T, dir string) map[string]rbacRule {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := map[string]rbacRule{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, m := range rbacMarker.FindAllStringSubmatch(string(data), -1) {
			group := strings.Trim(m[1], `"`)
			for _, res := range strings.Split(m[2], ";") {
				for _, verb := range strings.Split(m[3], ";") {
					r := rbacRule{group: group, resource: res, verb: verb, where: name}
					out[r.String()] = r
				}
			}
		}
	}
	return out
}

func grantedRules(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	var roles []Object
	for _, obj := range testInstall().Objects() {
		kind, _ := obj["kind"].(string)
		if kind != "Role" && kind != "ClusterRole" {
			continue
		}
		meta, _ := obj["metadata"].(Object)
		if name, _ := meta["name"].(string); strings.HasPrefix(name, ControllerName) {
			roles = append(roles, obj)
		}
	}
	if len(roles) == 0 {
		t.Fatalf("%d controller role(s) found: the selection finds nothing, "+
			"the guard would compare an empty set", len(roles))
	}
	for _, obj := range roles {
		rules, ok := obj["rules"].([]any)
		if !ok {
			meta, _ := obj["metadata"].(Object)
			t.Fatalf("%v: rules missing or mistyped", meta["name"])
		}
		for _, raw := range rules {
			rule, ok := raw.(Object)
			if !ok {
				t.Fatalf("mistyped rule: %#v", raw)
			}
			for _, g := range strs(t, rule["apiGroups"]) {
				for _, res := range strs(t, rule["resources"]) {
					for _, verb := range strs(t, rule["verbs"]) {
						out[rbacRule{group: g, resource: res, verb: verb}.String()] = true
					}
				}
			}
		}
	}
	return out
}

func strs(t *testing.T, v any) []string {
	t.Helper()
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("rule field should be a list, got %#v", v)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("non-string rule item: %#v", item)
		}
		out = append(out, s)
	}
	return out
}

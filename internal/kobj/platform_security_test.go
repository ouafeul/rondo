package kobj

import (
	"fmt"
	"testing"
)

func podSpecs(t *testing.T, objs []Object) map[string]Object {
	t.Helper()
	out := map[string]Object{}
	for _, o := range objs {
		kind, _ := o["kind"].(string)
		if kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet" {
			continue
		}
		meta, _ := o["metadata"].(Object)
		name, _ := meta["name"].(string)
		spec, ok := o["spec"].(Object)
		if !ok {
			t.Fatalf("%s/%s: spec missing", kind, name)
		}
		tmpl, ok := spec["template"].(Object)
		if !ok {
			t.Fatalf("%s/%s: spec.template missing", kind, name)
		}
		ps, ok := tmpl["spec"].(Object)
		if !ok {
			t.Fatalf("%s/%s: spec.template.spec missing", kind, name)
		}
		out[fmt.Sprintf("%s/%s", kind, name)] = ps
	}
	return out
}

func TestEveryNonRootPodDeclaresANumericUser(t *testing.T) {
	objs := testInstall().Objects()

	specs := podSpecs(t, objs)
	if len(specs) == 0 {
		t.Fatal("no pod rendered, the test checked NOTHING (empty selection)")
	}
	t.Logf("%d pods examined", len(specs))

	for path, ps := range specs {
		sc, ok := ps["securityContext"].(Object)
		if !ok {
			t.Errorf("%s: no pod securityContext", path)
			continue
		}
		if sc["runAsNonRoot"] == nil {
			t.Errorf("%s: runAsNonRoot missing", path)
			continue
		}
		uid, present := sc["runAsUser"]
		if !present {
			t.Errorf("%s: runAsNonRoot required but runAsUser MISSING, the kubelet will refuse "+
				"the container if the image declares its user by name "+
				"(\"cannot verify user is non-root\")", path)
			continue
		}
		switch v := uid.(type) {
		case int, int32, int64:
		default:
			t.Errorf("%s: runAsUser = %v (%T), must be NUMERIC, the kubelet cannot "+
				"verify a name", path, uid, v)
		}
	}
}

func TestProbeCanReadRBACObjects(t *testing.T) {
	objs := testInstall().Objects()

	var rules []any
	for _, o := range objs {
		if kind, _ := o["kind"].(string); kind != "ClusterRole" {
			continue
		}
		meta, _ := o["metadata"].(Object)
		name, _ := meta["name"].(string)
		if name != "rondo-probe" && name != ProbeName {
			continue
		}
		rules, _ = o["rules"].([]any)
	}
	if rules == nil {
		t.Fatalf("no probe ClusterRole among the rendered objects, the test checked NOTHING")
	}

	need := map[string]bool{"clusterroles": false, "clusterrolebindings": false,
		"roles": false, "rolebindings": false}
	for _, raw := range rules {
		r, ok := raw.(Object)
		if !ok {
			continue
		}
		groups, _ := r["apiGroups"].([]any)
		hasRBAC := false
		for _, g := range groups {
			if g == "rbac.authorization.k8s.io" {
				hasRBAC = true
			}
		}
		if !hasRBAC {
			continue
		}
		res, _ := r["resources"].([]any)
		for _, x := range res {
			if name, ok := x.(string); ok {
				if _, wanted := need[name]; wanted {
					need[name] = true
				}
			}
		}
	}
	for name, granted := range need {
		if !granted {
			t.Errorf("rondo-probe cannot read %q: `view` excludes it, so any Enrollment "+
				"delivering this kind would stay Ready=False on \"cannot read\"", name)
		}
	}
}

func TestProbeCanWatchWhatItReads(t *testing.T) {
	objs := testInstall().Objects()

	checked := 0
	for _, o := range objs {
		kind, _ := o["kind"].(string)
		if kind != "Role" && kind != "ClusterRole" {
			continue
		}
		meta, _ := o["metadata"].(Object)
		if name, _ := meta["name"].(string); name != ProbeName {
			continue
		}
		rules, _ := o["rules"].([]any)
		for _, raw := range rules {
			r, ok := raw.(Object)
			if !ok {
				continue
			}
			verbs, _ := r["verbs"].([]any)
			has := map[string]bool{}
			for _, v := range verbs {
				if s, ok := v.(string); ok {
					has[s] = true
				}
			}
			if !has["get"] || has["patch"] {
				continue
			}
			checked++
			if !has["list"] || !has["watch"] {
				t.Errorf("%s %s, resources %v: verbs %v, an informer needs `list` "+
					"AND `watch`, or it fails at startup", kind, ProbeName,
					r["resources"], verbs)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no probe read rule found, the test checked NOTHING")
	}
}

func TestProbeCanPublishEnrollmentHealth(t *testing.T) {
	granted := map[string]bool{}
	for _, o := range testInstall().Objects() {
		meta, _ := o["metadata"].(Object)
		if kind, _ := o["kind"].(string); kind != "Role" || meta["name"] != ProbeName {
			continue
		}
		rules, _ := o["rules"].([]any)
		for _, raw := range rules {
			r, ok := raw.(Object)
			if !ok {
				continue
			}
			for _, res := range strs(t, r["resources"]) {
				for _, verb := range strs(t, r["verbs"]) {
					granted[res+":"+verb] = true
				}
			}
		}
	}
	for _, need := range []string{"enrollments:list", "enrollments:watch",
		"enrollments/status:patch", "targets:get", "components:get"} {
		if !granted[need] {
			t.Errorf("Role %s does not grant %s: the probe pass is refused, an Enrollment's "+
				"health stays frozen on its Job's (a ClusterIssuer not yet "+
				"ready at helm upgrade time stays Degraded)", ProbeName, need)
		}
	}
}

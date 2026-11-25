package controllers

import (
	"context"
	"go/ast"
	"go/token"
	"reflect"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"rondo/api/v1alpha1"
)

func TestEnrollmentsForWakesOnlyAffected(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.createComponent(t, "kps", "83.3.0", "")
	h.createComponent(t, "traefik", "1.0.0", "")
	h.createEnrollment(t, "kps-mon", "kps", "monitoring", "", nil)
	h.createEnrollment(t, "traefik-edge", "traefik", "edge", "", nil)

	comp := &v1alpha1.Component{ObjectMeta: metav1.ObjectMeta{Name: "kps", Namespace: h.ns}}
	if got, want := names(h.enrollment.enrollmentsFor(ctx, comp)), []string{"kps-mon"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Component: %v, want %v (a chart bump would no longer propagate)", got, want)
	}
	target := &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "acme", Namespace: h.ns},
		Spec:       v1alpha1.TargetSpec{Roles: []string{"monitoring"}},
	}
	if got, want := names(h.enrollment.enrollmentsFor(ctx, target)), []string{"kps-mon"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Target: %v, want %v (a new target would only be placed on the "+
			"periodic pass)", got, want)
	}
}

func TestTargetsForSecretWakesOnlyReferencingTargets(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, tc := range []struct{ name, secret string }{
		{"downstream", "kubeconfig-downstream"},
		{"other", "kubeconfig-other"},
	} {
		target := &v1alpha1.Target{
			ObjectMeta: metav1.ObjectMeta{Name: tc.name, Namespace: h.ns},
			Spec: v1alpha1.TargetSpec{Kubernetes: &v1alpha1.KubernetesCluster{
				KubeconfigSecretRef: &v1alpha1.SecretKeySelector{Name: tc.secret, Key: "value"},
			}},
		}
		if err := h.client.Create(ctx, target); err != nil {
			t.Fatalf("creating Target %s: %v", tc.name, err)
		}
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "kubeconfig-downstream", Namespace: h.ns}}
	got := names(h.target.targetsForSecret(ctx, secret))
	if want := []string{"downstream"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("targetsForSecret = %v, want %v: a downstream's kubeconfig is "+
			"written AFTER its provisioning, so without this wakeup the target stays failed "+
			"even though its Secret just arrived", got, want)
	}
}

var wantWatches = map[string][]string{
	"EnrollmentReconciler": {
		"For:Enrollment", "Owns:Job",
		"Watches:Target:enrollmentsFor",
		"Watches:Component:enrollmentsFor",
	},
	"TargetReconciler": {
		"For:Target", "Watches:Secret:targetsForSecret",
	},
}

func TestExpectedWakeupsAreWired(t *testing.T) {
	fset := token.NewFileSet()
	files := parsePackageSources(t, fset, ".")
	got := map[string][]string{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "SetupWithManager" || fn.Recv == nil {
				return true
			}
			got[receiverType(fn)] = watchesOf(fn)
			return true
		})
	}
	if len(got) == 0 {
		t.Fatal("no SetupWithManager found: the guard checks nothing anymore")
	}

	for name, want := range wantWatches {
		have, ok := got[name]
		if !ok {
			t.Errorf("%s no longer declares SetupWithManager", name)
			continue
		}
		sort.Strings(want)
		sort.Strings(have)
		if !reflect.DeepEqual(want, have) {
			t.Errorf("%s:\n  wired = %v\n  want  = %v\n"+
				"A removed wakeup breaks no behaviour test: the periodic requeues "+
				"catch up, so everything stays green and only the latency changes "+
				"(immediate -> 5 min). If the change is intended, update "+
				"wantWatches, and say in the commit what the platform stops "+
				"observing.", name, have, want)
		}
	}
	for name := range got {
		if _, ok := wantWatches[name]; !ok {
			t.Errorf("%s declares SetupWithManager but is not in wantWatches: "+
				"nothing guards its wakeups", name)
		}
	}
}

func receiverType(fn *ast.FuncDecl) string {
	if len(fn.Recv.List) == 0 {
		return ""
	}
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func watchesOf(fn *ast.FuncDecl) []string {
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		switch sel.Sel.Name {
		case "For", "Owns":
			out = append(out, sel.Sel.Name+":"+kindOf(call.Args[0]))
		case "Watches":
			entry := "Watches:" + kindOf(call.Args[0])
			if len(call.Args) > 1 {
				entry += ":" + mapFuncOf(call.Args[1])
			}
			out = append(out, entry)
		}
		return true
	})
	return out
}

func kindOf(e ast.Expr) string {
	if u, ok := e.(*ast.UnaryExpr); ok {
		e = u.X
	}
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return "?"
	}
	if sel, ok := lit.Type.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return "?"
}

func mapFuncOf(e ast.Expr) string {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return "?"
	}
	switch arg := call.Args[0].(type) {
	case *ast.SelectorExpr:
		return arg.Sel.Name
	case *ast.Ident:
		return arg.Name
	}
	return "?"
}

func names(reqs []reconcile.Request) []string {
	if reqs == nil {
		return nil
	}
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Name)
	}
	sort.Strings(out)
	return out
}

func TestFailedListIsNotReadAsNoEnrollment(t *testing.T) {
	fset := token.NewFileSet()
	files := parsePackageSources(t, fset, ".")
	mappers := map[string]string{
		"enrollmentsFor":   "enrollment.go",
		"targetsForSecret": "target.go",
	}
	for fname, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			if want, ok := mappers[fn.Name.Name]; !ok || want != fname {
				return true
			}
			if len(callsTo(fn.Body, "log", "FromContext")) == 0 {
				t.Errorf("%s:%s swallows its List error by returning nil: a failed "+
					"List is then indistinguishable from \"no object\". The controller "+
					"seems deaf, and nothing anywhere says why.", fname, fn.Name.Name)
			}
			delete(mappers, fn.Name.Name)
			return true
		})
	}
	if len(mappers) > 0 {
		var missing []string
		for k := range mappers {
			missing = append(missing, k)
		}
		sort.Strings(missing)
		t.Errorf("mapping functions not found: %s, the guard no longer watches "+
			"what it thinks it watches", strings.Join(missing, ", "))
	}
}

package kobj

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func nested(t *testing.T, o Object, path ...string) any {
	t.Helper()
	var cur any = o
	for _, p := range path {
		m, ok := cur.(Object)
		if !ok {
			t.Fatalf("%v: %q is not an object (%T)", path, p, cur)
		}
		cur = m[p]
	}
	return cur
}

func TestSharedImportsOnlyTheStandardLibrary(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") || first == "rondo" {
				t.Errorf("%s imports %s: kobj stays standard-library only, cmd/rondo "+
					"must not pull apimachinery or an rondo package through it", f, path)
			}
		}
	}
}

func TestObjectsAreSerialisable(t *testing.T) {
	for _, inst := range []PlatformInstall{{}, {DashboardHostname: "rondo.example.org"}} {
		for _, o := range inst.Objects() {
			if _, err := json.Marshal(o); err != nil {
				t.Errorf("%v/%v : %v", o["kind"], nested(t, o, "metadata", "name"), err)
			}
			if o["apiVersion"] == nil || o["kind"] == nil || nested(t, o, "metadata", "name") == nil {
				t.Errorf("object without apiVersion, kind or name: %v", o)
			}
		}
	}
}

func TestIngressOnlyWithAHostname(t *testing.T) {
	kinds := func(inst PlatformInstall) map[string]bool {
		out := map[string]bool{}
		for _, o := range inst.Objects() {
			out[o["kind"].(string)] = true
		}
		return out
	}
	if kinds(PlatformInstall{})["Ingress"] {
		t.Error("an Ingress is rendered without a hostname")
	}
	if !kinds(PlatformInstall{DashboardHostname: "rondo.example.org"})["Ingress"] {
		t.Error("no Ingress although a hostname is given")
	}
}

func TestOnlyTheControllerAndTheProbeRecreate(t *testing.T) {
	for _, o := range (PlatformInstall{}).Objects() {
		if o["kind"] != "Deployment" {
			continue
		}
		name := nested(t, o, "metadata", "name")
		_, recreate := nested(t, o, "spec").(Object)["strategy"]
		if want := name != DashboardName; recreate != want {
			t.Errorf("Deployment %v: strategy Recreate = %v, want %v", name, recreate, want)
		}
	}
}

func TestObjectsDoNotShareState(t *testing.T) {
	a, b := (PlatformInstall{}).Objects(), (PlatformInstall{}).Objects()
	a[0]["kind"] = "mutated"
	if b[0]["kind"] == "mutated" {
		t.Error("two renders share the same map")
	}
	sa, sb := probeRBACClusterRole(), probeRBACClusterRole()
	sa["rules"].([]any)[0].(Object)["verbs"].([]any)[0] = "mutated"
	if sb["rules"].([]any)[0].(Object)["verbs"].([]any)[0] == "mutated" {
		t.Error("two ClusterRoles share the same verbs slice")
	}
}

func TestSecretAndConfigMapShapes(t *testing.T) {
	s := OpaqueSecret("n", map[string]string{"k": "v"})
	if s["kind"] != "Secret" || s["type"] != "Opaque" || nested(t, s, "stringData").(Object)["k"] != "v" {
		t.Errorf("Secret = %v", s)
	}
	cm := configMap("n", "ns", map[string]string{"k": "v"})
	if cm["kind"] != "ConfigMap" || nested(t, cm, "data").(Object)["k"] != "v" {
		t.Errorf("ConfigMap = %v", cm)
	}
	if nested(t, cm, "metadata", "namespace") != "ns" {
		t.Errorf("ConfigMap without namespace: %v", cm)
	}
}

func TestMirrorObjectsMatchWhatTheJobsRead(t *testing.T) {
	c := MirrorCredentials("u", "p", "", "")
	sd := nested(t, c, "stringData").(Object)
	if nested(t, c, "metadata", "name") != CredentialsSecret || sd["MIRROR_USER"] != "u" || sd["MIRROR_PASSWORD"] != "p" {
		t.Errorf("credentials = %v", c)
	}
	ca := MirrorCA("PEM")
	if nested(t, ca, "metadata", "name") != MirrorCACM || nested(t, ca, "data").(Object)["ca.pem"] != "PEM" {
		t.Errorf("CA = %v", ca)
	}
}

func TestTofuMirrorKeepsTheTokenOnlyWhenAsked(t *testing.T) {
	with := nested(t, TofuMirror("provider_installation {}\n", "tok", true), "stringData").(Object)
	without := nested(t, TofuMirror("provider_installation {}\n", "tok", false), "stringData").(Object)
	if with["token"] != "tok" {
		t.Errorf("token missing although it must be kept: %v", with)
	}
	if _, ok := without["token"]; ok {
		t.Errorf("token kept although it comes from an override: %v", without)
	}
	if rc, _ := with[".tofurc"].(string); rc != "provider_installation {}\n" {
		t.Errorf(".tofurc = %q, want the one given", rc)
	}
}

func TestTargetCarriesBackendRolesAndValues(t *testing.T) {
	tg := Target("t", []string{"a", "b"}, KubeconfigBackend("https://k", "rondo-kubeconfig-t"), map[string]string{"x": "1"})
	if tg["apiVersion"] != "rondo.ouafeul.github.io/v1alpha1" || tg["kind"] != "Target" {
		t.Errorf("Target = %v", tg)
	}
	if nested(t, tg, "metadata", "namespace") != PlatformNamespace {
		t.Errorf("namespace = %v", nested(t, tg, "metadata", "namespace"))
	}
	spec := nested(t, tg, "spec").(Object)
	if roles := spec["roles"].([]any); len(roles) != 2 || roles[0] != "a" {
		t.Errorf("roles = %v", spec["roles"])
	}
	if spec["values"].(Object)["x"] != "1" {
		t.Errorf("values = %v", spec["values"])
	}
	ref := nested(t, tg, "spec", "kubernetes", "kubeconfigSecretRef").(Object)
	if ref["name"] != "rondo-kubeconfig-t" || ref["key"] != "kubeconfig" {
		t.Errorf("kubeconfigSecretRef = %v", ref)
	}
}

func TestTargetWithoutRolesOmitsThem(t *testing.T) {
	spec := nested(t, Target("t", nil, VSphereBackend("rondo-vsphere-t"), nil), "spec").(Object)
	if _, ok := spec["roles"]; ok {
		t.Errorf("roles present without any role: %v", spec)
	}
	if nested(t, Object{"s": spec}, "s", "vsphere", "settingsConfigMapRef").(Object)["name"] != "rondo-vsphere-t" {
		t.Errorf("vsphere = %v", spec)
	}
	if !nested(t, Object{"s": InClusterBackend()}, "s", "kubernetes", "inCluster").(bool) {
		t.Error("inCluster missing")
	}
}

func TestNamedObjects(t *testing.T) {
	if got := VSphereObjectName("z"); got != "rondo-vsphere-z" {
		t.Errorf("VSphereObjectName = %q", got)
	}
	if got := KubeconfigSecretName("z"); got != "rondo-kubeconfig-z" {
		t.Errorf("KubeconfigSecretName = %q", got)
	}
	if got := RecordName("base-z"); got != "rondo-component-base-z" {
		t.Errorf("RecordName = %q", got)
	}
	if o := VSphereCredentials("z", "u", "p"); nested(t, o, "metadata", "name") != "rondo-vsphere-z" ||
		nested(t, o, "stringData").(Object)["vsphere_password"] != "p" {
		t.Errorf("VSphereCredentials = %v", o)
	}
	if o := VSphereSettings("z", map[string]string{"a": "b"}); nested(t, o, "metadata", "name") != "rondo-vsphere-z" {
		t.Errorf("VSphereSettings = %v", o)
	}
	if o := KubeconfigSecret("z", "KC"); nested(t, o, "stringData").(Object)["kubeconfig"] != "KC" {
		t.Errorf("KubeconfigSecret = %v", o)
	}
	if o := VMSSHKey("PRIV", "PUB"); nested(t, o, "metadata", "name") != VMSSHSecret ||
		nested(t, o, "stringData").(Object)["ssh-publickey"] != "PUB" {
		t.Errorf("VMSSHKey = %v", o)
	}
}

func TestRegcredLivesInFleetDefault(t *testing.T) {
	r := Regcred("u", "p")
	if nested(t, r, "metadata", "namespace") != "fleet-default" || r["type"] != "kubernetes.io/basic-auth" {
		t.Errorf("Regcred = %v", r)
	}
}

func TestCoreDNSCustomAndTemplate(t *testing.T) {
	cm := CoreDNSCustom("X")
	if nested(t, cm, "metadata", "name") != "coredns-custom" || nested(t, cm, "metadata", "namespace") != "kube-system" ||
		nested(t, cm, "data").(Object)["rondo-ingress-wildcard.override"] != "X" {
		t.Errorf("CoreDNSCustom = %v", cm)
	}
	got := CoreDNSTemplate("^a[.]$", "10.0.0.1")
	for _, want := range []string{"template IN A {", "match ^a[.]$", "60 IN A 10.0.0.1", "fallthrough"} {
		if !strings.Contains(got, want) {
			t.Errorf("template without %q:\n%s", want, got)
		}
	}
}

func TestComponentRecordListsObjects(t *testing.T) {
	r := ComponentRecord("base-z", "v1", []string{"Component/a", "Enrollment/b"})
	data := nested(t, r, "data").(Object)
	if data["version"] != "v1" || data["objects"] != "Component/a\nEnrollment/b" {
		t.Errorf("record = %v", data)
	}
	if nested(t, r, "metadata", "name") != "rondo-component-base-z" {
		t.Errorf("record = %v", r)
	}
}

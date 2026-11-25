package synth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type stubComponent struct {
	name     string
	objs     []Object
	valErr   error
	manErr   error
	valCalls *int
}

func (s stubComponent) ComponentName() string { return s.name }

func (s stubComponent) Validate() error {
	if s.valCalls != nil {
		*s.valCalls++
	}
	return s.valErr
}

func (s stubComponent) Manifests() ([]Object, error) {
	if s.manErr != nil {
		return nil, s.manErr
	}
	return s.objs, nil
}

func cm(name string) Object {
	return Object{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": Object{"name": name},
	}
}

func TestMarshalSeparatesDocuments(t *testing.T) {
	raw, err := Marshal([]Object{cm("a"), cm("b")})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(raw)
	if n := strings.Count(got, "\n---\n"); n != 1 {
		t.Errorf("%d separators for 2 objects:\n%s", n, got)
	}
	if strings.HasPrefix(got, "---") {
		t.Errorf("leading separator:\n%s", got)
	}
	if !strings.Contains(got, "name: a") || !strings.Contains(got, "name: b") {
		t.Errorf("objects missing:\n%s", got)
	}
}

func TestMarshalEmpty(t *testing.T) {
	raw, err := Marshal(nil)
	if err != nil {
		t.Fatalf("Marshal(nil): %v", err)
	}
	if len(raw) != 0 {
		t.Errorf("non-empty output for zero objects: %q", raw)
	}
}

func TestRenderAggregatesInOrder(t *testing.T) {
	objs, err := Render(
		stubComponent{name: "pki", objs: []Object{cm("ca")}},
		stubComponent{name: "metallb", objs: []Object{cm("pool"), cm("l2")}},
	)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(objs) != 3 {
		t.Fatalf("%d objects, want 3", len(objs))
	}
	if objs[0]["metadata"].(Object)["name"] != "ca" {
		t.Errorf("order = %v", objs)
	}
}

func TestRenderNamesTheFaultyConstruct(t *testing.T) {
	_, err := Render(
		stubComponent{name: "good", objs: []Object{cm("x")}},
		stubComponent{name: "faulty", valErr: errors.New("required field missing")},
	)
	if err == nil {
		t.Fatal("invalid construct accepted")
	}
	if !strings.Contains(err.Error(), "faulty") {
		t.Errorf("message = %q, must name the construct", err)
	}
	if !strings.Contains(err.Error(), "validation") {
		t.Errorf("message = %q, must say which phase failed", err)
	}
}

func TestRenderSurfacesManifestError(t *testing.T) {
	_, err := Render(stubComponent{name: "faulty", manErr: errors.New("broken template")})
	if err == nil {
		t.Fatal("generation failure swallowed")
	}
	if !strings.Contains(err.Error(), "manifests") || !strings.Contains(err.Error(), "faulty") {
		t.Errorf("message = %q", err)
	}
}

func TestSynthWritesOneFilePerConstruct(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub", "dir")
	written, err := Synth(dir,
		stubComponent{name: "pki-stack", objs: []Object{cm("ca")}},
		stubComponent{name: "metallb-config", objs: []Object{cm("pool")}},
	)
	if err != nil {
		t.Fatalf("Synth: %v", err)
	}
	if len(written) != 2 {
		t.Fatalf("%d files, want 2: %v", len(written), written)
	}
	for _, p := range written {
		if filepath.Dir(p) != dir {
			t.Errorf("file outside the requested directory: %s", p)
		}
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) == 0 {
			t.Errorf("empty file: %s", p)
		}
	}
	if filepath.Base(written[0]) != "pki-stack.yaml" {
		t.Errorf("file name = %s, want the construct's", written[0])
	}
}

func TestSynthValidatesBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	_, err := Synth(dir, stubComponent{name: "faulty", valErr: errors.New("name not kebab-case")})
	if err == nil {
		t.Fatal("invalid construct accepted")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("%d files written despite the validation failure", len(entries))
	}
}

func TestSynthValidatesEveryConstruct(t *testing.T) {
	calls := 0
	if _, err := Synth(t.TempDir(),
		stubComponent{name: "a", objs: []Object{cm("x")}, valCalls: &calls},
		stubComponent{name: "b", objs: []Object{cm("y")}, valCalls: &calls},
	); err != nil {
		t.Fatalf("Synth: %v", err)
	}
	if calls != 2 {
		t.Errorf("%d validations for 2 constructs", calls)
	}
}

func TestSynthSurfacesManifestError(t *testing.T) {
	if _, err := Synth(t.TempDir(),
		stubComponent{name: "f", manErr: errors.New("boom")}); err == nil {
		t.Fatal("generation failure swallowed")
	}
}

func TestStdLabelsIdentifyGeneratedObjects(t *testing.T) {
	got := StdLabels("cluster-ca", "PKIStack")
	if got["app.kubernetes.io/managed-by"] != "rondo-synth" {
		t.Errorf("managed-by = %q", got["app.kubernetes.io/managed-by"])
	}
	if got["rondo.ouafeul.github.io/component-name"] != "cluster-ca" {
		t.Errorf("component-name = %q", got["rondo.ouafeul.github.io/component-name"])
	}
	if got["rondo.ouafeul.github.io/component-type"] != "PKIStack" {
		t.Errorf("component-type = %q", got["rondo.ouafeul.github.io/component-type"])
	}
	if got["rondo.ouafeul.github.io/synth-version"] == "" {
		t.Error("empty synth-version")
	}
}

func TestValidatorCustomRules(t *testing.T) {
	type sample struct {
		Name    string `validate:"kebab-case"`
		Version string `validate:"semver"`
	}
	v := Validator()
	if v == nil {
		t.Fatal("nil validator")
	}
	for _, ok := range []sample{
		{"metallb", "0.14.9"},
		{"kube-prometheus-stack", "83.3.0"},
		{"cert-manager", "v1.16.2"},
		{"victoria-logs", "0.11.31-rc1"},
	} {
		if err := v.Struct(ok); err != nil {
			t.Errorf("%+v rejected: %v", ok, err)
		}
	}
	for _, bad := range []sample{
		{"MetalLB", "0.14.9"},
		{"metal_lb", "0.14.9"},
		{"-metallb", "0.14.9"},
		{"metallb", "1.2"},
		{"metallb", ">=1.2.3"},
		{"metallb", "latest"},
	} {
		if err := v.Struct(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

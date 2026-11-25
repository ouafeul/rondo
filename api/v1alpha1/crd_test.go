package v1alpha1

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

const pkgPath = "rondo/api/v1alpha1"

var crdDir = filepath.Join("..", "..", "manifests", "crds")

type crdDoc struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Group string `json:"group"`
		Scope string `json:"scope"`
		Names struct {
			Kind     string `json:"kind"`
			ListKind string `json:"listKind"`
			Plural   string `json:"plural"`
		} `json:"names"`
		Versions []struct {
			Name         string `json:"name"`
			Served       bool   `json:"served"`
			Storage      bool   `json:"storage"`
			Subresources *struct {
				Status *struct{} `json:"status"`
			} `json:"subresources"`
			AdditionalPrinterColumns []struct {
				Name string `json:"name"`
			} `json:"additionalPrinterColumns"`
		} `json:"versions"`
	} `json:"spec"`
}

func loadCRDs(t *testing.T) map[string]crdDoc {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(crdDir, "*.yaml"))
	if err != nil {
		t.Fatalf("glob %s: %v", crdDir, err)
	}
	if len(files) == 0 {
		t.Fatalf("no CRD in %s, regenerate: scripts/ci/gen-crds.sh", crdDir)
	}
	out := make(map[string]crdDoc, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		var doc crdDoc
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parsing %s: %v", f, err)
		}
		if doc.Kind != "CustomResourceDefinition" {
			t.Errorf("%s: kind %q, want CustomResourceDefinition", filepath.Base(f), doc.Kind)
			continue
		}
		out[doc.Spec.Names.Kind] = doc
	}
	return out
}

func schemeKinds(t *testing.T) map[string]bool {
	t.Helper()
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	kinds := map[string]bool{}
	for gvk, typ := range s.AllKnownTypes() {
		if gvk.Group != GroupVersion.Group || typ.PkgPath() != pkgPath {
			continue
		}
		if strings.HasSuffix(gvk.Kind, "List") {
			continue
		}
		kinds[gvk.Kind] = true
	}
	return kinds
}

func TestCRDsCoverEveryRegisteredKind(t *testing.T) {
	crds := loadCRDs(t)
	kinds := schemeKinds(t)

	if len(kinds) == 0 {
		t.Fatal("no kind registered in the scheme")
	}
	for kind := range kinds {
		if _, ok := crds[kind]; !ok {
			t.Errorf("kind %s registered in the scheme but has no committed CRD, regenerate: scripts/ci/gen-crds.sh", kind)
		}
	}
	for kind := range crds {
		if !kinds[kind] {
			t.Errorf("CRD committed for %s, but this kind is not registered in the scheme (type deleted? orphan CRD?)", kind)
		}
	}
}

func TestCRDInvariants(t *testing.T) {
	for kind, doc := range loadCRDs(t) {
		t.Run(kind, func(t *testing.T) {
			if doc.Spec.Group != GroupVersion.Group {
				t.Errorf("group %q, want %q", doc.Spec.Group, GroupVersion.Group)
			}
			if doc.Spec.Scope != "Namespaced" {
				t.Errorf("scope %q, want Namespaced", doc.Spec.Scope)
			}
			if want := kind + "List"; doc.Spec.Names.ListKind != want {
				t.Errorf("listKind %q, want %q", doc.Spec.Names.ListKind, want)
			}
			if want := doc.Spec.Names.Plural + "." + GroupVersion.Group; doc.Metadata.Name != want {
				t.Errorf("metadata.name %q, want %q", doc.Metadata.Name, want)
			}

			if len(doc.Spec.Versions) != 1 {
				t.Fatalf("%d versions, want 1 (v1alpha1)", len(doc.Spec.Versions))
			}
			v := doc.Spec.Versions[0]
			if v.Name != GroupVersion.Version {
				t.Errorf("version %q, want %q", v.Name, GroupVersion.Version)
			}
			if !v.Served || !v.Storage {
				t.Errorf("version %s: served=%v storage=%v, want true/true", v.Name, v.Served, v.Storage)
			}
			if kind == "Component" {
				return
			}
			if v.Subresources == nil || v.Subresources.Status == nil {
				t.Error("status subresource missing: the controller's status patches would fail")
			}
			if !hasColumn(v.AdditionalPrinterColumns, "Ready") {
				t.Error("no Ready printcolumn: the common condition vocabulary must be visible in `kubectl get`")
			}
		})
	}
}

func hasColumn(cols []struct {
	Name string `json:"name"`
}, name string) bool {
	for _, c := range cols {
		if c.Name == name {
			return true
		}
	}
	return false
}

func TestDeepCopyIsGenerated(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	objIface := reflect.TypeOf((*runtime.Object)(nil)).Elem()
	for gvk, typ := range s.AllKnownTypes() {
		if gvk.Group != GroupVersion.Group || typ.PkgPath() != pkgPath {
			continue
		}
		if !reflect.PointerTo(typ).Implements(objIface) {
			t.Errorf("%s does not implement runtime.Object: deepcopy not generated", gvk.Kind)
		}
	}
}

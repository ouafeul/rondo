package platform

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"rondo/api/v1alpha1"
)

func TestProvenanceConstantMatchesAPI(t *testing.T) {
	if provenanceComponent != string(v1alpha1.ProvenanceComponent) {
		t.Fatalf("local copy out of sync: provenanceComponent=%q but "+
			"v1alpha1.ProvenanceComponent=%q.\n"+
			"apicopy.go COPIES this value instead of importing it, to keep "+
			"cmd/rondo free of typed k8s.io (scripts/ci/check-rondo-deps.sh). Fix the "+
			"copy, do NOT import api/v1alpha1 here.",
			provenanceComponent, v1alpha1.ProvenanceComponent)
	}
}

func TestPlatformPlaceholdersAreNotEmpty(t *testing.T) {
	if len(platformPlaceholders) == 0 {
		t.Fatal("empty platform token whitelist, validatePlatformTokens would " +
			"check nothing")
	}
	for placeholder := range platformPlaceholders {
		if !strings.HasPrefix(placeholder, "${") || !strings.HasSuffix(placeholder, "}") {
			t.Errorf("%q is not a placeholder: the table is keyed by the FORM "+
				"written in the tree, not by a variable name", placeholder)
		}
	}
}

func TestProductionFilesImportNoTypedKubernetes(t *testing.T) {
	forbidden := []string{
		"k8s.io/api",
		"k8s.io/apimachinery",
		"k8s.io/client-go",
		"k8s.io/apiextensions-apiserver",
		"sigs.k8s.io/controller-runtime",
		"sigs.k8s.io/controller-tools",
		"rondo/api/v1alpha1",
		"rondo/internal/exec",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package: %v", err)
	}
	fset := token.NewFileSet()
	seen := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		seen++
		file, pErr := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
		if pErr != nil {
			t.Fatalf("%s: %v", name, pErr)
		}
		for _, imp := range file.Imports {
			path, uErr := strconv.Unquote(imp.Path.Value)
			if uErr != nil {
				t.Fatalf("%s: unreadable import path %s", name, imp.Path.Value)
			}
			for _, bad := range forbidden {
				if path != bad && !strings.HasPrefix(path, bad+"/") {
					continue
				}
				t.Errorf("%s imports %q, forbidden in this package's production "+
					"code.\n"+
					"internal/platform is in the import closure of cmd/rondo, the "+
					"binary shipped to operator workstations and scanned for zero CVEs: it "+
					"must stay free of typed k8s.io (scripts/ci/check-rondo-deps.sh). "+
					"api/v1alpha1 is typed, so importing it, even for a single "+
					"constant, pulls in apimachinery, apiextensions-apiserver and "+
					"controller-runtime; internal/exec imports it itself, so it pulls "+
					"in the same closure.\n"+
					"The validator works on map[string]any ON PURPOSE. Copy the "+
					"value into apicopy.go and pin it in apicopy_test.go (a "+
					"_test.go file does not enter the binary's closure).",
					name, path)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no production file parsed, the test checks nothing")
	}
}

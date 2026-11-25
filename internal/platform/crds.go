package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"sigs.k8s.io/yaml"
)

func LoadSchemasFromDir(crdDir string) (map[string]*Schema, error) {
	entries, err := os.ReadDir(crdDir)
	if err != nil {
		return nil, fmt.Errorf("CRDs not found (%s): incomplete bundle, or "+
			"`scripts/ci/gen-crds.sh` never run? %w", crdDir, err)
	}
	var crds []map[string]any
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		raw, rErr := os.ReadFile(filepath.Join(crdDir, name)) //nolint:gosec // bundle tree
		if rErr != nil {
			return nil, rErr
		}
		for _, doc := range splitDocuments(string(raw)) {
			var crd map[string]any
			if err := yaml.Unmarshal([]byte(doc), &crd); err != nil {
				return nil, fmt.Errorf("%s: unreadable CRD: %w", name, err)
			}
			if kind, _ := crd["kind"].(string); kind != "CustomResourceDefinition" {
				continue
			}
			crds = append(crds, crd)
		}
	}
	return LoadSchemas(crds)
}

func ValidateDir(treeRoot, crdDir, depsPath string) ([]Finding, error) {
	schemas, err := LoadSchemasFromDir(crdDir)
	if err != nil {
		return nil, err
	}
	tree, err := Load(treeRoot)
	if err != nil {
		return nil, err
	}
	if len(tree.Objects) == 0 {
		return nil, fmt.Errorf("no manifest under %s, wrong path?", treeRoot)
	}
	findings := tree.Validate(schemas)

	if depsPath == "" {
		return findings, nil
	}
	deps, dErr := LoadDependencies(depsPath)
	if dErr != nil {
		findings = append(findings, Finding{
			Path:  depsPath,
			Level: LevelWarning,
			Message: "unreadable dependencies manifest, chart version consistency " +
				"NOT verified: " + dErr.Error(),
		})
		return findings, nil
	}
	return append(findings, tree.ValidateDependencies(deps)...), nil
}

package platform

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"rondo/internal/subst"
)

var platformPlaceholders = func() map[string]bool {
	out := map[string]bool{}
	for _, placeholder := range subst.Placeholders() {
		out[placeholder] = true
	}
	return out
}()

type Level string

const (
	LevelError   Level = "error"
	LevelWarning Level = "warning"
)

type Finding struct {
	Path    string
	Level   Level
	Message string
}

func (f Finding) String() string { return fmt.Sprintf("%s: %s: %s", f.Path, f.Level, f.Message) }

type Object struct {
	Path     string
	Kind     string
	Name     string
	Raw      map[string]any
	docIndex int
}

type Tree struct {
	Root    string
	Objects []Object
}

var kindDir = map[string]string{
	"Component":  "components",
	"Enrollment": "enrollments",
}

func Load(root string) (*Tree, error) {
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rErr := filepath.Rel(root, p)
		if rErr != nil {
			return rErr
		}
		if d.IsDir() {
			if p == root || inTreeDir(rel) {
				return nil
			}
			return fs.SkipDir
		}
		switch filepath.Ext(p) {
		case ".yaml", ".yml":
			if inTreeDir(filepath.Dir(rel)) {
				paths = append(paths, p)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read tree %s: %w", root, err)
	}
	sort.Strings(paths)

	tree := &Tree{Root: root}
	for _, p := range paths {
		rel, rErr := filepath.Rel(root, p)
		if rErr != nil {
			rel = p
		}
		raw, rErr := os.ReadFile(p) //nolint:gosec // manifest tree, not user input
		if rErr != nil {
			return nil, rErr
		}
		for i, doc := range splitDocuments(string(raw)) {
			if strings.TrimSpace(doc) == "" {
				continue
			}
			var obj map[string]any
			if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
				return nil, fmt.Errorf("%s (document %d): unreadable YAML: %w", rel, i+1, err)
			}
			if len(obj) == 0 {
				continue
			}
			kind, _ := obj["kind"].(string)
			name, _ := nestedString(obj, "metadata", "name")
			tree.Objects = append(tree.Objects, Object{
				Path: rel, Kind: kind, Name: name, Raw: obj, docIndex: i + 1,
			})
		}
	}
	return tree, nil
}

func inTreeDir(rel string) bool {
	head, _, _ := strings.Cut(rel, string(filepath.Separator))
	for _, dir := range kindDir {
		if head == dir {
			return true
		}
	}
	return false
}

func splitDocuments(content string) []string {
	lines := strings.Split(content, "\n")
	var docs []string
	var cur []string
	for _, l := range lines {
		if strings.TrimRight(l, " \t") == "---" {
			docs = append(docs, strings.Join(cur, "\n"))
			cur = nil
			continue
		}
		cur = append(cur, l)
	}
	return append(docs, strings.Join(cur, "\n"))
}

func (t *Tree) Validate(schemas map[string]*Schema) []Finding {
	var out []Finding
	byKind := map[string]map[string]Object{}
	for _, obj := range t.Objects {
		if byKind[obj.Kind] == nil {
			byKind[obj.Kind] = map[string]Object{}
		}
	}

	seen := map[string]string{}
	for _, obj := range t.Objects {
		add := func(level Level, format string, args ...any) {
			out = append(out, Finding{obj.Path, level, fmt.Sprintf(format, args...)})
		}
		if obj.Kind == "" {
			add(LevelError, "manifest without `kind`")
			continue
		}
		if obj.Name == "" {
			add(LevelError, "%s without `metadata.name`", obj.Kind)
			continue
		}
		if api, _ := obj.Raw["apiVersion"].(string); !strings.HasPrefix(api, "rondo.ouafeul.github.io/") {
			add(LevelError, "apiVersion %q: want rondo.ouafeul.github.io/v1alpha1", api)
		}
		id := obj.Kind + "/" + obj.Name
		if prev, dup := seen[id]; dup {
			add(LevelError, "%s already declared in %s, the second would overwrite the first on apply", id, prev)
		}
		seen[id] = obj.Path

		if _, hasStatus := obj.Raw["status"]; hasStatus {
			add(LevelWarning, "%s carries a `status`: that is OBSERVED state, it will be "+
				"ignored on apply; remove it so nobody thinks it can be set from the tree", id)
		}
		if want, known := kindDir[obj.Kind]; known {
			if dir := filepath.Dir(obj.Path); dir != want {
				add(LevelWarning, "%s filed under %q, want %q", obj.Kind, dir, want)
			}
		}

		schema, known := schemas[obj.Kind]
		if !known {
			add(LevelError, "kind %q unknown to the platform (known: %s)",
				obj.Kind, strings.Join(sortedSchemaKinds(schemas), ", "))
			continue
		}
		errs := schema.Validate(obj.Raw)
		for _, e := range errs {
			add(LevelError, "%s: %s", id, e)
		}
		if len(errs) == 0 {
			byKind[obj.Kind][obj.Name] = obj
		}
	}

	out = append(out, validateArchetype(byKind)...)
	out = append(out, validateReferences(byKind)...)
	out = append(out, validateDependsOn(byKind)...)
	out = append(out, validateTokens(byKind)...)
	out = append(out, validateVMSecrets(byKind)...)
	return out
}

func validateArchetype(byKind map[string]map[string]Object) []Finding {
	var out []Finding
	for _, name := range sortedObjectNames(byKind["Component"]) {
		obj := byKind["Component"][name]
		_, hasChart := nestedMap(obj.Raw, "spec", "chart")
		_, hasVM := nestedMap(obj.Raw, "spec", "vm")
		switch {
		case hasChart && hasVM:
			out = append(out, Finding{obj.Path, LevelError, fmt.Sprintf(
				"Component/%s sets both `spec.chart` and `spec.vm`: a component "+
					"is EITHER a chart (archetype A) OR machines (B/C), there is "+
					"no execution path for both", name)})
		case !hasChart && !hasVM:
			out = append(out, Finding{obj.Path, LevelError, fmt.Sprintf(
				"Component/%s declares neither `spec.chart` nor `spec.vm`: nothing to deploy",
				name)})
		}
	}
	return out
}

func validateReferences(byKind map[string]map[string]Object) []Finding {
	var out []Finding
	components := byKind["Component"]
	for _, name := range sortedObjectNames(byKind["Enrollment"]) {
		enr := byKind["Enrollment"][name]
		ref, _ := nestedString(enr.Raw, "spec", "component")
		if ref == "" {
			continue
		}
		if _, ok := components[ref]; !ok {
			out = append(out, Finding{enr.Path, LevelError, fmt.Sprintf(
				"Enrollment/%s references Component %q, which does not exist in the tree",
				name, ref)})
		}
	}
	enrolled := map[string]bool{}
	for _, enr := range byKind["Enrollment"] {
		if ref, _ := nestedString(enr.Raw, "spec", "component"); ref != "" {
			enrolled[ref] = true
		}
	}
	for _, name := range sortedObjectNames(components) {
		if !enrolled[name] {
			out = append(out, Finding{components[name].Path, LevelWarning, fmt.Sprintf(
				"Component/%s is enrolled by no Enrollment, it will be deployed "+
					"nowhere (creating a Component deploys nothing)", name)})
		}
	}
	return out
}

func validateDependsOn(byKind map[string]map[string]Object) []Finding {
	var out []Finding
	components := byKind["Component"]
	deps := map[string][]string{}
	for _, name := range sortedObjectNames(components) {
		comp := components[name]
		refs, _ := nestedSlice(comp.Raw, "spec", "dependsOn")
		for _, r := range refs {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			dep, _ := rm["name"].(string)
			if dep == "" {
				continue
			}
			deps[name] = append(deps[name], dep)
			if _, exists := components[dep]; !exists {
				out = append(out, Finding{comp.Path, LevelWarning, fmt.Sprintf(
					"Component/%s depends on %q, missing from the tree: it must come from another "+
						"tree (RONDO), or the Enrollment will wait forever", name, dep)})
			}
			if dep == name {
				out = append(out, Finding{comp.Path, LevelError, fmt.Sprintf(
					"Component/%s depends on itself", name)})
			}
		}
	}
	for _, cycle := range findCycles(deps) {
		path := "components"
		if obj, ok := components[cycle[0]]; ok {
			path = obj.Path
		}
		out = append(out, Finding{path, LevelError, fmt.Sprintf(
			"dependency cycle: %s, none of the Enrollments could start",
			strings.Join(cycle, " -> "))})
	}
	return out
}

func findCycles(deps map[string][]string) [][]string {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var cycles [][]string

	var visit func(string)
	visit = func(n string) {
		color[n] = grey
		stack = append(stack, n)
		for _, d := range deps[n] {
			switch color[d] {
			case white:
				visit(d)
			case grey:
				for i, s := range stack {
					if s == d {
						cycles = append(cycles, append(append([]string{}, stack[i:]...), d))
						break
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
	}
	for _, n := range sortedKeysOfSlice(deps) {
		if color[n] == white {
			visit(n)
		}
	}
	return cycles
}

func validateTokens(byKind map[string]map[string]Object) []Finding {
	var out []Finding
	for _, kind := range []string{"Component", "Enrollment"} {
		for _, name := range sortedObjectNames(byKind[kind]) {
			obj := byKind[kind][name]
			declared := declaredSecretKeys(obj.Raw)
			for _, tok := range findTokens(obj.Raw) {
				switch {
				case strings.HasPrefix(tok, "target.values."):
				case strings.HasPrefix(tok, "target."):
					if tok != "target.name" && tok != "target.server" {
						out = append(out, Finding{obj.Path, LevelError, fmt.Sprintf(
							"%s/%s uses ${%s}: allowed target tokens = target.name, "+
								"target.server, target.values.<key>", kind, name, tok)})
					}
				case strings.HasPrefix(tok, "secrets."):
					if key := strings.TrimPrefix(tok, "secrets."); !declared[key] {
						out = append(out, Finding{obj.Path, LevelError, fmt.Sprintf(
							"%s/%s uses ${secrets.%s} without declaring it in "+
								"`valuesFrom`/`secretsFrom`", kind, name, key)})
					}
				}
			}
		}
	}
	return out
}

func validateVMSecrets(byKind map[string]map[string]Object) []Finding {
	var out []Finding
	for _, name := range sortedObjectNames(byKind["Component"]) {
		obj := byKind["Component"][name]
		vm, ok := nestedMap(obj.Raw, "spec", "vm")
		if !ok {
			continue
		}
		fields := []any{vm["template"], vm["config"]}
		for _, g := range toSlice(vm["groups"]) {
			if gm, ok := g.(map[string]any); ok {
				fields = append(fields, gm["config"])
			}
		}
		found := map[string]bool{}
		for _, tok := range findTokens(fields) {
			if strings.HasPrefix(tok, "secrets.") {
				found[tok] = true
			}
		}
		for _, tok := range sortedNames(found) {
			out = append(out, Finding{obj.Path, LevelError, fmt.Sprintf(
				"Component/%s uses ${%s} in spec.vm: the configuration is rendered "+
					"as 0640 and STAYS on the machine. Declare the key in "+
					"`spec.vm.installer.secretsFrom` and read it from "+
					"`installer.secretsFile` (0600, removed after installation)",
				name, tok)})
		}
	}
	return out
}

type valuesSource struct {
	field  string
	secret string
	keys   []string
}

func valuesSources(obj map[string]any) []valuesSource {
	var out []valuesSource
	collect := func(field string, sources []any) {
		for i, s := range sources {
			sm, ok := s.(map[string]any)
			if !ok {
				continue
			}
			src := valuesSource{field: fmt.Sprintf("%s[%d]", field, i)}
			src.secret, _ = nestedString(sm, "secretRef", "name")
			for _, k := range toSlice(sm["keys"]) {
				if ks, ok := k.(string); ok {
					src.keys = append(src.keys, ks)
				}
			}
			out = append(out, src)
		}
	}
	if v, ok := nestedSlice(obj, "spec", "valuesFrom"); ok {
		collect("spec.valuesFrom", v)
	}
	if v, ok := nestedSlice(obj, "spec", "vm", "installer", "secretsFrom"); ok {
		collect("spec.vm.installer.secretsFrom", v)
	}
	return out
}

func declaredSecretKeys(obj map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, src := range valuesSources(obj) {
		for _, k := range src.keys {
			out[k] = true
		}
	}
	return out
}

func findTokens(v any) []string {
	var out []string
	switch t := v.(type) {
	case string:
		rest := t
		for {
			start := strings.Index(rest, "${")
			if start < 0 {
				break
			}
			rest = rest[start+2:]
			end := strings.Index(rest, "}")
			if end < 0 {
				break
			}
			out = append(out, rest[:end])
			rest = rest[end+1:]
		}
	case map[string]any:
		for _, k := range sortedKeys(t) {
			out = append(out, findTokens(t[k])...)
		}
	case []any:
		for _, item := range t {
			out = append(out, findTokens(item)...)
		}
	}
	return out
}

func sortedObjectNames(m map[string]Object) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSchemaKinds(m map[string]*Schema) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysOfSlice(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func HasErrors(findings []Finding) bool {
	for _, f := range findings {
		if f.Level == LevelError {
			return true
		}
	}
	return false
}

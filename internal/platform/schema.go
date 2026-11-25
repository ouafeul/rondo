package platform

import (
	"fmt"
	"sort"
	"strings"
)

type Schema struct {
	Type                 string
	Properties           map[string]*Schema
	Items                *Schema
	AdditionalProperties *Schema
	AllowsAnyProperty    bool
	Required             []string
	Enum                 []string
	Pattern              string
}

func LoadSchemas(crds []map[string]any) (map[string]*Schema, error) {
	out := map[string]*Schema{}
	for _, crd := range crds {
		kind, _ := nestedString(crd, "spec", "names", "kind")
		if kind == "" {
			continue
		}
		versions, _ := nestedSlice(crd, "spec", "versions")
		for _, v := range versions {
			vm, ok := v.(map[string]any)
			if !ok {
				continue
			}
			raw, ok := nestedMap(vm, "schema", "openAPIV3Schema")
			if !ok {
				continue
			}
			out[kind] = parseSchema(raw)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no usable CRD schema")
	}
	return out, nil
}

func parseSchema(raw map[string]any) *Schema {
	s := &Schema{}
	if t, ok := raw["type"].(string); ok {
		s.Type = t
	}
	if b, ok := raw["x-kubernetes-preserve-unknown-fields"].(bool); ok && b {
		s.AllowsAnyProperty = true
	}
	if p, ok := raw["pattern"].(string); ok {
		s.Pattern = p
	}
	for _, e := range toSlice(raw["enum"]) {
		if str, ok := e.(string); ok {
			s.Enum = append(s.Enum, str)
		}
	}
	for _, r := range toSlice(raw["required"]) {
		if str, ok := r.(string); ok {
			s.Required = append(s.Required, str)
		}
	}
	if props, ok := raw["properties"].(map[string]any); ok {
		s.Properties = map[string]*Schema{}
		for name, sub := range props {
			if sm, ok := sub.(map[string]any); ok {
				s.Properties[name] = parseSchema(sm)
			}
		}
	}
	if items, ok := raw["items"].(map[string]any); ok {
		s.Items = parseSchema(items)
	}
	switch ap := raw["additionalProperties"].(type) {
	case bool:
		if ap {
			s.AllowsAnyProperty = true
		}
	case map[string]any:
		s.AdditionalProperties = parseSchema(ap)
	}
	if s.Type == "object" && s.Properties == nil && s.AdditionalProperties == nil {
		s.AllowsAnyProperty = true
	}
	return s
}

func (s *Schema) Validate(obj map[string]any) []string {
	return s.validate(obj, "")
}

func (s *Schema) validate(value any, path string) []string {
	var out []string
	at := func(p string) string {
		if p == "" {
			return "(root)"
		}
		return p
	}

	switch s.Type {
	case "object", "":
		m, ok := value.(map[string]any)
		if !ok {
			if value == nil {
				return nil
			}
			return []string{fmt.Sprintf("%s: object expected, got %s", at(path), typeName(value))}
		}
		for _, req := range s.Required {
			if _, present := m[req]; !present {
				out = append(out, fmt.Sprintf("%s: required field %q missing", at(path), req))
			}
		}
		if s.AllowsAnyProperty && s.Properties == nil && s.AdditionalProperties == nil {
			return out
		}
		keys := sortedKeys(m)
		for _, k := range keys {
			child := k
			if path != "" {
				child = path + "." + k
			}
			switch {
			case s.Properties != nil && s.Properties[k] != nil:
				out = append(out, s.Properties[k].validate(m[k], child)...)
			case s.AdditionalProperties != nil:
				out = append(out, s.AdditionalProperties.validate(m[k], child)...)
			case s.AllowsAnyProperty:
			default:
				out = append(out, fmt.Sprintf(
					"%s: unknown field, it would be silently dropped on "+
						"apply%s", child, suggest(k, s.Properties)))
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			if value == nil {
				return nil
			}
			return []string{fmt.Sprintf("%s: list expected, got %s", at(path), typeName(value))}
		}
		if s.Items != nil {
			for i, item := range items {
				out = append(out, s.Items.validate(item, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	case "string":
		str, ok := value.(string)
		if !ok {
			if value == nil {
				return nil
			}
			return []string{fmt.Sprintf("%s: string expected, got %s", at(path), typeName(value))}
		}
		if len(s.Enum) > 0 && !contains(s.Enum, str) {
			out = append(out, fmt.Sprintf("%s: value %q not among the allowed values (%s)",
				at(path), str, strings.Join(s.Enum, ", ")))
		}
	case "integer":
		switch value.(type) {
		case int, int32, int64, float64, nil:
		default:
			out = append(out, fmt.Sprintf("%s: integer expected, got %s", at(path), typeName(value)))
		}
	case "boolean":
		if _, ok := value.(bool); !ok && value != nil {
			out = append(out, fmt.Sprintf("%s: boolean expected, got %s", at(path), typeName(value)))
		}
	}
	return out
}

func suggest(key string, props map[string]*Schema) string {
	best, bestDist := "", 1<<30
	for candidate := range props {
		d := editDistance(strings.ToLower(key), strings.ToLower(candidate))
		if d < bestDist {
			best, bestDist = candidate, d
		}
	}
	if best != "" && bestDist <= 1+len(key)/3 {
		return fmt.Sprintf(" (did you mean %q?)", best)
	}
	return ""
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func typeName(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case int, int32, int64, float64:
		return "number"
	case []any:
		return "list"
	case map[string]any:
		return "object"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func nestedMap(m map[string]any, keys ...string) (map[string]any, bool) {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func nestedBool(m map[string]any, keys ...string) (bool, bool) {
	if len(keys) == 0 {
		return false, false
	}
	parent := m
	if len(keys) > 1 {
		var ok bool
		parent, ok = nestedMap(m, keys[:len(keys)-1]...)
		if !ok {
			return false, false
		}
	}
	b, ok := parent[keys[len(keys)-1]].(bool)
	return b, ok
}

func nestedString(m map[string]any, keys ...string) (string, bool) {
	if len(keys) == 0 {
		return "", false
	}
	parent := m
	if len(keys) > 1 {
		var ok bool
		parent, ok = nestedMap(m, keys[:len(keys)-1]...)
		if !ok {
			return "", false
		}
	}
	s, ok := parent[keys[len(keys)-1]].(string)
	return s, ok
}

func nestedSlice(m map[string]any, keys ...string) ([]any, bool) {
	if len(keys) == 0 {
		return nil, false
	}
	parent := m
	if len(keys) > 1 {
		var ok bool
		parent, ok = nestedMap(m, keys[:len(keys)-1]...)
		if !ok {
			return nil, false
		}
	}
	s, ok := parent[keys[len(keys)-1]].([]any)
	return s, ok
}

func toSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

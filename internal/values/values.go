package values

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"rondo/api/v1alpha1"
)

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:32]
}

type Vars map[string]string

func TargetVars(t *v1alpha1.Target) Vars {
	v := Vars{"target.name": t.Name}
	if t.Spec.Kubernetes != nil {
		v["target.server"] = t.Spec.Kubernetes.Server
	}
	for k, val := range t.Spec.Values {
		v["target.values."+k] = val
	}
	return v
}

// ForEnrollment is the Component values with the Enrollment overlay, target tokens substituted.
func ForEnrollment(c *v1alpha1.Component, e *v1alpha1.Enrollment, t *v1alpha1.Target) (*apiextensionsv1.JSON, error) {
	merged, err := Merge(c.Spec.Values, e.Spec.Values)
	if err != nil {
		return nil, err
	}
	return Resolve(merged, TargetVars(t))
}

func Merge(layers ...*apiextensionsv1.JSON) (*apiextensionsv1.JSON, error) {
	out := map[string]any{}
	for i, layer := range layers {
		if layer == nil || len(layer.Raw) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(layer.Raw, &m); err != nil {
			return nil, fmt.Errorf("values of layer %d unreadable: %w", i, err)
		}
		out = mergeMaps(out, m)
	}
	if len(out) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &apiextensionsv1.JSON{Raw: raw}, nil
}

func mergeMaps(dst, src map[string]any) map[string]any {
	for k, sv := range src {
		dv, exists := dst[k]
		if !exists {
			dst[k] = sv
			continue
		}
		dm, dok := dv.(map[string]any)
		sm, sok := sv.(map[string]any)
		if dok && sok {
			dst[k] = mergeMaps(dm, sm)
			continue
		}
		dst[k] = sv
	}
	return dst
}

func Resolve(in *apiextensionsv1.JSON, vars Vars) (*apiextensionsv1.JSON, error) {
	if in == nil || len(in.Raw) == 0 {
		return in, nil
	}
	var root any
	if err := json.Unmarshal(in.Raw, &root); err != nil {
		return nil, fmt.Errorf("unreadable values: %w", err)
	}
	var missing []string
	resolved := walk(root, vars, &missing)
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, &MissingVarsError{Tokens: dedupe(missing), Known: knownKeys(vars)}
	}
	raw, err := json.Marshal(resolved)
	if err != nil {
		return nil, err
	}
	return &apiextensionsv1.JSON{Raw: raw}, nil
}

type MissingVarsError struct {
	Tokens []string
	Known  []string
}

func (e *MissingVarsError) Error() string {
	msg := fmt.Sprintf("unknown substitution token(s): %s", strings.Join(e.Tokens, ", "))
	if len(e.Known) > 0 {
		msg += fmt.Sprintf(", available: %s", strings.Join(e.Known, ", "))
	} else {
		msg += ", the target declares no site values (Target.spec.values)"
	}
	return msg
}

func walk(node any, vars Vars, missing *[]string) any {
	switch v := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, child := range v {
			out[substitute(k, vars, missing, nil)] = walk(child, vars, missing)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = walk(child, vars, missing)
		}
		return out
	case string:
		return substitute(v, vars, missing, nil)
	default:
		return node
	}
}

func substitute(s string, vars Vars, missing, forbidden *[]string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	for {
		start := strings.Index(s, "${")
		if start < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := strings.Index(s[start:], "}")
		if end < 0 {
			b.WriteString(s)
			return b.String()
		}
		end += start
		b.WriteString(s[:start])
		name := s[start+2 : end]
		switch {
		case forbidden != nil && strings.HasPrefix(name, secretPrefix):
			*forbidden = append(*forbidden, name)
			b.WriteString(s[start : end+1])
		case !strings.HasPrefix(name, v1alpha1.SubstitutionPrefix):
			b.WriteString(s[start : end+1])
		default:
			val, ok := vars[name]
			if !ok {
				*missing = append(*missing, name)
				b.WriteString(s[start : end+1])
			} else {
				b.WriteString(val)
			}
		}
		s = s[end+1:]
	}
}

func knownKeys(vars Vars) []string {
	out := make([]string, 0, len(vars))
	for k := range vars {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupe(in []string) []string {
	out := in[:0:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

func Hash(in *apiextensionsv1.JSON) (string, error) {
	if in == nil || len(in.Raw) == 0 {
		return sha("{}"), nil
	}
	var m any
	if err := json.Unmarshal(in.Raw, &m); err != nil {
		return "", fmt.Errorf("unreadable values: %w", err)
	}
	canon, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return sha(string(canon)), nil
}

package config

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-playground/validator/v10"
	"go.yaml.in/yaml/v3"
)

// Problem is where a manifest goes wrong, for an editor to point at it.
type Problem struct {
	// Line is 1-based, 0 when the error names no place in the manifest.
	Line int
	// Path is the YAML path, e.g. targets.vsphere[0].vmNetwork.gatewayIp.
	Path    string
	Message string
	// More counts the problems found beyond this one.
	More int
}

var (
	syntaxLine   = regexp.MustCompile(`yaml: line (\d+): (.*)`)
	unknownField = regexp.MustCompile(`unknown field "([^"]+)"`)
	wrongType    = regexp.MustCompile(`Go struct field (?:\w+\.)*?((?:[a-z][\w-]*\.)*[a-z][\w-]*) of type (\S+)`)
	namedTarget  = regexp.MustCompile(`target ([\w-]+):`)
	dottedPath   = regexp.MustCompile(`\b[a-z][\w-]*(?:\.[\w-]+)+`)
)

// Explain locates in raw the error Parse returned for it.
func Explain(raw []byte, err error) Problem {
	var doc yaml.Node
	_ = yaml.Unmarshal(raw, &doc)
	root := &doc
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root = doc.Content[0]
	}

	var verrs validator.ValidationErrors
	if errors.As(err, &verrs) && len(verrs) > 0 {
		fe := verrs[0]
		steps := yamlSteps(fe.StructNamespace())
		return Problem{Line: lineOf(root, steps), Path: render(steps), Message: ruleMessage(fe), More: len(verrs) - 1}
	}
	msg := err.Error()
	if m := syntaxLine.FindStringSubmatch(msg); m != nil {
		n, _ := strconv.Atoi(m[1])
		return Problem{Line: n, Message: "YAML: " + m[2]}
	}
	if m := unknownField.FindStringSubmatch(msg); m != nil {
		line, path := findKey(root, m[1], "")
		return Problem{Line: line, Path: path, Message: fmt.Sprintf("unknown field %q", m[1])}
	}
	if m := wrongType.FindStringSubmatch(msg); m != nil {
		steps := splitPath(m[1])
		return Problem{Line: lineOf(root, steps), Path: m[1], Message: "expected type: " + m[2]}
	}
	p := Problem{Message: strings.TrimPrefix(msg, "invalid manifest: ")}
	for _, path := range dottedPath.FindAllString(msg, -1) {
		if line, ok := resolve(root, splitPath(path)); ok {
			p.Line, p.Path = line, path
			return p
		}
	}
	if m := namedTarget.FindStringSubmatch(msg); m != nil {
		p.Line = findName(root, m[1])
	}
	return p
}

// step is a mapping key, or a sequence index when key is empty.
type step struct {
	key   string
	index int
}

// yamlSteps turns the validator's Go namespace (Config.Targets.VSphere[0].Placement.Name) into
// the manifest's keys (targets.vsphere[0].name), through the json tags.
func yamlSteps(namespace string) []step {
	t := reflect.TypeOf(Config{})
	var out []step
	parts := strings.Split(namespace, ".")
	for _, part := range parts[1:] {
		name, sub, _ := strings.Cut(part, "[")
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			break
		}
		f, ok := t.FieldByName(name)
		if !ok {
			break
		}
		t = f.Type
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if f.Anonymous && tag == "" {
			continue
		}
		if tag == "" {
			tag = name
		}
		out = append(out, step{key: tag})
		if sub == "" {
			continue
		}
		sub = strings.TrimSuffix(sub, "]")
		switch t.Kind() {
		case reflect.Slice, reflect.Array:
			i, _ := strconv.Atoi(sub)
			out = append(out, step{index: i})
		case reflect.Map:
			out = append(out, step{key: sub})
		}
		t = t.Elem()
	}
	return out
}

func splitPath(path string) []step {
	var out []step
	for _, k := range strings.Split(path, ".") {
		out = append(out, step{key: k})
	}
	return out
}

func render(steps []step) string {
	var b strings.Builder
	for _, s := range steps {
		if s.key == "" {
			fmt.Fprintf(&b, "[%d]", s.index)
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s.key)
	}
	return b.String()
}

// lineOf is the line of the deepest node of steps the manifest has: a missing field points at
// the block that lacks it.
func lineOf(n *yaml.Node, steps []step) int {
	line, _ := resolve(n, steps)
	return line
}

// resolve follows steps as far as the manifest goes; ok when it goes all the way. A key looked
// up in a sequence is looked up in each of its items.
func resolve(n *yaml.Node, steps []step) (line int, ok bool) {
	line = n.Line
	for _, s := range steps {
		next, at := child(n, s)
		if next == nil {
			return line, false
		}
		n, line = next, at
	}
	return line, true
}

func child(n *yaml.Node, s step) (*yaml.Node, int) {
	switch {
	case n.Kind == yaml.SequenceNode && s.key == "":
		if s.index < len(n.Content) {
			return n.Content[s.index], n.Content[s.index].Line
		}
	case n.Kind == yaml.SequenceNode:
		for _, item := range n.Content {
			if c, line := child(item, s); c != nil {
				return c, line
			}
		}
	case n.Kind == yaml.MappingNode && s.key != "":
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == s.key {
				return n.Content[i+1], n.Content[i].Line
			}
		}
	}
	return nil, 0
}

// findKey is the first key named name, depth first, with its path.
func findKey(n *yaml.Node, name, path string) (int, string) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			p := strings.TrimPrefix(path+"."+k, ".")
			if k == name {
				return n.Content[i].Line, p
			}
			if line, found := findKey(n.Content[i+1], name, p); line > 0 {
				return line, found
			}
		}
	case yaml.SequenceNode:
		for i, item := range n.Content {
			if line, found := findKey(item, name, fmt.Sprintf("%s[%d]", path, i)); line > 0 {
				return line, found
			}
		}
	}
	return 0, ""
}

// findName is the line of the `name: <name>` that declares a target.
func findName(n *yaml.Node, name string) int {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "name" && n.Content[i+1].Value == name {
				return n.Content[i].Line
			}
			if line := findName(n.Content[i+1], name); line > 0 {
				return line
			}
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			if line := findName(item, name); line > 0 {
				return line
			}
		}
	}
	return 0
}

func ruleMessage(fe validator.FieldError) string {
	p := fe.Param()
	switch fe.Tag() {
	case "required":
		return "value required"
	case "url":
		return "must be a URL"
	case "ip":
		return "must be an IP address"
	case "fqdn":
		return "must be a fully qualified domain name"
	case "hostname_rfc1123":
		return "must be a hostname (RFC 1123)"
	case "lowercase":
		return "must be lowercase"
	case "number":
		return "must be a number"
	case "excludes":
		return fmt.Sprintf("must not contain %q", p)
	case "ne":
		return fmt.Sprintf("cannot be %q", p)
	case "oneof":
		return "one of: " + strings.ReplaceAll(p, " ", ", ")
	case "min":
		return "at least " + p
	case "max":
		return "at most " + p
	case "len":
		return "exactly " + p
	}
	return fmt.Sprintf("rule %s=%s not satisfied", fe.Tag(), p)
}

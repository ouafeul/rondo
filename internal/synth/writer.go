package synth

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

func Marshal(objs []Object) ([]byte, error) {
	var buf bytes.Buffer
	for i, o := range objs {
		raw, err := yaml.Marshal(o)
		if err != nil {
			return nil, fmt.Errorf("marshal yaml: %w", err)
		}
		if i > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(raw)
	}
	return buf.Bytes(), nil
}

func Render(components ...Component) ([]Object, error) {
	var out []Object
	for _, c := range components {
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("%s: validation: %w", c.ComponentName(), err)
		}
		objs, err := c.Manifests()
		if err != nil {
			return nil, fmt.Errorf("%s: manifests: %w", c.ComponentName(), err)
		}
		out = append(out, objs...)
	}
	return out, nil
}

func Synth(dir string, components ...Component) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	for _, c := range components {
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("%s: validation: %w", c.ComponentName(), err)
		}
		objs, err := c.Manifests()
		if err != nil {
			return nil, fmt.Errorf("%s: manifests: %w", c.ComponentName(), err)
		}
		raw, err := Marshal(objs)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.ComponentName(), err)
		}
		path := filepath.Join(dir, c.ComponentName()+".yaml")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			return nil, err
		}
		written = append(written, path)
	}
	return written, nil
}

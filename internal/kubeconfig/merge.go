package kubeconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

func ContextName(env string) string {
	if env == "" {
		return "rondo"
	}
	return "rondo-" + env
}

// Install merges src's first cluster and user into ~/.kube/config under name.
func Install(src []byte, name string, use bool) error {
	return edit(func(d map[string]any) (bool, error) {
		var s map[string]any
		if err := yaml.Unmarshal(src, &s); err != nil {
			return false, fmt.Errorf("unreadable kubeconfig: %w", err)
		}
		cluster, err := first(s, "clusters", "cluster")
		if err != nil {
			return false, err
		}
		user, err := first(s, "users", "user")
		if err != nil {
			return false, err
		}
		put(d, "clusters", name, "cluster", cluster)
		put(d, "users", name, "user", user)
		put(d, "contexts", name, "context", map[string]any{"cluster": name, "user": name})
		if use {
			d["current-context"] = name
		}
		return true, nil
	})
}

// Select makes name the current context of ~/.kube/config; false if it has none.
func Select(name string) (bool, error) {
	found := false
	err := edit(func(d map[string]any) (bool, error) {
		items, _ := d["contexts"].([]any)
		for _, it := range items {
			if m, ok := it.(map[string]any); ok && m["name"] == name {
				d["current-context"] = name
				found = true
			}
		}
		return found, nil
	})
	return found, err
}

func edit(change func(map[string]any) (bool, error)) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home directory not found: %w", err)
	}
	path := filepath.Join(home, ".kube", "config")
	d := map[string]any{"apiVersion": "v1", "kind": "Config"}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &d); err != nil {
			return fmt.Errorf("%s unreadable: %w", path, err)
		}
	}
	if ok, err := change(d); err != nil || !ok {
		return err
	}
	out, err := yaml.Marshal(d)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func first(doc map[string]any, list, key string) (any, error) {
	items, _ := doc[list].([]any)
	if len(items) == 0 {
		return nil, fmt.Errorf("kubeconfig has no `%s` entry", list)
	}
	m, _ := items[0].(map[string]any)
	return m[key], nil
}

func put(doc map[string]any, list, name, key string, v any) {
	entry := map[string]any{"name": name, key: v}
	items, _ := doc[list].([]any)
	for i, it := range items {
		if m, ok := it.(map[string]any); ok && m["name"] == name {
			items[i] = entry
			return
		}
	}
	doc[list] = append(items, entry)
}

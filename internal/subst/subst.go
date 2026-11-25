package subst

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var substitutionVars = map[string]string{
	"${REGISTRY_HOST}":            "REGISTRY_HOST",
	"${ARTIFACTORY_URL}":          "ARTIFACTORY_URL",
	"${ARTIFACTORY_HELM_REPO}":    "ARTIFACTORY_HELM_REPO",
	"${ARTIFACTORY_DOCKER_REPO}":  "ARTIFACTORY_DOCKER_REPO",
	"${ARTIFACTORY_GENERIC_REPO}": "ARTIFACTORY_GENERIC_REPO",
	"${ARTIFACTORY_RPM_REPO}":     "ARTIFACTORY_RPM_REPO",
	"${INGRESS_BASE_DOMAIN}":      "INGRESS_BASE_DOMAIN",
}

func Placeholders() []string {
	out := make([]string, 0, len(substitutionVars))
	for placeholder := range substitutionVars {
		out = append(out, placeholder)
	}
	sort.Strings(out)
	return out
}

func Values(lookup func(string) string) map[string]string {
	values := map[string]string{}
	for placeholder, name := range substitutionVars {
		if v := strings.TrimSpace(lookup(name)); v != "" {
			values[placeholder] = v
		}
	}
	return values
}

func Apply(root string, values map[string]string) (int, error) {
	return walkTree(root, values, true)
}

func Check(root string, values map[string]string) error {
	_, err := walkTree(root, values, false)
	return err
}

func walkTree(root string, values map[string]string, write bool) (int, error) {
	var touched int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".yaml", ".yml", ".json":
		default:
			return nil
		}
		raw, rerr := os.ReadFile(path) //nolint:gosec // bundle or artefact tree
		if rerr != nil {
			return rerr
		}
		out, changed, serr := substitute(string(raw), values)
		if serr != nil {
			return fmt.Errorf("%s: %w", path, serr)
		}
		if !changed {
			return nil
		}
		touched++
		if !write {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		return os.WriteFile(path, []byte(out), info.Mode().Perm())
	})
	if err != nil {
		return 0, err
	}
	return touched, nil
}

func substitute(doc string, values map[string]string) (string, bool, error) {
	before := doc
	for placeholder, v := range values {
		doc = strings.ReplaceAll(doc, placeholder, v)
	}
	if unknown := unknownPlaceholders(doc); len(unknown) > 0 {
		return "", false, fmt.Errorf("unknown placeholders: %s; declare them in "+
			"substitutionVars or pass them through ${target.values.} / ${secrets.}",
			strings.Join(unknown, ", "))
	}
	return doc, doc != before, nil
}

func unknownPlaceholders(doc string) []string {
	seen := map[string]bool{}
	var out []string
	rest := doc
	for {
		start := strings.Index(rest, "${")
		if start < 0 {
			break
		}
		rest = rest[start:]
		end := strings.Index(rest, "}")
		if end < 0 {
			break
		}
		token := rest[:end+1]
		rest = rest[end+1:]
		if seen[token] || !isPlatformPlaceholder(token) {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	sort.Strings(out)
	return out
}

func isPlatformPlaceholder(token string) bool {
	inner := strings.TrimSuffix(strings.TrimPrefix(token, "${"), "}")
	if inner == "" {
		return false
	}
	for _, r := range inner {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

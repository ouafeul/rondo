package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type treeChartComponent struct {
	path   string
	values map[string]any
}

func treeChartComponents(t *testing.T) []treeChartComponent {
	t.Helper()

	dirs := []string{
		filepath.Join("..", "..", "platform", "components"),
		filepath.Join("testdata", "guard-tree", "components"),
	}

	var out []treeChartComponent
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
				continue
			}
			path := filepath.Join(dir, e.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s unreadable: %v", path, err)
			}
			var doc struct {
				Kind string `json:"kind"`
				Spec struct {
					Chart  map[string]any `json:"chart"`
					Values map[string]any `json:"values"`
				} `json:"spec"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("%s unreadable: %v", path, err)
			}
			if doc.Kind != "Component" || doc.Spec.Chart == nil {
				continue
			}
			out = append(out, treeChartComponent{path, doc.Spec.Values})
		}
	}
	if len(out) == 0 {
		t.Skip("no chart Component found, test running outside the repository")
	}
	return out
}

func walkStrings(node any, path []string, visit func(path []string, s string)) {
	switch v := node.(type) {
	case string:
		visit(path, v)
	case map[string]any:
		for k, child := range v {
			walkStrings(child, append(path, k), visit)
		}
	case []any:
		for i, child := range v {
			walkStrings(child, append(path, fmt.Sprintf("[%d]", i)), visit)
		}
	}
}

func TestMirrorDownloadInAChartIsAuthenticated(t *testing.T) {
	const (
		authURL   = "${secrets.mirrorAuthURL}"
		baseURL   = "${FILES_URL}"
		user      = "${secrets.mirrorUser}"
		password  = "${secrets.mirrorPassword}"
		hardcoded = "https://${OCI_REGISTRY}"
	)

	downloads := 0
	for _, c := range treeChartComponents(t) {
		var bare []string
		var hasUser, hasPassword bool

		walkStrings(c.values, nil, func(path []string, s string) {
			switch {
			case strings.Contains(s, authURL):
				downloads++
			case strings.Contains(s, baseURL):
				bare = append(bare, strings.Join(path, "."))
				downloads++
			}
			if strings.Contains(s, user) {
				hasUser = true
			}
			if strings.Contains(s, password) {
				hasPassword = true
			}
			if strings.Contains(s, hardcoded) {
				t.Errorf("%s (%s): %s HARDCODES the context path, and goes out "+
					"anonymously. Write %s, with %s and %s",
					c.path, strings.Join(path, "."), hardcoded, baseURL, user, password)
			}
			if i := strings.Index(s, "://"); i >= 0 {
				authority := s[i+3:]
				if j := strings.IndexAny(authority, "/?#"); j >= 0 {
					authority = authority[:j]
				}
				if at := strings.Index(authority, "@"); at >= 0 &&
					!strings.Contains(authority[:at], "${") {
					t.Errorf("%s (%s): userinfo %q is written in clear, it would be "+
						"committed with the component tree. Use %s and %s",
						c.path, strings.Join(path, "."), authority[:at], user, password)
				}
			}
		})

		if len(bare) > 0 && !(hasUser && hasPassword) {
			t.Errorf("%s: %s is a base URL WITHOUT credentials (%s) and the component "+
				"carries none outside the URL. A mirror with credentials refuses anonymous reads, "+
				"the pod would get \"401 Authentication is required\", and neither the CA nor "+
				"`regcred` changes that. Write %s and %s in its `envRenderSecret` "+
				"(or the chart's equivalent), or use %s",
				c.path, baseURL, strings.Join(bare, ", "), user, password, authURL)
		}
	}
	if downloads == 0 {
		t.Fatalf("no value references %s or %s: the guard guards nothing "+
			"(did a token name change?)", baseURL, authURL)
	}
}

func TestSecretIsNeverRenderedUnderAnEnvKey(t *testing.T) {
	for _, c := range treeChartComponents(t) {
		walkStrings(c.values, nil, func(path []string, s string) {
			if !strings.Contains(s, "${secrets.") {
				return
			}
			for _, seg := range path {
				if seg != "env" {
					continue
				}
				t.Errorf("%s: %s holds a `${secrets.}` under an `env` key, the chart "+
					"renders it as a ConfigMap (or in clear in the PodSpec), not as a Secret. "+
					"`envRenderSecret` is the key that produces a Secret",
					c.path, strings.Join(path, "."))
				return
			}
		})
	}
}

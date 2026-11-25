package versions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToolDigests(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dependencies.yaml")
	manifest := `
binaries:
  - {name: opentofu, version: "1.11.5", sha256: " ABCDEF "}
  - {name: kubectl, version: "v1.34.0"}
  - {name: rke2, version: "v1.34.5+rke2r1", sha256: "ignored"}
`
	if err := os.WriteFile(p, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ToolDigests(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["tofu"] != "abcdef" {
		t.Errorf("digests = %v, want tofu alone, by tool name, lowercased", got)
	}
	if _, err := ToolDigests(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("missing file accepted")
	}
	if err := os.WriteFile(p, []byte("binaries: {"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ToolDigests(p); err == nil {
		t.Error("unreadable YAML accepted")
	}
}

// The repository's own manifest pins a well-formed digest for every workstation tool but
// helm-diff, whose releases publish none.
func TestRepositoryPinsTheToolDigests(t *testing.T) {
	got, err := ToolDigests(filepath.Join("..", "..", "manifests", "dependencies.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"tofu", "helm", "helmfile", "kubectl", "k9s", "yq"} {
		if len(got[tool]) != 64 {
			t.Errorf("%s: sha256 %q", tool, got[tool])
		}
	}
}

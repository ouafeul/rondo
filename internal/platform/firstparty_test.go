package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFirstPartyChartsMatchApps(t *testing.T) {
	appsDir := filepath.Join("..", "..", "apps")
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		t.Skipf("apps/ missing (%v): outside the repository, the map is the only source", err)
	}

	onDisk := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(appsDir, e.Name(), "Chart.yaml")); err != nil {
			continue
		}
		onDisk[e.Name()] = true
	}
	if len(onDisk) == 0 {
		t.Fatal("no chart found under apps/: the test would check nothing")
	}

	for name := range onDisk {
		if !firstPartyCharts[name] {
			t.Errorf("apps/%s exists but is not in firstPartyCharts: a Component "+
				"pinning it will fail on \"missing from manifests/dependencies.yaml\", "+
				"a message that points at the wrong fix", name)
		}
	}
	for name := range firstPartyCharts {
		if !onDisk[name] {
			t.Errorf("firstPartyCharts declares %q, but apps/%s does not exist: "+
				"validation would let through a chart that is neither mirrored nor published",
				name, name)
		}
	}
}

package constructs

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"rondo/internal/synth"
)

var update = flag.Bool("update", false, "regenerate the testdata/ golden files")

func assertSnapshot(t *testing.T, golden string, objs []Object) {
	t.Helper()
	got, err := synth.Marshal(objs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join("testdata", golden)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run `go test -update`?): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("snapshot %s differs:\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
	}
}

func renderSnapshot(t *testing.T, golden string, components ...synth.Component) {
	t.Helper()
	objs, err := synth.Render(components...)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	assertSnapshot(t, golden, objs)
}

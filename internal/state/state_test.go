package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.RancherAccessKey != "" || s.RancherSecretKey != "" {
		t.Errorf("missing state should be empty: %+v", s)
	}
}

func TestSaveThenLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rondo.state.json")
	in := &State{RancherAccessKey: "ak", RancherSecretKey: "sk"}
	if err := in.Save(p); err != nil {
		t.Fatal(err)
	}
	out, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if out.RancherAccessKey != "ak" || out.RancherSecretKey != "sk" {
		t.Errorf("round-trip failed: %+v", out)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestLoadReadError(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err == nil {
		t.Fatal("reading a directory should fail")
	}
}

func TestSaveBadPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&State{}).Save(filepath.Join(file, "nested.json")); err == nil {
		t.Fatal("Save under a file should fail")
	}
}

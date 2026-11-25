package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHomeRondoHomeOverride(t *testing.T) {
	t.Setenv("RONDO_HOME", "/custom/rondo")
	if got := Home(); got != "/custom/rondo" {
		t.Errorf("Home = %q", got)
	}
	if got := BinDir(); got != filepath.Join("/custom/rondo", "bin") {
		t.Errorf("BinDir = %q", got)
	}
	if got := CacheDir(); got != filepath.Join("/custom/rondo", "cache") {
		t.Errorf("CacheDir = %q", got)
	}
	if got := StateDir(""); got != filepath.Join("/custom/rondo", "state") {
		t.Errorf("StateDir = %q", got)
	}
}

func TestStateDirOutsideCache(t *testing.T) {
	t.Setenv("RONDO_HOME", "/custom/rondo")
	state, cache := StateDir(""), CacheDir()
	if state == cache || strings.HasPrefix(state, cache+string(filepath.Separator)) {
		t.Errorf("StateDir (%q) under CacheDir (%q): a version change would destroy it", state, cache)
	}
}

func TestHomeDefault(t *testing.T) {
	t.Setenv("RONDO_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got := Home(); got != filepath.Join(home, ".rondo") {
		t.Errorf("Home = %q", got)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	cases := map[string]string{
		"~":            home,
		"~/ca/ca.pem":  filepath.Join(home, "ca", "ca.pem"),
		"/abs/ca.pem":  "/abs/ca.pem",
		"rel/ca.pem":   "rel/ca.pem",
		"~other/x.pem": "~other/x.pem",
	}
	for in, want := range cases {
		if got := ExpandHome(in); got != want {
			t.Errorf("ExpandHome(%q) = %q, want %q", in, got, want)
		}
	}
}

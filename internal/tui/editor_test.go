package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"rondo"
)

func (h *harness) prodManifest() string { return filepath.Join(h.home, "envs", "prod", "rondo.yaml") }

func (h *harness) write(text string) {
	h.t.Helper()
	for _, r := range text {
		if r == '\n' {
			h.keys("enter")
			continue
		}
		h.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func (h *harness) fileIs(want string) {
	h.t.Helper()
	got, err := os.ReadFile(h.prodManifest())
	if err != nil {
		h.t.Fatal(err)
	}
	if string(got) != want {
		h.t.Fatalf("rondo.yaml on disk:\n%s", got)
	}
}

// setText puts text in the editor as if typed.
func (h *harness) setText(text string) {
	h.m.editor.ta.SetValue(text)
	h.m.editor.check()
}

func TestEditorSavesTheManifest(t *testing.T) {
	h := newHarness(t)
	h.keys("8", "down", "e")
	h.see("rondo.yaml prod", "holds the site's secrets", "ingressBaseDomain: cluster.example.local",
		"✓ valid manifest", "line 1, col 1", "ctrl+s save")

	h.write("# edited in the TUI\n")
	h.keys("tab")
	h.send(tea.PasteMsg{Content: "# pasted"})
	h.keys("enter")
	h.see("● modified", "✓ valid manifest")
	h.keys("ctrl+s")
	h.see("saved")
	h.dontSee("● modified")
	h.fileIs("# edited in the TUI\n  # pasted\n" + string(rondo.ExampleConfig))
	if fi, _ := os.Stat(h.prodManifest()); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}

	h.keys("esc")
	if h.m.editor != nil {
		t.Fatal("esc without a change must close")
	}
	h.see("Environments", "environmentName")
}

func TestEditorPointsAtTheProblem(t *testing.T) {
	h := newHarness(t)
	h.keys("8", "down", "e")
	bad := strings.Replace(string(rondo.ExampleConfig), "gatewayIp: 10.0.0.1", "gatewayIp: gateway", 1)
	line := 1 + strings.Count(bad[:strings.Index(bad, "gatewayIp: gateway")], "\n")
	h.setText(bad)
	h.see(fmt.Sprintf("✗ line %d · targets.vsphere[0].vmNetwork.gatewayIp: must be an IP address", line),
		"ctrl+l go to the error")
	h.keys("ctrl+l")
	if got := h.m.editor.ta.Line() + 1; got != line {
		t.Errorf("cursor line %d, want %d", got, line)
	}

	h.keys("ctrl+s")
	h.see("Invalid manifest: save it anyway?")
	h.keys("n")
	h.fileIs(string(rondo.ExampleConfig))
	h.keys("ctrl+s", "y")
	h.fileIs(bad)

	h.keys("esc")
	h.see("must be an IP address", "e: edit the manifest")
	h.keys("e")
	if got := h.m.editor.ta.Line() + 1; got != line {
		t.Errorf("an invalid manifest opens on its error, not on line %d", got)
	}
}

func TestEditorAsksBeforeLosingWork(t *testing.T) {
	h := newHarness(t)
	h.keys("8", "down", "e")
	h.write("x")
	h.keys("esc")
	h.see("Unsaved changes: close without saving?", "y yes")
	h.keys("n")
	h.see("● modified")
	h.keys("ctrl+c", "y")
	if h.m.editor != nil {
		t.Fatal("editor still open")
	}
	h.fileIs(string(rondo.ExampleConfig))
}

func TestEditorAsksBeforeOverwritingAnotherChange(t *testing.T) {
	h := newHarness(t)
	h.keys("8", "down", "e")
	h.write("# mine\n")
	if err := os.WriteFile(h.prodManifest(), []byte("# another\n"+string(rondo.ExampleConfig)), 0o600); err != nil {
		t.Fatal(err)
	}
	h.keys("ctrl+s")
	h.see("rondo.yaml changed on disk since it was opened: overwrite it?")
	h.keys("n")
	h.fileIs("# another\n" + string(rondo.ExampleConfig))
	h.keys("ctrl+s", "y")
	h.fileIs("# mine\n" + string(rondo.ExampleConfig))
}

func TestEditorWarnsWhenTheEnvironmentNameDiffers(t *testing.T) {
	h := newHarness(t)
	h.keys("8", "down", "e")
	h.setText(strings.Replace(string(rondo.ExampleConfig), "environmentName: prod", "environmentName: lab", 1))
	h.see(`⚠ environmentName is "lab", the environment is "prod": rondo will refuse this manifest`)
}

func TestEditorOpensAfterInitAndFromAnywhere(t *testing.T) {
	h := newHarness(t)
	h.keys("8", "e")
	if h.m.editor != nil || !strings.Contains(h.m.flash, "no rondo.yaml for default") {
		t.Fatalf("without a file: flash %q", h.m.flash)
	}

	h.keys("n")
	h.write("lab")
	h.keys("enter")
	h.finish("y")
	if h.m.editor == nil || h.m.editor.env != "lab" {
		t.Fatal("rondo init must open the manifest to fill in")
	}
	h.see("rondo.yaml lab", "environmentName: lab", "✗")
	h.keys("esc")

	h.do(h.m.switchEnv("prod"))
	h.keys("1", "E")
	h.see("rondo.yaml prod", "✓ valid manifest")
}

func TestExternalEditorReadsTheFileBack(t *testing.T) {
	h := newHarness(t)
	t.Setenv("EDITOR", "true")
	h.keys("8", "down", "e")
	h.write("x")
	h.keys("ctrl+o")
	h.see("ctrl+s to save, or esc to discard")
	h.keys("esc", "y", "e")

	if cmd := h.m.externalEditor(); cmd == nil {
		t.Fatal("no command for $EDITOR")
	}
	changed := "# seen by vi\n" + string(rondo.ExampleConfig)
	if err := os.WriteFile(h.prodManifest(), []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	h.send(editedMsg{path: h.prodManifest()})
	if h.m.editor.ta.Value() != changed || h.m.editor.modified() {
		t.Errorf("read back: %q…", h.m.editor.ta.Value()[:20])
	}
	h.see("# seen by vi", "✓ valid manifest")
}

func TestEditorFitsTheTerminal(t *testing.T) {
	h := newHarness(t)
	h.keys("8", "down", "e")
	h.setText(strings.Replace(string(rondo.ExampleConfig), "gatewayIp: 10.0.0.1", "gatewayIp: x", 1))
	for _, size := range [][2]int{{140, 40}, {80, 24}, {60, 15}} {
		h.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		lines := strings.Split(h.m.View().Content, "\n")
		if len(lines) != size[1] {
			t.Errorf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > size[0] {
				t.Errorf("%dx%d: line of %d columns: %q", size[0], size[1], w, ansi.Strip(l))
			}
		}
	}
}

func TestWriteAtomicFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yaml")
	link := filepath.Join(dir, "rondo.yaml")
	if err := os.WriteFile(real, []byte("a"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(link, []byte("b")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced by a file")
	}
	got, _ := os.ReadFile(real)
	fi, _ := os.Stat(real)
	if string(got) != "b" || fi.Mode().Perm() != 0o640 {
		t.Errorf("content %q, mode %v", got, fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("temporary file left behind: %v", entries)
	}
}

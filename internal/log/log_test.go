package log

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLevelFor(t *testing.T) {
	cases := map[string]slog.Level{
		"trace": slog.LevelDebug,
		"debug": slog.LevelDebug,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"info":  slog.LevelInfo,
		"":      slog.LevelInfo,
		"bogus": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := levelFor(in); got != want {
			t.Errorf("levelFor(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestMakePalette(t *testing.T) {
	if p := makePalette(false); p.red != "" || p.reset != "" {
		t.Error("colourless palette should be empty")
	}
	if p := makePalette(true); p.red == "" || p.reset == "" {
		t.Error("colour palette should be set")
	}
}

func TestColorEnabled(t *testing.T) {
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("NO_COLOR", "1")
	if colorEnabled(os.Stderr) {
		t.Error("NO_COLOR should disable colour")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	if !colorEnabled(os.Stderr) {
		t.Error("FORCE_COLOR should enable colour")
	}
	t.Setenv("FORCE_COLOR", "")
	f := filepath.Join(t.TempDir(), "f")
	os.WriteFile(f, nil, 0o600)
	fh, _ := os.Open(f)
	defer fh.Close()
	if colorEnabled(fh) {
		t.Error("a regular file should not enable colour")
	}
}

func TestLevelTag(t *testing.T) {
	for _, c := range []struct {
		lvl slog.Level
		tag string
	}{
		{slog.LevelError, "ERROR"},
		{slog.LevelWarn, " WARN"},
		{slog.LevelInfo, " INFO"},
		{slog.LevelDebug, "DEBUG"},
	} {
		if tag, _ := levelTag(c.lvl); tag != c.tag {
			t.Errorf("levelTag(%v) = %q, want %q", c.lvl, tag, c.tag)
		}
	}
}

func TestHandlerHandleWritesMessageAndAttrs(t *testing.T) {
	f := filepath.Join(t.TempDir(), "out.log")
	fh, err := os.Create(f)
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{out: fh, level: slog.LevelInfo, pal: makePalette(false)}

	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("DEBUG should not be enabled at INFO level")
	}
	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("INFO should be enabled")
	}

	rec := slog.NewRecord(time.Time{}, slog.LevelInfo, "hello", 0)
	rec.AddAttrs(slog.String("b", "2"), slog.String("a", "1"))
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	raw, _ := os.ReadFile(f)
	out := string(raw)
	if !strings.Contains(out, "hello") {
		t.Errorf("message missing: %q", out)
	}
	if !strings.Contains(out, "a=1 b=2") {
		t.Errorf("sorted attributes missing: %q", out)
	}

	if h.WithAttrs(nil) != h || h.WithGroup("g") != h {
		t.Error("WithAttrs/WithGroup should return the handler")
	}
}

func TestPublicHelpers(t *testing.T) {
	Setup("debug")
	Step("step", "k", "v")
	Info("info")
	Debug("debug")
	Warn("warn")
	Error("error", "err", "x")
	Setup("info")
}

func TestStripANSI(t *testing.T) {
	in := "\x1b[32mtofu\x1b[0m: \x1b[1mcreating\x1b[0m node[1]\r"
	want := "tofu: creating node[1]"
	if got := stripANSI(in); got != want {
		t.Fatalf("stripANSI = %q, want %q", got, want)
	}
}

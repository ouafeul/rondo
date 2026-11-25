package log

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureRegion(t *testing.T, live bool) (*liveRegion, func() string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	r := newRegion(f)
	r.live = live
	return r, func() string {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
}

func TestRegionOutsideTTYHasNoCursorCodes(t *testing.T) {
	r, read := captureRegion(t, false)
	r.begin("apply tofu")
	r.streamLine("Plan: 6 to add")
	r.streamLine("Apply complete!")
	r.finish(true)

	got := read()
	if strings.Contains(got, "\033[") && !strings.Contains(got, "\033[0m") {
		t.Errorf("cursor movement codes outside a TTY:\n%q", got)
	}
	for _, want := range []string{"▶ apply tofu", "Plan: 6 to add", "Apply complete!", "✓"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from:\n%s", want, got)
		}
	}
}

func TestRegionReplaysOutputOnFailure(t *testing.T) {
	r, read := captureRegion(t, true)
	r.begin("apply tofu")
	for i := 0; i < maxTail+5; i++ {
		r.streamLine(lineN(i))
	}
	r.finish(false)

	got := read()
	if !strings.Contains(got, "✗") {
		t.Errorf("failure not reported:\n%s", got)
	}
	for i := 0; i < maxTail+5; i++ {
		if !strings.Contains(got, lineN(i)) {
			t.Errorf("line %q lost: the tool output is not replayed in full", lineN(i))
		}
	}
}

func TestRegionSuccessCollapsesToOneLine(t *testing.T) {
	r, read := captureRegion(t, true)
	r.begin("apply helm")
	r.streamLine("Release \"victoria-logs\" has been upgraded")
	r.finish(true)

	got := read()
	if !strings.Contains(got, "✓") {
		t.Errorf("success not reported:\n%s", got)
	}
	if !strings.Contains(got, "\033[0J") {
		t.Errorf("region not cleared on success:\n%q", got)
	}
}

func TestRegionStripsToolANSI(t *testing.T) {
	r, read := captureRegion(t, false)
	r.begin("plan tofu")
	r.streamLine("\033[32m+ create\033[0m\r")
	r.finish(true)

	got := read()
	if !strings.Contains(got, "+ create") {
		t.Errorf("content lost:\n%q", got)
	}
	if strings.Contains(got, "\033[32m") {
		t.Errorf("tool colour kept:\n%q", got)
	}
	if strings.Contains(got, "\r") {
		t.Errorf("carriage return kept:\n%q", got)
	}
}

func TestRegionBeginFoldsPreviousStep(t *testing.T) {
	r, read := captureRegion(t, false)
	r.begin("step one")
	r.begin("step two")
	r.finish(true)

	got := read()
	if strings.Count(got, "✓") != 2 {
		t.Errorf("%d steps collapsed, want 2:\n%s", strings.Count(got, "✓"), got)
	}
}

func TestRegionStreamLineWithoutStepIsNotLost(t *testing.T) {
	r, read := captureRegion(t, false)
	r.streamLine("error outside a step")
	if !strings.Contains(read(), "error outside a step") {
		t.Errorf("line lost:\n%s", read())
	}
}

func TestRegionFinishTwiceIsHarmless(t *testing.T) {
	r, read := captureRegion(t, false)
	r.begin("step")
	r.finish(true)
	r.finish(true)
	if n := strings.Count(read(), "✓"); n != 1 {
		t.Errorf("%d ✓ for one step", n)
	}
}

func TestRegionWriteGoesAboveTheWindow(t *testing.T) {
	r, read := captureRegion(t, true)
	r.begin("step")
	if _, err := r.Write([]byte("INFO structured message\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	r.finish(true)
	if !strings.Contains(read(), "structured message") {
		t.Errorf("structured log lost:\n%s", read())
	}
}

func TestTruncateBoundsToRunes(t *testing.T) {
	if got := truncate("abcdef", 4); got != "abc" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("truncate of a short string = %q", got)
	}
	if got := truncate("ééééé", 3); got != "éé" {
		t.Errorf("truncate of multi-byte runes = %q", got)
	}
	if got := truncate("abc", 0); got != "" {
		t.Errorf("truncate(0) = %q", got)
	}
}

func TestWidthFallsBackTo80(t *testing.T) {
	r, _ := captureRegion(t, false)
	if got := r.width(); got != 80 {
		t.Errorf("width = %d, want 80 outside a terminal", got)
	}
}

func TestStepNameFlattensKeyValues(t *testing.T) {
	if got := stepName("apply tofu"); got != "apply tofu" {
		t.Errorf("stepName without pairs = %q", got)
	}
	got := stepName("apply tofu", "stack", "acme", "module", "1.0.0")
	if got != "apply tofu stack=acme module=1.0.0" {
		t.Errorf("stepName = %q", got)
	}
	if got := stepName("x", "orphan"); got != "x" {
		t.Errorf("stepName with an orphan key = %q", got)
	}
}

func TestHandlerIsShared(t *testing.T) {
	Setup("debug")
	if Handler() == nil {
		t.Fatal("nil handler: the operator would get no log")
	}
}

func TestPackageLevelHelpersDoNotPanic(t *testing.T) {
	Step("test step", "key", "value")
	StreamLine("tool line")
	Finish(nil)
	Step("failing step")
	Finish(errBoom{})
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }

func lineN(i int) string { return "line-" + string(rune('a'+i)) }

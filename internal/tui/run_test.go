package tui

import (
	"strings"
	"testing"
	"testing/iotest"
)

func TestParseLineReadsWhatInternalLogPrints(t *testing.T) {
	cases := []struct {
		in   string
		kind lineKind
		text string
	}{
		{"▶ forming the k3s HA cluster", lineStepBegin, "forming the k3s HA cluster"},
		{"✓ forming the k3s HA cluster (12.3s)", lineStepOK, "forming the k3s HA cluster (12.3s)"},
		{"✗ provisioning (1s)", lineStepFail, "provisioning (1s)"},
		{"│ Apply complete!", lineStream, "Apply complete!"},
		{"ERROR failed  err=boom", lineError, "failed  err=boom"},
		{" WARN versions: dependencies.yaml unreadable", lineWarn, "versions: dependencies.yaml unreadable"},
		{" INFO valid tree  path=.", lineInfo, "valid tree  path=."},
		{"DEBUG exec  cmd=tofu", lineDebug, "exec  cmd=tofu"},
		{"rondo v1.0.0 (commit abc)", linePlain, "rondo v1.0.0 (commit abc)"},
		{"\x1b[32m│ colored\x1b[0m\r", lineStream, "colored"},
	}
	for _, c := range cases {
		got := parseLine(c.in)
		if got.kind != c.kind || got.text != c.text {
			t.Errorf("parseLine(%q) = %+v, want {%v %q}", c.in, got, c.kind, c.text)
		}
		if got.render() == "" {
			t.Errorf("empty render for %q", c.in)
		}
	}
}

func TestReadOutputSeesAPromptWithoutNewline(t *testing.T) {
	out := "▶ step\n│ line\nConfirm the destruction of alpha, beta" + PromptSuffix
	var lines []string
	var prompts []string
	readOutput(iotest.OneByteReader(strings.NewReader(out)), func(ev runEvent) {
		switch {
		case ev.line != nil:
			lines = append(lines, ev.line.text)
		case ev.prompt != "":
			prompts = append(prompts, ev.prompt)
		}
	})
	if strings.Join(lines, "|") != "step|line" {
		t.Errorf("lines = %q", lines)
	}
	if len(prompts) != 1 || prompts[0] != "Confirm the destruction of alpha, beta" {
		t.Errorf("prompts = %q", prompts)
	}
}

func TestReadOutputKeepsAnUnterminatedLastLine(t *testing.T) {
	var lines []string
	readOutput(strings.NewReader("a\nno end"), func(ev runEvent) {
		if ev.line != nil {
			lines = append(lines, ev.line.text)
		}
	})
	if strings.Join(lines, "|") != "a|no end" {
		t.Errorf("lines = %q", lines)
	}
}

func TestRunFollowsStepsAndKeepsTheTail(t *testing.T) {
	r := &run{}
	for _, l := range []string{"▶ a", "│ x", "✓ a (1s)", "▶ b", "✗ b (2s)", "▶ d", "✓ elsewhere (0s)"} {
		r.addLine(parseLine(l))
	}
	// "elsewhere" closes no step it did not open: d keeps running.
	want := []runStep{{"a", runSucceeded, "1s"}, {"b", runFailed, "2s"}, {"d", runRunning, ""}, {"elsewhere", runSucceeded, "0s"}}
	if len(r.steps) != len(want) {
		t.Fatalf("steps = %+v", r.steps)
	}
	for i, s := range want {
		if r.steps[i] != s {
			t.Errorf("step %d = %+v, want %+v", i, r.steps[i], s)
		}
	}
	r.apply(runEvent{done: true})
	if r.state != runSucceeded || r.steps[2].state != runSucceeded {
		t.Errorf("end: state %v, step d %v", r.state, r.steps[2].state)
	}

	for i := 0; i < maxRunLines+10; i++ {
		r.addLine(outLine{text: "l"})
	}
	if len(r.lines) != maxRunLines {
		t.Errorf("%d lines kept", len(r.lines))
	}
}

func TestSplitDuration(t *testing.T) {
	for in, want := range map[string][2]string{
		"finished (0s)":        {"finished", "0s"},
		"cluster alpha (1m2s)": {"cluster alpha", "1m2s"},
		"no duration":          {"no duration", ""},
		"(parens) in the mid":  {"(parens) in the mid", ""},
	} {
		if name, dur := splitDuration(in); name != want[0] || dur != want[1] {
			t.Errorf("splitDuration(%q) = %q, %q", in, name, dur)
		}
	}
}

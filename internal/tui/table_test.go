package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func sample() *table {
	t := &table{cols: cols("Name", "Version", "Message")}
	t.flex("Message")
	t.optional("Version")
	t.add("a", plain("traefik-management"), plain("41.0.2"), plain(strings.Repeat("long message ", 6)))
	t.add("b", plain("mft"), plain("1.0"), plain("short"))
	return t
}

func TestTableShrinksFlexThenDropsOptionalColumns(t *testing.T) {
	tb := sample()
	if w := tb.widths(200); w[2] != len(strings.Repeat("long message ", 6)) {
		t.Errorf("natural width: %v", w)
	}
	if w := tb.widths(48); w[0] != 18 || w[1] != 7 || w[2] != 48-18-7-2*colGap {
		t.Errorf("message shrunk first: %v", w)
	}
	if w := tb.widths(34); w[0] != 18 || w[1] != -1 || w[2] != 34-18-colGap {
		t.Errorf("version hidden, message widened: %v", w)
	}
	if w := tb.widths(20); w[0] >= 18 {
		t.Errorf("the name shrinks in the end: %v", w)
	}
}

func TestTableRendersTheRowsAroundTheCursor(t *testing.T) {
	tb := sample()
	for i := 0; i < 10; i++ {
		tb.add(string(rune('c'+i)), plain(string(rune('c'+i))))
	}
	out := ansi.Strip(tb.render(40, 4, 9))
	lines := strings.Split(out, "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "Name") || !strings.HasPrefix(lines[3], "j") {
		t.Errorf("render:\n%s", out)
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > 40 {
			t.Errorf("line too wide: %q", l)
		}
	}
	if out := ansi.Strip((&table{cols: cols("Name")}).render(20, 5, -1)); !strings.Contains(out, "No items") {
		t.Errorf("empty table: %q", out)
	}
}

func TestTableFilterIgnoresCase(t *testing.T) {
	tb := sample()
	tb.filter("MFT")
	if len(tb.rows) != 1 || tb.keys[0] != "b" || tb.index("a") != -1 {
		t.Errorf("filter: %v", tb.keys)
	}
}

func TestTimeFormatting(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for iso, want := range map[string]string{
		"2026-10-28T11:59:30Z": "30 s ago",
		"2026-10-28T11:15:00Z": "45 min ago",
		"2026-10-28T07:00:00Z": "5 h ago",
		"2026-10-25T12:00:00Z": "3 d ago",
		"":                     "—",
		"not a date":           "—",
	} {
		if got := ago(iso, now); got != want {
			t.Errorf("ago(%q) = %q, want %q", iso, got, want)
		}
	}
	if got := duration("2026-10-28T10:00:00Z", "2026-10-28T11:02:00Z", now); got != "1 h 02 min" {
		t.Errorf("duration = %q", got)
	}
	if got := duration("2026-10-28T11:58:55Z", "", now); got != "1 min 05 s" {
		t.Errorf("running duration = %q", got)
	}
	if got := labelOf("True") + labelOf("") + labelOf("Degraded"); got != "ReadyUnknownDegraded" {
		t.Errorf("labels = %q", got)
	}
	if archetypeOf("chart", nil) != "A · Helm" || archetypeOf("vm", []string{"a"}) != "B · VM" {
		t.Error("archetypes")
	}
}

package tui

import (
	"fmt"
	"time"

	"charm.land/lipgloss/v2"
)

type tone int

const (
	toneNeutral tone = iota
	toneOK
	toneBad
	toneInfo
	toneWarn
)

// tones and labels mirror web/dashboard/src/lib/status.ts.
var tones = map[string]tone{
	"True": toneOK, "Healthy": toneOK, "Succeeded": toneOK,
	"False": toneBad, "Degraded": toneBad, "Down": toneBad, "Failed": toneBad, "TimedOut": toneBad,
	"CrashLoopBackOff": toneBad, "ImagePullBackOff": toneBad, "ErrImagePull": toneBad,
	"OOMKilled": toneBad, "Error": toneBad,
	"Progressing": toneInfo, "Running": toneInfo, "Provision": toneInfo, "Install": toneInfo,
	"Pending": toneWarn, "ContainerCreating": toneWarn, "Aborted": toneWarn,
}

var labels = map[string]string{"True": "Ready", "False": "Not ready", "Unknown": "Unknown"}

var (
	stBold   = lipgloss.NewStyle().Bold(true)
	stDim    = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	stAccent = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	stTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan)
	stOK     = lipgloss.NewStyle().Foreground(lipgloss.Green)
	stBad    = lipgloss.NewStyle().Foreground(lipgloss.Red)
	stInfo   = lipgloss.NewStyle().Foreground(lipgloss.Blue)
	stWarn   = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	stSel    = lipgloss.NewStyle().Reverse(true)
	// stLink is a name a click opens.
	stLink = lipgloss.NewStyle().Underline(true)
	// stSelBlur is the selection of the pane without the focus.
	stSelBlur = lipgloss.NewStyle().Reverse(true).Foreground(lipgloss.BrightBlack)
	stKey     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan)
	stBrand   = lipgloss.NewStyle().Bold(true).Reverse(true).Foreground(lipgloss.Cyan).Padding(0, 1)
	stFlag    = lipgloss.NewStyle().Bold(true).Reverse(true).Foreground(lipgloss.Yellow).Padding(0, 1)
)

func (t tone) style() lipgloss.Style {
	switch t {
	case toneOK:
		return stOK
	case toneBad:
		return stBad
	case toneInfo:
		return stInfo
	case toneWarn:
		return stWarn
	}
	return stDim
}

func toneOf(v string) tone { return tones[v] }

func labelOf(v string) string {
	if v == "" {
		return "Unknown"
	}
	if l, ok := labels[v]; ok {
		return l
	}
	return v
}

// badge is a status as the web dashboard shows it: a colored dot and its label.
func badge(v string) cell {
	return cell{text: "● " + labelOf(v), style: toneOf(v).style()}
}

func archetypeOf(archetype string, groups []string) string {
	if archetype == "chart" {
		return "A · Helm"
	}
	if len(groups) > 1 {
		return "C · multi-VM"
	}
	return "B · VM"
}

func parseStamp(iso string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, iso)
	return t, iso != "" && err == nil
}

func ago(iso string, now time.Time) string {
	t, ok := parseStamp(iso)
	if !ok {
		return "—"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d d ago", int(d.Hours()/24))
}

func dateTime(iso string, now time.Time) string {
	t, ok := parseStamp(iso)
	if !ok {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04:05") + " " + stDim.Render("("+ago(iso, now)+")")
}

func duration(start, end string, now time.Time) string {
	s, ok := parseStamp(start)
	if !ok {
		return "—"
	}
	e, ok := parseStamp(end)
	if !ok {
		e = now
	}
	return humanDuration(e.Sub(s))
}

func humanDuration(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s < 0 {
		s = 0
	}
	if s < 60 {
		return fmt.Sprintf("%d s", s)
	}
	m := s / 60
	if m < 60 {
		return fmt.Sprintf("%d min %02d s", m, s%60)
	}
	return fmt.Sprintf("%d h %02d min", m/60, m%60)
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

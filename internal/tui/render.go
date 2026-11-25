package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *Model) showSidebar() bool { return m.width >= 100 }

// bodyHeight is the height of the boxes, between the header and the footer.
func (m *Model) bodyHeight() int { return max(5, m.height-2) }

func (m *Model) mainWidth() int {
	if m.showSidebar() {
		return m.width - sidebarWidth
	}
	return m.width
}

// contentWidth and contentHeight are the inside of the main box, under its title.
func (m *Model) contentWidth() int { return max(10, m.mainWidth()-4) }

func (m *Model) contentHeight() int { return max(3, m.height-2-2-2) }

// consoleLayout is the size of the console's output pane: the steps sit left, or above when narrow.
func (m *Model) consoleLayout(w, h int) (int, int) {
	h -= 2
	if w >= 90 {
		return w - stepsWidth(w) - 3, h
	}
	return w, h - min(6, h/3) - 1
}

func stepsWidth(w int) int { return min(40, w/3) }

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	if m.mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	v.WindowTitle = "rondo · " + envLabel(m.env)
	return v
}

func (m *Model) render() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	m.zones = m.zones[:0]
	bodyH := m.bodyHeight()
	main := m.renderMain(m.mainWidth(), bodyH)
	mainX := 0
	if m.showSidebar() {
		main = lipgloss.JoinHorizontal(lipgloss.Top, m.renderSidebar(bodyH), main)
		m.zones = append(m.zones, zone{y: boxTop, h: bodyH, x1: sidebarWidth, kind: zoneMenu})
		mainX = sidebarWidth
	}
	m.zones = append(m.zones, zone{y: boxTop, h: bodyH, x0: mainX, x1: m.width, kind: zoneContent})
	return lipgloss.JoinVertical(lipgloss.Left, m.renderHeader(), main, m.renderFooter())
}

// clip cuts s to the w×h box the layout gives it, so lipgloss never wraps it.
func clip(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:max(0, h)]
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "…")
	}
	return strings.Join(lines, "\n")
}

var boxStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.BrightBlack).Padding(0, 1)

// focusBox is the box of a pane, its border lit while it has the focus.
func focusBox(focused bool) lipgloss.Style {
	if focused {
		return boxStyle.BorderForeground(lipgloss.Cyan)
	}
	return boxStyle
}

func (m *Model) renderHeader() string {
	parts := []string{stBrand.Render("RONDO")}
	if m.opts.ReadOnly {
		parts = append(parts, stFlag.Render("READ-ONLY"))
	}
	parts = append(parts, stDim.Render("env ")+stBold.Render(envLabel(m.env)))
	if m.kctx != "" {
		parts = append(parts, stDim.Render("ctx ")+m.kctx)
	}
	switch {
	case m.connErr != nil:
		parts = append(parts, stBad.Render("● disconnected"))
	case m.readErr != nil:
		parts = append(parts, stBad.Render("● unreachable"))
	case !m.readOK:
		parts = append(parts, stAccent.Render(spinner[m.frame%len(spinner)]+" connecting"))
	default:
		parts = append(parts, stOK.Render("● connected"))
	}
	left := strings.Join(parts, "  ")

	var right []string
	if r := m.active(); r != nil {
		right = append(right, runRunning.icon(m.frame)+" "+r.op.title+" "+stDim.Render(humanDuration(r.elapsed(m.now()))))
	}
	if m.dryRun {
		right = append(right, stFlag.Render("DRY-RUN"))
	}
	if m.skipDownload {
		right = append(right, stFlag.Render("SKIP-DL"))
	}
	if at, ok := m.at[m.cur().id]; ok {
		right = append(right, stDim.Render("updated "+at.Format("15:04:05")))
	}
	if m.auto {
		right = append(right, stDim.Render(fmt.Sprintf("auto %d s", int(pollInterval.Seconds()))))
	} else {
		right = append(right, stWarn.Render("paused"))
	}
	right = append(right, stDim.Render(m.opts.Version))
	r := strings.Join(right, "  ")
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(r)
	if gap < 1 {
		return ansi.Truncate(left+"  "+r, m.width, "…")
	}
	return left + strings.Repeat(" ", gap) + r
}

func (m *Model) renderFooter() string {
	var keys []binding
	var off map[string]bool
	modal := true
	switch {
	case m.help:
		keys = []binding{{"any key", "close"}}
	case m.confirmQuit:
		keys = []binding{{"y", "quit"}, {"n", "stay"}}
	case m.active() != nil && m.active().prompt != "":
		keys = []binding{{"y", "confirm"}, {"n", "refuse"}}
	case m.editor != nil:
		keys = m.editor.keys()
	case m.form != nil:
		keys = []binding{{"⏎", "run"}, {"esc", "cancel"}}
	default:
		modal = false
	}
	switch {
	case modal:
	case m.filtering:
		keys = []binding{{"⏎", "keep the filter"}, {"esc", "clear"}}
	case m.inMenu():
		keys = []binding{{"↑↓", "change view"}, {"→ ⏎", "go to content"}, {"1…0", "go to a view"}}
	case m.top() != nil:
		keys = []binding{{"esc", "back"}, {"↑↓", "scroll"}}
		if p := m.top(); p.kind == panelLogs {
			keys = append(keys, binding{"f", "follow"})
		} else if p.view == vEnrollments && !isRoot(p.key) {
			keys = append(keys, binding{"l", "logs"})
		}
	case m.cur().id == vConsole:
		keys = []binding{{"↑↓", "scroll"}, {"f", "follow"}, {"[ ]", "prev./next run"}, {"x", "interrupt"}}
	default:
		keys = append([]binding{{"↑↓", "navigate"}}, m.viewKeys(m.cur().id)...)
		keys = append(keys, binding{"/", "filter"})
		if m.opts.ReadOnly {
			off = refused[m.cur().id]
		}
	}
	if !modal && !m.inMenu() && m.showSidebar() {
		keys = append(keys, binding{"tab", "menu"})
	}
	if !modal {
		keys = append(keys, binding{"?", "help"}, binding{"q", "quit"})
	}
	left := " " + keyLine(keys, off)
	if m.flash == "" || m.now().Sub(m.flashAt) > flashFor {
		m.keyZones(keys, boxTop+m.bodyHeight())
		return ansi.Truncate(left, m.width, "…")
	}
	st := stOK
	if m.flashBad {
		st = stBad
	}
	flash := st.Render(m.flash) + " "
	room := m.width - lipgloss.Width(left) - 2
	if room < 20 {
		return ansi.Truncate(" "+flash, m.width, "…")
	}
	flash = ansi.Truncate(flash, room, "…")
	m.keyZones(keys, boxTop+m.bodyHeight())
	return left + strings.Repeat(" ", m.width-lipgloss.Width(left)-lipgloss.Width(flash)) + flash
}

// keyZones are where the footer draws its hints, as keyLine lays them out: a hint of one key,
// with its description, sends it; each key of a hint of several (↑↓, [ ]) sends its own.
func (m *Model) keyZones(keys []binding, y int) {
	x := 1
	for i, b := range keys {
		if i > 0 {
			x += ansi.StringWidth(" · ")
		}
		kw, dw := ansi.StringWidth(b.key), ansi.StringWidth(b.desc)
		hk := hintKeys(b.key)
		if len(hk) == 1 {
			m.zones = append(m.zones, zone{y: y, x0: x, x1: x + kw + 1 + dw, kind: zoneKey, key: hk[0].key})
		} else {
			for _, k := range hk {
				m.zones = append(m.zones, zone{y: y, x0: x + k.at, x1: x + k.at + k.width, kind: zoneKey, key: k.key})
			}
		}
		x += kw + 1 + dw
	}
}

type hintKey struct {
	key       string
	at, width int
}

// hintKeys are the keys a hint's label names, and where each sits in it: esc, ⏎ (enter), ↑↓ (up,
// down), [ ] ([, ])…; none in a label that names a range (1…0).
func hintKeys(label string) []hintKey {
	if label == "any key" {
		return []hintKey{{"esc", 0, ansi.StringWidth(label)}}
	}
	named := map[string]string{"⏎": "enter", "↑": "up", "↓": "down", "←": "left", "→": "right", "space": "space"}
	var out []hintKey
	at := 0
	for i, word := range strings.Split(label, " ") {
		if i > 0 {
			at++
		}
		parts := []string{word}
		if strings.Trim(word, "↑↓←→") == "" {
			parts = strings.Split(word, "")
		}
		for _, p := range parts {
			k, ok := named[p]
			switch {
			case ok:
			case p == "esc" || p == "tab" || strings.HasPrefix(p, "ctrl+") || len([]rune(p)) == 1:
				k = p
			}
			if k != "" && !strings.Contains(p, "…") {
				out = append(out, hintKey{k, at, ansi.StringWidth(p)})
			}
			at += ansi.StringWidth(p)
		}
	}
	return out
}

// The layout a click is mapped on: the header line, then the boxes, whose content starts inside
// their border and padding; the main box's body starts under its title and a blank line.
const (
	boxTop   = 1
	boxInset = 2
	bodyTop  = boxTop + 1 + 2
)

func (m *Model) renderSidebar(h int) string {
	w := sidebarWidth - 4
	var b strings.Builder
	group := ""
	line := 0
	for i, v := range views {
		if v.group != group {
			if group != "" {
				b.WriteByte('\n')
				line++
			}
			group = v.group
			b.WriteString(stDim.Render(group) + "\n")
			line++
		}
		if line < h-2 {
			m.zones = append(m.zones, zone{y: boxTop + 1 + line, x0: boxInset, x1: boxInset + w, kind: zoneView, view: i})
		}
		line++
		label := v.key + " " + v.label
		mark := ""
		if v.id == vConsole && len(m.runs) > 0 {
			mark = " " + m.runs[len(m.runs)-1].state.icon(m.frame)
		}
		line := fit(label, w-lipgloss.Width(mark), false)
		switch {
		case i == m.view && m.inMenu():
			line = stSel.Render(line)
		case i == m.view:
			line = stTitle.Render(line)
		case m.opts.ReadOnly && v.id == vOps:
			line = stDim.Render(line)
		}
		b.WriteString(line + mark + "\n")
	}
	return focusBox(m.inMenu()).Width(sidebarWidth).Height(h).Render(clip(strings.TrimRight(b.String(), "\n"), w, h-2))
}

func (m *Model) renderMain(w, h int) string {
	cw, ch := m.contentWidth(), m.contentHeight()
	title := stTitle.Render(m.cur().label) + "  " + stDim.Render(m.cur().desc)
	var body string
	switch {
	case m.help:
		title, body = stTitle.Render("Help"), m.renderHelp()
	case m.confirmQuit:
		title = stBad.Bold(true).Render("Quit?")
		body = `"` + m.active().op.title + `" is running: quitting interrupts it.` + "\n\n" + helpLine([]binding{{"y", "quit"}, {"n", "stay"}})
	case m.active() != nil && m.active().prompt != "":
		r := m.active()
		title = stWarn.Bold(true).Render("Confirmation asked by " + r.commandLine())
		body = stBold.Render(r.prompt+"?") + "\n\n" + helpLine([]binding{{"y", "yes"}, {"n", "no"}}) +
			"\n\n" + stDim.Render("Last lines:") + "\n" + tail(r, 8)
	case m.editor != nil:
		title, body = m.editor.title(), m.editor.render()
	case m.form != nil:
		title, body = stTitle.Render("Run a command"), m.form.render(m, cw)
	case m.top() != nil:
		title, body = m.breadcrumb(), m.renderPanel(m.top())
		m.linkZones(m.top(), cw)
	case m.cur().id == vConsole:
		body = m.renderConsole(cw, ch)
	default:
		body = m.renderList(cw, ch)
	}
	content := clip(title, cw, 1) + "\n\n" + clip(body, cw, ch)
	return focusBox(m.showSidebar() && !m.inMenu()).Width(w).Height(h).Render(content)
}

// breadcrumb is the path to the top panel: the view, then each panel over it. Each segment but
// the last, underlined, is a zone: a click there goes back to it.
func (m *Model) breadcrumb() string {
	segs := []string{m.cur().label}
	for _, p := range m.stack {
		segs = append(segs, p.title)
	}
	x := boxInset
	if m.showSidebar() {
		x += sidebarWidth
	}
	const sep = " › "
	var b strings.Builder
	for i, s := range segs {
		if i > 0 {
			b.WriteString(stDim.Render(sep))
			x += ansi.StringWidth(sep)
		}
		if i == len(segs)-1 {
			b.WriteString(stTitle.Render(s))
			break
		}
		b.WriteString(stDim.Underline(true).Render(s))
		m.zones = append(m.zones, zone{y: boxTop + 1, x0: x, x1: x + ansi.StringWidth(s), kind: zoneCrumb, view: i})
		x += ansi.StringWidth(s)
	}
	return b.String()
}

// linkZones are where a detail's links show, as far as the viewport scrolled.
func (m *Model) linkZones(p *panel, w int) {
	if p.kind != panelDetail {
		return
	}
	x := boxInset
	if m.showSidebar() {
		x += sidebarWidth
	}
	off := p.vp.YOffset()
	for _, l := range p.links {
		if l.y >= off && l.y < off+p.vp.Height() {
			m.zones = append(m.zones, zone{y: bodyTop + l.y - off, x0: x, x1: x + w, kind: zoneLink, key: l.key, to: l.view})
		}
	}
}

func tail(r *run, n int) string {
	start := max(0, len(r.lines)-n)
	out := make([]string, 0, n)
	for _, l := range r.lines[start:] {
		out = append(out, l.render())
	}
	return strings.Join(out, "\n")
}

func (m *Model) renderPanel(p *panel) string {
	if p.kind == panelDetail {
		return p.vp.View()
	}
	status := stDim.Render(fmt.Sprintf("last %d lines", logTail))
	switch {
	case p.loading && !p.loaded:
		status = stAccent.Render(spinner[m.frame%len(spinner)] + " reading logs…")
	case p.err != nil:
		status = stBad.Render("cannot read logs: " + p.err.Error())
	}
	if p.follow {
		status += stDim.Render(" · follow ") + stOK.Render("on")
	} else {
		status += stDim.Render(" · follow paused (f)")
	}
	if p.loaded && p.text == "" && p.err == nil {
		return status + "\n" + stDim.Render("No lines.")
	}
	return status + "\n" + p.vp.View()
}

func (m *Model) renderList(w, h int) string {
	id := m.cur().id
	if m.cur().fetch != nil {
		if m.connErr != nil {
			return stBad.Render("Cannot read the management cluster: "+m.connErr.Error()) + "\n\n" +
				stDim.Render("Deploy management (9) or select another environment (8).")
		}
		if _, ok := m.data[id]; !ok {
			if err := m.errs[id]; err != nil {
				return stBad.Render("Cannot read: " + err.Error())
			}
			return stAccent.Render(spinner[m.frame%len(spinner)]) + " Loading…"
		}
	}
	var parts []string
	if err := m.errs[id]; err != nil {
		parts = append(parts, stBad.Render("Cannot read, data from "+m.at[id].Format("15:04:05")+": "+err.Error()))
	}
	for _, r := range related[id] {
		if err := m.errs[r]; err != nil {
			parts = append(parts, stBad.Render(`Cannot read, view "`+views[viewIndex(r)].label+`": `+err.Error()))
		}
	}
	if a := m.above(id, w); a != "" {
		parts = append(parts, a)
	}
	t := m.table(id)
	t.blur = m.inMenu()
	cur := m.cursor(id, t)
	below := ""
	if cur >= 0 {
		below = m.below(id, t.keys[cur])
	}
	if below != "" {
		below = clip(strings.TrimRight(below, "\n"), w, max(3, h/2))
	}
	filter := ""
	switch {
	case m.filtering:
		filter = m.filterIn.View()
	case m.filter[id] != "":
		filter = stDim.Render("filter: ") + m.filter[id] + stDim.Render("  (esc to clear)")
	}
	used, top := 0, 0
	for _, p := range parts {
		used += lipgloss.Height(p) + 1
		top += lipgloss.Height(p)
	}
	if below != "" {
		used += lipgloss.Height(below) + 1
	}
	if filter != "" {
		used++
	}
	parts = append(parts, t.render(w, max(2, h-used), cur))
	m.rowZones(t, bodyTop+top+1, w)
	if filter != "" {
		parts = append(parts, filter)
	}
	if below != "" {
		parts = append(parts, "", below)
	}
	return strings.Join(parts, "\n")
}

// rowZones are where the rows of t render drew, from y: each row, and a root's ▾ ▸.
func (m *Model) rowZones(t *table, y, w int) {
	x := boxInset
	if m.showSidebar() {
		x += sidebarWidth
	}
	for i := t.offset; i < t.offset+t.shown; i++ {
		ry := y + i - t.offset
		if isRoot(t.keys[i]) {
			m.zones = append(m.zones, zone{y: ry, x0: x, x1: x + 2, kind: zoneFold, key: t.keys[i]})
		}
		m.zones = append(m.zones, zone{y: ry, x0: x, x1: x + w, kind: zoneRow, key: t.keys[i]})
	}
}

func (m *Model) renderConsole(w, h int) string {
	r := m.shownRun()
	if r == nil {
		return stDim.Render("No command run yet: Operations (9), Installations (7) and Targets (4) run them.")
	}
	state := map[runState]string{runRunning: "running", runSucceeded: "finished", runFailed: "failed"}[r.state]
	if r.stopped && r.state == runFailed {
		state = "interrupted"
	}
	head := r.state.icon(m.frame) + " " + stBold.Render(r.op.title) + "  " + stDim.Render(state+" · "+humanDuration(r.elapsed(m.now())))
	if len(m.runs) > 1 {
		head += stDim.Render(fmt.Sprintf(" · run %d/%d", m.shown+1, len(m.runs)))
	}
	cmdline := stDim.Render("$ ") + r.commandLine()

	steps := make([]string, 0, len(r.steps))
	for _, s := range r.steps {
		line := s.state.icon(m.frame) + " " + s.name
		if s.dur != "" {
			line += " " + stDim.Render(s.dur)
		}
		steps = append(steps, line)
	}
	if len(steps) == 0 {
		steps = append(steps, stDim.Render("no step"))
	}
	follow := ""
	if !m.follow {
		follow = stWarn.Render("  follow paused (f)")
	}
	cw, ch := m.consoleLayout(w, h)
	out := m.console.View()
	if w >= 90 {
		sw := stepsWidth(w)
		left := clip(strings.Join(steps, "\n"), sw, ch)
		left = lipgloss.NewStyle().Width(sw).Height(ch).Render(left)
		sep := stDim.Render(strings.TrimRight(strings.Repeat("│\n", ch), "\n"))
		return head + follow + "\n" + cmdline + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, left, " ", sep, " ", out)
	}
	stepH := h - 2 - ch - 1
	if len(steps) > stepH {
		steps = steps[len(steps)-stepH:]
	}
	return head + follow + "\n" + cmdline + "\n" + clip(strings.Join(steps, "\n"), cw, stepH) + "\n" +
		stDim.Render(strings.Repeat("─", cw)) + "\n" + out
}

func (m *Model) renderHelp() string {
	section := func(title string, bs []binding, off bool) string {
		var b strings.Builder
		if off {
			b.WriteString(stBold.Render(title) + stDim.Render(" — refused in read-only mode") + "\n")
		} else {
			b.WriteString(stBold.Render(title) + "\n")
		}
		for _, k := range bs {
			key, desc := stKey.Render(fit(k.key, 12, false)), k.desc
			if off {
				key, desc = stDim.Render(fit(k.key, 12, false)), stDim.Render(k.desc)
			}
			b.WriteString("  " + key + " " + desc + "\n")
		}
		return b.String()
	}
	nav := []binding{
		{"1…9, 0", "go to a view"},
		{"tab", "views menu: ↑↓ change the view, → or ⏎ go back to the content (narrow screen: next view)"},
		{"↑↓ j k", "navigate"}, {"pgup pgdn", "previous, next page"}, {"g G", "top, bottom"},
		{"space", "Components, Enrollments: collapse, expand an installation"},
		{"+ -", "Components, Enrollments: expand all, collapse all"},
		{"←", "the views menu"},
		{"click", "a pane: give it the focus; a view of the menu: go there; a row: select it,"},
		{"", "double-click: ⏎; ▾ ▸: collapse, expand; a key of the bottom bar: its action;"},
		{"", "a level of the breadcrumb: go back to it; an underlined name in a detail: its detail"},
		{"wheel", "the pane under the mouse: scroll; the menu: change the view"}, {"M", "mouse: on, off (shift+drag selects text)"},
		{"⏎", "detail, logs or run"}, {"esc", "back"}, {"/", "filter the rows"},
		{"r", "refresh"}, {"a", "automatic refresh"}, {"q", "quit"},
	}
	ops := []binding{
		{"D", "toggle --dry-run"}, {"S", "toggle --skip-download"},
		{"d x", "Targets: deploy, destroy the downstream"},
		{"i m u", "Installations: install, update, uninstall"},
		{"n", "Environments: initialize"}, {"e", "Environments: edit the rondo.yaml"},
		{"E", "edit the current environment's rondo.yaml"}, {"x", "Console: interrupt (SIGINT)"},
		{"[ ]", "Console: previous, next run"}, {"f", "Logs, console: follow the end"},
	}
	return section("Navigation", nav, false) + "\n" + section("Commands", ops, m.opts.ReadOnly) + "\n" +
		stDim.Render("The commands are the CLI's, run as subprocesses; their (y/N) confirmations\n"+
			"show here. The cluster views are the web dashboard's, read-only.")
}

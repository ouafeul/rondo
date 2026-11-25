// Package tui is `rondo tui`: the web dashboard's views of the management cluster, and the CLI's
// commands, in one terminal interface.
package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"rondo/internal/config"
)

const (
	pollInterval  = 5 * time.Second
	frameInterval = 120 * time.Millisecond
	fetchTimeout  = 15 * time.Second
	logTail       = int64(500)
	flashFor      = 8 * time.Second
	sidebarWidth  = 24
	// defaultEnv names the unprefixed environment, as cmd/rondo does.
	defaultEnv = "default"
)

type Options struct {
	// Executable is the rondo binary the commands run.
	Executable string
	Version    string
	// Env is the environment at start, "" for the default one.
	Env string
	// Config is an explicit --config, forwarded until the environment changes.
	Config string
	// Forward are global flags every command gets (--iac-version, --log-level…).
	Forward      []string
	DryRun       bool
	SkipDownload bool
	// Connect reads the management cluster of env; the name is the kubectl context it used.
	Connect func(env string) (Source, string, error)
	// Envs lists the environment labels, "default" first.
	Envs func() []string
	// ConfigPath is where env's rondo.yaml lives.
	ConfigPath func(env string) string
	// ReadOnly shows the dashboard and launches nothing: no command, no form, no rondo.yaml
	// edit (rondo-sandbox, whose management is not a site's).
	ReadOnly bool
}

const readOnlyFlash = "read-only: this dashboard runs no RONDO command"

func envLabel(env string) string {
	if env == "" {
		return defaultEnv
	}
	return env
}

func envName(label string) string {
	if label == defaultEnv {
		return ""
	}
	return label
}

type siteInfo struct {
	cfg  *config.Config
	path string
	err  error
	// problem locates err in the file, when the file is there.
	problem *config.Problem
}

type panelKind int

const (
	panelDetail panelKind = iota
	panelLogs
)

// panel is a detail or a log pushed over a view; esc pops it.
type panel struct {
	kind     panelKind
	view     viewID
	key      string
	title    string
	job, pod string
	text     string
	err      error
	loaded   bool
	loading  bool
	follow   bool
	vp       viewport.Model
	// links are the lines of a detail that open another one, once wrapped as the viewport does.
	links []link
}

// link is a line of a detail naming an object a click opens: an installation's Components and
// Enrollments, a Component's placements.
type link struct {
	y    int
	view viewID
	key  string
}

type (
	pollMsg  struct{}
	frameMsg struct{}
	dataMsg  struct {
		gen  int
		view viewID
		data any
		err  error
	}
	logsMsg struct {
		gen   int
		panel *panel
		text  string
		err   error
	}
)

type Model struct {
	opts    Options
	env     string
	config  string
	src     Source
	kctx    string
	connErr error
	// readOK and readErr are the outcome of the last read of the view shown, or of any read when
	// it reads nothing: the reads a view relies on say their errors in it.
	readOK  bool
	readErr error
	// gen drops the reads of a previous environment.
	gen int

	width, height int
	view          int
	stack         []*panel
	sel           map[viewID]string
	// folded are the installations a tree view shows folded, by root key.
	folded map[viewID]map[string]bool
	// menu is the focus on the menu of views, where ↑↓ change the view.
	menu bool
	// mouse captures the mouse: clicks, wheel. zones are where the last render drew what a click
	// reaches; lastClick tells a double click.
	mouse     bool
	zones     []zone
	lastClick struct {
		key string
		at  time.Time
	}
	filter    map[viewID]string
	filtering bool
	filterIn  textinput.Model

	auto    bool
	data    map[viewID]any
	errs    map[viewID]error
	at      map[viewID]time.Time
	loading map[viewID]bool

	envs  []string
	sites map[string]siteInfo

	dryRun       bool
	skipDownload bool
	runs         []*run
	shown        int
	nextID       int
	console      viewport.Model
	follow       bool

	form        *form
	editor      *editor
	confirmQuit bool
	help        bool
	flash       string
	flashBad    bool
	flashAt     time.Time
	frame       int
	ticking     bool
	now         func() time.Time
	// send hands the program a message from outside Update.
	send func(tea.Msg)
}

func New(opts Options) *Model {
	m := &Model{
		opts: opts, env: opts.Env, config: opts.Config,
		sel: map[viewID]string{}, filter: map[viewID]string{}, folded: map[viewID]map[string]bool{},
		data: map[viewID]any{}, errs: map[viewID]error{}, at: map[viewID]time.Time{}, loading: map[viewID]bool{},
		auto: true, dryRun: opts.DryRun, skipDownload: opts.SkipDownload, follow: true, mouse: true,
		now: time.Now, send: func(tea.Msg) {},
	}
	m.filterIn = textinput.New()
	m.filterIn.Prompt = "/"
	m.filterIn.SetStyles(inputStyles())
	m.console = viewport.New()
	m.console.SoftWrap = true
	m.connect()
	m.loadEnvs()
	return m
}

// Run shows the TUI until the user quits, then waits for the command it launched to stop.
func Run(ctx context.Context, opts Options) error {
	m := New(opts)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	m.send = p.Send
	_, err := p.Run()
	m.shutdown(30 * time.Second)
	if m.src != nil {
		_ = m.src.Close()
	}
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

func (m *Model) shutdown(wait time.Duration) {
	r := m.active()
	if r == nil {
		return
	}
	_ = r.interrupt()
	select {
	case <-r.done:
	case <-time.After(wait):
		_ = r.proc.Kill()
	}
}

func (m *Model) connect() {
	if m.src != nil {
		_ = m.src.Close()
	}
	m.readOK, m.readErr = false, nil
	m.src, m.kctx, m.connErr = nil, "", errors.New("no connection configured")
	if m.opts.Connect != nil {
		m.src, m.kctx, m.connErr = m.opts.Connect(m.env)
	}
	if m.connErr != nil {
		m.src = nil
	}
}

func (m *Model) loadEnvs() {
	m.envs = nil
	if m.opts.Envs != nil {
		m.envs = m.opts.Envs()
	}
	if !slices.Contains(m.envs, envLabel(m.env)) {
		m.envs = append(m.envs, envLabel(m.env))
	}
	m.sites = map[string]siteInfo{}
	for _, e := range m.envs {
		m.sites[e] = m.loadSite(e)
	}
}

func (m *Model) loadSite(label string) siteInfo {
	path := m.config
	if path == "" || label != envLabel(m.env) {
		if m.opts.ConfigPath == nil {
			return siteInfo{err: errors.New("manifest location unknown")}
		}
		path = m.opts.ConfigPath(envName(label))
	}
	cfg, err := config.Load(path)
	s := siteInfo{cfg: cfg, path: path, err: err}
	if raw, rerr := os.ReadFile(path); err != nil && rerr == nil {
		p := config.Explain(raw, err)
		s.problem = &p
	}
	return s
}

func (m *Model) downstreams() ([]string, error) {
	s := m.sites[envLabel(m.env)]
	if s.err != nil {
		return nil, s.err
	}
	if s.cfg == nil {
		return nil, errors.New("manifest not loaded")
	}
	names := make([]string, len(s.cfg.Targets.Downstreams))
	for i, d := range s.cfg.Targets.Downstreams {
		names[i] = d.Name
	}
	return names, nil
}

func (m *Model) treeNames() []string {
	list, _ := data[[]Tree](m, vTrees)
	names := make([]string, len(list))
	for i, t := range list {
		names[i] = t.Name
	}
	return names
}

// commandArgs adds the TUI's environment and toggles to what a form describes.
func (m *Model) commandArgs(op *operation, args []string) []string {
	out := append([]string{}, args...)
	if !slices.Contains(out, "--env") {
		out = append(out, "--env", envLabel(m.env))
	}
	if m.config != "" {
		out = append(out, "--config", m.config)
	}
	if op.plan && m.dryRun {
		out = append(out, "--dry-run")
	}
	if op.plan && m.skipDownload {
		out = append(out, "--skip-download")
	}
	return append(out, m.opts.Forward...)
}

// childEnv keeps rondo from coloring its output: the TUI parses it, then colors it.
func childEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "FORCE_COLOR=") {
			env = append(env, kv)
		}
	}
	return append(env, "NO_COLOR=1")
}

func (m *Model) cur() viewDef { return views[m.view] }

func (m *Model) top() *panel {
	if len(m.stack) == 0 {
		return nil
	}
	return m.stack[len(m.stack)-1]
}

func (m *Model) active() *run {
	for _, r := range m.runs {
		if r.state == runRunning {
			return r
		}
	}
	return nil
}

func (m *Model) setFlash(s string, bad bool) {
	m.flash, m.flashBad, m.flashAt = s, bad, m.now()
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.fetch(m.cur().id), m.fetch(vTrees), poll())
}

// tick is tea.Tick; tests stop the clock.
var tick = tea.Tick

func poll() tea.Cmd {
	return tick(pollInterval, func(time.Time) tea.Msg { return pollMsg{} })
}

func (m *Model) busy() bool {
	if m.active() != nil {
		return true
	}
	if p := m.top(); p != nil && p.loading {
		return true
	}
	for _, l := range m.loading {
		if l {
			return true
		}
	}
	return false
}

// frames animates the spinners while something is in flight.
func (m *Model) frames() tea.Cmd {
	if m.ticking || !m.busy() {
		return nil
	}
	m.ticking = true
	return tick(frameInterval, func(time.Time) tea.Msg { return frameMsg{} })
}

// related are the views a view reads too: the installations its rows sit under, and the other
// half of what an installation's panel shows.
var related = map[viewID][]viewID{
	vComponents:  {vTrees, vEnrollments},
	vEnrollments: {vTrees, vComponents},
}

func (m *Model) fetch(id viewID) tea.Cmd {
	cmds := []tea.Cmd{m.fetchOne(id)}
	for _, r := range related[id] {
		cmds = append(cmds, m.fetchOne(r))
	}
	return tea.Batch(cmds...)
}

func (m *Model) fetchOne(id viewID) tea.Cmd {
	def := views[viewIndex(id)]
	if def.fetch == nil || m.src == nil || m.loading[id] {
		return nil
	}
	m.loading[id] = true
	src, gen := m.src, m.gen
	return tea.Batch(func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		d, err := def.fetch(ctx, src)
		return dataMsg{gen: gen, view: id, data: d, err: err}
	}, m.frames())
}

func (m *Model) fetchLogs(p *panel) tea.Cmd {
	if m.src == nil || p.loading {
		return nil
	}
	p.loading = true
	src, gen, job, pod := m.src, m.gen, p.job, p.pod
	return tea.Batch(func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		var rc io.ReadCloser
		var err error
		if job != "" {
			rc, err = src.JobLogs(ctx, job, logTail)
		} else {
			rc, err = src.ServiceLogs(ctx, pod, logTail)
		}
		if err != nil {
			return logsMsg{gen: gen, panel: p, err: err}
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, 4<<20))
		return logsMsg{gen: gen, panel: p, text: string(b), err: err}
	}, m.frames())
}

// refresh reads again what is on screen.
func (m *Model) refresh() tea.Cmd {
	if m.cur().id == vEnvs {
		m.loadEnvs()
	}
	cmds := []tea.Cmd{m.fetch(m.cur().id)}
	if p := m.top(); p != nil && p.kind == panelLogs {
		cmds = append(cmds, m.fetchLogs(p))
	}
	return tea.Batch(cmds...)
}

func (m *Model) switchView(i int) tea.Cmd {
	m.view = (i + len(views)) % len(views)
	m.stack = nil
	m.filtering = false
	if m.cur().id == vEnvs {
		m.loadEnvs()
	}
	return m.fetch(m.cur().id)
}

func (m *Model) switchEnv(env string) tea.Cmd {
	m.env, m.config = env, ""
	m.gen++
	m.data, m.errs, m.at, m.loading = map[viewID]any{}, map[viewID]error{}, map[viewID]time.Time{}, map[viewID]bool{}
	m.stack = nil
	m.connect()
	m.loadEnvs()
	m.setFlash("environment "+envLabel(env)+" selected", false)
	return tea.Batch(m.fetch(m.cur().id), m.fetch(vTrees))
}

func (m *Model) push(p *panel) {
	p.vp = viewport.New()
	p.vp.SoftWrap = true
	m.stack = append(m.stack, p)
}

func (m *Model) openLogs(title, job, pod string) tea.Cmd {
	p := &panel{kind: panelLogs, view: m.cur().id, title: title, job: job, pod: pod, follow: true}
	m.push(p)
	return m.fetchLogs(p)
}

func (m *Model) openForm(op *operation, preset string) tea.Cmd {
	if m.opts.ReadOnly {
		m.setFlash(readOnlyFlash, true)
		return nil
	}
	m.form = newForm(m, op, preset)
	return m.form.setFocus(0)
}

func (m *Model) launch(op *operation, args []string) tea.Cmd {
	if m.opts.ReadOnly {
		m.setFlash(readOnlyFlash, true)
		return nil
	}
	if r := m.active(); r != nil {
		m.setFlash(`"`+r.op.title+`" is running: wait for it to end or interrupt it (x, console)`, true)
		return nil
	}
	m.nextID++
	r, err := startRun(m.nextID, op, m.opts.Executable, m.commandArgs(op, args), childEnv(), m.send)
	if err != nil {
		m.setFlash("cannot run: "+err.Error(), true)
		return nil
	}
	m.runs = append(m.runs, r)
	var cmd tea.Cmd
	if !op.quiet {
		m.shown, m.follow = len(m.runs)-1, true
		cmd = m.switchView(viewIndex(vConsole))
	}
	return tea.Batch(m.frames(), cmd)
}

func (m *Model) runDone(r *run) tea.Cmd {
	switch {
	case r.stopped && r.state == runFailed:
		m.setFlash(r.op.title+" interrupted", true)
	case r.state == runFailed:
		m.setFlash("✗ "+r.op.title+": "+r.failure(), true)
	default:
		m.setFlash("✓ "+r.op.title+" finished in "+humanDuration(r.elapsed(m.now())), false)
	}
	if r.state != runSucceeded {
		return nil
	}
	switch r.op {
	case opSelectEnv:
		return m.switchEnv(envName(r.args[2]))
	case opInit:
		m.loadEnvs()
		// rondo init writes a manifest to complete: open it.
		if i := slices.Index(r.args, "--env"); i >= 0 && i+1 < len(r.args) {
			return m.openEditor(r.args[i+1])
		}
	}
	if m.src == nil {
		m.connect()
	}
	return tea.Batch(m.refresh(), m.fetch(vTrees))
}

func (r *run) failure() string {
	for i := len(r.lines) - 1; i >= 0; i-- {
		if r.lines[i].kind == lineError {
			return r.lines[i].text
		}
	}
	if r.err != nil {
		return r.err.Error()
	}
	return "failed"
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	m.sync()
	return m, cmd
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case pollMsg:
		cmds := []tea.Cmd{poll()}
		if m.auto {
			cmds = append(cmds, m.fetch(m.cur().id))
			if p := m.top(); p != nil && p.kind == panelLogs && p.follow {
				cmds = append(cmds, m.fetchLogs(p))
			}
		}
		return tea.Batch(cmds...)
	case frameMsg:
		m.frame++
		m.ticking = false
		return m.frames()
	case dataMsg:
		if msg.gen != m.gen {
			return nil
		}
		m.loading[msg.view] = false
		m.errs[msg.view] = msg.err
		if cur := m.cur(); cur.fetch == nil || msg.view == cur.id {
			m.readOK, m.readErr = msg.err == nil, msg.err
		}
		if msg.err == nil {
			m.data[msg.view], m.at[msg.view] = msg.data, m.now()
		}
	case logsMsg:
		if msg.gen != m.gen {
			return nil
		}
		p := msg.panel
		p.loading, p.loaded, p.err = false, true, msg.err
		if msg.err == nil {
			p.text = msg.text
		}
	case runMsg:
		for _, r := range m.runs {
			if r.id != msg.id {
				continue
			}
			done := false
			for _, ev := range r.take() {
				r.apply(ev)
				done = done || ev.done
			}
			if done {
				return m.runDone(r)
			}
			return nil
		}
	case editedMsg:
		m.edited(msg)
	case tea.PasteMsg:
		switch {
		case m.editor != nil:
			return m.editor.update(msg)
		case m.form != nil && len(m.form.fields) > 0 && m.form.fields[m.form.focus].kind == fieldText:
			var cmd tea.Cmd
			f := &m.form.fields[m.form.focus]
			f.input, cmd = f.input.Update(msg)
			return cmd
		}
	case tea.KeyPressMsg:
		return m.key(msg)
	case tea.MouseClickMsg:
		return m.click(msg.Mouse())
	case tea.MouseWheelMsg:
		return m.wheel(msg.Mouse())
	}
	return nil
}

// zone is a place of the screen a click reaches: a view of the menu, a row, a root's fold mark,
// then the panes themselves; h lines from y, one when 0.
type zone struct {
	y, h, x0, x1 int
	kind         zoneKind
	view         int
	key          string
	to           viewID
}

type zoneKind int

const (
	zoneView zoneKind = iota
	zoneFold
	zoneRow
	zoneMenu
	zoneContent
	// zoneKey is a hint of the footer: a click sends its key.
	zoneKey
	// zoneCrumb is a level of the breadcrumb, view the panels kept over the list.
	zoneCrumb
	// zoneLink is a name in a detail: a click opens its detail over it.
	zoneLink
)

func (z zone) hit(ms tea.Mouse) bool {
	return ms.Y >= z.y && ms.Y < z.y+max(1, z.h) && ms.X >= z.x0 && ms.X < z.x1
}

// keyMsg is the key press of a key's name, as msg.String() gives it back.
func keyMsg(name string) tea.KeyPressMsg {
	if c, ok := strings.CutPrefix(name, "ctrl+"); ok && len(c) == 1 {
		return tea.KeyPressMsg{Code: rune(c[0]), Mod: tea.ModCtrl}
	}
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(name)[0]
	return tea.KeyPressMsg{Code: r, Text: name}
}

const doubleClick = 500 * time.Millisecond

// modal says whether a dialog has the keys: help, a question, the editor, a form, the filter.
func (m *Model) modal() bool {
	return m.help || m.confirmQuit || m.editor != nil || m.form != nil || m.filtering ||
		(m.active() != nil && m.active().prompt != "")
}

// click does what is under the mouse: a hint of the footer, its key; a view of the menu, go
// there; a level of the breadcrumb, go back to it; a name in a detail, open it; a row, select
// it (a double click opens it as ⏎ does); a root's ▾ ▸, fold or unfold it; and it gives its pane
// the focus, the menu's or the content's.
func (m *Model) click(ms tea.Mouse) tea.Cmd {
	if ms.Button != tea.MouseLeft {
		return nil
	}
	for _, z := range m.zones {
		if z.kind == zoneKey && z.hit(ms) {
			return m.key(keyMsg(z.key))
		}
	}
	if m.help {
		m.help = false
		return nil
	}
	if m.modal() {
		return nil
	}
	for _, z := range m.zones {
		if z.kind == zoneKey || !z.hit(ms) {
			continue
		}
		id := m.cur().id
		m.menu = z.kind == zoneView || z.kind == zoneMenu
		switch z.kind {
		case zoneView:
			return m.switchView(z.view)
		case zoneCrumb:
			m.stack = m.stack[:min(z.view, len(m.stack))]
		case zoneLink:
			m.push(&panel{kind: panelDetail, view: z.to, key: z.key, title: z.key})
		case zoneFold:
			if m.folded[id] == nil {
				m.folded[id] = map[string]bool{}
			}
			m.folded[id][z.key], m.sel[id] = !m.folded[id][z.key], z.key
		case zoneRow:
			double := m.lastClick.key == z.key && m.now().Sub(m.lastClick.at) < doubleClick
			m.sel[id] = z.key
			m.lastClick.key, m.lastClick.at = z.key, m.now()
			if double {
				m.lastClick.key = ""
				cmd, _ := m.act(id, "enter", z.key)
				return cmd
			}
		}
		return nil
	}
	return nil
}

// wheel acts on the pane under the mouse, which it gives the focus as a click does, else on the
// pane that has it: in the menu, it changes the view as ↑↓ do; in the content, it scrolls a
// panel, the console, a list's selection.
func (m *Model) wheel(ms tea.Mouse) tea.Cmd {
	k := map[tea.MouseButton]string{tea.MouseWheelUp: "up", tea.MouseWheelDown: "down"}[ms.Button]
	if k == "" || m.modal() {
		return nil
	}
	for _, z := range m.zones {
		if (z.kind == zoneMenu || z.kind == zoneContent) && z.hit(ms) {
			m.menu = z.kind == zoneMenu
		}
	}
	if m.inMenu() {
		if k == "up" {
			return m.switchView(m.view - 1)
		}
		return m.switchView(m.view + 1)
	}
	switch p := m.top(); {
	case p != nil:
		for range 3 {
			m.panelKey(p, k)
		}
	case m.cur().id == vConsole:
		for range 3 {
			m.consoleKey(k)
		}
	default:
		return m.listKey(k)
	}
	return nil
}

func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if m.help {
		m.help = false
		return nil
	}
	if m.confirmQuit {
		m.confirmQuit = false
		if k == "y" || k == "o" {
			return tea.Quit
		}
		return nil
	}
	if r := m.active(); r != nil && r.prompt != "" {
		switch k {
		case "y", "o":
			r.answer(true)
		case "n", "esc":
			r.answer(false)
		}
		return nil
	}
	if m.editor != nil {
		return m.editorKey(msg)
	}
	if m.form != nil {
		if k == "esc" {
			m.form = nil
			return nil
		}
		cmd, submit := m.form.update(msg)
		if !submit {
			return cmd
		}
		args, err := m.form.args(false)
		if err != nil {
			m.form.err = err.Error()
			return nil
		}
		op := m.form.op
		m.form = nil
		return m.launch(op, args)
	}
	if m.filtering {
		switch k {
		case "esc":
			m.filtering = false
			m.filter[m.cur().id] = ""
		case "enter":
			m.filtering = false
		default:
			var cmd tea.Cmd
			m.filterIn, cmd = m.filterIn.Update(msg)
			m.filter[m.cur().id] = m.filterIn.Value()
			return cmd
		}
		return nil
	}
	if m.inMenu() {
		switch k {
		case "up", "k":
			return m.switchView(m.view - 1)
		case "down", "j":
			return m.switchView(m.view + 1)
		case "right", "enter", "tab", "shift+tab", "esc":
			m.menu = false
			return nil
		}
	}

	switch k {
	case "ctrl+c", "q":
		if m.active() != nil {
			m.confirmQuit = true
			return nil
		}
		return tea.Quit
	case "?":
		m.help = true
		return nil
	case "tab", "shift+tab":
		switch {
		case m.showSidebar():
			m.menu = true
			return nil
		case k == "tab":
			return m.switchView(m.view + 1)
		}
		return m.switchView(m.view - 1)
	case "r":
		return m.refresh()
	case "a":
		m.auto = !m.auto
		return nil
	case "D":
		m.dryRun = !m.dryRun
		return nil
	case "S":
		m.skipDownload = !m.skipDownload
		return nil
	case "E":
		return m.openEditor(envLabel(m.env))
	case "M":
		m.mouse = !m.mouse
		if m.mouse {
			m.setFlash("mouse on: shift+drag selects text", false)
		} else {
			m.setFlash("mouse off: the terminal selects text", false)
		}
		return nil
	case "esc":
		switch {
		case len(m.stack) > 0:
			m.stack = m.stack[:len(m.stack)-1]
		case m.filter[m.cur().id] != "":
			m.filter[m.cur().id] = ""
		}
		return nil
	}
	for i, v := range views {
		if k == v.key {
			m.menu = false
			return m.switchView(i)
		}
	}
	if m.inMenu() {
		return nil
	}

	if p := m.top(); p != nil {
		return m.panelKey(p, k)
	}
	if m.cur().id == vConsole {
		return m.consoleKey(k)
	}
	return m.listKey(k)
}

func (m *Model) panelKey(p *panel, k string) tea.Cmd {
	if !scroll(&p.vp, k) {
		switch k {
		case "f":
			p.follow = !p.follow
		case "l":
			if p.kind == panelDetail && p.view == vEnrollments && !isRoot(p.key) {
				return m.enrollmentLogs(p.key)
			}
		}
		return nil
	}
	if p.kind == panelLogs {
		p.follow = p.vp.AtBottom()
	}
	return nil
}

func scroll(vp *viewport.Model, k string) bool {
	switch k {
	case "up", "k":
		vp.ScrollUp(1)
	case "down", "j":
		vp.ScrollDown(1)
	case "pgup", "ctrl+u":
		vp.PageUp()
	case "pgdown", "ctrl+d", "space":
		vp.PageDown()
	case "home", "g":
		vp.GotoTop()
	case "end", "G":
		vp.GotoBottom()
	default:
		return false
	}
	return true
}

func (m *Model) consoleKey(k string) tea.Cmd {
	switch k {
	case "x":
		if r := m.active(); r != nil {
			if err := r.interrupt(); err != nil {
				m.setFlash("cannot interrupt: "+err.Error(), true)
			} else {
				m.setFlash("interrupt of "+r.op.title+" requested", false)
			}
		}
	case "[":
		if m.shown > 0 {
			m.shown--
			m.follow = true
		}
	case "]":
		if m.shown < len(m.runs)-1 {
			m.shown++
			m.follow = true
		}
	case "f":
		m.follow = !m.follow
	default:
		if scroll(&m.console, k) {
			m.follow = m.console.AtBottom()
		}
	}
	return nil
}

func (m *Model) listKey(k string) tea.Cmd {
	id := m.cur().id
	t := m.table(id)
	i := m.cursor(id, t)
	page := max(1, m.contentHeight()-4)
	switch k {
	case "up", "k":
		i--
	case "down", "j":
		i++
	case "pgup", "ctrl+u":
		i -= page
	case "pgdown", "ctrl+d":
		i += page
	case "home", "g":
		i = 0
	case "end", "G":
		i = len(t.keys) - 1
	case "/":
		m.filtering = true
		m.filterIn.SetValue(m.filter[id])
		return m.filterIn.Focus()
	case "left":
		m.menu = true
		return nil
	case "space":
		m.fold(id, t, i)
		return nil
	case "+":
		delete(m.folded, id)
		return nil
	case "-":
		m.foldAll(id, t, i)
		return nil
	default:
		sel := ""
		if i >= 0 && i < len(t.keys) {
			sel = t.keys[i]
		}
		cmd, _ := m.act(id, k, sel)
		return cmd
	}
	if len(t.keys) > 0 {
		m.sel[id] = t.keys[max(0, min(i, len(t.keys)-1))]
	}
	return nil
}

// inMenu says whether the menu of views has the focus; a narrow screen hides it.
func (m *Model) inMenu() bool { return m.menu && m.showSidebar() }

// fold folds or unfolds the installation of the selected row in a tree view: a root itself, a
// row its root, which it then selects.
func (m *Model) fold(id viewID, t *table, i int) {
	if i < 0 || i >= len(t.keys) {
		return
	}
	key := t.keys[i]
	if p := t.parents[i]; p != "" {
		key = p
	}
	if !isRoot(key) {
		return
	}
	if m.folded[id] == nil {
		m.folded[id] = map[string]bool{}
	}
	m.folded[id][key], m.sel[id] = !m.folded[id][key], key
}

// foldAll folds every installation of a tree view; a selected row gives the selection to its root.
func (m *Model) foldAll(id viewID, t *table, i int) {
	for _, k := range t.keys {
		if isRoot(k) {
			if m.folded[id] == nil {
				m.folded[id] = map[string]bool{}
			}
			m.folded[id][k] = true
		}
	}
	if i >= 0 && i < len(t.keys) && t.parents[i] != "" {
		m.sel[id] = t.parents[i]
	}
}

// cursor is the row of the selected key, the first row when it is gone.
func (m *Model) cursor(id viewID, t *table) int {
	if i := t.index(m.sel[id]); i >= 0 {
		return i
	}
	if len(t.keys) > 0 {
		m.sel[id] = t.keys[0]
		return 0
	}
	return -1
}

// sync sizes the scrollable panes and gives them the latest content.
func (m *Model) sync() {
	w, h := m.contentWidth(), m.contentHeight()
	if m.editor != nil {
		m.editor.ta.SetWidth(w)
		m.editor.ta.SetHeight(max(1, h-1))
	}
	if p := m.top(); p != nil {
		vh := h
		if p.kind == panelLogs {
			vh--
		}
		p.vp.SetWidth(w)
		p.vp.SetHeight(max(1, vh))
		switch p.kind {
		case panelDetail:
			content := m.detail(p, w)
			p.vp.SetContent(content)
			wrapLinks(p.links, content, w)
		case panelLogs:
			p.vp.SetContent(strings.TrimRight(p.text, "\n"))
			if p.follow {
				p.vp.GotoBottom()
			}
		}
	}
	if r := m.shownRun(); r != nil {
		cw, ch := m.consoleLayout(w, h)
		m.console.SetWidth(cw)
		m.console.SetHeight(max(1, ch))
		out := make([]string, len(r.lines))
		for i, l := range r.lines {
			out[i] = l.render()
		}
		m.console.SetContentLines(out)
		if m.follow {
			m.console.GotoBottom()
		}
	}
}

// wrapLinks turns the content line of each link into its line once the viewport wraps the
// lines wider than w, as it does: by width, not by word (bubbles' viewport.softWrap cuts every
// w columns; TestLinksAfterAWrappedLineOfWords fails if that changes).
func wrapLinks(links []link, content string, w int) {
	var at []int
	y := 0
	for _, l := range strings.Split(content, "\n") {
		at = append(at, y)
		y += max(1, (ansi.StringWidth(l)+w-1)/max(1, w))
	}
	for i := range links {
		if links[i].y < len(at) {
			links[i].y = at[links[i].y]
		}
	}
}

func (m *Model) shownRun() *run {
	if m.shown < 0 || m.shown >= len(m.runs) {
		return nil
	}
	return m.runs[m.shown]
}

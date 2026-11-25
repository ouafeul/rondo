package tui

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"rondo/internal/config"
)

type editorAsk int

const (
	askNone editorAsk = iota
	askDiscard
	askInvalid
	askOverwrite
)

// editor edits an environment's rondo.yaml, checked as rondo checks it, on every change.
type editor struct {
	env  string
	path string
	// disk is the file as it was opened or last saved: a save over another change asks first.
	disk    string
	ta      textarea.Model
	problem *config.Problem
	warning string
	ask     editorAsk
	// allowInvalid and allowOverwrite carry a yes through the rest of the save.
	allowInvalid   bool
	allowOverwrite bool
}

// editedMsg ends a session in $EDITOR.
type editedMsg struct {
	path string
	err  error
}

func editorStyles() textarea.Styles {
	s := textarea.DefaultDarkStyles()
	s.Cursor.Blink = false
	for _, st := range []*textarea.StyleState{&s.Focused, &s.Blurred} {
		st.CursorLine = lipgloss.NewStyle()
		st.CursorLineNumber = stAccent
		st.LineNumber = stDim
		st.EndOfBuffer = stDim
		st.Placeholder = stDim
		st.Prompt = stDim
		st.Text = lipgloss.NewStyle()
	}
	return s
}

func (m *Model) openEditor(label string) tea.Cmd {
	if m.opts.ReadOnly {
		m.setFlash(readOnlyFlash, true)
		return nil
	}
	site, ok := m.sites[label]
	if !ok {
		site = m.loadSite(label)
	}
	if site.path == "" {
		m.setFlash("no known rondo.yaml location for "+label, true)
		return nil
	}
	raw, err := os.ReadFile(site.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		m.setFlash("no rondo.yaml for "+label+": n (Environments) creates one", true)
		return nil
	case err != nil:
		m.setFlash("cannot read: "+err.Error(), true)
		return nil
	}
	e := &editor{env: label, path: site.path, disk: string(raw), ta: textarea.New()}
	e.ta.Prompt = ""
	e.ta.CharLimit = 0
	// MaxHeight also sizes the line numbers: four digits.
	e.ta.MaxHeight = 9999
	e.ta.SetStyles(editorStyles())
	e.ta.SetValue(string(raw))
	e.check()
	if e.problem != nil {
		e.gotoProblem()
	} else {
		e.ta.MoveToBegin()
	}
	m.editor = e
	return e.ta.Focus()
}

func (e *editor) modified() bool { return e.ta.Value() != e.disk }

// check validates the buffer as config.Load will validate the file.
func (e *editor) check() {
	raw := []byte(e.ta.Value())
	e.problem, e.warning = nil, ""
	c, err := config.Parse(raw)
	if err != nil {
		p := config.Explain(raw, err)
		e.problem = &p
		return
	}
	if name := envName(e.env); name != "" && c.EnvironmentName != name {
		e.warning = fmt.Sprintf("environmentName is %q, the environment is %q: rondo will refuse this manifest",
			c.EnvironmentName, name)
	}
}

func (e *editor) gotoProblem() {
	if e.problem == nil || e.problem.Line == 0 {
		return
	}
	e.ta.MoveToBegin()
	// CursorDown moves by screen row: a wrapped line takes several.
	for i := 0; i < e.ta.LineCount()*4 && e.ta.Line() < e.problem.Line-1; i++ {
		e.ta.CursorDown()
	}
	e.ta.CursorStart()
}

func (m *Model) editorKey(msg tea.KeyPressMsg) tea.Cmd {
	e := m.editor
	k := msg.String()
	if e.ask != askNone {
		ask := e.ask
		e.ask = askNone
		if k != "y" && k != "o" {
			e.allowInvalid, e.allowOverwrite = false, false
			return nil
		}
		switch ask {
		case askDiscard:
			m.editor = nil
			return nil
		case askInvalid:
			e.allowInvalid = true
		case askOverwrite:
			e.allowOverwrite = true
		}
		return m.saveEditor()
	}
	switch k {
	case "esc", "ctrl+c":
		if e.modified() {
			e.ask = askDiscard
			return nil
		}
		m.editor = nil
		return nil
	case "ctrl+s":
		return m.saveEditor()
	case "ctrl+o":
		return m.externalEditor()
	case "ctrl+l":
		e.gotoProblem()
		return nil
	case "tab":
		// YAML refuses tabs.
		e.ta.InsertString("  ")
		e.check()
		return nil
	}
	return e.update(msg)
}

func (e *editor) update(msg tea.Msg) tea.Cmd {
	before := e.ta.Value()
	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(msg)
	if e.ta.Value() != before {
		e.check()
	}
	return cmd
}

func (m *Model) saveEditor() tea.Cmd {
	e := m.editor
	if e.problem != nil && !e.allowInvalid {
		e.ask = askInvalid
		return nil
	}
	disk, err := os.ReadFile(e.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		m.setFlash("cannot read back: "+err.Error(), true)
		return nil
	}
	if string(disk) != e.disk && !e.allowOverwrite {
		e.ask = askOverwrite
		return nil
	}
	e.allowInvalid, e.allowOverwrite = false, false
	if err := writeAtomic(e.path, []byte(e.ta.Value())); err != nil {
		m.setFlash("cannot save: "+err.Error(), true)
		return nil
	}
	e.disk = e.ta.Value()
	m.loadEnvs()
	m.setFlash("rondo.yaml of "+e.env+" saved", false)
	return nil
}

// writeAtomic replaces path in one rename and keeps its mode: rondo.yaml holds the site's secrets.
func writeAtomic(path string, data []byte) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := fs.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".rondo.yaml-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// externalEditor hands the file to $VISUAL or $EDITOR, then reads it back.
func (m *Model) externalEditor() tea.Cmd {
	e := m.editor
	switch {
	case e.modified():
		m.setFlash("ctrl+s to save, or esc to discard, before the external editor", true)
		return nil
	case m.active() != nil:
		// The command's output would wait on the TUI while the editor holds the terminal.
		m.setFlash("a command is running: the external editor would wait for it to end", true)
		return nil
	}
	args := strings.Fields(cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi"))
	c := exec.Command(args[0], append(args[1:], e.path)...)
	path := e.path
	return tea.ExecProcess(c, func(err error) tea.Msg { return editedMsg{path: path, err: err} })
}

func (m *Model) edited(msg editedMsg) {
	m.loadEnvs()
	if msg.err != nil {
		m.setFlash("external editor: "+msg.err.Error(), true)
	}
	e := m.editor
	if e == nil || e.path != msg.path {
		return
	}
	raw, err := os.ReadFile(e.path)
	if err != nil {
		m.setFlash("cannot read back: "+err.Error(), true)
		return
	}
	e.disk = string(raw)
	e.ta.SetValue(e.disk)
	e.check()
	e.gotoProblem()
}

func (e *editor) title() string {
	t := stTitle.Render("rondo.yaml") + " " + stBold.Render(e.env)
	if e.modified() {
		t += "  " + stWarn.Render("● modified")
	}
	return t + stDim.Render(" · holds the site's secrets · "+e.path)
}

func (e *editor) status() string {
	switch e.ask {
	case askDiscard:
		return stWarn.Bold(true).Render("Unsaved changes: close without saving?") + "  " + yesNo()
	case askInvalid:
		return stBad.Bold(true).Render("Invalid manifest: save it anyway?") + "  " + yesNo()
	case askOverwrite:
		return stWarn.Bold(true).Render("rondo.yaml changed on disk since it was opened: overwrite it?") + "  " + yesNo()
	}
	pos := stDim.Render(fmt.Sprintf("  line %d, col %d", e.ta.Line()+1, e.ta.Column()+1))
	switch {
	case e.problem != nil:
		return stBad.Render(problemText(*e.problem)) + pos
	case e.warning != "":
		return stWarn.Render("⚠ "+e.warning) + pos
	}
	return stOK.Render("✓ valid manifest") + pos
}

func problemText(p config.Problem) string {
	var where []string
	if p.Line > 0 {
		where = append(where, fmt.Sprintf("line %d", p.Line))
	}
	if p.Path != "" {
		where = append(where, p.Path)
	}
	text := "✗ " + p.Message
	if len(where) > 0 {
		text = "✗ " + strings.Join(where, " · ") + ": " + p.Message
	}
	if p.More > 0 {
		text += fmt.Sprintf(" (+%d)", p.More)
	}
	return text
}

func yesNo() string { return helpLine([]binding{{"y", "yes"}, {"n", "no"}}) }

func (e *editor) render() string { return e.ta.View() + "\n" + e.status() }

func (e *editor) keys() []binding {
	if e.ask != askNone {
		return []binding{{"y", "yes"}, {"n", "no"}}
	}
	bs := []binding{{"ctrl+s", "save"}}
	if e.problem != nil && e.problem.Line > 0 {
		bs = append(bs, binding{"ctrl+l", "go to the error"})
	}
	return append(bs, binding{"ctrl+o", "$EDITOR"}, binding{"esc", "close"})
}

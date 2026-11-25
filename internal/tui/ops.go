package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"rondo/internal/bundle"
)

// operation is an `rondo` command the TUI can launch; the CLI does the work in a child process.
type operation struct {
	id      string
	title   string
	desc    string
	command []string
	// plan commands honor --dry-run and --skip-download.
	plan   bool
	danger bool
	// quiet commands run without switching to the console.
	quiet  bool
	fields func(m *Model, preset string) []field
}

var (
	opDeploy = &operation{
		id: "deploy", title: "Deploy management", command: []string{"deploy"}, plan: true,
		desc: "The k3s management cluster, its vSphere Targets and the RONDO components.",
	}
	opDeployDownstreams = &operation{
		id: "deploy-downstreams", title: "Deploy downstreams", command: []string{"deploy", "downstreams"},
		plan: true, fields: downstreamFields,
		desc: "Provisions the rke2 targets and installs the base layer on them.",
	}
	opInstall = &operation{
		id: "component-install", title: "Install a component", command: []string{"component", "install"},
		plan: true, fields: installFields,
		desc: "Downloads the tree a component publishes and applies its Components and Enrollments.",
	}
	opUninstall = &operation{
		id: "component-uninstall", title: "Uninstall a component", command: []string{"component", "uninstall"},
		plan: true, danger: true, fields: uninstallFields,
		desc: "Deletes the Components and Enrollments of a component, and their workloads.",
	}
	opDestroyDownstreams = &operation{
		id: "destroy-downstreams", title: "Destroy downstreams", command: []string{"destroy", "downstreams"},
		plan: true, danger: true, fields: downstreamFields,
		desc: "Uninstalls the base layer, then destroys the rke2 clusters.",
	}
	opDestroy = &operation{
		id: "destroy", title: "Destroy management", command: []string{"destroy"}, plan: true, danger: true,
		desc: "Destroys the management cluster (tofu destroy).",
	}
	opInit = &operation{
		id: "init", title: "Initialize an environment", command: []string{"init"}, fields: initFields,
		desc: "Creates ~/.rondo and an rondo.yaml to fill in.",
	}
	opSelectEnv = &operation{
		id: "env-select", title: "Select the environment", command: []string{"env", "select"}, quiet: true,
		desc: "The environment of the next commands, and its kubectl context.",
	}
)

var operations = []*operation{opDeploy, opDeployDownstreams, opInstall, opUninstall, opDestroyDownstreams, opDestroy, opInit}

type fieldKind int

const (
	fieldText fieldKind = iota
	fieldToggle
	fieldPick
)

type field struct {
	kind  fieldKind
	label string
	// flag is empty for a positional argument.
	flag     string
	help     string
	required bool
	// def is passed when the text is left empty.
	def     string
	input   textinput.Model
	on      bool
	choices []string
	picked  []bool
	cursor  int
}

func textField(label, flag, placeholder, value string) field {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = placeholder
	in.SetValue(value)
	in.SetWidth(48)
	in.SetStyles(inputStyles())
	return field{kind: fieldText, label: label, flag: flag, input: in}
}

func inputStyles() textinput.Styles {
	s := textinput.DefaultDarkStyles()
	s.Cursor.Blink = false
	for _, st := range []*textinput.StyleState{&s.Focused, &s.Blurred} {
		st.Placeholder = stDim
		st.Suggestion = stDim
		st.Text = lipgloss.NewStyle()
	}
	return s
}

func pickField(label, help string, choices []string, preset string) field {
	f := field{kind: fieldPick, label: label, help: help, choices: choices, picked: make([]bool, len(choices))}
	for i, c := range choices {
		if c == preset {
			f.picked[i], f.cursor = true, i
		}
	}
	return f
}

func downstreamFields(m *Model, preset string) []field {
	names, err := m.downstreams()
	help := "none checked => all"
	if err != nil {
		help = "unreadable manifest: " + err.Error()
	} else if len(names) == 0 {
		help = "no rke2 target in the manifest"
	}
	return []field{pickField("rke2 Targets", help, names, preset)}
}

func installFields(m *Model, preset string) []field {
	name := textField("Component", "", "acme, mft…", preset)
	name.required = true
	name.input.ShowSuggestions = true
	name.input.SetSuggestions(m.treeNames())
	return []field{
		name,
		textField("Version", "--version", "empty => the channel's version", ""),
		textField("Channel", "--channel", bundle.ComponentChannel, ""),
		textField("From", "--from", "already extracted directory, no network", ""),
	}
}

func uninstallFields(m *Model, preset string) []field {
	name := textField("Component", "", "name of the installed tree", preset)
	name.required = true
	name.input.ShowSuggestions = true
	name.input.SetSuggestions(m.treeNames())
	return []field{name}
}

func initFields(_ *Model, preset string) []field {
	env := textField("Environment", "--env", "empty => default ($RONDO_HOME/rondo.yaml)", preset)
	env.def = defaultEnv
	return []field{
		env,
		textField("Artifact mirror", "--preset", "generic (default) or artifactory", ""),
		{kind: fieldToggle, label: "Overwrite", flag: "--force", help: "replaces an existing rondo.yaml"},
	}
}

type form struct {
	op     *operation
	fields []field
	focus  int
	err    string
}

func newForm(m *Model, op *operation, preset string) *form {
	f := &form{op: op}
	if op.fields != nil {
		f.fields = op.fields(m, preset)
	}
	f.setFocus(0)
	return f
}

func (f *form) setFocus(i int) tea.Cmd {
	if len(f.fields) == 0 {
		return nil
	}
	f.focus = (i + len(f.fields)) % len(f.fields)
	var cmd tea.Cmd
	for j := range f.fields {
		if f.fields[j].kind != fieldText {
			continue
		}
		if j == f.focus {
			cmd = f.fields[j].input.Focus()
		} else {
			f.fields[j].input.Blur()
		}
	}
	return cmd
}

// args is the command line the form describes, without the TUI's global flags; a preview names
// the missing values instead of failing.
func (f *form) args(preview bool) ([]string, error) {
	args := append([]string{}, f.op.command...)
	var flags []string
	for _, fl := range f.fields {
		switch fl.kind {
		case fieldText:
			v := strings.TrimSpace(fl.input.Value())
			if v == "" {
				v = fl.def
			}
			switch {
			case v == "" && fl.required && preview:
				args = append(args, "<"+strings.ToLower(fl.label)+">")
			case v == "" && fl.required:
				return nil, fmt.Errorf("%s: value required", fl.label)
			case v == "":
			case fl.flag == "":
				args = append(args, v)
			default:
				flags = append(flags, fl.flag, v)
			}
		case fieldToggle:
			if fl.on {
				flags = append(flags, fl.flag)
			}
		case fieldPick:
			for i, c := range fl.choices {
				if fl.picked[i] {
					args = append(args, c)
				}
			}
		}
	}
	return append(args, flags...), nil
}

// update handles a key; submit is true when the form is to be launched.
func (f *form) update(msg tea.KeyPressMsg) (cmd tea.Cmd, submit bool) {
	f.err = ""
	var cur *field
	if len(f.fields) > 0 {
		cur = &f.fields[f.focus]
	}
	switch msg.String() {
	case "enter":
		return nil, true
	case "tab":
		if cur != nil && cur.kind == fieldText && cur.input.ShowSuggestions && cur.input.CurrentSuggestion() != "" &&
			cur.input.CurrentSuggestion() != cur.input.Value() {
			break
		}
		return f.setFocus(f.focus + 1), false
	case "shift+tab":
		return f.setFocus(f.focus - 1), false
	}
	if cur == nil {
		return nil, false
	}
	switch cur.kind {
	case fieldToggle:
		switch msg.String() {
		case "space", "x":
			cur.on = !cur.on
		case "up":
			return f.setFocus(f.focus - 1), false
		case "down":
			return f.setFocus(f.focus + 1), false
		}
		return nil, false
	case fieldPick:
		switch msg.String() {
		case "space", "x":
			if len(cur.choices) > 0 {
				cur.picked[cur.cursor] = !cur.picked[cur.cursor]
			}
		case "up", "k":
			if cur.cursor == 0 {
				return f.setFocus(f.focus - 1), false
			}
			cur.cursor--
		case "down", "j":
			if cur.cursor >= len(cur.choices)-1 {
				return f.setFocus(f.focus + 1), false
			}
			cur.cursor++
		case "a":
			all := true
			for _, p := range cur.picked {
				all = all && p
			}
			for i := range cur.picked {
				cur.picked[i] = !all
			}
		}
		return nil, false
	}
	switch msg.String() {
	case "up":
		return f.setFocus(f.focus - 1), false
	case "down":
		return f.setFocus(f.focus + 1), false
	}
	cur.input, cmd = cur.input.Update(msg)
	return cmd, false
}

func (f *form) render(m *Model, width int) string {
	var b strings.Builder
	title := stTitle
	if f.op.danger {
		title = stBad.Bold(true)
	}
	b.WriteString(title.Render(f.op.title) + "\n")
	b.WriteString(stDim.Render(f.op.desc) + "\n\n")
	labelW := 0
	for _, fl := range f.fields {
		labelW = max(labelW, lipgloss.Width(fl.label))
	}
	for i, fl := range f.fields {
		mark := "  "
		label := stDim.Render(fit(fl.label, labelW, false))
		if i == f.focus {
			mark = stAccent.Render("› ")
			label = stBold.Render(fit(fl.label, labelW, false))
		}
		pad := strings.Repeat(" ", labelW+4)
		switch fl.kind {
		case fieldText:
			b.WriteString(mark + label + "  " + fl.input.View() + "\n")
		case fieldToggle:
			box := "[ ]"
			if fl.on {
				box = "[x]"
			}
			b.WriteString(mark + label + "  " + box + " " + stDim.Render(fl.help) + "\n")
		case fieldPick:
			b.WriteString(mark + label + "  " + stDim.Render(fl.help) + "\n")
			for j, c := range fl.choices {
				box := "[ ] "
				if fl.picked[j] {
					box = "[x] "
				}
				line := box + c
				if i == f.focus && j == fl.cursor {
					line = stSel.Render(line)
				}
				b.WriteString(pad + line + "\n")
			}
		}
	}
	if len(f.fields) > 0 {
		b.WriteByte('\n')
	}
	args, _ := f.args(true)
	b.WriteString(stDim.Render("$ ") + stBold.Render(fit("rondo "+strings.Join(m.commandArgs(f.op, args), " "), width-2, false)) + "\n")
	if f.op.danger {
		b.WriteString(stBad.Render("⚠ destructive operation: the CLI will ask for confirmation.") + "\n")
	}
	if f.err != "" {
		b.WriteString(stBad.Render(f.err) + "\n")
	}
	b.WriteString("\n" + helpLine([]binding{
		{"⏎", "run"}, {"tab", "next field"}, {"space", "check"}, {"esc", "cancel"},
	}))
	return b.String()
}

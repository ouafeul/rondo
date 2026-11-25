package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"rondo/internal/dashapi"
	"rondo/internal/kubeconfig"
)

type viewID string

const (
	vOverview    viewID = "overview"
	vComponents  viewID = "components"
	vEnrollments viewID = "enrollments"
	vTargets     viewID = "targets"
	vJobs        viewID = "jobs"
	vServices    viewID = "services"
	vTrees       viewID = "trees"
	vEnvs        viewID = "envs"
	vOps         viewID = "ops"
	vConsole     viewID = "console"
)

type viewDef struct {
	id    viewID
	key   string
	label string
	group string
	desc  string
	// fetch is nil for the views that read the workstation, not the cluster.
	fetch func(context.Context, Source) (any, error)
}

// views are the web dashboard's pages, then what the CLI does from the workstation.
var views = []viewDef{
	{vOverview, "1", "Overview", "Platform", "Readiness of every object of the platform.",
		func(ctx context.Context, s Source) (any, error) { return s.Overview(ctx) }},
	{vComponents, "2", "Components", "Platform", "What is deployed (Component) and where (Enrollment).",
		func(ctx context.Context, s Source) (any, error) { return s.Components(ctx) }},
	{vEnrollments, "3", "Enrollments", "Platform", "A Component on a Target: health, attempts and history.",
		func(ctx context.Context, s Source) (any, error) { return s.Enrollments(ctx) }},
	{vTargets, "4", "Targets", "Platform", "The deployment Targets: clusters and vSphere environments.",
		func(ctx context.Context, s Source) (any, error) { return s.Targets(ctx) }},
	{vJobs, "5", "Jobs & logs", "Runtime", "The Jobs rondo-controller launched, and their logs.",
		func(ctx context.Context, s Source) (any, error) { return s.Jobs(ctx) }},
	{vServices, "6", "Services", "Runtime", "The RONDO services of the management cluster.",
		func(ctx context.Context, s Source) (any, error) { return s.Services(ctx) }},
	{vTrees, "7", "Installations", "Workstation", "The components `rondo component install` installed.",
		func(ctx context.Context, s Source) (any, error) { return s.Trees(ctx) }},
	{vEnvs, "8", "Environments", "Workstation", "The environments of $RONDO_HOME and their rondo.yaml manifest.", nil},
	{vOps, "9", "Operations", "Workstation", "The CLI's commands: deployment, components, destruction.", nil},
	{vConsole, "0", "Console", "Workstation", "The output of the commands run from the interface.", nil},
}

func viewIndex(id viewID) int {
	for i, v := range views {
		if v.id == id {
			return i
		}
	}
	return 0
}

type binding struct{ key, desc string }

func helpLine(bs []binding) string { return keyLine(bs, nil) }

// keyLine shades the keys in off: refused, in read-only mode.
func keyLine(bs []binding, off map[string]bool) string {
	parts := make([]string, len(bs))
	for i, b := range bs {
		key := stKey.Render(b.key)
		if off[b.key] {
			key = stDim.Render(b.key)
		}
		parts[i] = key + " " + stDim.Render(b.desc)
	}
	return strings.Join(parts, stDim.Render(" · "))
}

// refused are the keys of a view that launch a command or edit an rondo.yaml: in read-only mode
// they are shaded, and launch nothing.
var refused = map[viewID]map[string]bool{
	vTargets: {"d": true, "x": true},
	vTrees:   {"i": true, "m": true, "u": true},
	vEnvs:    {"⏎": true, "e": true, "n": true},
	vOps:     {"⏎": true},
}

func data[T any](m *Model, id viewID) (T, bool) {
	v, ok := m.data[id].(T)
	return v, ok
}

// table builds the rows of a view from its latest data.
func (m *Model) table(id viewID) *table {
	now := m.now()
	t := &table{}
	switch id {
	case vOverview:
		t.cols = cols("Kind", "Name", "Ready", "State", "Message")
		t.flex("Message")
		sums, _ := data[[]dashapi.Summary](m, id)
		for _, s := range sums {
			for _, r := range s.Rows {
				if r.Ready != "True" {
					t.add(s.Kind+"/"+r.Name, dim(s.Kind), plain(r.Name), badge(r.Ready), cell{text: r.Phase, style: toneOf(r.Phase).style()}, dim(r.Message))
				}
			}
		}
	case vComponents:
		t.cols = cols("Name", "Archetype", "Chart", "Version", "Namespace", "Placements", "Depends on")
		t.flex("Chart", "Depends on")
		t.optional("Namespace", "Depends on")
		list, _ := data[[]dashapi.ComponentView](m, id)
		trees, grouped := data[[]Tree](m, vTrees)
		by := installedBy(trees)
		groups := map[string]*group{}
		for _, c := range list {
			ready := readyPlacements(c)
			cells := []cell{{text: c.Name, style: stBold}, plain(archetypeOf(c.Archetype, c.Groups)), dim(lastSegment(c.Chart)),
				plain(c.Version), plain(c.Namespace), readiness(ready, len(c.Enrollments)), dim(strings.Join(c.DependsOn, ", "))}
			if !grouped {
				t.add(c.Name, cells...)
				continue
			}
			g := groupOf(groups, by["Component/"+c.Name])
			g.rows = append(g.rows, leaf{c.Name, cells})
			g.ready, g.total = g.ready+ready, g.total+len(c.Enrollments)
		}
		if grouped {
			var roots []node
			for _, name := range treeOrder(groups) {
				g := groups[name]
				roots = append(roots, node{key: rootKey(name), rows: g.rows, cells: []cell{
					{text: rootLabel(name), style: stTitle}, plain(""), dim(fmt.Sprintf("%d Component%s", len(g.rows), plural(len(g.rows)))),
					dim(versionOf(trees, name)), plain(""), readiness(g.ready, g.total), plain(""),
				}})
			}
			t.addTree(roots, m.folded[id], m.filter[id])
			return t
		}
	case vEnrollments:
		t.cols = cols("Enrollment", "Component", "Target", "Ready", "Health", "Version", "Objects", "Attempts", "Failures", "Last attempt")
		for _, i := range []int{6, 7, 8} {
			t.cols[i].right = true
		}
		t.flex("Last attempt")
		t.optional("Objects", "Attempts", "Version", "Component")
		list, _ := data[[]dashapi.EnrollmentView](m, id)
		trees, grouped := data[[]Tree](m, vTrees)
		by := installedBy(trees)
		groups := map[string]*group{}
		for _, e := range list {
			last := dim("—")
			if a := e.LastAttempt; a != nil {
				last = cell{text: a.Result + " · " + ago(a.StartedAt, now), style: toneOf(a.Result).style()}
			}
			fails := dim("0")
			if e.ConsecutiveFailures > 0 {
				fails = cell{text: fmt.Sprint(e.ConsecutiveFailures), style: stBad}
			}
			health := dim("—")
			if e.Health != "" {
				health = badge(e.Health)
			}
			cells := []cell{plain(e.Name), plain(e.Component), plain(e.Target), badge(e.Ready), health,
				plain(e.ChartVersion), plain(fmt.Sprint(e.InventoryCount)), plain(fmt.Sprint(e.Attempts)), fails, last}
			if !grouped {
				t.add(e.Name, cells...)
				continue
			}
			g := groupOf(groups, enrollmentTree(by, e))
			g.rows = append(g.rows, leaf{e.Name, cells})
			g.total++
			if e.Ready == "True" {
				g.ready++
			}
			g.objects += e.InventoryCount
			g.fails += int(e.ConsecutiveFailures)
			g.health = worse(g.health, e.Health)
		}
		if grouped {
			var roots []node
			for _, name := range treeOrder(groups) {
				g := groups[name]
				health, fails := dim("—"), dim("0")
				if g.health != "" {
					health = badge(g.health)
				}
				if g.fails > 0 {
					fails = cell{text: fmt.Sprint(g.fails), style: stBad}
				}
				roots = append(roots, node{key: rootKey(name), rows: g.rows, cells: []cell{
					{text: rootLabel(name), style: stTitle}, dim(fmt.Sprintf("%d Enrollment%s", len(g.rows), plural(len(g.rows)))),
					plain(""), readiness(g.ready, g.total), health, dim(versionOf(trees, name)),
					plain(fmt.Sprint(g.objects)), plain(""), fails, plain(""),
				}})
			}
			t.addTree(roots, m.folded[id], m.filter[id])
			return t
		}
	case vTargets:
		t.cols = cols("Name", "Type", "Access", "Ready", "Roles", "Server", "Message")
		t.flex("Server", "Message")
		t.optional("Roles", "Type", "Server")
		list, _ := data[[]dashapi.TargetView](m, id)
		for _, tg := range list {
			t.add(tg.Name, plain(tg.Name), plain(tg.Type), plain(access(tg.InCluster)), badge(tg.Ready),
				plain(strings.Join(tg.Roles, ", ")), dim(tg.Server), dim(tg.Message))
		}
	case vJobs:
		t.cols = cols("State", "Job", "Verb", "Owner", "Started", "Duration")
		t.flex("Owner")
		t.optional("Duration", "Verb")
		list, _ := data[[]dashapi.JobView](m, id)
		for _, j := range list {
			t.add(j.Name, badge(j.State), plain(j.Name), plain(j.Verb), plain(j.Owner),
				dim(ago(j.StartedAt, now)), dim(duration(j.StartedAt, j.EndedAt, now)))
		}
	case vServices:
		t.cols = cols("Service", "State", "Version", "Ready", "Pod", "Pod state", "Restarts", "Node", "Started")
		t.cols[6].right = true
		t.flex("Node", "Started")
		t.optional("Node", "Started", "Version", "Ready")
		list, _ := data[[]dashapi.ServiceView](m, id)
		for _, s := range list {
			ready := fmt.Sprintf("%d/%d", s.Ready, s.Desired)
			if len(s.Pods) == 0 {
				t.add(s.Name+"/", plain(s.Name), badge(s.State), plain(s.Version), plain(ready), dim("no pod"))
			}
			for _, p := range s.Pods {
				state := p.Reason
				if state == "" {
					state = p.Phase
				}
				restarts := plain(fmt.Sprint(p.Restarts))
				if p.Restarts > 0 {
					restarts.style = stWarn
				}
				t.add(s.Name+"/"+p.Name, plain(s.Name), badge(s.State), plain(s.Version), plain(ready),
					plain(p.Name), cell{text: state, style: toneOf(state).style()}, restarts,
					dim(p.Node), dim(ago(p.StartedAt, now)))
			}
		}
	case vTrees:
		t.cols = cols("Name", "Version", "Components", "Enrollments", "Objects")
		for _, i := range []int{2, 3, 4} {
			t.cols[i].right = true
		}
		list, _ := data[[]Tree](m, id)
		for _, tr := range list {
			comps, enrolls := countKinds(tr.Objects)
			t.add(tr.Name, plain(tr.Name), plain(tr.Version), plain(fmt.Sprint(comps)),
				plain(fmt.Sprint(enrolls)), plain(fmt.Sprint(len(tr.Objects))))
		}
	case vEnvs:
		t.cols = cols(" ", "Environment", "kubectl context", "Manifest")
		t.flex("Manifest")
		for _, e := range m.envs {
			mark := plain("")
			if e == envLabel(m.env) {
				mark = cell{text: "●", style: stOK}
			}
			path := ""
			if s, ok := m.sites[e]; ok {
				path = s.path
			}
			t.add(e, mark, plain(e), plain(kubeconfig.ContextName(envName(e))), dim(path))
		}
	case vOps:
		t.cols = cols("Operation", "Command", "Description")
		t.flex("Description")
		for _, op := range operations {
			title, command := plain(op.title), plain("rondo "+strings.Join(op.command, " "))
			switch {
			case m.opts.ReadOnly:
				title, command = dim(op.title), dim("rondo "+strings.Join(op.command, " "))
			case op.danger:
				title.style = stBad
			}
			t.add(op.id, title, command, dim(op.desc))
		}
	}
	t.filter(m.filter[id])
	return t
}

// rootPrefix keys the roots of the Components and Enrollments views: an installation, the tree
// `rondo component install` (or `rondo deploy`) applied and recorded.
const rootPrefix = "installation:"

func rootKey(tree string) string { return rootPrefix + tree }

func rootName(key string) (string, bool) { return strings.CutPrefix(key, rootPrefix) }

// rootLabel names an installation's root; "" gathers what no installation recorded.
func rootLabel(tree string) string {
	if tree == "" {
		return "no installation"
	}
	return tree
}

// group is an installation's rows in a tree view, and what its root sums up.
type group struct {
	rows           []leaf
	ready, total   int
	objects, fails int
	health         string
}

func groupOf(groups map[string]*group, tree string) *group {
	if groups[tree] == nil {
		groups[tree] = &group{}
	}
	return groups[tree]
}

// installedBy maps each Kind/name to the installation that applied it.
func installedBy(trees []Tree) map[string]string {
	out := map[string]string{}
	for _, t := range trees {
		for _, o := range t.Objects {
			out[o] = t.Name
		}
	}
	return out
}

// enrollmentTree is the installation of an Enrollment's Component, so RONDO's base Enrollments
// (base-<target>) sit with their Components under rondo; else the one that applied it.
func enrollmentTree(by map[string]string, e dashapi.EnrollmentView) string {
	if t, ok := by["Component/"+e.Component]; ok {
		return t
	}
	return by["Enrollment/"+e.Name]
}

// treeOrder lists the installations of a view: the components by name, then RONDO's own, then
// what none recorded.
func treeOrder(groups map[string]*group) []string {
	rank := func(name string) int {
		switch {
		case name == "":
			return 2
		case name == "rondo" || strings.HasPrefix(name, "base-"):
			return 1
		}
		return 0
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if ri, rj := rank(names[i]), rank(names[j]); ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	return names
}

func versionOf(trees []Tree, name string) string {
	for _, t := range trees {
		if t.Name == name {
			return t.Version
		}
	}
	return ""
}

func readyPlacements(c dashapi.ComponentView) int {
	ready := 0
	for _, p := range c.Enrollments {
		if p.Ready == "True" {
			ready++
		}
	}
	return ready
}

// readiness is "ready/total ready", red while one is not.
func readiness(ready, total int) cell {
	switch {
	case total == 0:
		return dim("none")
	case ready < total:
		return cell{text: fmt.Sprintf("%d/%d ready", ready, total), style: stBad}
	}
	return cell{text: fmt.Sprintf("%d/%d ready", ready, total), style: stOK}
}

// worse is the worse of two healths, for an installation's root.
func worse(a, b string) string {
	rank := map[string]int{"Healthy": 1, "Progressing": 2, "Degraded": 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func access(inCluster bool) string {
	if inCluster {
		return "local"
	}
	return "remote"
}

func lastSegment(ref string) string { return ref[strings.LastIndex(ref, "/")+1:] }

func countKinds(objects []string) (comps, enrolls int) {
	for _, o := range objects {
		switch {
		case strings.HasPrefix(o, "Component/"):
			comps++
		case strings.HasPrefix(o, "Enrollment/"):
			enrolls++
		}
	}
	return comps, enrolls
}

// above is what a view shows over its table.
func (m *Model) above(id viewID, width int) string {
	switch id {
	case vOverview:
		sums, ok := data[[]dashapi.Summary](m, id)
		if !ok {
			return ""
		}
		return summaryCards(sums, width) + "\n" + stBold.Render("Needs attention") + " " +
			stDim.Render("objects whose Ready condition is not True")
	case vEnrollments:
		list, ok := data[[]dashapi.EnrollmentView](m, id)
		if !ok {
			return ""
		}
		var healthy, degraded, failing int
		for _, e := range list {
			switch e.Health {
			case "Healthy":
				healthy++
			case "Degraded":
				degraded++
			}
			if e.ConsecutiveFailures > 0 {
				failing++
			}
		}
		stat := func(label string, n int, st lipgloss.Style) string {
			if n == 0 {
				st = stBold
			}
			return stDim.Render(label+" ") + st.Bold(true).Render(fmt.Sprint(n))
		}
		return strings.Join([]string{
			stat("Enrollments", len(list), stBold), stat("Healthy", healthy, stOK),
			stat("Degraded", degraded, stBad), stat("Failing", failing, stBad),
		}, stDim.Render("  ·  "))
	case vOps:
		box := func(on bool) string {
			if on {
				return stWarn.Render("[x]")
			}
			return "[ ]"
		}
		line := box(m.dryRun) + " dry-run " + stKey.Render("D") + "    " +
			box(m.skipDownload) + " skip-download " + stKey.Render("S")
		if len(m.opts.Forward) > 0 {
			line += "    " + stDim.Render("forwarded: "+strings.Join(m.opts.Forward, " "))
		}
		return line
	}
	return ""
}

func summaryCards(sums []dashapi.Summary, width int) string {
	names := map[string]string{"Target": "Targets", "Component": "Components", "Enrollment": "Enrollments"}
	n := max(1, len(sums))
	cardW := (width - 2*(n-1)) / n
	stacked := cardW < 26
	if stacked {
		cardW = width
	}
	inner := cardW - 4
	card := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.BrightBlack).
		Padding(0, 1).Width(cardW)
	out := make([]string, 0, 2*n)
	for i, s := range sums {
		label := names[s.Kind]
		if label == "" {
			label = s.Kind
		}
		failing := s.Total - s.Ready
		status := stOK.Render("OK")
		switch {
		case s.Total == 0:
			status = stDim.Render("empty")
		case failing > 0:
			status = stBad.Render(fmt.Sprintf("%d not ready", failing))
		}
		label = fit(label, max(1, inner-lipgloss.Width(status)-1), false)
		count := stBold.Render(fmt.Sprint(s.Ready)) + stDim.Render(fmt.Sprintf("/%d ", s.Total))
		line2 := count + meter(s.Ready, s.Total, inner-lipgloss.Width(count))
		if i > 0 && !stacked {
			out = append(out, "  ")
		}
		out = append(out, card.Render(stBold.Render(label)+" "+status+"\n"+line2))
	}
	if stacked {
		return lipgloss.JoinVertical(lipgloss.Left, out...)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, out...)
}

func meter(ready, total, width int) string {
	if total == 0 || width <= 0 {
		return stDim.Render(strings.Repeat("─", max(0, width)))
	}
	full := width * ready / total
	st := stOK
	if ready < total {
		st = stBad
	}
	return st.Render(strings.Repeat("━", full)) + stDim.Render(strings.Repeat("─", width-full))
}

func plural(n int) string {
	if n > 1 {
		return "s"
	}
	return ""
}

// below is what a view shows under its table.
func (m *Model) below(id viewID, key string) string {
	if id != vEnvs || key == "" {
		return ""
	}
	return m.siteSummary(key)
}

// viewKeys are the keys a view adds to navigation, in the order the footer shows them.
func (m *Model) viewKeys(id viewID) []binding {
	switch id {
	case vOverview, vTargets:
		bs := []binding{{"⏎", "detail"}}
		if id == vTargets {
			bs = append(bs, binding{"d", "deploy"}, binding{"x", "destroy"})
		}
		return bs
	case vComponents:
		return []binding{{"⏎", "detail"}, {"space", "collapse, expand"}, {"+", "expand all"}, {"-", "collapse all"}}
	case vEnrollments:
		return []binding{{"⏎", "detail"}, {"space", "collapse, expand"}, {"+", "expand all"}, {"-", "collapse all"},
			{"l", "logs"}}
	case vJobs, vServices:
		return []binding{{"⏎", "logs"}}
	case vTrees:
		return []binding{{"⏎", "objects"}, {"i", "install"}, {"m", "update"}, {"u", "uninstall"}}
	case vEnvs:
		return []binding{{"⏎", "select"}, {"e", "edit rondo.yaml"}, {"n", "new"}}
	case vOps:
		return []binding{{"⏎", "run"}, {"D", "dry-run"}, {"S", "skip-download"}}
	}
	return nil
}

// act runs a view's key on the selected row; handled is false for a key the view ignores.
func (m *Model) act(id viewID, key, sel string) (tea.Cmd, bool) {
	switch {
	case key == "enter" && sel != "":
		return m.enter(id, sel), true
	case id == vEnrollments && key == "l" && sel != "" && !isRoot(sel):
		return m.enrollmentLogs(sel), true
	case id == vTargets && key == "d":
		return m.openForm(opDeployDownstreams, sel), true
	case id == vTargets && key == "x":
		return m.openForm(opDestroyDownstreams, sel), true
	case id == vTrees && key == "i":
		return m.openForm(opInstall, ""), true
	case id == vTrees && key == "m" && sel != "":
		return m.openForm(opInstall, sel), true
	case id == vTrees && key == "u" && sel != "":
		return m.openForm(opUninstall, sel), true
	case id == vEnvs && key == "e" && sel != "":
		return m.openEditor(sel), true
	case id == vEnvs && key == "n":
		return m.openForm(opInit, ""), true
	}
	return nil, false
}

func (m *Model) enter(id viewID, sel string) tea.Cmd {
	switch id {
	case vOverview:
		kind, name, _ := strings.Cut(sel, "/")
		to := map[string]viewID{"Target": vTargets, "Component": vComponents, "Enrollment": vEnrollments}[kind]
		if to == "" {
			return nil
		}
		m.sel[to] = name
		delete(m.folded, to)
		delete(m.filter, to)
		return m.switchView(viewIndex(to))
	case vComponents, vEnrollments, vTargets, vTrees:
		title := sel
		if name, ok := rootName(sel); ok {
			title = rootLabel(name)
		}
		m.push(&panel{kind: panelDetail, view: id, key: sel, title: title})
	case vJobs:
		return m.openLogs("Job "+sel, sel, "")
	case vServices:
		_, pod, _ := strings.Cut(sel, "/")
		if pod == "" {
			m.setFlash("no pod for this service", true)
			return nil
		}
		return m.openLogs("Pod "+pod, "", pod)
	case vEnvs:
		if sel == envLabel(m.env) {
			m.setFlash("environment "+sel+" already selected", false)
			return nil
		}
		return m.launch(opSelectEnv, []string{"env", "select", sel})
	case vOps:
		for _, op := range operations {
			if op.id == sel {
				return m.openForm(op, "")
			}
		}
	}
	return nil
}

func (m *Model) enrollmentLogs(name string) tea.Cmd {
	list, _ := data[[]dashapi.EnrollmentView](m, vEnrollments)
	for _, e := range list {
		if e.Name == name && e.LastAttempt != nil && e.LastAttempt.JobName != "" {
			return m.openLogs("Job "+e.LastAttempt.JobName, e.LastAttempt.JobName, "")
		}
	}
	m.setFlash("no attempt for "+name, true)
	return nil
}

const labelWidth = 22

func kv(label, value string) string {
	return stDim.Render(fit(label, labelWidth, false)) + " " + value + "\n"
}

// detail renders the object a detail panel shows, from the latest data.
func (m *Model) detail(p *panel, width int) string {
	now := m.now()
	p.links = p.links[:0]
	if name, ok := rootName(p.key); ok && (p.view == vComponents || p.view == vEnrollments) {
		return m.installation(p, name, width)
	}
	var b strings.Builder
	switch p.view {
	case vComponents:
		list, _ := data[[]dashapi.ComponentView](m, vComponents)
		for _, c := range list {
			if c.Name != p.key {
				continue
			}
			b.WriteString(kv("Archetype", archetypeOf(c.Archetype, c.Groups)))
			b.WriteString(kv("Chart", dash(c.Chart)))
			b.WriteString(kv("Version", dash(c.Version)))
			b.WriteString(kv("Namespace", dash(c.Namespace)))
			b.WriteString(kv("VM groups", dash(strings.Join(c.Groups, ", "))))
			b.WriteString(kv("Depends on", dash(strings.Join(c.DependsOn, ", "))))
			b.WriteString("\n" + stBold.Render("Placements") + "\n")
			t := &table{cols: cols("Enrollment", "Target", "Ready")}
			for _, e := range c.Enrollments {
				t.add(e.Name, cell{text: e.Name, style: stLink}, plain(e.Target), badge(e.Ready))
			}
			p.links = append(p.links, tableLinks(&b, t, vEnrollments)...)
			b.WriteString(t.render(width, len(t.rows)+1, -1))
			return b.String()
		}
	case vEnrollments:
		list, _ := data[[]dashapi.EnrollmentView](m, vEnrollments)
		for _, e := range list {
			if e.Name != p.key {
				continue
			}
			b.WriteString(kv("Component", e.Component))
			b.WriteString(kv("Target", dash(e.Target)))
			b.WriteString(kv("Ready", badge(e.Ready).style.Render(badge(e.Ready).text)+" "+stDim.Render(e.Message)))
			health := "—"
			if e.Health != "" {
				health = badge(e.Health).style.Render(badge(e.Health).text) + " " + stDim.Render(e.HealthMessage)
			}
			b.WriteString(kv("Health", health))
			b.WriteString(kv("Phase (VM)", dash(e.Phase)))
			b.WriteString(kv("Chart version", dash(e.ChartVersion)))
			b.WriteString(kv("Values hash", dash(e.ValuesHash)))
			b.WriteString(kv("Objects", fmt.Sprint(e.InventoryCount)))
			b.WriteString(kv("Attempts", fmt.Sprint(e.Attempts)))
			b.WriteString(kv("Consecutive failures", fmt.Sprint(e.ConsecutiveFailures)))
			if a := e.LastAttempt; a != nil {
				b.WriteString("\n" + stBold.Render("Last attempt") + "  " + stDim.Render("l: Job logs") + "\n")
				b.WriteString(kv("Job", a.JobName))
				b.WriteString(kv("Result", toneOf(a.Result).style().Render(dash(a.Result))))
				b.WriteString(kv("Version", dash(a.Version)))
				b.WriteString(kv("Started", dateTime(a.StartedAt, now)))
				b.WriteString(kv("Duration", duration(a.StartedAt, a.FinishedAt, now)))
				b.WriteString(kv("Message", dash(a.Message)))
			}
			if len(e.History) > 0 {
				b.WriteString("\n" + stBold.Render("History") + "\n")
				t := &table{cols: cols("Result", "Job", "Version", "Started", "Duration", "Message")}
				t.flex("Message")
				for i := len(e.History) - 1; i >= 0; i-- {
					a := e.History[i]
					t.add(a.JobName, cell{text: a.Result, style: toneOf(a.Result).style()}, plain(a.JobName),
						plain(a.Version), dim(ago(a.StartedAt, now)), dim(duration(a.StartedAt, a.FinishedAt, now)), dim(a.Message))
				}
				b.WriteString(t.render(width, len(t.rows)+1, -1))
			}
			return b.String()
		}
	case vTargets:
		list, _ := data[[]dashapi.TargetView](m, vTargets)
		for _, t := range list {
			if t.Name != p.key {
				continue
			}
			b.WriteString(kv("Type", t.Type))
			b.WriteString(kv("Access", access(t.InCluster)))
			b.WriteString(kv("Server", dash(t.Server)))
			b.WriteString(kv("Ready", badge(t.Ready).style.Render(badge(t.Ready).text)+" "+stDim.Render(t.Message)))
			b.WriteString(kv("Roles", dash(strings.Join(t.Roles, ", "))))
			if len(t.Values) > 0 {
				b.WriteString("\n" + stBold.Render("Values") + "\n")
				keys := make([]string, 0, len(t.Values))
				for k := range t.Values {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					b.WriteString(kv(k, t.Values[k]))
				}
			}
			return b.String()
		}
	case vTrees:
		list, _ := data[[]Tree](m, vTrees)
		for _, t := range list {
			if t.Name != p.key {
				continue
			}
			b.WriteString(kv("Version", dash(t.Version)))
			b.WriteString(kv("Record", "configmap/rondo-component-"+t.Name))
			b.WriteString("\n" + stBold.Render("Applied objects") + "\n")
			for _, o := range t.Objects {
				b.WriteString("  " + o + "\n")
			}
			return b.String()
		}
	}
	return stDim.Render(p.key + " is gone.")
}

func isRoot(key string) bool {
	_, ok := rootName(key)
	return ok
}

// tableLinks are the rows of t, about to be written after b, as links to their detail in view.
func tableLinks(b *strings.Builder, t *table, view viewID) []link {
	header := strings.Count(b.String(), "\n")
	out := make([]link, len(t.keys))
	for i, k := range t.keys {
		out[i] = link{y: header + 1 + i, view: view, key: k}
	}
	return out
}

// installation renders what an installation put on the site: its Components, charts and
// machines, then their Enrollments, each a link to its detail.
func (m *Model) installation(p *panel, name string, width int) string {
	trees, _ := data[[]Tree](m, vTrees)
	by := installedBy(trees)
	var b strings.Builder
	record := "—"
	for _, t := range trees {
		if t.Name == name {
			record = "configmap/rondo-component-" + name
		}
	}
	b.WriteString(kv("Version", dash(versionOf(trees, name))))
	b.WriteString(kv("Record", record))
	b.WriteString("\n" + stBold.Render("Components") + "  " + stDim.Render("charts and machines") + "\n")
	if comps, ok := data[[]dashapi.ComponentView](m, vComponents); ok {
		t := &table{cols: cols("Component", "Archetype", "Chart", "Version", "Namespace", "Placements")}
		t.flex("Chart")
		for _, c := range comps {
			if by["Component/"+c.Name] == name {
				t.add(c.Name, cell{text: c.Name, style: stLink}, plain(archetypeOf(c.Archetype, c.Groups)), dim(dash(c.Chart)),
					plain(c.Version), plain(c.Namespace), readiness(readyPlacements(c), len(c.Enrollments)))
			}
		}
		p.links = append(p.links, tableLinks(&b, t, vComponents)...)
		b.WriteString(t.render(width, len(t.rows)+1, -1) + "\n")
	} else {
		b.WriteString(m.pending(vComponents) + "\n")
	}
	b.WriteString("\n" + stBold.Render("Enrollments") + "\n")
	if enrolls, ok := data[[]dashapi.EnrollmentView](m, vEnrollments); ok {
		t := &table{cols: cols("Enrollment", "Component", "Target", "Ready", "Health", "Version")}
		for _, e := range enrolls {
			if enrollmentTree(by, e) != name {
				continue
			}
			health := dim("—")
			if e.Health != "" {
				health = badge(e.Health)
			}
			t.add(e.Name, cell{text: e.Name, style: stLink}, plain(e.Component), plain(e.Target), badge(e.Ready), health,
				plain(e.ChartVersion))
		}
		p.links = append(p.links, tableLinks(&b, t, vEnrollments)...)
		b.WriteString(t.render(width, len(t.rows)+1, -1))
	} else {
		b.WriteString(m.pending(vEnrollments))
	}
	return b.String()
}

// pending says why half of an installation's panel is missing: its read failed, or it is still
// coming.
func (m *Model) pending(id viewID) string {
	if err := m.errs[id]; err != nil {
		return stBad.Render("cannot read: " + err.Error())
	}
	return stDim.Render("loading…")
}

// siteSummary shows what rondo.yaml declares, never its secrets.
func (m *Model) siteSummary(env string) string {
	s, ok := m.sites[env]
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString(stBold.Render("Manifest") + " " + stDim.Render(s.path) + "\n")
	switch {
	case s.problem != nil:
		return b.String() + stBad.Render(problemText(*s.problem)) + "\n" + stDim.Render("e: edit the manifest") + "\n"
	case s.err != nil:
		return b.String() + stBad.Render(s.err.Error()) + "\n"
	}
	c := s.cfg
	b.WriteString(kv("environmentName", dash(c.EnvironmentName)))
	b.WriteString(kv("ingressBaseDomain", c.IngressBaseDomain))
	b.WriteString(kv("Artifactory", c.Artifactory.URL+" "+stDim.Render("("+c.Artifactory.Username+")")))
	b.WriteString(kv("RONDO channel", dash(c.Artifactory.RondoReleaseChannel)))
	mg := c.Targets.Management
	b.WriteString(kv("Management", fmt.Sprintf("k3s %s on %s, %d nodes, ingress %s",
		mg.K3sVersion, mg.VSphereTarget, len(mg.ServerNodes), mg.IngressLoadBalancerIP)))
	for _, v := range c.Targets.VSphere {
		b.WriteString(kv("vSphere "+v.Name, v.VCenterAddress+" "+stDim.Render(v.Datacenter+"/"+v.ComputeCluster)))
	}
	for _, d := range c.Targets.Downstreams {
		b.WriteString(kv("rke2 "+d.Name, fmt.Sprintf("%s on %s, ingress %s",
			d.RKE2.RKE2Version, d.RKE2.VSphereTarget, d.RKE2.IngressLoadBalancerIP)))
	}
	return b.String()
}

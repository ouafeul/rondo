package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type cell struct {
	text  string
	style lipgloss.Style
}

func plain(s string) cell { return cell{text: s} }

func dim(s string) cell { return cell{text: s, style: stDim} }

type column struct {
	title string
	right bool
	// flex columns, free text, give their width up before the others.
	flex bool
	// drop orders the columns hidden when the table cannot fit, 1 first; 0 is always shown.
	drop int
}

func cols(titles ...string) []column {
	out := make([]column, len(titles))
	for i, t := range titles {
		out[i] = column{title: t}
	}
	return out
}

// table is rebuilt from the latest data on every render; the model keeps the selection by key.
type table struct {
	cols []column
	rows [][]cell
	keys []string
	// parents are the root key of each row of a tree, "" for a root or a flat row.
	parents []string
	// blur draws the cursor dimmed: the focus is elsewhere.
	blur bool
	// offset and shown are the rows render drew: shown from offset, under the header.
	offset, shown int
}

func (t *table) add(key string, cells ...cell) {
	t.keys = append(t.keys, key)
	t.rows = append(t.rows, cells)
	t.parents = append(t.parents, "")
}

// node is a root of a tree table: its cells, and the rows under it.
type node struct {
	key   string
	cells []cell
	rows  []leaf
}

type leaf struct {
	key   string
	cells []cell
}

// addTree adds each root, then its rows unless it is folded, the first cell marked ▾ or ▸ for a
// root and indented with a branch for a row. q keeps a root whose name matches it with all its
// rows, not its sums, else the rows that match it under their root, unfolded.
func (t *table) addTree(roots []node, folded map[string]bool, q string) {
	q = strings.ToLower(q)
	for _, r := range roots {
		rows := r.rows
		if q != "" && !matches(r.cells[:min(1, len(r.cells))], q) {
			rows = nil
			for _, l := range r.rows {
				if matches(l.cells, q) {
					rows = append(rows, l)
				}
			}
			if len(rows) == 0 {
				continue
			}
		}
		closed := folded[r.key] && q == ""
		mark := "▾ "
		if closed {
			mark = "▸ "
		}
		t.add(r.key, prefixed(mark, r.cells)...)
		if closed {
			continue
		}
		for i, l := range rows {
			branch := "  ├ "
			if i == len(rows)-1 {
				branch = "  └ "
			}
			t.add(l.key, prefixed(branch, l.cells)...)
			t.parents[len(t.parents)-1] = r.key
		}
	}
}

func prefixed(p string, cells []cell) []cell {
	out := append([]cell(nil), cells...)
	if len(out) > 0 {
		out[0].text = p + out[0].text
	}
	return out
}

// matches says whether a cell contains q, already lowered.
func matches(cells []cell, q string) bool {
	for _, c := range cells {
		if strings.Contains(strings.ToLower(c.text), q) {
			return true
		}
	}
	return false
}

// filter keeps the rows where a cell contains q, ignoring case.
func (t *table) filter(q string) {
	if q == "" {
		return
	}
	q = strings.ToLower(q)
	var rows [][]cell
	var keys, parents []string
	for i, r := range t.rows {
		if matches(r, q) {
			rows = append(rows, r)
			keys = append(keys, t.keys[i])
			parents = append(parents, t.parents[i])
		}
	}
	t.rows, t.keys, t.parents = rows, keys, parents
}

func (t *table) index(key string) int {
	for i, k := range t.keys {
		if k == key {
			return i
		}
	}
	return -1
}

const colGap = 2

// widths fits the columns in width: it shrinks the flex columns, hides the optional ones, then
// shrinks the widest. A hidden column is -1 wide.
func (t *table) widths(width int) []int {
	w := make([]int, len(t.cols))
	for i, c := range t.cols {
		w[i] = ansi.StringWidth(c.title)
	}
	for _, r := range t.rows {
		for i := range w {
			if i < len(r) {
				w[i] = max(w[i], ansi.StringWidth(r[i].text))
			}
		}
	}
	natural := append([]int(nil), w...)
	total := colGap * (len(w) - 1)
	for _, n := range w {
		total += n
	}
	shrink := func(flexOnly bool, floor int) {
		for total > width {
			widest := -1
			for i := range w {
				if (!flexOnly || t.cols[i].flex) && w[i] > floor && (widest < 0 || w[i] > w[widest]) {
					widest = i
				}
			}
			if widest < 0 {
				return
			}
			w[widest]--
			total--
		}
	}
	shrink(true, 12)
	for rank := 1; total > width; rank++ {
		found := false
		for i, c := range t.cols {
			if c.drop == rank {
				total -= w[i] + colGap
				w[i], found = -1, true
			}
		}
		if !found {
			break
		}
	}
	for i := range w {
		if t.cols[i].flex && w[i] >= 0 && total < width {
			grow := min(natural[i]-w[i], width-total)
			w[i] += grow
			total += grow
		}
	}
	shrink(false, 4)
	return w
}

// optional lets the titled columns be hidden on a narrow screen, the first one first.
func (t *table) optional(titles ...string) {
	for rank, title := range titles {
		for i := range t.cols {
			if t.cols[i].title == title {
				t.cols[i].drop = rank + 1
			}
		}
	}
}

// flex marks the free-text columns, by title.
func (t *table) flex(titles ...string) {
	for i := range t.cols {
		for _, title := range titles {
			if t.cols[i].title == title {
				t.cols[i].flex = true
			}
		}
	}
}

func fit(s string, w int, right bool) string {
	s = ansi.Truncate(s, w, "…")
	pad := strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
	if right {
		return pad + s
	}
	return s + pad
}

// render draws the header and the rows around cursor that fit in height.
func (t *table) render(width, height, cursor int) string {
	if height < 2 {
		height = 2
	}
	w := t.widths(width)
	gap := strings.Repeat(" ", colGap)
	var b strings.Builder
	var head []string
	for i, c := range t.cols {
		if w[i] >= 0 {
			head = append(head, fit(c.title, w[i], c.right))
		}
	}
	b.WriteString(stDim.Bold(true).Render(strings.Join(head, gap)))
	if len(t.rows) == 0 {
		b.WriteString("\n" + stDim.Render("No items."))
		return b.String()
	}
	visible := height - 1
	offset := 0
	if cursor >= visible {
		offset = cursor - visible + 1
	}
	t.offset, t.shown = offset, min(visible, len(t.rows)-offset)
	for i := offset; i < len(t.rows) && i < offset+visible; i++ {
		b.WriteByte('\n')
		var parts []string
		for j, c := range t.cols {
			if w[j] < 0 {
				continue
			}
			var cl cell
			if j < len(t.rows[i]) {
				cl = t.rows[i][j]
			}
			part := fit(cl.text, w[j], c.right)
			if i != cursor {
				part = cl.style.Render(part)
			}
			parts = append(parts, part)
		}
		line := strings.Join(parts, gap)
		switch {
		case i == cursor && t.blur:
			line = stSelBlur.Render(fit(line, width, false))
		case i == cursor:
			line = stSel.Render(fit(line, width, false))
		}
		b.WriteString(line)
	}
	return b.String()
}

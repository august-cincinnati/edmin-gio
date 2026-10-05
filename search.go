package main

import (
	"bytes"
	"fmt"
	"image"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
)

const maxSearchResults = 5000

// SearchDialog hosts project-wide search (Ctrl+Shift+F) and symbol results
// in a window of its own, with definitions and usages on separate tabs.
type SearchDialog struct {
	app *App
	win *app.Window

	entry  *Entry
	cs     Toggle
	clear  Button
	status string
	nb     Notebook
	gen    atomic.Int64

	defRows, useRows []*resultRow
	defList, useList scrollList
	defN, useN       int
	sel              *resultRow

	focusEntry bool
	deco       windowDeco
	tip        tooltipState
	mouse      image.Point

	// The results on display.
	lastStatus, lastNeedle string
	lastResults            []match
}

type match struct {
	path    string
	line    int // 0-based
	colByte int
	text    string
	isDef   bool
}

// resultRow is a file group (line < 0) or a match.
type resultRow struct {
	m        match
	label    []TextSpan
	children []*resultRow
	expanded bool
	group    bool
	click    Clicker
	path     string
}

const (
	tabDefs = iota
	tabUsages
)

func NewSearchDialog(app *App) *SearchDialog {
	s := &SearchDialog{app: app, entry: NewEntry()}
	s.entry.Placeholder = "Search in project (Enter)"
	s.nb.SetTabs([]any{tabDefs, tabUsages})
	s.nb.Current = tabUsages
	return s
}

// present shows the window, opening it if needed.
func (s *SearchDialog) present() {
	if s.win != nil {
		s.win.Perform(system.ActionRaise)
		s.win.Invalidate()
		return
	}
	w := new(app.Window)
	s.win = w
	w.Option(app.Title("Search"), app.Size(720, 520), app.Decorated(false))
	go s.loop(w)
}

func (s *SearchDialog) hide() {
	if s.win != nil {
		s.win.Perform(system.ActionClose)
	}
}

func (s *SearchDialog) loop(w *app.Window) {
	var ops op.Ops
	for {
		e := w.Event()
		uiMu.Lock()
		switch e := e.(type) {
		case app.DestroyEvent:
			if s.win == w {
				s.win = nil
			}
			uiMu.Unlock()
			return
		case app.ConfigEvent:
			s.deco.config(e.Config)
		case app.FrameEvent:
			s.app.drainQueue()
			gtx := app.NewContext(&ops, e)
			s.layout(gtx)
			e.Frame(gtx.Ops)
		}
		uiMu.Unlock()
	}
}

// Focus opens the dialog for a text search, optionally running one for text.
func (s *SearchDialog) Focus(text string) {
	s.present()
	s.nb.Current = tabUsages
	if text != "" {
		s.entry.SetText(text)
		s.run()
	}
	s.focusEntry = true
}

// walkProject calls fn for each regular, non-ignored file in the project.
func walkProject(root string, fn func(path string)) {
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && (ignoredDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			fn(p)
		}
		return nil
	})
}

// readTextFile returns file contents, or nil for large or binary files.
func readTextFile(p string) []byte {
	st, err := os.Stat(p)
	if err != nil || st.Size() > 4<<20 {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return nil
	}
	return data
}

func (s *SearchDialog) run() {
	needle := s.entry.Text()
	if needle == "" {
		return
	}
	matchCase := s.cs.On
	gen := s.gen.Add(1)
	root := s.app.root
	open := s.app.openBuffers()
	s.status = "Searching…"
	s.defRows, s.useRows = nil, nil

	go func() {
		var results []match
		lowNeedle := strings.ToLower(needle)
		walkProject(root, func(p string) {
			if s.gen.Load() != gen || len(results) >= maxSearchResults {
				return
			}
			var data []byte
			if txt, ok := open[p]; ok {
				data = []byte(txt)
			} else {
				data = readTextFile(p)
			}
			if data == nil {
				return
			}
			for i, line := range strings.Split(string(data), "\n") {
				hay := line
				n := needle
				if !matchCase {
					hay, n = strings.ToLower(line), lowNeedle
				}
				if idx := strings.Index(hay, n); idx >= 0 {
					results = append(results, match{path: p, line: i, colByte: idx, text: line})
				}
			}
		})
		s.app.later(func() {
			if s.gen.Load() != gen {
				return
			}
			msg := fmt.Sprintf("%s for “%s”", pluralize(len(results), "result", "results"), needle)
			if len(results) >= maxSearchResults {
				msg += " (truncated)"
			}
			s.show(msg, results, needle, false)
		})
	}()
}

func pluralize(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// show fills the Definition tab with the definitions among results and the
// Usages tab with the rest, grouped by file. symbol selects the tab to show:
// symbol lookups open on their definitions when there are any.
func (s *SearchDialog) show(status string, results []match, needle string, symbol bool) {
	s.lastStatus, s.lastResults, s.lastNeedle = status, results, needle
	s.status = status
	th := s.app.theme
	fg, dim := rgb(th.FG), rgb(th.Dim)

	var defs []match
	byFile := map[string][]match{}
	var files []string
	for _, m := range results {
		if m.isDef {
			defs = append(defs, m)
			continue
		}
		if _, ok := byFile[m.path]; !ok {
			files = append(files, m.path)
		}
		byFile[m.path] = append(byFile[m.path], m)
	}

	s.defRows, s.useRows = nil, nil
	for _, m := range defs {
		label := append([]TextSpan{{Text: relPath(s.app.root, m.path) + " ", Size: small(uiSize), Color: fg}}, matchSpans(th, m, needle)...)
		s.defRows = append(s.defRows, &resultRow{m: m, label: label, path: m.path})
	}
	if len(defs) == 0 {
		s.defRows = append(s.defRows, &resultRow{m: match{line: -1}, group: false,
			label: []TextSpan{{Text: "No definitions found", Color: dim, Font: italic(uiFont)}}})
	}
	sort.Strings(files)
	for _, f := range files {
		ms := byFile[f]
		g := &resultRow{group: true, expanded: len(files) <= 30, path: f, m: match{path: f, line: -1},
			label: []TextSpan{
				{Text: relPath(s.app.root, f), Color: fg, Weight: font.Bold},
				{Text: fmt.Sprintf(" (%d)", len(ms)), Color: fg, Size: small(uiSize)},
			}}
		for _, m := range ms {
			g.children = append(g.children, &resultRow{m: m, label: matchSpans(th, m, needle), path: m.path})
		}
		s.useRows = append(s.useRows, g)
	}
	s.defN, s.useN = len(defs), len(results)-len(defs)
	s.defList.offY, s.useList.offY = 0, 0
	s.sel = nil
	if symbol && len(defs) > 0 {
		s.nb.Current = tabDefs
	} else {
		s.nb.Current = tabUsages
	}
	if s.win != nil {
		s.win.Invalidate()
	}
}

// restyle redraws the results in the window's theme colours.
func (s *SearchDialog) restyle() {
	if s.lastResults != nil {
		s.show(s.lastStatus, s.lastResults, s.lastNeedle, s.nb.Current == tabDefs)
	}
	if s.win != nil {
		s.win.Invalidate()
	}
}

// matchSpans renders a result line: dim line number, an optional "def"
// marker, and the text with the match highlighted.
func matchSpans(theme *Theme, m match, needle string) []TextSpan {
	fg := rgb(theme.FG)
	text := strings.TrimLeft(m.text, " \t")
	trimmed := len(m.text) - len(text)
	if len(text) > 200 {
		cut := 200
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	text = strings.ReplaceAll(text, "\t", "    ")
	out := []TextSpan{{Text: fmt.Sprintf("%d: ", m.line+1), Color: rgb(theme.Dim)}}
	if m.isDef {
		out = append(out, TextSpan{Text: "def ", Color: rgb(theme.DefMarker), Weight: font.Bold})
	}
	col := m.colByte - trimmed
	if col >= 0 && col+len(needle) <= len(text) && !strings.Contains(text[:col+len(needle)], "    ") {
		return append(out,
			TextSpan{Text: text[:col], Color: fg},
			TextSpan{Text: text[col : col+len(needle)], Color: rgb(theme.MatchFG), BG: rgb(theme.MatchBG), Weight: font.Bold},
			TextSpan{Text: text[col+len(needle):], Color: fg})
	}
	return append(out, TextSpan{Text: text, Color: fg})
}

// activate opens a match in the editor, or toggles a file group.
func (s *SearchDialog) activate(r *resultRow) {
	if r.group {
		r.expanded = !r.expanded
		return
	}
	if r.m.line < 0 || r.path == "" {
		return
	}
	if e := s.app.editors.Open(r.path); e != nil {
		e.GotoLine(r.m.line, r.m.colByte)
		s.hide()
		s.app.win.Perform(system.ActionRaise)
	}
}

// visibleRows flattens the current tab's rows.
func (s *SearchDialog) visibleRows() []*resultRow {
	if s.nb.Current == tabDefs {
		return s.defRows
	}
	var out []*resultRow
	for _, g := range s.useRows {
		out = append(out, g)
		if g.expanded {
			out = append(out, g.children...)
		}
	}
	return out
}

// ---- Layout ----

func (s *SearchDialog) layout(gtx layout.Context) {
	pal := s.app.theme.palette()
	curTip = &s.tip
	currentPalette = pal
	size := gtx.Constraints.Max

	root := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &s.mouse)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &s.mouse, Kinds: pointer.Move | pointer.Press | pointer.Drag})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok {
			s.tip.mouse = e.Position.Round()
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			s.hide()
		}
	}
	fill(gtx, pal.Panel)

	if s.focusEntry {
		s.focusEntry = false
		s.entry.Focus(gtx)
		s.entry.SetCaret(s.entry.Len(), 0)
	}
	s.entry.Update(gtx)
	if s.entry.Submitted() {
		s.run()
	}
	if s.cs.Changed(gtx, false) && s.entry.Text() != "" {
		s.run()
	}
	if s.clear.Clicked(gtx) {
		s.entry.SetText("")
	}

	hb := s.deco.headerBar(gtx, s.win, pal, "Search", "", nil, nil, system.ActionClose)
	func() {
		defer op.Offset(image.Pt(0, hb)).Push(gtx.Ops).Pop()
		g := gtx
		g.Constraints = layout.Exact(image.Pt(size.X, size.Y-hb))
		layout.UniformInset(8).Layout(g, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return s.layoutSearchEntry(gtx, pal)
						}),
						layout.Rigid(spacer(8, 0)),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return s.cs.Layout(gtx, pal, "Match case", false)
						}),
					)
				}),
				layout.Rigid(spacer(0, 6)),
				layout.Rigid(Label{Text: s.status, Color: pal.FG, Truncate: true}.Layout),
				layout.Rigid(spacer(0, 6)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return s.layoutResults(gtx, pal)
				}),
			)
		})
	}()
	root.Pop()
	layoutTooltip(gtx, &s.tip)
}

// layoutSearchEntry draws a GTK search entry: a magnifier, the text and a
// clear icon.
func (s *SearchDialog) layoutSearchEntry(gtx layout.Context, pal palette) layout.Dimensions {
	w := gtx.Constraints.Max.X
	d := s.entry.layoutWithIcons(gtx, pal, w)
	return d
}

func (s *SearchDialog) layoutResults(gtx layout.Context, pal palette) layout.Dimensions {
	size := gtx.Constraints.Max
	r := image.Rectangle{Max: size}
	s.nb.SetTabs([]any{tabDefs, tabUsages})
	inner := gtx
	inner.Constraints = layout.Exact(image.Pt(size.X-2*gtx.Dp(1), size.Y-2*gtx.Dp(1)))
	func() {
		defer op.Offset(image.Pt(gtx.Dp(1), gtx.Dp(1))).Push(gtx.Ops).Pop()
		sd := s.nb.LayoutStrip(inner, notebookStyle{
			Pal:     pal,
			NoClose: true,
			Label: func(gtx layout.Context, i int, active bool) layout.Dimensions {
				t := fmt.Sprintf("Definition (%d)", s.defN)
				if i == tabUsages {
					t = fmt.Sprintf("Usages (%d)", s.useN)
				}
				return Label{Text: t, Color: pal.FG}.Layout(gtx)
			},
		})
		g := inner
		g.Constraints = layout.Exact(image.Pt(inner.Constraints.Max.X, inner.Constraints.Max.Y-sd.Size.Y))
		defer op.Offset(image.Pt(0, sd.Size.Y)).Push(gtx.Ops).Pop()
		fill(g, pal.BG)
		rows := s.visibleRows()
		list := &s.useList
		if s.nb.Current == tabDefs {
			list = &s.defList
		}
		rowH := gtx.Dp(24)
		indent := gtx.Dp(20)
		var act *resultRow
		list.layout(g, len(rows), rowH, 0, func(gtx layout.Context, i int) {
			row := rows[i]
			depth := 0
			if !row.group && s.nb.Current == tabUsages {
				depth = 1
			}
			x0 := depth * indent
			for _, c := range row.click.Update(gtx) {
				if c.Button != pointer.ButtonPrimary {
					continue
				}
				s.sel = row
				if row.group && c.Pos.X < x0+indent {
					row.expanded = !row.expanded
				} else if c.Count == 2 {
					act = row
				}
			}
			if row == s.sel {
				fill(gtx, pal.Selection)
			}
			hasExpander := s.nb.Current == tabUsages
			if row.group {
				icon := "pan-end-symbolic"
				if row.expanded {
					icon = "pan-down-symbolic"
				}
				func() {
					defer op.Offset(image.Pt(x0+(indent-gtx.Dp(16))/2, (rowH-gtx.Dp(16))/2)).Push(gtx.Ops).Pop()
					drawIcon(gtx, icon, 16, pal.FG)
				}()
			}
			tx := x0 + gtx.Dp(2)
			if hasExpander {
				tx = x0 + indent
			}
			func() {
				defer op.Offset(image.Pt(tx, 0)).Push(gtx.Ops).Pop()
				g := gtx
				g.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X-tx, rowH))
				layout.W.Layout(g, func(gtx layout.Context) layout.Dimensions { return layoutSpans(gtx, row.label) })
			}()
			rr := image.Rect(0, 0, gtx.Constraints.Max.X, rowH)
			row.click.Add(gtx, rr, pointer.CursorDefault)
			if row.path != "" {
				tooltipArea(gtx, &row.click, rr, row.path)
			}
		})
		if act != nil {
			s.activate(act)
		}
	}()
	notebookOutline(gtx, r)
	return layout.Dimensions{Size: size}
}

// layoutWithIcons draws the entry as a GTK search entry.
func (e *Entry) layoutWithIcons(gtx layout.Context, pal palette, w int) layout.Dimensions {
	h := gtx.Dp(34)
	r := image.Rect(0, 0, w, h)
	rad := gtx.Dp(5)
	fillRRect(gtx, r, rad, pal.BG)
	strokeShape(gtx, clip.UniformRRect(r, rad), gtx.Dp(1), pal.Border)
	if gtx.Focused(&e.Editor) {
		strokeShape(gtx, clip.UniformRRect(r, rad), gtx.Dp(2), accent)
	}
	icon := gtx.Dp(16)
	func() {
		defer op.Offset(image.Pt(gtx.Dp(8), (h-icon)/2)).Push(gtx.Ops).Pop()
		drawIcon(gtx, "edit-find-symbolic", 16, withAlpha(pal.FG, 0.7))
	}()
	padL, padR := gtx.Dp(32), gtx.Dp(8)
	if e.Len() > 0 {
		padR = gtx.Dp(32)
		cr := image.Rect(w-gtx.Dp(28), (h-icon)/2-gtx.Dp(4), w-gtx.Dp(4), (h+icon)/2+gtx.Dp(4))
		for _, c := range e.clearClick.Update(gtx) {
			if c.Button == pointer.ButtonPrimary {
				e.SetText("")
			}
		}
		func() {
			defer op.Offset(image.Pt(w-gtx.Dp(24), (h-icon)/2)).Push(gtx.Ops).Pop()
			drawIcon(gtx, "edit-clear-symbolic", 16, withAlpha(pal.FG, 0.7))
		}()
		e.clearClick.Add(gtx, cr, pointer.CursorDefault)
	}
	func() {
		defer clip.Rect{Min: image.Pt(padL, 0), Max: image.Pt(w-padR, h)}.Push(gtx.Ops).Pop()
		lh := lineHeight(gtx, uiFont, uiSize)
		defer op.Offset(image.Pt(padL, (h-lh)/2)).Push(gtx.Ops).Pop()
		g := gtx
		g.Constraints = layout.Exact(image.Pt(w-padL-padR, lh))
		if e.Editor.Len() == 0 && e.Placeholder != "" {
			Label{Text: e.Placeholder, Color: withAlpha(pal.FG, 0.5)}.Layout(g)
		}
		e.Editor.LineHeightScale = 1.2
		e.Editor.Layout(g, shaper, uiFont, uiSize, colorMaterial(gtx, pal.FG), colorMaterial(gtx, pal.Selection))
	}()
	return layout.Dimensions{Size: r.Size()}
}

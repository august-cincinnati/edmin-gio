package main

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
)

// undoOp is a single reversible buffer change.
type undoOp struct {
	insert bool
	offset int // byte offset
	text   string
}

// Editor is one open file in a tab.
type Editor struct {
	Path string
	View *CodeView
	lang *Language
	area *EditorArea

	hlPending bool
	hlGen     int // incremented on every edit; stale highlight results are dropped
	undo      []undoOp
	redo      []undoOp
	replaying bool
	lastBreak bool // next insert starts a new undo group
}

// EditorArea manages the tabbed editors and the in-file search bar.
type EditorArea struct {
	app     *App
	nb      Notebook
	editors []*Editor // in tab order

	findOpen   bool
	findEnt    *Entry
	findCase   Toggle
	findInfo   string
	prevBtn    Button
	nextBtn    Button
	findClose  Button
	focusFind  bool
	lastNeedle string
}

func NewEditorArea(app *App) *EditorArea {
	a := &EditorArea{app: app, findEnt: NewEntry()}
	a.nb.OnSwitch = func(int) {
		a.app.updateStatus()
		a.app.updateTitle()
		if e := a.Current(); e != nil {
			a.app.focusTag(e.View)
		}
	}
	a.nb.OnReorder = func() {
		var es []*Editor
		for _, k := range a.nb.Keys() {
			es = append(es, k.(*Editor))
		}
		a.editors = es
	}
	return a
}

func (a *EditorArea) syncTabs() {
	keys := make([]any, len(a.editors))
	for i, e := range a.editors {
		keys[i] = e
	}
	a.nb.SetTabs(keys)
}

func (a *EditorArea) Current() *Editor {
	if len(a.editors) == 0 {
		return nil
	}
	return a.editors[max(0, min(a.nb.Current, len(a.editors)-1))]
}

func (a *EditorArea) indexOf(e *Editor) int {
	for i, x := range a.editors {
		if x == e {
			return i
		}
	}
	return -1
}

// setCurrent switches to tab i.
func (a *EditorArea) setCurrent(i int) {
	a.nb.Current = i
	a.app.updateStatus()
	a.app.updateTitle()
}

func (a *EditorArea) find(path string) *Editor {
	for _, e := range a.editors {
		if e.Path == path {
			return e
		}
	}
	return nil
}

// Open opens path in a tab (or focuses its existing tab).
func (a *EditorArea) Open(path string) *Editor {
	path, _ = filepath.Abs(path)
	if e := a.find(path); e != nil {
		a.setCurrent(a.indexOf(e))
		return e
	}
	data, err := os.ReadFile(path)
	if err != nil {
		a.app.showError("Cannot open file", err.Error())
		return nil
	}
	if !utf8.Valid(data) {
		a.app.showError("Cannot open file", path+" does not look like a UTF-8 text file.")
		return nil
	}
	e := &Editor{Path: path, area: a, lang: languageFor(path), lastBreak: true}
	e.View = NewCodeView(string(data))
	e.connect()

	// The new tab goes after the current one's position at the end, as in GTK.
	a.editors = append(a.editors, e)
	a.syncTabs()
	a.setCurrent(len(a.editors) - 1)
	e.scheduleHighlight()
	a.app.focusTag(e.View)
	return e
}

// Close closes e's tab, first asking whether to save unsaved changes.
// then, if given, runs once the tab has closed.
func (a *EditorArea) Close(e *Editor, then ...func()) {
	finish := func() {
		i := a.indexOf(e)
		if i < 0 {
			return
		}
		a.editors = append(a.editors[:i], a.editors[i+1:]...)
		cur := a.nb.Current
		if i < cur || cur >= len(a.editors) {
			cur--
		}
		a.syncTabs()
		a.nb.Current = max(cur, 0)
		a.app.updateStatus()
		a.app.updateTitle()
		for _, fn := range then {
			fn()
		}
	}
	if !e.View.Modified() {
		finish()
		return
	}
	a.setCurrent(a.indexOf(e))
	a.app.askSave(filepath.Base(e.Path), func(resp int) {
		switch resp {
		case RespYes:
			if !e.Save() {
				return
			}
		case RespNo:
		default:
			return
		}
		finish()
	})
}

// Unsaved returns editors with unsaved changes.
func (a *EditorArea) Unsaved() []*Editor {
	var out []*Editor
	for _, e := range a.editors {
		if e.View.Modified() {
			out = append(out, e)
		}
	}
	return out
}

func (e *Editor) Text() string { return e.View.Text() }

func (e *Editor) Save() bool {
	if err := os.WriteFile(e.Path, []byte(e.Text()), 0644); err != nil {
		e.area.app.showError("Save failed", err.Error())
		return false
	}
	e.View.SetModified(false)
	e.lastBreak = true
	return true
}

func (e *Editor) connect() {
	v := e.View
	v.OnModified = func() { e.area.app.updateTitle() }
	v.OnChange = func() { e.scheduleHighlight() }
	v.OnCursor = func() {
		if e == e.area.Current() {
			e.area.app.updateStatus()
		}
	}
	v.OnInsert = func(off int, text string) { e.record(undoOp{insert: true, offset: off, text: text}) }
	v.OnDelete = func(off int, text string) { e.record(undoOp{insert: false, offset: off, text: text}) }
	v.OnKey = e.onKey
	v.OnPress = func() {
		e.lastBreak = true
		e.clearJumpLine()
	}
	v.OnCtrlClick = func(p Pos) { e.area.app.symbolAction(e, p.Line, p.Col) }
}

// record appends to the undo history, merging runs of typed characters.
func (e *Editor) record(op undoOp) {
	if e.replaying {
		return
	}
	e.redo = nil
	if n := len(e.undo); n > 0 && !e.lastBreak {
		last := &e.undo[n-1]
		wordish := !strings.ContainsAny(op.text, " \n\t")
		if op.insert && last.insert && wordish && utf8.RuneCountInString(op.text) == 1 &&
			last.offset+len(last.text) == op.offset {
			last.text += op.text
			return
		}
		if !op.insert && !last.insert && utf8.RuneCountInString(op.text) == 1 && op.offset+len(op.text) == last.offset && wordish {
			last.text = op.text + last.text
			last.offset = op.offset
			return
		}
	}
	e.undo = append(e.undo, op)
	e.lastBreak = utf8.RuneCountInString(op.text) != 1
}

func (e *Editor) apply(op undoOp, reverse bool) {
	e.replaying = true
	defer func() { e.replaying = false }()
	v := e.View
	insert := op.insert != reverse
	start := v.posAt(op.offset)
	if insert {
		v.PlaceCursor(v.Replace(start, start, op.text))
	} else {
		end := v.posAt(op.offset + len(op.text))
		v.PlaceCursor(v.Replace(start, end, ""))
	}
	v.ScrollToCursor()
}

func (e *Editor) Undo() {
	if n := len(e.undo); n > 0 {
		op := e.undo[n-1]
		e.undo = e.undo[:n-1]
		e.apply(op, true)
		e.redo = append(e.redo, op)
		e.lastBreak = true
	}
}

func (e *Editor) Redo() {
	if n := len(e.redo); n > 0 {
		op := e.redo[n-1]
		e.redo = e.redo[:n-1]
		e.apply(op, false)
		e.undo = append(e.undo, op)
		e.lastBreak = true
	}
}

func (e *Editor) onKey(k key.Event) bool {
	mods := shortcutMods(k.Modifiers)
	switch k.Name {
	case key.NameReturn, key.NameEnter:
		if mods != 0 {
			return false
		}
		// Auto-indent: carry the current line's leading whitespace.
		v := e.View
		line := v.Line(v.cur.Line)[:v.cur.Col]
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		e.lastBreak = true
		v.InsertAtCursor("\n" + indent)
		return true
	case key.NameEscape:
		e.clearJumpLine()
		return false
	case key.NameLeftArrow, key.NameRightArrow, key.NameUpArrow, key.NameDownArrow, key.NameHome, key.NameEnd,
		key.NamePageUp, key.NamePageDown:
		e.lastBreak = true
	}
	return false
}

func (e *Editor) scheduleHighlight() {
	e.hlGen++
	if e.lang == nil || e.hlPending {
		return
	}
	e.hlPending = true
	e.area.app.after(120*time.Millisecond, func() {
		e.hlPending = false
		e.highlight()
	})
}

func (e *Editor) highlight() {
	if e.lang == nil || e.View.CharCount() > 2_000_000 {
		return
	}
	// Parse and query off the UI thread (the first use of a grammar also
	// compiles its queries), then apply the colours if nothing changed since.
	src := []byte(e.Text())
	gen, lang := e.hlGen, e.lang
	go func() {
		spans := lang.Highlight(src)
		e.area.app.later(func() {
			if gen == e.hlGen {
				e.View.ApplySpans(spans)
			}
		})
	}()
}

// GotoLine moves the cursor to a 0-based line and byte column and highlights it.
func (e *Editor) GotoLine(line, colByte int) {
	v := e.View
	line = max(0, min(line, v.LineCount()-1))
	v.SetJumpLine(line)
	col := 0
	if colByte > 0 && colByte <= len(v.Line(line)) {
		col = colByte
	}
	v.PlaceCursor(Pos{line, col})
	v.ScrollToLine(line, 0.3)
	e.area.app.focusTag(v)
}

func (e *Editor) clearJumpLine() { e.View.SetJumpLine(-1) }

// ---- Layout ----

func (a *EditorArea) Layout(gtx layout.Context) layout.Dimensions {
	app := a.app
	th := app.theme
	pal := th.palette()
	size := gtx.Constraints.Max
	fill(gtx, pal.Panel)

	findH := 0
	if a.findOpen {
		findH = gtx.Dp(38)
	}
	mainH := size.Y - findH

	if len(a.editors) == 0 {
		g := gtx
		g.Constraints = layout.Exact(image.Pt(size.X, mainH))
		layout.Center.Layout(g, func(gtx layout.Context) layout.Dimensions {
			return Label{Text: "Open a file from the explorer.\n\n" +
				"Ctrl+S save · Ctrl+W close tab · Ctrl+Tab next tab · Ctrl+F find · Ctrl+Shift+F find in project\n" +
				"Ctrl+Click a symbol: jump to definition / find usages\n" +
				"Ctrl+1 explorer · Ctrl+2 terminal · Ctrl+3 build panel · Ctrl+4 settings · Ctrl+5 editor\n" +
				"Ctrl+Shift+1–4 closes them", Color: pal.FG, Alignment: text.Middle, MaxLines: 10}.Layout(gtx)
		})
	} else {
		r := image.Rect(0, 0, size.X, mainH)
		g := gtx
		g.Constraints = layout.Exact(image.Pt(size.X-2*gtx.Dp(1), mainH))
		func() {
			defer op.Offset(image.Pt(gtx.Dp(1), gtx.Dp(1))).Push(gtx.Ops).Pop()
			sd := a.nb.LayoutStrip(g, notebookStyle{
				Pal: pal,
				Label: func(gtx layout.Context, i int, active bool) layout.Dimensions {
					e := a.editors[i]
					name := filepath.Base(e.Path)
					if e.View.Modified() {
						name = "● " + name
					}
					return Label{Text: name, Color: pal.FG}.Layout(gtx)
				},
				Tip:    func(i int) string { return a.editors[i].Path },
				Closed: func(i int) { a.Close(a.editors[i], app.focusWorkspace) },
			})
			e := a.Current()
			g.Constraints = layout.Exact(image.Pt(g.Constraints.Max.X, mainH-sd.Size.Y-2*gtx.Dp(1)))
			defer op.Offset(image.Pt(0, sd.Size.Y)).Push(gtx.Ops).Pop()
			e.View.Layout(g, app.codeStyle())
		}()
		notebookOutline(gtx, r)
	}
	if a.findOpen {
		func() {
			defer op.Offset(image.Pt(0, mainH)).Push(gtx.Ops).Pop()
			g := gtx
			g.Constraints = layout.Exact(image.Pt(size.X, findH))
			a.layoutFindBar(g, pal)
		}()
	}
	return layout.Dimensions{Size: size}
}

// codeStyle is how the window's editors draw.
func (app *App) codeStyle() codeStyle {
	th := app.theme
	syn := map[string]color.NRGBA{}
	for k, v := range th.Syntax {
		syn[k] = rgb(v)
	}
	return codeStyle{
		BG: rgb(th.BG), FG: rgb(th.FG), Selection: rgb(th.Selection), MatchBG: rgb(th.MatchBG), MatchFG: rgb(th.MatchFG),
		JumpLine: rgb(th.JumpLine), Syntax: syn, Gutter: true, Font: monoFont, Size: monoSize,
	}
}

// ---- In-file search (Ctrl+F) ----

func (a *EditorArea) layoutFindBar(gtx layout.Context, pal palette) layout.Dimensions {
	e := a.findEnt
	e.Update(gtx)
	if a.focusFind {
		a.focusFind = false
		e.Focus(gtx)
		e.SetCaret(e.Len(), 0)
	}
	if e.Escaped() {
		defer a.HideFind()
	}
	if e.ShiftEnter() || a.prevBtn.Clicked(gtx) {
		defer a.searchCurrent(false, false)
	}
	if e.Submitted() || a.nextBtn.Clicked(gtx) {
		defer a.searchCurrent(true, false)
	}
	if e.Changed() {
		defer a.searchCurrent(true, true)
	}
	if a.findCase.Changed(gtx, false) {
		defer a.searchCurrent(true, true)
	}
	if a.findClose.Clicked(gtx) {
		defer a.HideFind()
	}
	return layout.Inset{Left: 4, Right: 4, Top: 2, Bottom: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(Label{Text: "Find:", Color: pal.FG}.Layout),
			layout.Rigid(spacer(4, 0)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return e.Layout(gtx, pal, 236) // GTK's 30 characters
			}),
			layout.Rigid(spacer(4, 0)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return a.prevBtn.Layout(gtx, buttonStyle{Icon: "go-up-symbolic", Pal: pal})
			}),
			layout.Rigid(spacer(4, 0)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return a.nextBtn.Layout(gtx, buttonStyle{Icon: "go-down-symbolic", Pal: pal})
			}),
			layout.Rigid(spacer(8, 0)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return a.findCase.Layout(gtx, pal, "Match case", false)
			}),
			layout.Rigid(spacer(12, 0)),
			layout.Rigid(Label{Text: a.findInfo, Color: pal.FG}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return a.findClose.Layout(gtx, buttonStyle{Icon: "window-close-symbolic", Pal: pal})
			}),
		)
	})
}

func (a *EditorArea) ShowFind() {
	if e := a.Current(); e != nil {
		if s, en, ok := e.View.Selection(); ok && s.Line == en.Line {
			a.findEnt.SetText(e.View.SelectedText())
		}
	}
	a.findOpen = true
	a.focusFind = true
	a.app.invalidate()
	a.searchCurrent(true, true)
}

func (a *EditorArea) HideFind() {
	a.findOpen = false
	for _, e := range a.editors {
		e.View.SetMatches(nil)
	}
	if e := a.Current(); e != nil {
		a.app.focusTag(e.View)
	}
}

// findAll returns the byte ranges of needle in line.
func findAll(line, needle string, matchCase bool) [][2]int {
	var out [][2]int
	if needle == "" {
		return nil
	}
	if matchCase {
		for off := 0; ; {
			i := strings.Index(line[off:], needle)
			if i < 0 {
				return out
			}
			out = append(out, [2]int{off + i, off + i + len(needle)})
			off += i + len(needle)
		}
	}
	n := utf8.RuneCountInString(needle)
	for i := 0; i < len(line); {
		// Take n runes from i and compare case-insensitively.
		j, k := i, 0
		for k < n && j < len(line) {
			_, sz := utf8.DecodeRuneInString(line[j:])
			j += sz
			k++
		}
		if k == n && strings.EqualFold(line[i:j], needle) {
			out = append(out, [2]int{i, j})
			i = j
			continue
		}
		_, sz := utf8.DecodeRuneInString(line[i:])
		i += sz
	}
	return out
}

// searchCurrent highlights every match and selects the next/previous one.
// If fromSelStart is set, the search restarts at the current selection start
// (used while typing so the match under the cursor stays selected).
func (a *EditorArea) searchCurrent(forward, fromSelStart bool) {
	e := a.Current()
	if e == nil {
		return
	}
	v := e.View
	needle := a.findEnt.Text()
	v.SetMatches(nil)
	if needle == "" {
		a.findInfo = ""
		return
	}
	var ms []posRange
	for i := 0; i < v.LineCount(); i++ {
		for _, r := range findAll(v.Line(i), needle, a.findCase.On) {
			ms = append(ms, posRange{Pos{i, r[0]}, Pos{i, r[1]}})
		}
	}
	if len(ms) == 0 {
		a.findInfo = "No matches"
		return
	}
	v.SetMatches(ms)
	a.findInfo = pluralize(len(ms), "match", "matches")

	selS, selE, _ := v.Selection()
	var pick *posRange
	if forward {
		from := selE
		if fromSelStart {
			from = selS
		}
		for i := range ms {
			if !ms[i].a.less(from) {
				pick = &ms[i]
				break
			}
		}
		if pick == nil {
			pick = &ms[0]
		}
	} else {
		for i := len(ms) - 1; i >= 0; i-- {
			if !selS.less(ms[i].b) {
				pick = &ms[i]
				break
			}
		}
		if pick == nil {
			pick = &ms[len(ms)-1]
		}
	}
	v.Select(pick.a, pick.b)
	v.ScrollToCursor()
}

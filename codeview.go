package main

import (
	"image"
	"image/color"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
)

// Pos is a position in a CodeView: a 0-based line and a byte column.
type Pos struct{ Line, Col int }

func (p Pos) less(q Pos) bool { return p.Line < q.Line || p.Line == q.Line && p.Col < q.Col }

const tabWidth = 8 // GTK's default

// lineSpan colours bytes [Start, End) of one line with a highlight class.
type lineSpan struct {
	Start, End int
	Class      string
}

// CodeView is a monospace text editing widget with a line-number gutter,
// syntax colouring, search-match and jump-line highlights, in the style of
// a GTK TextView.
type CodeView struct {
	lines  []string
	cur    Pos // the insertion cursor
	anchor Pos // the other end of the selection; == cur when none
	wantX  int // visual column kept while moving up and down

	spans   [][]lineSpan // per line
	matches []posRange   // search matches
	jump    int          // highlighted line after a jump, or -1

	scrollX, scrollY int // pixels
	view             image.Point
	charW            float32
	lineH            int
	gutterW          int

	modified bool
	editable bool
	// OnInsert and OnDelete report every change at a byte offset.
	OnInsert, OnDelete func(off int, text string)
	OnChange           func() // after any change
	OnModified         func() // when the modified flag changes
	OnCursor           func() // after the cursor moves
	OnCtrlClick        func(p Pos)
	OnKey              func(e key.Event) bool // return true to consume

	focused   bool
	dragging  bool
	dragWords bool
	blinkT    time.Time
	scrollReq bool    // keep the cursor visible on the next frame
	scrollTo  *scroll // a pending scroll position
	sb        scrollbars
	lineStart []int // byte offset of each line, rebuilt lazily
	margin    int   // space between the gutter and the text
	mm        monoMetrics

	OnPress      func() // on any primary press
	lastClick    time.Time
	lastClickPos image.Point
	clickCount   int
}

type posRange struct{ a, b Pos }

type scroll struct {
	line int
	frac float32 // position the line this far down the view
}

func NewCodeView(text string) *CodeView {
	cv := &CodeView{jump: -1, editable: true}
	cv.lines = strings.Split(text, "\n")
	cv.spans = make([][]lineSpan, len(cv.lines))
	return cv
}

// ---- Buffer ----

func (cv *CodeView) Text() string { return strings.Join(cv.lines, "\n") }

func (cv *CodeView) LineCount() int { return len(cv.lines) }

func (cv *CodeView) Line(i int) string { return cv.lines[i] }

func (cv *CodeView) Cursor() Pos { return cv.cur }

func (cv *CodeView) Modified() bool { return cv.modified }

func (cv *CodeView) SetModified(m bool) {
	if cv.modified != m {
		cv.modified = m
		if cv.OnModified != nil {
			cv.OnModified()
		}
	}
}

func (cv *CodeView) CharCount() int {
	n := 0
	for _, l := range cv.lines {
		n += len(l) + 1
	}
	return n
}

// offset converts a position to a byte offset in Text().
func (cv *CodeView) offset(p Pos) int {
	if cv.lineStart == nil {
		cv.lineStart = make([]int, len(cv.lines))
		off := 0
		for i, l := range cv.lines {
			cv.lineStart[i] = off
			off += len(l) + 1
		}
	}
	return cv.lineStart[p.Line] + p.Col
}

// posAt converts a byte offset to a position.
func (cv *CodeView) posAt(off int) Pos {
	cv.offset(Pos{})
	lo, hi := 0, len(cv.lines)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if cv.lineStart[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return cv.clamp(Pos{lo, off - cv.lineStart[lo]})
}

func (cv *CodeView) clamp(p Pos) Pos {
	p.Line = max(0, min(p.Line, len(cv.lines)-1))
	l := cv.lines[p.Line]
	p.Col = max(0, min(p.Col, len(l)))
	for p.Col > 0 && p.Col < len(l) && !utf8.RuneStart(l[p.Col]) {
		p.Col--
	}
	return p
}

// textRange returns the text between a and b (a before b).
func (cv *CodeView) textRange(a, b Pos) string {
	if a.Line == b.Line {
		return cv.lines[a.Line][a.Col:b.Col]
	}
	var sb strings.Builder
	sb.WriteString(cv.lines[a.Line][a.Col:])
	for i := a.Line + 1; i < b.Line; i++ {
		sb.WriteByte('\n')
		sb.WriteString(cv.lines[i])
	}
	sb.WriteByte('\n')
	sb.WriteString(cv.lines[b.Line][:b.Col])
	return sb.String()
}

// Replace replaces the text between a and b with s and returns the end of
// the inserted text.
func (cv *CodeView) Replace(a, b Pos, s string) Pos {
	if b.less(a) {
		a, b = b, a
	}
	a, b = cv.clamp(a), cv.clamp(b)
	if a != b {
		if cv.OnDelete != nil {
			cv.OnDelete(cv.offset(a), cv.textRange(a, b))
		}
		head, tail := cv.lines[a.Line][:a.Col], cv.lines[b.Line][b.Col:]
		cv.lines[a.Line] = head + tail
		cv.lines = append(cv.lines[:a.Line+1], cv.lines[b.Line+1:]...)
		// Keep the colouring of the joined line's start until the next highlight.
		cv.spans = append(cv.spans[:a.Line+1], cv.spans[b.Line+1:]...)
		cv.shiftSpans(a.Line, a.Col, b.Col-a.Col, b.Line != a.Line)
		cv.lineStart = nil
	}
	end := a
	if s != "" {
		if cv.OnInsert != nil {
			cv.OnInsert(cv.offset(a), s)
		}
		parts := strings.Split(s, "\n")
		head, tail := cv.lines[a.Line][:a.Col], cv.lines[a.Line][a.Col:]
		if len(parts) == 1 {
			cv.lines[a.Line] = head + s + tail
			cv.shiftSpans(a.Line, a.Col, -len(s), false)
			end = Pos{a.Line, a.Col + len(s)}
		} else {
			nl := make([]string, 0, len(cv.lines)+len(parts)-1)
			nl = append(nl, cv.lines[:a.Line]...)
			nl = append(nl, head+parts[0])
			nl = append(nl, parts[1:len(parts)-1]...)
			nl = append(nl, parts[len(parts)-1]+tail)
			nl = append(nl, cv.lines[a.Line+1:]...)
			cv.lines = nl
			ns := make([][]lineSpan, 0, len(cv.lines))
			ns = append(ns, cv.spans[:a.Line+1]...)
			ns = append(ns, make([][]lineSpan, len(parts)-1)...)
			ns = append(ns, cv.spans[a.Line+1:]...)
			cv.spans = ns
			end = Pos{a.Line + len(parts) - 1, len(parts[len(parts)-1])}
		}
		cv.lineStart = nil
	}
	cv.matches = nil
	cv.SetModified(true)
	if cv.OnChange != nil {
		cv.OnChange()
	}
	return end
}

// shiftSpans adjusts line's spans after removing n bytes at col (or
// inserting -n bytes). Spans of a joined line are dropped after col.
func (cv *CodeView) shiftSpans(line, col, n int, joined bool) {
	var out []lineSpan
	for _, s := range cv.spans[line] {
		if s.End <= col {
			out = append(out, s)
			continue
		}
		if joined || s.Start < col {
			continue
		}
		s.Start -= n
		s.End -= n
		if s.Start >= 0 && s.End > s.Start {
			out = append(out, s)
		}
	}
	cv.spans[line] = out
}

// SetText replaces the whole buffer without recording changes.
func (cv *CodeView) SetText(s string) {
	cv.lines = strings.Split(s, "\n")
	cv.spans = make([][]lineSpan, len(cv.lines))
	cv.cur, cv.anchor = Pos{}, Pos{}
	cv.lineStart = nil
}

// ---- Selection ----

// Selection returns the ordered selection bounds and whether it's non-empty.
func (cv *CodeView) Selection() (Pos, Pos, bool) {
	a, b := cv.anchor, cv.cur
	if b.less(a) {
		a, b = b, a
	}
	return a, b, a != b
}

func (cv *CodeView) SelectedText() string {
	a, b, ok := cv.Selection()
	if !ok {
		return ""
	}
	return cv.textRange(a, b)
}

// PlaceCursor moves the cursor, dropping the selection.
func (cv *CodeView) PlaceCursor(p Pos) { cv.Select(p, p) }

// Select selects from anchor to cur (where the cursor goes).
func (cv *CodeView) Select(anchor, cur Pos) {
	cv.anchor, cv.cur = cv.clamp(anchor), cv.clamp(cur)
	cv.wantX = visCol(cv.lines[cv.cur.Line], cv.cur.Col)
	cv.blinkT = time.Now()
	if cv.OnCursor != nil {
		cv.OnCursor()
	}
}

func (cv *CodeView) moveTo(p Pos, extend bool) {
	if extend {
		cv.Select(cv.anchor, p)
	} else {
		cv.PlaceCursor(p)
	}
	cv.scrollReq = true
}

// ScrollToCursor keeps the cursor in view on the next frame.
func (cv *CodeView) ScrollToCursor() { cv.scrollReq = true }

// ScrollToLine positions line frac of the way down the view.
func (cv *CodeView) ScrollToLine(line int, frac float32) { cv.scrollTo = &scroll{line, frac} }

func (cv *CodeView) SetJumpLine(l int) { cv.jump = l }

func (cv *CodeView) SetMatches(m []posRange) { cv.matches = m }

// ---- Highlighting ----

// ApplySpans replaces the syntax colouring.
func (cv *CodeView) ApplySpans(spans []Span) {
	cv.spans = make([][]lineSpan, len(cv.lines))
	for _, s := range spans {
		for row := s.StartRow; row <= s.EndRow && row < len(cv.lines); row++ {
			start, end := 0, len(cv.lines[row])
			if row == s.StartRow {
				start = min(s.StartCol, end)
			}
			if row == s.EndRow {
				end = min(s.EndCol, end)
			}
			if end > start {
				cv.spans[row] = append(cv.spans[row], lineSpan{start, end, s.Class})
			}
		}
	}
}

// ---- Columns ----

// visCol is the screen column of byte col in line, expanding tabs.
func visCol(line string, col int) int {
	c := 0
	for i, r := range line {
		if i >= col {
			break
		}
		if r == '\t' {
			c = (c/tabWidth + 1) * tabWidth
		} else {
			c++
		}
	}
	return c
}

// byteCol is the byte column nearest to screen column vc.
func byteCol(line string, vc int) int {
	c := 0
	for i, r := range line {
		next := c + 1
		if r == '\t' {
			next = (c/tabWidth + 1) * tabWidth
		}
		if vc < next {
			if vc-c > next-vc {
				return i + utf8.RuneLen(r)
			}
			return i
		}
		c = next
	}
	return len(line)
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// wordBounds returns the word around col in line.
func wordBounds(line string, col int) (int, int) {
	s, e := col, col
	for s > 0 {
		r, n := utf8.DecodeLastRuneInString(line[:s])
		if !isWordRune(r) {
			break
		}
		s -= n
	}
	for e < len(line) {
		r, n := utf8.DecodeRuneInString(line[e:])
		if !isWordRune(r) {
			break
		}
		e += n
	}
	return s, e
}

// wordMove moves by one word, like GTK's Ctrl+Left/Right.
func (cv *CodeView) wordMove(p Pos, dir int) Pos {
	line := cv.lines[p.Line]
	if dir > 0 {
		if p.Col >= len(line) {
			if p.Line+1 < len(cv.lines) {
				return Pos{p.Line + 1, 0}
			}
			return p
		}
		i := p.Col
		for i < len(line) {
			r, n := utf8.DecodeRuneInString(line[i:])
			if isWordRune(r) {
				break
			}
			i += n
		}
		for i < len(line) {
			r, n := utf8.DecodeRuneInString(line[i:])
			if !isWordRune(r) {
				break
			}
			i += n
		}
		return Pos{p.Line, i}
	}
	if p.Col == 0 {
		if p.Line > 0 {
			return Pos{p.Line - 1, len(cv.lines[p.Line-1])}
		}
		return p
	}
	i := p.Col
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(line[:i])
		if isWordRune(r) {
			break
		}
		i -= n
	}
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(line[:i])
		if !isWordRune(r) {
			break
		}
		i -= n
	}
	return Pos{p.Line, i}
}

func (cv *CodeView) charMove(p Pos, dir int) Pos {
	line := cv.lines[p.Line]
	if dir > 0 {
		if p.Col < len(line) {
			_, n := utf8.DecodeRuneInString(line[p.Col:])
			return Pos{p.Line, p.Col + n}
		}
		if p.Line+1 < len(cv.lines) {
			return Pos{p.Line + 1, 0}
		}
		return p
	}
	if p.Col > 0 {
		_, n := utf8.DecodeLastRuneInString(line[:p.Col])
		return Pos{p.Line, p.Col - n}
	}
	if p.Line > 0 {
		return Pos{p.Line - 1, len(cv.lines[p.Line-1])}
	}
	return p
}

// ---- Editing commands ----

// InsertAtCursor replaces the selection with s.
func (cv *CodeView) InsertAtCursor(s string) {
	a, b, _ := cv.Selection()
	end := cv.Replace(a, b, s)
	cv.PlaceCursor(end)
	cv.scrollReq = true
}

func (cv *CodeView) deleteDir(dir int, word bool) {
	if a, b, ok := cv.Selection(); ok {
		cv.PlaceCursor(cv.Replace(a, b, ""))
		cv.scrollReq = true
		return
	}
	var other Pos
	if word {
		other = cv.wordMove(cv.cur, dir)
	} else {
		other = cv.charMove(cv.cur, dir)
	}
	if other == cv.cur {
		return
	}
	cv.PlaceCursor(cv.Replace(cv.cur, other, ""))
	cv.scrollReq = true
}

// ---- Layout ----

// codeStyle is how a CodeView draws: colours from the window's theme.
type codeStyle struct {
	BG, FG, Selection, MatchBG, MatchFG, JumpLine color.NRGBA
	Syntax                                        map[string]color.NRGBA
	Gutter                                        bool
	Font                                          font.Font
	Size                                          unit.Sp
}

// metrics measures the font's character cell.
func (cv *CodeView) metrics(gtx layout.Context, st *codeStyle) {
	cv.mm = monoMetricsFor(gtx, st.Font, st.Size)
	cv.charW, cv.lineH = cv.mm.cw, cv.mm.lineH
}

func (cv *CodeView) Layout(gtx layout.Context, st codeStyle) layout.Dimensions {
	cv.metrics(gtx, &st)
	size := gtx.Constraints.Max
	cv.view = size
	cv.handleEvents(gtx, st)

	digits := max(len(strconv.Itoa(len(cv.lines))), 2)
	pad := gtx.Dp(8)
	cv.gutterW = 0
	if st.Gutter {
		cv.gutterW = int(float32(digits)*cv.charW) + 2*pad
	}
	textX := cv.gutterW + cv.margin
	textW := size.X - textX

	// Scrolling.
	contentH := len(cv.lines) * cv.lineH
	maxW := 0
	first := max(cv.scrollY/cv.lineH, 0)
	for i := first; i < min(len(cv.lines), first+size.Y/max(cv.lineH, 1)+2); i++ {
		maxW = max(maxW, visCol(cv.lines[i], len(cv.lines[i])))
	}
	if cv.scrollTo != nil {
		cv.scrollY = cv.scrollTo.line*cv.lineH - int(float32(size.Y)*cv.scrollTo.frac)
		cx := int(float32(visCol(cv.lines[cv.cur.Line], cv.cur.Col)) * cv.charW)
		if cx < textW*8/10 {
			cv.scrollX = 0
		} else {
			cv.scrollX = cx - textW/2
		}
		cv.scrollTo = nil
	} else if cv.scrollReq {
		cy := cv.cur.Line * cv.lineH
		if cy < cv.scrollY {
			cv.scrollY = cy
		} else if cy+cv.lineH > cv.scrollY+size.Y {
			cv.scrollY = cy + cv.lineH - size.Y
		}
		cx := int(float32(visCol(cv.lines[cv.cur.Line], cv.cur.Col)) * cv.charW)
		if cx < cv.scrollX {
			cv.scrollX = max(cx-textW/4, 0)
		} else if cx+gtx.Dp(2) > cv.scrollX+textW {
			cv.scrollX = cx - textW*3/4
		}
	}
	cv.scrollReq = false
	cv.scrollY = max(0, min(cv.scrollY, contentH-size.Y+cv.lineH/2))
	cv.scrollY = max(cv.scrollY, 0)
	cv.scrollX = max(cv.scrollX, 0)

	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	fill(gtx, st.BG)

	first = cv.scrollY / cv.lineH
	last := min(len(cv.lines)-1, (cv.scrollY+size.Y)/cv.lineH)
	selA, selB, hasSel := cv.Selection()

	// Text area.
	{
		area := clip.Rect{Min: image.Pt(cv.gutterW, 0), Max: size}.Push(gtx.Ops)
		for i := first; i <= last; i++ {
			y := i*cv.lineH - cv.scrollY
			line := cv.lines[i]
			if i == cv.jump {
				fillRect(gtx, image.Rect(cv.gutterW, y, size.X, y+cv.lineH), st.JumpLine)
			}
			xAt := func(col int) int {
				return textX - cv.scrollX + int(float32(visCol(line, col))*cv.charW)
			}
			// Selection background.
			if hasSel && i >= selA.Line && i <= selB.Line {
				s, e := 0, len(line)
				if i == selA.Line {
					s = selA.Col
				}
				x1 := xAt(e)
				if i == selB.Line {
					e = selB.Col
					x1 = xAt(e)
				} else {
					x1 += int(cv.charW) // the newline
				}
				fillRect(gtx, image.Rect(xAt(s), y, x1, y+cv.lineH), st.Selection)
			}
			// Search matches.
			for _, m := range cv.matches {
				if i < m.a.Line || i > m.b.Line {
					continue
				}
				s, e := 0, len(line)
				if i == m.a.Line {
					s = m.a.Col
				}
				if i == m.b.Line {
					e = m.b.Col
				}
				fillRect(gtx, image.Rect(xAt(s), y, xAt(e), y+cv.lineH), st.MatchBG)
			}
			cv.drawLine(gtx, st, i, textX-cv.scrollX, y, selA, selB, hasSel)
		}
		// Cursor.
		if cv.focused && cv.editable {
			blink := time.Since(cv.blinkT)
			on := blink > 10*time.Second || blink%(1200*time.Millisecond) < 800*time.Millisecond
			if on && cv.cur.Line >= first && cv.cur.Line <= last {
				x := xAt0(cv, textX, cv.cur)
				y := cv.cur.Line*cv.lineH - cv.scrollY
				fillRect(gtx, image.Rect(x, y, x+max(gtx.Dp(1), 1), y+cv.lineH), st.FG)
			}
			if blink < 10*time.Second {
				next := 1200*time.Millisecond - blink%(1200*time.Millisecond)
				if blink%(1200*time.Millisecond) < 800*time.Millisecond {
					next = 800*time.Millisecond - blink%(1200*time.Millisecond)
				}
				gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(next)})
			}
		}
		area.Pop()
	}

	// Gutter.
	if st.Gutter {
		fillRect(gtx, image.Rect(0, 0, cv.gutterW, size.Y), st.BG)
		for i := first; i <= last; i++ {
			y := i*cv.lineH - cv.scrollY
			alpha := float32(0.45)
			if i == cv.cur.Line {
				alpha = 1
			}
			num := strconv.Itoa(i + 1)
			w := float32(len(num)) * cv.charW
			drawMono(gtx, num, st.Font, st.Size, withAlpha(st.FG, alpha), float32(cv.gutterW-pad)-w, y, cv.mm)
		}
	}

	// Input areas.
	{
		r := clip.Rect{Max: size}.Push(gtx.Ops)
		event.Op(gtx.Ops, cv)
		pointer.CursorText.Add(gtx.Ops)
		key.InputHintOp{Tag: cv, Hint: key.HintText}.Add(gtx.Ops)
		r.Pop()
	}
	cv.sb.layout(gtx, size, contentH+size.Y/2, cv.scrollY, max(maxW*int(cv.charW)+textX+gtx.Dp(20), size.X), cv.scrollX, &cv.scrollY, &cv.scrollX)
	return layout.Dimensions{Size: size}
}

func xAt0(cv *CodeView, textX int, p Pos) int {
	return textX - cv.scrollX + int(float32(visCol(cv.lines[p.Line], p.Col))*cv.charW)
}

// drawLine draws line i's text with its colours at (x, y).
func (cv *CodeView) drawLine(gtx layout.Context, st codeStyle, i, x, y int, selA, selB Pos, hasSel bool) {
	line := cv.lines[i]
	if line == "" {
		return
	}
	// Per-byte class; later spans override earlier ones, as tags applied later win.
	type seg struct {
		start, end int
		col        color.NRGBA
		f          font.Font
	}
	n := len(line)
	cls := make([]string, n)
	for _, s := range cv.spans[i] {
		for b := max(s.Start, 0); b < min(s.End, n); b++ {
			cls[b] = s.Class
		}
	}
	inMatch := func(b int) bool {
		for _, m := range cv.matches {
			p := Pos{i, b}
			if !p.less(m.a) && p.less(m.b) {
				return true
			}
		}
		return false
	}
	inSel := func(b int) bool {
		p := Pos{i, b}
		return hasSel && !p.less(selA) && p.less(selB)
	}
	styleAt := func(b int) (color.NRGBA, font.Font) {
		f := st.Font
		c := st.FG
		if cl := cls[b]; cl != "" {
			if sc, ok := st.Syntax[cl]; ok {
				c = sc
			}
			switch cl {
			case hlKeyword:
				f.Weight = font.Bold
			case hlComment:
				f.Style = font.Italic
			}
		}
		if inMatch(b) {
			c = st.MatchFG
		} else if inSel(b) {
			c = st.FG
		}
		return c, f
	}
	var segs []seg
	for b := 0; b < n; {
		_, sz := utf8.DecodeRuneInString(line[b:])
		c, f := styleAt(b)
		if k := len(segs) - 1; k >= 0 && segs[k].col == c && segs[k].f == f && segs[k].end == b {
			segs[k].end = b + sz
		} else {
			segs = append(segs, seg{b, b + sz, c, f})
		}
		b += sz
	}
	maxX := cv.view.X
	for _, s := range segs {
		// Draw tab-free runs at their grid positions.
		text := line[s.start:s.end]
		start := s.start
		for len(text) > 0 {
			tab := strings.IndexByte(text, '\t')
			run := text
			if tab >= 0 {
				run = text[:tab]
			}
			if run != "" {
				px := float32(x) + float32(visCol(line, start))*cv.charW
				if px < float32(maxX) && px+float32(len(run))*cv.charW > 0 {
					drawMono(gtx, run, s.f, st.Size, s.col, px, y, cv.mm)
				}
			}
			if tab < 0 {
				break
			}
			start += tab + 1
			text = text[tab+1:]
		}
	}
}

// posAtPoint maps a point in the widget to a buffer position.
func (cv *CodeView) posAtPoint(p image.Point) Pos {
	line := (p.Y + cv.scrollY) / max(cv.lineH, 1)
	line = max(0, min(line, len(cv.lines)-1))
	x := float32(p.X-cv.textX()+cv.scrollX) / cv.charW
	vc := int(x + 0.5)
	return Pos{line, byteCol(cv.lines[line], max(vc, 0))}
}

// textX is where text starts, in widget coordinates, before scrolling.
func (cv *CodeView) textX() int { return cv.gutterW + cv.margin }

// ---- Events ----

func (cv *CodeView) handleEvents(gtx layout.Context, st codeStyle) {
	if cv.margin == 0 {
		cv.margin = gtx.Dp(6)
	}
	filters := []event.Filter{
		key.FocusFilter{Target: cv},
		pointer.Filter{Target: cv, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Scroll,
			ScrollX: pointer.ScrollRange{Min: -1 << 30, Max: 1 << 30}, ScrollY: pointer.ScrollRange{Min: -1 << 30, Max: 1 << 30}},
		transfer.TargetFilter{Target: cv, Type: "application/text"},
	}
	if cv.focused {
		filters = append(filters, cv.keyFilters()...)
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.FocusEvent:
			cv.focused = e.Focus
			cv.blinkT = time.Now()
			if e.Focus {
				filters = append(filters[:3], cv.keyFilters()...)
			}
		case key.EditEvent:
			if cv.editable {
				cv.InsertAtCursor(e.Text)
			}
		case key.Event:
			if e.State == key.Press {
				cv.onKey(gtx, e)
			}
		case transfer.DataEvent:
			if r := e.Open(); r != nil {
				data, _ := io.ReadAll(r)
				r.Close()
				if cv.editable && len(data) > 0 {
					cv.InsertAtCursor(strings.ReplaceAll(string(data), "\r\n", "\n"))
				}
			}
		case pointer.Event:
			cv.onPointer(gtx, e)
		}
	}
}

func (cv *CodeView) keyFilters() []event.Filter {
	var fs []event.Filter
	sm := key.ModShift | key.ModCtrl | key.ModAlt | key.ModShortcut
	for _, n := range []key.Name{key.NameLeftArrow, key.NameRightArrow, key.NameUpArrow, key.NameDownArrow,
		key.NameHome, key.NameEnd, key.NamePageUp, key.NamePageDown, key.NameDeleteBackward, key.NameDeleteForward,
		key.NameReturn, key.NameEnter, key.NameTab, key.NameEscape} {
		fs = append(fs, key.Filter{Focus: cv, Name: n, Optional: sm})
	}
	for _, n := range []key.Name{"A", "C", "X", "V"} {
		fs = append(fs, key.Filter{Focus: cv, Name: n, Required: key.ModShortcut})
	}
	return fs
}

func (cv *CodeView) onKey(gtx layout.Context, e key.Event) {
	if cv.OnKey != nil && cv.OnKey(e) {
		return
	}
	shift := e.Modifiers.Contain(key.ModShift)
	ctrl := e.Modifiers.Contain(key.ModShortcut) || e.Modifiers.Contain(key.ModCtrl)
	cv.blinkT = time.Now()
	page := max(cv.view.Y/max(cv.lineH, 1)-1, 1)
	vert := func(d int) {
		l := max(0, min(cv.cur.Line+d, len(cv.lines)-1))
		want := cv.wantX
		p := Pos{l, byteCol(cv.lines[l], want)}
		if d < 0 && cv.cur.Line == 0 {
			p = Pos{0, 0}
		} else if d > 0 && cv.cur.Line == len(cv.lines)-1 {
			p = Pos{l, len(cv.lines[l])}
		}
		cv.moveTo(p, shift)
		if l != cv.cur.Line || p.Line == l {
			cv.wantX = want
		}
	}
	switch e.Name {
	case key.NameLeftArrow, key.NameRightArrow:
		dir := 1
		if e.Name == key.NameLeftArrow {
			dir = -1
		}
		if a, b, ok := cv.Selection(); ok && !shift && !ctrl {
			if dir < 0 {
				cv.moveTo(a, false)
			} else {
				cv.moveTo(b, false)
			}
			return
		}
		if ctrl {
			cv.moveTo(cv.wordMove(cv.cur, dir), shift)
		} else {
			cv.moveTo(cv.charMove(cv.cur, dir), shift)
		}
	case key.NameUpArrow:
		if ctrl {
			cv.scrollY -= cv.lineH
			return
		}
		vert(-1)
	case key.NameDownArrow:
		if ctrl {
			cv.scrollY += cv.lineH
			return
		}
		vert(1)
	case key.NamePageUp:
		cv.scrollY -= page * cv.lineH
		vert(-page)
	case key.NamePageDown:
		cv.scrollY += page * cv.lineH
		vert(page)
	case key.NameHome:
		if ctrl {
			cv.moveTo(Pos{}, shift)
		} else {
			// GTK's smart home: first non-blank, then the line start.
			line := cv.lines[cv.cur.Line]
			ind := len(line) - len(strings.TrimLeft(line, " \t"))
			col := ind
			if cv.cur.Col == ind {
				col = 0
			}
			cv.moveTo(Pos{cv.cur.Line, col}, shift)
		}
	case key.NameEnd:
		if ctrl {
			l := len(cv.lines) - 1
			cv.moveTo(Pos{l, len(cv.lines[l])}, shift)
		} else {
			cv.moveTo(Pos{cv.cur.Line, len(cv.lines[cv.cur.Line])}, shift)
		}
	case key.NameDeleteBackward:
		if cv.editable {
			cv.deleteDir(-1, ctrl)
		}
	case key.NameDeleteForward:
		if cv.editable {
			cv.deleteDir(1, ctrl)
		}
	case key.NameReturn, key.NameEnter:
		if cv.editable {
			cv.InsertAtCursor("\n")
		}
	case key.NameTab:
		if cv.editable && !ctrl {
			cv.InsertAtCursor("\t")
		}
	case "A":
		l := len(cv.lines) - 1
		cv.Select(Pos{}, Pos{l, len(cv.lines[l])})
	case "C", "X":
		if s := cv.SelectedText(); s != "" {
			gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))})
			if e.Name == "X" && cv.editable {
				cv.deleteDir(1, false)
			}
		}
	case "V":
		if cv.editable {
			gtx.Execute(clipboard.ReadCmd{Tag: cv})
		}
	}
}

func (cv *CodeView) onPointer(gtx layout.Context, e pointer.Event) {
	pos := e.Position.Round()
	switch e.Kind {
	case pointer.Scroll:
		if e.Modifiers.Contain(key.ModShift) && e.Scroll.X == 0 {
			e.Scroll.X, e.Scroll.Y = e.Scroll.Y, 0
		}
		cv.scrollY += int(e.Scroll.Y)
		cv.scrollX += int(e.Scroll.X)
		cv.sb.poke(gtx)
	case pointer.Press:
		if e.Buttons != pointer.ButtonPrimary {
			return
		}
		gtx.Execute(key.FocusCmd{Tag: cv})
		if cv.OnPress != nil {
			cv.OnPress()
		}
		p := cv.posAtPoint(pos)
		if pos.X < cv.gutterW {
			p.Col = 0
		}
		if e.Modifiers.Contain(key.ModShortcut) && cv.OnCtrlClick != nil {
			if pos.X < cv.gutterW {
				return // Ctrl+click in the gutter
			}
			cv.PlaceCursor(p)
			cv.OnCtrlClick(p)
			return
		}
		n := 1
		if time.Since(cv.lastClick) < 400*time.Millisecond && abs(cv.lastClickPos.X-pos.X) < 6 && abs(cv.lastClickPos.Y-pos.Y) < 6 {
			n = cv.clickCount%3 + 1
		}
		cv.lastClick, cv.lastClickPos, cv.clickCount = time.Now(), pos, n
		cv.dragging = true
		cv.dragWords = false
		switch n {
		case 1:
			if e.Modifiers.Contain(key.ModShift) {
				cv.Select(cv.anchor, p)
			} else {
				cv.PlaceCursor(p)
			}
		case 2:
			s, en := wordBounds(cv.lines[p.Line], p.Col)
			cv.Select(Pos{p.Line, s}, Pos{p.Line, en})
			cv.dragWords = true
		case 3:
			end := Pos{p.Line + 1, 0}
			if p.Line+1 >= len(cv.lines) {
				end = Pos{p.Line, len(cv.lines[p.Line])}
			}
			cv.Select(Pos{p.Line, 0}, end)
			cv.dragging = false
		}
	case pointer.Drag:
		if !cv.dragging {
			return
		}
		p := cv.posAtPoint(pos)
		if cv.dragWords {
			s, en := wordBounds(cv.lines[p.Line], p.Col)
			if p.less(cv.anchor) {
				p.Col = s
			} else {
				p.Col = en
			}
		}
		cv.Select(cv.anchor, p)
		cv.scrollReq = true
	case pointer.Release:
		cv.dragging = false
	}
}

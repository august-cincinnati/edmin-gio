package main

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op/clip"
)

const termScrollback = 5000

// Terminal hosts a shell in a pty, rendered through VT.
type Terminal struct {
	app   *App
	vt    *VT
	pty   *ptyProc
	dir   string
	shell []string

	started  bool
	closed   atomic.Bool
	dirty    atomic.Bool
	onExit   func()
	errText  string // shown when the shell could not start
	scroll   [][]cell
	screen   Snapshot
	follow   bool // keep the view pinned to the bottom
	offY     int  // scroll position in pixels
	sb       scrollbars
	charW    float32
	charH    int
	mm       monoMetrics
	view     image.Point
	focused  bool
	focusReq bool

	selA, selB  tpos // selection, in rows of scroll+screen
	selecting   bool
	hasSel      bool
	pasteTarget bool
	lastClick   time.Time
	clickCount  int
	lastPos     image.Point
}

// tpos is a cell in the combined scrollback and screen.
type tpos struct{ row, col int }

func (p tpos) less(q tpos) bool { return p.row < q.row || p.row == q.row && p.col < q.col }

// NewTerminal runs shell (the user's shell if empty) in dir once it has a size.
func NewTerminal(app *App, dir string, shell []string, onExit func()) *Terminal {
	return &Terminal{app: app, dir: dir, shell: shell, onExit: onExit, follow: true}
}

func (t *Terminal) start(rows, cols int) {
	t.started = true
	t.vt = NewVT(rows, cols)
	t.vt.Reply = func(b []byte) { t.pty.Write(b) }
	pty, err := startShell(t.dir, t.shell, rows, cols)
	if err != nil {
		shell := t.shell
		if len(shell) == 0 {
			shell = defaultShell()
		}
		t.errText = "failed to start " + strings.Join(shell, " ") + ": " + err.Error()
		return
	}
	t.pty = pty
	go t.readLoop()
}

func (t *Terminal) readLoop() {
	b := make([]byte, 32*1024)
	for {
		n, err := t.pty.Read(b)
		if n > 0 {
			t.vt.Write(b[:n])
			t.scheduleRender()
		}
		if err != nil {
			break
		}
	}
	t.pty.Wait()
	t.app.later(func() {
		if !t.closed.Load() && t.onExit != nil {
			t.onExit()
		}
	})
}

// scheduleRender redraws within a frame's time, waiting while a program
// holds synchronized output.
func (t *Terminal) scheduleRender() {
	if t.dirty.Swap(true) {
		return
	}
	var tick func()
	tick = func() {
		if t.vt != nil && t.vt.Holding() && !t.closed.Load() {
			time.AfterFunc(16*time.Millisecond, tick)
			return
		}
		t.app.later(func() {
			t.dirty.Store(false)
			t.update()
		})
	}
	time.AfterFunc(16*time.Millisecond, tick)
}

// update takes a snapshot of the emulator.
func (t *Terminal) update() {
	if t.vt == nil || t.closed.Load() {
		return
	}
	s := t.vt.Snapshot()
	if s.ClearScrollback {
		t.scroll = nil
		t.hasSel = false
	}
	for _, l := range s.Scrollback {
		t.scroll = append(t.scroll, trimLine(l, 0))
	}
	if n := len(t.scroll); n > termScrollback+500 {
		drop := n - termScrollback
		t.scroll = append([][]cell(nil), t.scroll[drop:]...)
		t.selA.row -= drop
		t.selB.row -= drop
	}
	t.screen = s
}

var xtermBase = [16]string{
	"#000000", "#cd3131", "#0dbc79", "#e5e510", "#2472c8", "#bc3fbc", "#11a8cd", "#e5e5e5",
	"#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#ffffff",
}

func colorHex(c int, def string) string {
	switch {
	case c == colorDefault:
		return def
	case c&0x1000000 != 0:
		return fmt.Sprintf("#%06x", c&0xffffff)
	case c < 16:
		return xtermBase[c]
	case c < 232:
		c -= 16
		lv := func(x int) int {
			if x == 0 {
				return 0
			}
			return 55 + x*40
		}
		return fmt.Sprintf("#%02x%02x%02x", lv(c/36), lv(c/6%6), lv(c%6))
	default:
		g := 8 + (c-232)*10
		return fmt.Sprintf("#%02x%02x%02x", g, g, g)
	}
}

// colors resolves a's foreground and background in theme; bg is empty when
// it is the terminal's default background.
func (a attr) colors(theme *Theme) (fg, bg string) {
	f, b := a.fg, a.bg
	if a.bold && f >= 0 && f < 8 {
		f += 8
	}
	fg, bg = colorHex(f, theme.TermFG), colorHex(b, theme.TermBG)
	if a.inverse {
		fg, bg = bg, fg
	}
	if bg == theme.TermBG {
		bg = ""
	}
	return fg, bg
}

// trimLine drops trailing blank cells from l, keeping at least minLen.
func trimLine(l []cell, minLen int) []cell {
	end := len(l)
	for end > minLen && l[end-1].ch == ' ' && l[end-1].a.bg == colorDefault && !l[end-1].a.inverse {
		end--
	}
	return l[:end]
}

func lineBlank(l []cell) bool {
	for _, c := range l {
		if c.ch != ' ' || c.a.bg != colorDefault {
			return false
		}
	}
	return true
}

// rows returns the lines on display: scrollback, then the screen. Before
// anything has scrolled off, the blank rows below the cursor are left out
// to avoid a huge empty area.
func (t *Terminal) rows() [][]cell {
	s := t.screen
	last := len(s.Lines) - 1
	for len(t.scroll) == 0 && last > s.CY && lineBlank(s.Lines[last]) {
		last--
	}
	out := make([][]cell, 0, len(t.scroll)+last+1)
	out = append(out, t.scroll...)
	for y := 0; y <= last; y++ {
		out = append(out, s.Lines[y])
	}
	return out
}

// Send writes raw input to the shell.
func (t *Terminal) Send(s string) {
	if t.pty != nil {
		t.pty.Write([]byte(s))
	}
}

// RunCommand types a command line into the shell and executes it.
func (t *Terminal) RunCommand(cmd string) {
	if t.pty == nil {
		// Shell not started yet; retry shortly.
		t.app.after(100*time.Millisecond, func() {
			if !t.closed.Load() && t.errText == "" {
				t.RunCommand(cmd)
			}
		})
		return
	}
	t.Send(cmd + "\r")
}

func (t *Terminal) Close() {
	if t.closed.Swap(true) {
		return
	}
	if t.pty != nil {
		t.pty.Kill()
		t.pty.Close()
	}
}

func (t *Terminal) Focus() { t.app.focusTag(t) }

func (t *Terminal) HasFocus() bool { return t.focused }

func (t *Terminal) selectedText() string {
	if !t.hasSel {
		return ""
	}
	a, b := t.selA, t.selB
	if b.less(a) {
		a, b = b, a
	}
	rows := t.rows()
	var sb strings.Builder
	for r := a.row; r <= b.row && r < len(rows); r++ {
		if r < 0 {
			continue
		}
		l := trimLine(rows[r], 0)
		from, to := 0, len(l)
		if r == a.row {
			from = min(a.col, len(l))
		}
		if r == b.row {
			to = min(b.col, len(l))
		}
		for i := from; i < to; i++ {
			sb.WriteRune(l[i].ch)
		}
		if r != b.row {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func (t *Terminal) copySelection(gtx layout.Context) {
	if s := t.selectedText(); s != "" {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))})
	}
}

func (t *Terminal) paste(gtx layout.Context) {
	gtx.Execute(clipboard.ReadCmd{Tag: t})
}

// ---- Layout ----

func (t *Terminal) Layout(gtx layout.Context) layout.Dimensions {
	th := t.app.theme
	size := gtx.Constraints.Max
	t.view = size
	t.mm = monoMetricsFor(gtx, monoFont, monoSize)
	t.charW, t.charH = t.mm.cw, t.mm.lineH
	t.handleEvents(gtx)

	// Start the shell, or follow size changes.
	rowsN := max(size.Y/max(t.charH, 1), 3)
	cols := max(int(float32(size.X)/t.charW)-1, 10)
	if size.X > 0 && size.Y > 0 && !t.closed.Load() {
		if !t.started {
			t.start(rowsN, cols)
		} else if t.vt != nil && (rowsN != t.vt.rows || cols != t.vt.cols) {
			t.vt.Resize(rowsN, cols)
			if t.pty != nil {
				t.pty.Resize(rowsN, cols)
			}
			t.update()
		}
	}

	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	bg, fg := rgb(th.TermBG), rgb(th.TermFG)
	fill(gtx, bg)
	if t.errText != "" {
		Label{Text: t.errText, Font: monoFont, Size: monoSize, Color: fg}.Layout(gtx)
		return layout.Dimensions{Size: size}
	}
	rows := t.rows()
	contentH := len(rows) * t.charH
	maxOff := max(contentH-size.Y, 0)
	if t.follow {
		t.offY = maxOff
	}
	t.offY = max(0, min(t.offY, maxOff))

	selA, selB := t.selA, t.selB
	if selB.less(selA) {
		selA, selB = selB, selA
	}
	selBG := rgb(th.Selection)
	first := t.offY / max(t.charH, 1)
	screenStart := len(t.scroll)
	for r := first; r < len(rows) && r*t.charH-t.offY < size.Y; r++ {
		y := r*t.charH - t.offY
		l := rows[r]
		// Cursor cell.
		curCol := -1
		if t.screen.CursorVisible && r == screenStart+t.screen.CY {
			curCol = t.screen.CX
		}
		t.drawRow(gtx, th, l, y, curCol, r, selA, selB, selBG, fg, bg)
	}

	{
		r := clip.Rect{Max: size}.Push(gtx.Ops)
		event.Op(gtx.Ops, t)
		pointer.CursorText.Add(gtx.Ops)
		r.Pop()
	}
	prev := t.offY
	t.sb.layout(gtx, size, max(contentH, size.Y), t.offY, size.X, 0, &t.offY, new(int))
	if t.offY != prev {
		t.follow = t.offY >= maxOff-t.charH*3/2
	}
	return layout.Dimensions{Size: size}
}

type termRun struct {
	from, to int
	fg, bg   color.NRGBA
	hasBG    bool
	bold     bool
	under    bool
}

func (t *Terminal) drawRow(gtx layout.Context, th *Theme, l []cell, y, curCol, row int, selA, selB tpos, selBG, fg, bg color.NRGBA) {
	n := len(l)
	if curCol >= n {
		n = curCol + 1
	}
	cellAt := func(i int) cell {
		if i < len(l) {
			return l[i]
		}
		return cell{ch: ' ', a: defaultAttr}
	}
	inSel := func(i int) bool {
		p := tpos{row, i}
		return t.hasSel && !p.less(selA) && p.less(selB)
	}
	var runs []termRun
	for i := 0; i < n; i++ {
		c := cellAt(i)
		fs, bs := c.a.colors(th)
		r := termRun{from: i, to: i + 1, fg: rgb(fs), bold: c.a.bold, under: c.a.under}
		if bs != "" {
			r.bg, r.hasBG = rgb(bs), true
		}
		if inSel(i) {
			r.bg, r.hasBG, r.fg = selBG, true, rgb(th.FG)
		}
		if i == curCol {
			r.bg, r.hasBG, r.fg = fg, true, bg
		}
		if k := len(runs) - 1; k >= 0 && runs[k].to == i && runs[k].fg == r.fg && runs[k].bg == r.bg &&
			runs[k].hasBG == r.hasBG && runs[k].bold == r.bold && runs[k].under == r.under {
			runs[k].to++
		} else {
			runs = append(runs, r)
		}
	}
	for _, r := range runs {
		x0 := float32(r.from) * t.charW
		x1 := float32(r.to) * t.charW
		if r.hasBG {
			fillRect(gtx, image.Rect(int(x0), y, int(x1+0.5), y+t.charH), r.bg)
		}
		var sb strings.Builder
		blank := true
		for i := r.from; i < r.to; i++ {
			ch := cellAt(i).ch
			if ch == 0 {
				ch = ' '
			}
			if ch != ' ' {
				blank = false
			}
			sb.WriteRune(ch)
		}
		if !blank {
			f := monoFont
			if r.bold {
				f.Weight = font.Bold
			}
			drawMono(gtx, sb.String(), f, monoSize, r.fg, x0, y, t.mm)
		}
		if r.under {
			fillRect(gtx, image.Rect(int(x0), y+t.charH-gtx.Dp(2), int(x1), y+t.charH-gtx.Dp(1)), r.fg)
		}
	}
}

func (t *Terminal) cellAt(p image.Point) tpos {
	row := (p.Y + t.offY) / max(t.charH, 1)
	col := int(float32(p.X)/t.charW + 0.5)
	return tpos{row, max(col, 0)}
}

func (t *Terminal) handleEvents(gtx layout.Context) {
	filters := []event.Filter{
		key.FocusFilter{Target: t},
		pointer.Filter{Target: t, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Scroll,
			ScrollY: pointer.ScrollRange{Min: -1 << 30, Max: 1 << 30}},
		transfer.TargetFilter{Target: t, Type: "application/text"},
	}
	if t.focused {
		all := key.ModCtrl | key.ModShift | key.ModAlt | key.ModCommand | key.ModSuper
		filters = append(filters,
			key.Filter{Focus: t, Name: "", Optional: all},
			key.Filter{Focus: t, Name: key.NameTab, Optional: key.ModShift | key.ModAlt},
		)
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.FocusEvent:
			t.focused = e.Focus
			if e.Focus {
				all := key.ModCtrl | key.ModShift | key.ModAlt | key.ModCommand | key.ModSuper
				filters = append(filters[:3],
					key.Filter{Focus: t, Name: "", Optional: all},
					key.Filter{Focus: t, Name: key.NameTab, Optional: key.ModShift | key.ModAlt})
			}
		case key.EditEvent:
			t.Send(e.Text)
			t.follow = true
		case key.Event:
			if e.State == key.Press {
				t.onKey(gtx, e)
			}
		case transfer.DataEvent:
			if r := e.Open(); r != nil {
				data, _ := io.ReadAll(r)
				r.Close()
				text := string(data)
				if text != "" {
					if t.vt != nil && t.vt.BracketedPaste {
						text = "\x1b[200~" + text + "\x1b[201~"
					}
					t.Send(text)
				}
			}
		case pointer.Event:
			t.onPointer(gtx, e)
		}
	}
}

func (t *Terminal) onPointer(gtx layout.Context, e pointer.Event) {
	switch e.Kind {
	case pointer.Scroll:
		t.offY += int(e.Scroll.Y)
		t.follow = false
		if rows := len(t.rows()); t.offY >= rows*t.charH-t.view.Y-t.charH*3/2 {
			t.follow = true
		}
		t.sb.poke(gtx)
	case pointer.Press:
		gtx.Execute(key.FocusCmd{Tag: t})
		if e.Buttons == pointer.ButtonSecondary {
			return
		}
		p := e.Position.Round()
		c := t.cellAt(p)
		n := 1
		if time.Since(t.lastClick) < 400*time.Millisecond && abs(p.X-t.lastPos.X) < 6 && abs(p.Y-t.lastPos.Y) < 6 {
			n = t.clickCount%3 + 1
		}
		t.lastClick, t.lastPos, t.clickCount = time.Now(), p, n
		rows := t.rows()
		switch n {
		case 1:
			t.selA, t.selB = c, c
			t.hasSel = false
			t.selecting = true
		case 2:
			if c.row < len(rows) {
				l := rows[c.row]
				s, en := min(c.col, len(l)), min(c.col, len(l))
				isW := func(r rune) bool { return r != ' ' && r != 0 && !strings.ContainsRune("\"'()[]{}<>,;|`", r) }
				for s > 0 && isW(l[s-1].ch) {
					s--
				}
				for en < len(l) && isW(l[en].ch) {
					en++
				}
				t.selA, t.selB = tpos{c.row, s}, tpos{c.row, en}
				t.hasSel = en > s
			}
		case 3:
			t.selA, t.selB = tpos{c.row, 0}, tpos{c.row + 1, 0}
			t.hasSel = true
		}
	case pointer.Drag:
		if t.selecting {
			t.selB = t.cellAt(e.Position.Round())
			t.hasSel = t.selA != t.selB
		}
	case pointer.Release:
		t.selecting = false
	}
}

func (t *Terminal) onKey(gtx layout.Context, k key.Event) {
	mods := k.Modifiers
	ctrl := mods.Contain(key.ModCtrl)
	shift := mods.Contain(key.ModShift)
	alt := mods.Contain(key.ModAlt)

	// Ctrl+Shift+C/V copy and paste; on macOS so do Cmd+C/V, and other Cmd
	// combos are left to the app since Ctrl stays the terminal's own key.
	if isCommand(mods) {
		switch k.Name {
		case "C":
			t.copySelection(gtx)
		case "V":
			t.paste(gtx)
		}
		return
	}
	if ctrl && shift {
		switch k.Name {
		case "C":
			t.copySelection(gtx)
		case "V":
			t.paste(gtx)
		}
		return
	}
	if t.pty == nil {
		return
	}
	app := t.vt != nil && t.vt.AppCursorKeys
	arrow := func(c string) string {
		if app {
			return "\x1bO" + c
		}
		return "\x1b[" + c
	}
	var out string
	switch k.Name {
	case key.NameReturn, key.NameEnter:
		out = "\r"
	case key.NameDeleteBackward:
		out = "\x7f"
		if ctrl {
			out = "\x08"
		}
	case key.NameTab:
		out = "\t"
		if shift {
			out = "\x1b[Z"
		}
	case key.NameEscape:
		out = "\x1b"
	case key.NameUpArrow:
		out = arrow("A")
	case key.NameDownArrow:
		out = arrow("B")
	case key.NameRightArrow:
		out = arrow("C")
		if ctrl {
			out = "\x1b[1;5C"
		}
	case key.NameLeftArrow:
		out = arrow("D")
		if ctrl {
			out = "\x1b[1;5D"
		}
	case key.NameHome:
		out = arrow("H")
	case key.NameEnd:
		out = arrow("F")
	case key.NameDeleteForward:
		out = "\x1b[3~"
	case key.NamePageUp:
		if shift {
			t.offY -= max(t.view.Y-t.charH, t.charH)
			t.follow = false
			t.sb.poke(gtx)
			return
		}
		out = "\x1b[5~"
	case key.NamePageDown:
		if shift {
			t.offY += max(t.view.Y-t.charH, t.charH)
			if rows := len(t.rows()); t.offY >= rows*t.charH-t.view.Y-t.charH*3/2 {
				t.follow = true
			}
			t.sb.poke(gtx)
			return
		}
		out = "\x1b[6~"
	case key.NameF1, key.NameF2, key.NameF3, key.NameF4:
		out = "\x1bO" + string(rune('P'+(k.Name[1]-'1')))
	case key.NameF5, key.NameF6, key.NameF7, key.NameF8, key.NameF9, key.NameF10, key.NameF11, key.NameF12:
		codes := map[key.Name]string{key.NameF5: "15", key.NameF6: "17", key.NameF7: "18", key.NameF8: "19",
			key.NameF9: "20", key.NameF10: "21", key.NameF11: "23", key.NameF12: "24"}
		out = "\x1b[" + codes[k.Name] + "~"
	case key.NameSpace:
		if !ctrl && !alt {
			return // typed through EditEvent
		}
		out = " "
		if ctrl {
			out = "\x00"
		}
	default:
		// Printable keys arrive as text; only Ctrl and Alt combinations
		// are handled here.
		if !ctrl && !alt {
			return
		}
		rs := []rune(string(k.Name))
		if len(rs) != 1 {
			return
		}
		r := rs[0]
		if r >= 'A' && r <= 'Z' && !shift {
			r += 'a' - 'A'
		}
		if ctrl {
			switch {
			case r >= 'a' && r <= 'z':
				r = r - 'a' + 1
			case r >= 'A' && r <= 'Z':
				r = r - 'A' + 1
			case r == '@' || r == '2':
				r = 0
			case r == '[':
				r = 0x1b
			case r == '\\':
				r = 0x1c
			case r == ']':
				r = 0x1d
			default:
				return
			}
		}
		out = string(r)
	}
	if alt {
		out = "\x1b" + out
	}
	t.Send(out)
	// Jump to the live screen when typing.
	t.follow = true
	t.hasSel = false
}

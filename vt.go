package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// A small VT100/xterm-compatible screen emulator, written in pure Go.

const colorDefault = -1

type attr struct {
	fg, bg  int // -1 default, 0..255 palette, 0x1000000|rgb truecolor
	bold    bool
	inverse bool
	under   bool
}

var defaultAttr = attr{fg: colorDefault, bg: colorDefault}

type cell struct {
	ch rune
	a  attr
}

type vtState int

const (
	stGround vtState = iota
	stEsc
	stCSI
	stOSC
	stOSCEsc
	stCharset
)

type VT struct {
	mu sync.Mutex

	rows, cols int
	lines      [][]cell
	cx, cy     int
	top, bot   int // scroll region, inclusive
	cur        attr
	wrapNext   bool

	savedX, savedY int
	savedAttr      attr

	altLines          [][]cell
	altActive         bool
	altSX, altSY      int
	CursorVisible     bool
	AppCursorKeys     bool
	BracketedPaste    bool
	syncSince         time.Time // when synchronized output (mode 2026) began; zero if off
	pendingScrollback [][]cell  // lines scrolled off the top since last drain
	clearScrollback   bool      // ED 3 was received since last drain

	state   vtState
	params  []byte
	partial []byte // incomplete utf-8 sequence

	Reply func([]byte) // used to answer device status queries
	Title string
}

func NewVT(rows, cols int) *VT {
	v := &VT{rows: rows, cols: cols, CursorVisible: true, cur: defaultAttr}
	v.lines = make([][]cell, rows)
	for i := range v.lines {
		v.lines[i] = v.blankLine()
	}
	v.top, v.bot = 0, rows-1
	return v
}

func (v *VT) blankLine() []cell {
	l := make([]cell, v.cols)
	for i := range l {
		l[i] = cell{' ', defaultAttr}
	}
	return l
}

func (v *VT) blankCell() cell {
	return cell{' ', attr{fg: colorDefault, bg: v.cur.bg}}
}

// Resize changes the screen dimensions, pushing lines into scrollback if needed.
func (v *VT) Resize(rows, cols int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if rows < 1 || cols < 1 || (rows == v.rows && cols == v.cols) {
		return
	}
	resizeLines := func(lines [][]cell, pushScroll bool, cy *int) [][]cell {
		for i, l := range lines {
			if len(l) < cols {
				for len(l) < cols {
					l = append(l, cell{' ', defaultAttr})
				}
			} else {
				l = l[:cols]
			}
			lines[i] = l
		}
		for len(lines) > rows {
			if *cy >= rows {
				if pushScroll {
					v.pendingScrollback = append(v.pendingScrollback, lines[0])
				}
				lines = lines[1:]
				*cy--
			} else {
				lines = lines[:len(lines)-1]
			}
		}
		for len(lines) < rows {
			nl := make([]cell, cols)
			for i := range nl {
				nl[i] = cell{' ', defaultAttr}
			}
			lines = append(lines, nl)
		}
		return lines
	}
	v.cols = cols
	v.lines = resizeLines(v.lines, !v.altActive, &v.cy)
	if v.altLines != nil {
		v.altLines = resizeLines(v.altLines, true, &v.altSY)
	}
	v.rows = rows
	v.top, v.bot = 0, rows-1
	v.cx = clamp(v.cx, 0, cols-1)
	v.cy = clamp(v.cy, 0, rows-1)
	v.wrapNext = false
}

func clamp(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// Write feeds program output into the emulator.
func (v *VT) Write(p []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.partial) > 0 {
		p = append(v.partial, p...)
		v.partial = nil
	}
	for len(p) > 0 {
		r, size := utf8.DecodeRune(p)
		if r == utf8.RuneError && size == 1 && !utf8.FullRune(p) {
			v.partial = append([]byte(nil), p...)
			return
		}
		p = p[size:]
		v.feed(r)
	}
}

func (v *VT) feed(r rune) {
	switch v.state {
	case stGround:
		v.ground(r)
	case stEsc:
		v.escape(r)
	case stCSI:
		if r >= 0x40 && r <= 0x7e {
			v.state = stGround
			v.csi(string(v.params), r)
		} else if r == 0x1b {
			v.state = stEsc
		} else if r < 0x20 {
			v.ground(r) // C0 controls execute inside CSI
		} else {
			v.params = append(v.params, byte(r))
		}
	case stOSC:
		switch r {
		case 0x07:
			v.osc()
			v.state = stGround
		case 0x1b:
			v.state = stOSCEsc
		default:
			if len(v.params) < 4096 {
				v.params = utf8.AppendRune(v.params, r)
			}
		}
	case stOSCEsc:
		v.osc()
		v.state = stGround
		if r != '\\' {
			v.escape(r)
		}
	case stCharset:
		v.state = stGround
	}
}

func (v *VT) osc() {
	s := string(v.params)
	if i := strings.IndexByte(s, ';'); i > 0 && (s[:i] == "0" || s[:i] == "2") {
		v.Title = s[i+1:]
	}
}

func (v *VT) ground(r rune) {
	switch r {
	case 0x07, 0x00, 0x0e, 0x0f:
	case 0x08:
		if v.cx > 0 {
			v.cx--
		}
		v.wrapNext = false
	case 0x09:
		v.cx = min((v.cx/8+1)*8, v.cols-1)
	case 0x0a, 0x0b, 0x0c:
		v.lineFeed()
	case 0x0d:
		v.cx = 0
		v.wrapNext = false
	case 0x1b:
		v.state = stEsc
	default:
		if r < 0x20 || r == 0x7f {
			return
		}
		v.put(r)
	}
}

func (v *VT) put(r rune) {
	if v.wrapNext {
		v.cx = 0
		v.lineFeed()
		v.wrapNext = false
	}
	v.lines[v.cy][v.cx] = cell{r, v.cur}
	if v.cx == v.cols-1 {
		v.wrapNext = true
	} else {
		v.cx++
	}
}

func (v *VT) escape(r rune) {
	v.state = stGround
	switch r {
	case '[':
		v.state = stCSI
		v.params = v.params[:0]
	case ']':
		v.state = stOSC
		v.params = v.params[:0]
	case '(', ')', '*', '+', '#', '%':
		v.state = stCharset
	case '7':
		v.saveCursor()
	case '8':
		v.restoreCursor()
	case 'D':
		v.lineFeed()
	case 'E':
		v.cx = 0
		v.lineFeed()
	case 'M':
		v.reverseIndex()
	case 'c':
		v.lines = make([][]cell, v.rows)
		for i := range v.lines {
			v.lines[i] = v.blankLine()
		}
		v.altLines, v.altActive = nil, false
		v.cx, v.cy, v.cur, v.wrapNext = 0, 0, defaultAttr, false
		v.CursorVisible, v.AppCursorKeys, v.BracketedPaste = true, false, false
		v.top, v.bot = 0, v.rows-1
	}
}

func (v *VT) saveCursor() {
	v.savedX, v.savedY, v.savedAttr = v.cx, v.cy, v.cur
}

func (v *VT) restoreCursor() {
	v.cx, v.cy, v.cur = clamp(v.savedX, 0, v.cols-1), clamp(v.savedY, 0, v.rows-1), v.savedAttr
	v.wrapNext = false
}

func (v *VT) lineFeed() {
	if v.cy == v.bot {
		v.scrollUp(1)
	} else if v.cy < v.rows-1 {
		v.cy++
	}
}

func (v *VT) reverseIndex() {
	if v.cy == v.top {
		v.scrollDown(1)
	} else if v.cy > 0 {
		v.cy--
	}
}

func (v *VT) scrollUp(n int) {
	for ; n > 0; n-- {
		if v.top == 0 && !v.altActive {
			v.pendingScrollback = append(v.pendingScrollback, v.lines[0])
		}
		copy(v.lines[v.top:v.bot], v.lines[v.top+1:v.bot+1])
		v.lines[v.bot] = v.blankLine()
	}
}

func (v *VT) scrollDown(n int) {
	for ; n > 0; n-- {
		copy(v.lines[v.top+1:v.bot+1], v.lines[v.top:v.bot])
		v.lines[v.top] = v.blankLine()
	}
}

func parseParams(s string) []int {
	if s == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ':' })
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

func (v *VT) csi(raw string, final rune) {
	private := false
	if raw != "" && (raw[0] == '?' || raw[0] == '>' || raw[0] == '=') {
		private = raw[0] == '?'
		if raw[0] != '?' {
			if final == 'c' && v.Reply != nil {
				v.Reply([]byte("\x1b[>0;0;0c"))
			}
			return
		}
		raw = raw[1:]
	}
	if private && final == 'p' && strings.HasSuffix(raw, "$") {
		// DECRQM: report which private modes are supported.
		m, _ := strconv.Atoi(strings.TrimSuffix(raw, "$"))
		if v.Reply != nil {
			v.Reply(fmt.Appendf(nil, "\x1b[?%d;%d$y", m, v.modeStatus(m)))
		}
		return
	}
	raw = strings.TrimRight(raw, " !\"#$%&'()*+,-./")
	ps := parseParams(raw)
	p := func(i, def int) int {
		if i < len(ps) && ps[i] != 0 {
			return ps[i]
		}
		return def
	}
	v.wrapNext = false
	switch final {
	case 'A':
		v.cy = clamp(v.cy-p(0, 1), v.top*b2i(v.cy >= v.top), v.rows-1)
	case 'B', 'e':
		lim := v.rows - 1
		if v.cy <= v.bot {
			lim = v.bot
		}
		v.cy = clamp(v.cy+p(0, 1), 0, lim)
	case 'C', 'a':
		v.cx = clamp(v.cx+p(0, 1), 0, v.cols-1)
	case 'D':
		v.cx = clamp(v.cx-p(0, 1), 0, v.cols-1)
	case 'E':
		v.cx = 0
		v.cy = clamp(v.cy+p(0, 1), 0, v.rows-1)
	case 'F':
		v.cx = 0
		v.cy = clamp(v.cy-p(0, 1), 0, v.rows-1)
	case 'G', '`':
		v.cx = clamp(p(0, 1)-1, 0, v.cols-1)
	case 'd':
		v.cy = clamp(p(0, 1)-1, 0, v.rows-1)
	case 'H', 'f':
		v.cy = clamp(p(0, 1)-1, 0, v.rows-1)
		v.cx = clamp(p(1, 1)-1, 0, v.cols-1)
	case 'J':
		mode := 0
		if len(ps) > 0 {
			mode = ps[0]
		}
		switch mode {
		case 0:
			v.eraseLine(v.cy, v.cx, v.cols)
			for y := v.cy + 1; y < v.rows; y++ {
				v.eraseLine(y, 0, v.cols)
			}
		case 1:
			v.eraseLine(v.cy, 0, v.cx+1)
			for y := 0; y < v.cy; y++ {
				v.eraseLine(y, 0, v.cols)
			}
		case 2:
			for y := 0; y < v.rows; y++ {
				v.eraseLine(y, 0, v.cols)
			}
		case 3:
			// Erase saved lines (what `clear` sends after ED 2).
			if !v.altActive {
				v.pendingScrollback = nil
				v.clearScrollback = true
			}
		}
	case 'K':
		mode := 0
		if len(ps) > 0 {
			mode = ps[0]
		}
		switch mode {
		case 0:
			v.eraseLine(v.cy, v.cx, v.cols)
		case 1:
			v.eraseLine(v.cy, 0, v.cx+1)
		case 2:
			v.eraseLine(v.cy, 0, v.cols)
		}
	case 'X':
		v.eraseLine(v.cy, v.cx, min(v.cx+p(0, 1), v.cols))
	case 'L':
		if v.cy >= v.top && v.cy <= v.bot {
			top := v.top
			v.top = v.cy
			v.scrollDown(min(p(0, 1), v.bot-v.cy+1))
			v.top = top
			v.cx = 0
		}
	case 'M':
		if v.cy >= v.top && v.cy <= v.bot {
			top, alt := v.top, v.altActive
			v.top = v.cy
			v.altActive = true // never push deleted lines to scrollback
			v.scrollUp(min(p(0, 1), v.bot-v.cy+1))
			v.top, v.altActive = top, alt
			v.cx = 0
		}
	case 'P':
		n := min(p(0, 1), v.cols-v.cx)
		l := v.lines[v.cy]
		copy(l[v.cx:], l[v.cx+n:])
		for i := v.cols - n; i < v.cols; i++ {
			l[i] = v.blankCell()
		}
	case '@':
		n := min(p(0, 1), v.cols-v.cx)
		l := v.lines[v.cy]
		copy(l[v.cx+n:], l[v.cx:v.cols-n])
		for i := v.cx; i < v.cx+n; i++ {
			l[i] = v.blankCell()
		}
	case 'S':
		v.scrollUp(p(0, 1))
	case 'T':
		v.scrollDown(p(0, 1))
	case 'm':
		v.sgr(ps)
	case 'r':
		top, bot := p(0, 1)-1, p(1, v.rows)-1
		if top < bot && bot < v.rows {
			v.top, v.bot = top, bot
			v.cx, v.cy = 0, 0
		}
	case 's':
		v.saveCursor()
	case 'u':
		v.restoreCursor()
	case 'h', 'l':
		if private {
			for _, m := range ps {
				v.setMode(m, final == 'h')
			}
		}
	case 'n':
		if len(ps) > 0 && ps[0] == 6 && v.Reply != nil {
			v.Reply(fmt.Appendf(nil, "\x1b[%d;%dR", v.cy+1, v.cx+1))
		} else if len(ps) > 0 && ps[0] == 5 && v.Reply != nil {
			v.Reply([]byte("\x1b[0n"))
		}
	case 'c':
		if v.Reply != nil {
			v.Reply([]byte("\x1b[?1;2c"))
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// modeStatus is a DECRQM answer for private mode m: 1 set, 2 reset,
// 0 not recognised.
func (v *VT) modeStatus(m int) int {
	var on bool
	switch m {
	case 1:
		on = v.AppCursorKeys
	case 25:
		on = v.CursorVisible
	case 2004:
		on = v.BracketedPaste
	case 47, 1047, 1049:
		on = v.altActive
	case 2026:
		on = !v.syncSince.IsZero()
	default:
		return 0
	}
	if on {
		return 1
	}
	return 2
}

// maxSyncHold bounds how long a synchronized frame may hold back drawing,
// in case a program never ends it.
const maxSyncHold = 200 * time.Millisecond

// Holding reports whether the program is mid-way through a synchronized
// frame, so the screen should not be drawn yet.
func (v *VT) Holding() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return !v.syncSince.IsZero() && time.Since(v.syncSince) < maxSyncHold
}

func (v *VT) setMode(m int, on bool) {
	switch m {
	case 1:
		v.AppCursorKeys = on
	case 25:
		v.CursorVisible = on
	case 2004:
		v.BracketedPaste = on
	case 2026:
		// Synchronized output: the program is drawing a frame and the
		// screen should not be shown until it ends.
		if !on {
			v.syncSince = time.Time{}
		} else if v.syncSince.IsZero() {
			v.syncSince = time.Now()
		}
	case 47, 1047, 1049:
		if on == v.altActive {
			return
		}
		if on {
			if m == 1049 {
				v.saveCursor()
			}
			v.altLines = v.lines
			v.lines = make([][]cell, v.rows)
			for i := range v.lines {
				v.lines[i] = v.blankLine()
			}
			v.altActive = true
		} else {
			v.lines = v.altLines
			v.altLines = nil
			v.altActive = false
			if m == 1049 {
				v.restoreCursor()
			}
		}
		v.top, v.bot = 0, v.rows-1
	}
}

func (v *VT) eraseLine(y, from, to int) {
	l := v.lines[y]
	for x := max(from, 0); x < to && x < len(l); x++ {
		l[x] = v.blankCell()
	}
}

func (v *VT) sgr(ps []int) {
	if len(ps) == 0 {
		v.cur = defaultAttr
		return
	}
	for i := 0; i < len(ps); i++ {
		n := ps[i]
		switch {
		case n == 0:
			v.cur = defaultAttr
		case n == 1:
			v.cur.bold = true
		case n == 4:
			v.cur.under = true
		case n == 7:
			v.cur.inverse = true
		case n == 22:
			v.cur.bold = false
		case n == 24:
			v.cur.under = false
		case n == 27:
			v.cur.inverse = false
		case n >= 30 && n <= 37:
			v.cur.fg = n - 30
		case n == 39:
			v.cur.fg = colorDefault
		case n >= 40 && n <= 47:
			v.cur.bg = n - 40
		case n == 49:
			v.cur.bg = colorDefault
		case n >= 90 && n <= 97:
			v.cur.fg = n - 90 + 8
		case n >= 100 && n <= 107:
			v.cur.bg = n - 100 + 8
		case n == 38 || n == 48:
			var c int
			if i+2 < len(ps) && ps[i+1] == 5 {
				c = ps[i+2] & 0xff
				i += 2
			} else if i+4 < len(ps) && ps[i+1] == 2 {
				c = 0x1000000 | (ps[i+2]&0xff)<<16 | (ps[i+3]&0xff)<<8 | ps[i+4]&0xff
				i += 4
			} else {
				return
			}
			if n == 38 {
				v.cur.fg = c
			} else {
				v.cur.bg = c
			}
		}
	}
}

// Snapshot is a copy of the emulator state used for rendering.
type Snapshot struct {
	Scrollback    [][]cell
	Lines         [][]cell
	CX, CY        int
	CursorVisible bool

	// ClearScrollback asks the view to drop its scrollback before
	// appending Scrollback.
	ClearScrollback bool
}

// Snapshot copies the visible screen and drains pending scrollback.
func (v *VT) Snapshot() Snapshot {
	v.mu.Lock()
	defer v.mu.Unlock()
	s := Snapshot{Scrollback: v.pendingScrollback, CX: v.cx, CY: v.cy, CursorVisible: v.CursorVisible, ClearScrollback: v.clearScrollback}
	v.pendingScrollback = nil
	v.clearScrollback = false
	s.Lines = make([][]cell, len(v.lines))
	for i, l := range v.lines {
		s.Lines[i] = append([]cell(nil), l...)
	}
	return s
}

// PlainText returns the screen as text (used by tests).
func (v *VT) PlainText() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var sb strings.Builder
	for _, l := range v.lines {
		for _, c := range l {
			sb.WriteRune(c.ch)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

package main

import (
	"image"
	"image/color"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
)

// accent is Yaru's focus and active-tab colour.
var accent = rgb("#e95420")

func scaleAffine(s float32) f32.Affine2D { return f32.AffineId().Scale(f32.Point{}, f32.Pt(s, s)) }

// palette is the set of colours chrome widgets draw with.
type palette struct {
	BG, FG, Panel, Border, Hover, Selection, Dim color.NRGBA
}

func (t *Theme) palette() palette {
	return palette{rgb(t.BG), rgb(t.FG), rgb(t.Panel), rgb(t.Border), rgb(t.Hover), rgb(t.Selection), rgb(t.Dim)}
}

// settingsPalette is the fixed black-on-white look of the Settings window
// and context menus.
var settingsPalette = palette{
	BG: white, FG: black, Panel: white, Border: rgb("#c0c0c0"), Hover: rgb("#e8e8e8"),
	Selection: rgb("#add6ff"), Dim: rgb("#888888"),
}

// ---- Buttons ----

// Button is a GTK-style push button showing an icon and/or a label.
type Button struct {
	Clicker
	Tip string
}

// corners selects which corners are rounded, for linked button groups.
type corners uint8

const (
	roundLeft corners = 1 << iota
	roundRight
	roundAll = roundLeft | roundRight
)

type buttonStyle struct {
	Icon     string
	Text     string
	Pal      palette
	Checked  bool
	Size     image.Point // in dp; zero means fit
	Corners  corners
	Circle   bool
	Suggest  bool // the default button of a dialog
	Disabled bool
	IconSize unit.Dp
}

// Clicked reports whether the button was clicked since the last call.
func (b *Button) Clicked(gtx layout.Context) bool {
	clicked := false
	for _, c := range b.Update(gtx) {
		if c.Button == pointer.ButtonPrimary {
			clicked = true
		}
	}
	return clicked
}

func (b *Button) Layout(gtx layout.Context, st buttonStyle) layout.Dimensions {
	if st.Corners == 0 {
		st.Corners = roundAll
	}
	if st.IconSize == 0 {
		st.IconSize = 16
	}
	// Measure the content.
	var content op.CallOp
	var cdims layout.Dimensions
	{
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints.Min = image.Point{}
		fg := st.Pal.FG
		if st.Disabled {
			fg = withAlpha(fg, 0.5)
		}
		cdims = layout.Flex{Alignment: layout.Middle}.Layout(g,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if st.Icon == "" {
					return layout.Dimensions{}
				}
				return drawIcon(gtx, st.Icon, st.IconSize, fg)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if st.Text == "" {
					return layout.Dimensions{}
				}
				f := uiFont
				if st.Suggest {
					f.Weight = 600
				}
				return Label{Text: st.Text, Color: fg, Font: f}.Layout(gtx)
			}),
		)
		content = m.Stop()
	}
	size := image.Pt(gtx.Dp(unit.Dp(st.Size.X)), gtx.Dp(unit.Dp(st.Size.Y)))
	if st.Size.X == 0 {
		pad := gtx.Dp(16)
		if st.Text == "" {
			pad = gtx.Dp(9)
		}
		size.X = max(cdims.Size.X+2*pad, gtx.Dp(34))
	}
	if st.Size.Y == 0 {
		size.Y = gtx.Dp(34)
	}
	r := image.Rectangle{Max: size}
	bg := st.Pal.Panel
	switch {
	case st.Checked || b.pressed && b.hovered:
		bg = st.Pal.Selection
	case b.hovered && !st.Disabled:
		bg = st.Pal.Hover
	}
	rad := gtx.Dp(5)
	if st.Circle {
		rad = size.Y / 2
	}
	rr := clip.RRect{Rect: r}
	if st.Corners&roundLeft != 0 {
		rr.NW, rr.SW = rad, rad
	}
	if st.Corners&roundRight != 0 {
		rr.NE, rr.SE = rad, rad
	}
	func() {
		defer rr.Push(gtx.Ops).Pop()
		fill(gtx, bg)
	}()
	border := st.Pal.Border
	if st.Checked {
		border = mix(st.Pal.Border, st.Pal.Selection, 0.5)
	}
	strokeShape(gtx, rr, gtx.Dp(1), border)
	off := op.Offset(image.Pt((size.X-cdims.Size.X)/2, (size.Y-cdims.Size.Y)/2)).Push(gtx.Ops)
	content.Add(gtx.Ops)
	off.Pop()
	if !st.Disabled {
		b.Add(gtx, r, pointer.CursorDefault)
	}
	if b.Tip != "" {
		tooltipArea(gtx, &b.Clicker, r, b.Tip)
	}
	return layout.Dimensions{Size: size}
}

// strokeShape outlines a rounded rectangle with width w, inside it.
func strokeShape(gtx layout.Context, rr clip.RRect, w int, c color.NRGBA) {
	if rr.NW == 0 && rr.NE == 0 && rr.SW == 0 && rr.SE == 0 {
		strokeRect(gtx, rr.Rect, w, c)
		return
	}
	// Fill the ring between rr and rr inset by w.
	outer := rr
	inner := clip.RRect{Rect: rr.Rect.Inset(w), NW: max(rr.NW-w, 0), NE: max(rr.NE-w, 0), SW: max(rr.SW-w, 0), SE: max(rr.SE-w, 0)}
	var p clip.Path
	p.Begin(gtx.Ops)
	appendRRect(&p, outer, false)
	appendRRect(&p, inner, true)
	spec := p.End()
	defer clip.Outline{Path: spec}.Op().Push(gtx.Ops).Pop()
	fill(gtx, c)
}

// appendRRect adds rr's outline to p, clockwise or (reverse) anticlockwise,
// so an inner reversed outline cuts a hole under the non-zero rule.
func appendRRect(p *clip.Path, rr clip.RRect, reverse bool) {
	r := f32Rect(rr.Rect)
	const k = 0.4477 // 1 - 0.5523
	nw, ne, se, sw := float32(rr.NW), float32(rr.NE), float32(rr.SE), float32(rr.SW)
	start := f32.Pt(r.Min.X+nw, r.Min.Y)
	segs := []struct {
		line   f32.Point
		c1, c2 f32.Point
		end    f32.Point
	}{
		{f32.Pt(r.Max.X-ne, r.Min.Y), f32.Pt(r.Max.X-ne*k, r.Min.Y), f32.Pt(r.Max.X, r.Min.Y+ne*k), f32.Pt(r.Max.X, r.Min.Y+ne)},
		{f32.Pt(r.Max.X, r.Max.Y-se), f32.Pt(r.Max.X, r.Max.Y-se*k), f32.Pt(r.Max.X-se*k, r.Max.Y), f32.Pt(r.Max.X-se, r.Max.Y)},
		{f32.Pt(r.Min.X+sw, r.Max.Y), f32.Pt(r.Min.X+sw*k, r.Max.Y), f32.Pt(r.Min.X, r.Max.Y-sw*k), f32.Pt(r.Min.X, r.Max.Y-sw)},
		{f32.Pt(r.Min.X, r.Min.Y+nw), f32.Pt(r.Min.X, r.Min.Y+nw*k), f32.Pt(r.Min.X+nw*k, r.Min.Y), start},
	}
	if !reverse {
		p.MoveTo(start)
		for _, s := range segs {
			p.LineTo(s.line)
			p.CubeTo(s.c1, s.c2, s.end)
		}
		p.Close()
		return
	}
	// Walk the same outline backwards.
	p.MoveTo(start)
	for i := len(segs) - 1; i >= 0; i-- {
		s := segs[i]
		p.CubeTo(s.c2, s.c1, s.line)
		prev := start
		if i > 0 {
			prev = segs[i-1].end
		}
		p.LineTo(prev)
	}
	p.Close()
}

// ---- Tooltips ----

// tooltipState is a window's tooltip: the hovered widget, since when, and
// where the pointer is (in window coordinates).
type tooltipState struct {
	owner any
	text  string
	since time.Time
	mouse image.Point
	shown bool
}

var curTip *tooltipState // the state of the window being laid out

// tooltipArea arranges for text to show when c has been hovered a moment.
func tooltipArea(gtx layout.Context, c *Clicker, r image.Rectangle, text string) {
	t := curTip
	if t == nil {
		return
	}
	if c.hovered && !c.pressed {
		if t.owner != c {
			t.owner, t.since, t.shown = c, gtx.Now, false
		}
		t.text = text
		if !t.shown {
			gtx.Execute(op.InvalidateCmd{At: t.since.Add(600 * time.Millisecond)})
		}
	} else if t.owner == c {
		t.owner = nil
		t.shown = false
	}
}

// layoutTooltip draws the current tooltip, if it is due, over everything.
func layoutTooltip(gtx layout.Context, t *tooltipState) {
	if t.owner == nil || gtx.Now.Sub(t.since) < 600*time.Millisecond || t.text == "" {
		return
	}
	t.shown = true
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints.Min = image.Point{}
	g.Constraints.Max.X = gtx.Dp(480)
	dims := layout.Inset{Top: 5, Bottom: 5, Left: 8, Right: 8}.Layout(g, func(gtx layout.Context) layout.Dimensions {
		return Label{Text: t.text, Color: rgb("#ffffff"), MaxLines: 8}.Layout(gtx)
	})
	call := m.Stop()
	x := t.mouse.X - dims.Size.X/2
	y := t.mouse.Y + gtx.Dp(22)
	if y+dims.Size.Y > gtx.Constraints.Max.Y {
		y = t.mouse.Y - dims.Size.Y - gtx.Dp(10)
	}
	x = max(gtx.Dp(4), min(x, gtx.Constraints.Max.X-dims.Size.X-gtx.Dp(4)))
	defer op.Offset(image.Pt(x, y)).Push(gtx.Ops).Pop()
	fillRRect(gtx, image.Rectangle{Max: dims.Size}, gtx.Dp(5), color.NRGBA{0x1c, 0x1c, 0x1c, 0xee})
	call.Add(gtx.Ops)
}

// ---- Text entry ----

// Entry is a single-line text field in GTK's style.
type Entry struct {
	widget.Editor
	Placeholder string
	focused     bool
	// Escape and ShiftEnter are set when those keys are pressed.
	escaped, shiftEnter bool
	submitted           bool
	changed             bool
	clearClick          Clicker
}

func NewEntry() *Entry {
	e := &Entry{}
	e.SingleLine = true
	e.Submit = true
	return e
}

// Update processes the entry's events; call before Layout.
func (e *Entry) Update(gtx layout.Context) {
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: &e.Editor, Name: key.NameEscape},
			key.Filter{Focus: &e.Editor, Name: key.NameReturn, Required: key.ModShift},
			key.Filter{Focus: &e.Editor, Name: key.NameEnter, Required: key.ModShift},
		)
		if !ok {
			break
		}
		if k, ok := ev.(key.Event); ok && k.State == key.Press {
			if k.Name == key.NameEscape {
				e.escaped = true
			} else {
				e.shiftEnter = true
			}
		}
	}
	for {
		ev, ok := e.Editor.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.SubmitEvent:
			e.submitted = true
		case widget.ChangeEvent:
			e.changed = true
		}
	}
	e.focused = gtx.Focused(&e.Editor)
}

// take returns and clears a flag.
func take(b *bool) bool { v := *b; *b = false; return v }

func (e *Entry) Escaped() bool    { return take(&e.escaped) }
func (e *Entry) ShiftEnter() bool { return take(&e.shiftEnter) }
func (e *Entry) Submitted() bool  { return take(&e.submitted) }
func (e *Entry) Changed() bool    { return take(&e.changed) }

func (e *Entry) Focus(gtx layout.Context) { gtx.Execute(key.FocusCmd{Tag: &e.Editor}) }

func (e *Entry) Layout(gtx layout.Context, pal palette, width unit.Dp) layout.Dimensions {
	e.Update(gtx)
	w := gtx.Constraints.Min.X
	if width > 0 {
		w = gtx.Dp(width)
	}
	w = max(w, gtx.Dp(40))
	h := gtx.Dp(34)
	r := image.Rect(0, 0, w, h)
	rad := gtx.Dp(5)
	fillRRect(gtx, r, rad, pal.BG)
	strokeShape(gtx, clip.UniformRRect(r, rad), gtx.Dp(1), pal.Border)
	if e.focused {
		strokeShape(gtx, clip.UniformRRect(r, rad), gtx.Dp(2), accent)
	}
	padX := gtx.Dp(8)
	func() {
		defer clip.Rect{Min: image.Pt(padX, 0), Max: image.Pt(w-padX, h)}.Push(gtx.Ops).Pop()
		lh := lineHeight(gtx, uiFont, uiSize)
		defer op.Offset(image.Pt(padX, (h-lh)/2)).Push(gtx.Ops).Pop()
		g := gtx
		g.Constraints = layout.Exact(image.Pt(w-2*padX, lh))
		if e.Editor.Len() == 0 && e.Placeholder != "" {
			Label{Text: e.Placeholder, Color: withAlpha(pal.FG, 0.5)}.Layout(g)
		}
		e.Editor.LineHeightScale = 1.2
		e.Editor.Layout(g, shaper, uiFont, uiSize, colorMaterial(gtx, pal.FG), colorMaterial(gtx, withAlpha(pal.Selection, 1)))
	}()
	return layout.Dimensions{Size: r.Size()}
}

// ---- Check and radio buttons ----

type Toggle struct {
	Clicker
	On bool
}

// Changed reports whether a click toggled the box.
func (t *Toggle) Changed(gtx layout.Context, radio bool) bool {
	ch := false
	for _, c := range t.Update(gtx) {
		if c.Button != pointer.ButtonPrimary {
			continue
		}
		if radio {
			if !t.On {
				t.On = true
				ch = true
			}
		} else {
			t.On = !t.On
			ch = true
		}
	}
	return ch
}

func (t *Toggle) Layout(gtx layout.Context, pal palette, label string, radio bool) layout.Dimensions {
	box := gtx.Dp(16)
	gap := gtx.Dp(8)
	g := gtx
	g.Constraints.Min = image.Point{}
	m := op.Record(gtx.Ops)
	ld := Label{Text: label, Color: pal.FG}.Layout(g)
	lc := m.Stop()
	h := max(ld.Size.Y, box) + gtx.Dp(8)
	y := (h - box) / 2
	r := image.Rect(0, y, box, y+box)
	rad := gtx.Dp(3)
	if radio {
		rad = box / 2
	}
	if t.On {
		fillRRect(gtx, r, rad, accent)
		if radio {
			c := box / 2
			d := gtx.Dp(3)
			fillRRect(gtx, image.Rect(c-d, y+c-d, c+d, y+c+d), d, white)
		} else {
			drawCheck(gtx, r)
		}
	} else {
		bg := pal.BG
		if t.hovered {
			bg = pal.Hover
		}
		fillRRect(gtx, r, rad, bg)
		strokeShape(gtx, clip.UniformRRect(r, rad), gtx.Dp(1), mix(pal.Border, pal.FG, 0.35))
	}
	off := op.Offset(image.Pt(box+gap, (h-ld.Size.Y)/2)).Push(gtx.Ops)
	lc.Add(gtx.Ops)
	off.Pop()
	size := image.Pt(box+gap+ld.Size.X, h)
	t.Add(gtx, image.Rectangle{Max: size}, pointer.CursorDefault)
	return layout.Dimensions{Size: size}
}

func drawCheck(gtx layout.Context, r image.Rectangle) {
	s := float32(r.Dx()) / 16
	o := f32.Pt(float32(r.Min.X), float32(r.Min.Y))
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(o.Add(f32.Pt(3.5*s, 8*s)))
	p.LineTo(o.Add(f32.Pt(6.5*s, 11*s)))
	p.LineTo(o.Add(f32.Pt(12.5*s, 5*s)))
	defer clip.Stroke{Path: p.End(), Width: 2 * s}.Op().Push(gtx.Ops).Pop()
	fill(gtx, white)
}

// ---- Scrollbars ----

// scrollbars are GTK-style overlay scrollbars: a thin indicator appears
// while scrolling, and a wider slider when the pointer is near it.
type scrollbars struct {
	shownAt      time.Time
	v, h         sbar
	hoverV       bool
	hoverH       bool
	lastV, lastH image.Rectangle
}

type sbar struct {
	dragging bool
	grab     float32 // pointer offset into the slider when dragging
	hovered  bool
}

// poke shows the indicators for a moment after scrolling.
func (s *scrollbars) poke(gtx layout.Context) {
	s.shownAt = gtx.Now
	gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(1100 * time.Millisecond)})
}

// layout draws the scrollbars of a view of size view showing content of
// contentH×contentW scrolled to offY/offX, and applies drags to setY/setX.
func (s *scrollbars) layout(gtx layout.Context, view image.Point, contentH, offY, contentW, offX int, setY, setX *int) {
	pal := currentPalette
	show := gtx.Now.Sub(s.shownAt) < time.Second
	thin, wide := gtx.Dp(3), gtx.Dp(8)
	margin := gtx.Dp(2)
	vert := contentH > view.Y
	horiz := contentW > view.X
	// Vertical.
	if vert {
		zone := image.Rect(view.X-gtx.Dp(14), 0, view.X, view.Y)
		s.events(gtx, &s.v, zone, true, view.Y, contentH, setY)
		active := s.v.dragging || s.v.hovered
		if show || active {
			w := thin
			if active {
				w = wide
				fillRect(gtx, image.Rect(view.X-w-2*margin, 0, view.X, view.Y), withAlpha(pal.Panel, 0.9))
				fillRect(gtx, image.Rect(view.X-w-2*margin, 0, view.X-w-2*margin+gtx.Dp(1), view.Y), pal.Border)
			}
			l := max(view.Y*view.Y/contentH, gtx.Dp(32))
			t := (view.Y - l) * offY / max(contentH-view.Y, 1)
			col := pal.Dim
			if s.v.dragging {
				col = pal.FG
			}
			fillRRect(gtx, image.Rect(view.X-w-margin, t+margin, view.X-margin, t+l-margin), w/2, col)
		}
	}
	if horiz {
		zone := image.Rect(0, view.Y-gtx.Dp(14), view.X-gtx.Dp(14), view.Y)
		s.events(gtx, &s.h, zone, false, view.X, contentW, setX)
		active := s.h.dragging || s.h.hovered
		if show || active {
			w := thin
			if active {
				w = wide
				fillRect(gtx, image.Rect(0, view.Y-w-2*margin, view.X, view.Y), withAlpha(pal.Panel, 0.9))
			}
			l := max(view.X*view.X/contentW, gtx.Dp(32))
			t := (view.X - l) * offX / max(contentW-view.X, 1)
			col := pal.Dim
			if s.h.dragging {
				col = pal.FG
			}
			fillRRect(gtx, image.Rect(t+margin, view.Y-w-margin, t+l-margin, view.Y-margin), w/2, col)
		}
	}
}

func (s *scrollbars) events(gtx layout.Context, b *sbar, zone image.Rectangle, vertical bool, view, content int, set *int) {
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: b, Kinds: pointer.Enter | pointer.Leave | pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		pos := e.Position.Y - float32(zone.Min.Y)
		if !vertical {
			pos = e.Position.X - float32(zone.Min.X)
		}
		l := max(view*view/content, gtx.Dp(32))
		t := (view - l) * *set / max(content-view, 1)
		switch e.Kind {
		case pointer.Enter:
			b.hovered = true
		case pointer.Leave:
			b.hovered = false
		case pointer.Cancel:
			b.hovered, b.dragging = false, false
		case pointer.Press:
			b.dragging = true
			if pos >= float32(t) && pos < float32(t+l) {
				b.grab = pos - float32(t)
			} else {
				b.grab = float32(l) / 2
			}
			fallthrough
		case pointer.Drag:
			if b.dragging {
				nt := pos - b.grab
				*set = int(nt * float32(content-view) / float32(max(view-l, 1)))
				*set = max(0, min(*set, content-view))
			}
		case pointer.Release:
			b.dragging = false
			s.shownAt = gtx.Now
		}
	}
	defer clip.Rect(zone).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, b)
	pointer.CursorDefault.Add(gtx.Ops)
}

// currentPalette is the palette of the window being laid out.
var currentPalette palette

// ---- Scrollable list ----

// scrollList is a vertically (and optionally horizontally) scrolling area
// of fixed-height rows.
type scrollList struct {
	offY, offX int
	sb         scrollbars
	view       image.Point
}

// layout draws n rows of height rowH using row(i) and returns the size.
// contentW is the widest row in pixels, or 0.
func (l *scrollList) layout(gtx layout.Context, n, rowH, contentW int, row func(gtx layout.Context, i int)) layout.Dimensions {
	size := gtx.Constraints.Max
	l.view = size
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: l, Kinds: pointer.Scroll,
			ScrollX: pointer.ScrollRange{Min: -1 << 30, Max: 1 << 30}, ScrollY: pointer.ScrollRange{Min: -1 << 30, Max: 1 << 30}})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok && e.Kind == pointer.Scroll {
			if e.Modifiers.Contain(key.ModShift) && e.Scroll.X == 0 {
				e.Scroll.X, e.Scroll.Y = e.Scroll.Y, 0
			}
			l.offY += int(e.Scroll.Y)
			l.offX += int(e.Scroll.X)
			l.sb.poke(gtx)
		}
	}
	contentH := n * rowH
	l.offY = max(0, min(l.offY, contentH-size.Y))
	l.offX = max(0, min(l.offX, contentW-size.X))
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	first := l.offY / max(rowH, 1)
	for i := first; i < n && i*rowH-l.offY < size.Y; i++ {
		off := op.Offset(image.Pt(-l.offX, i*rowH-l.offY)).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(image.Pt(max(size.X+l.offX, contentW), rowH))
		row(g, i)
		off.Pop()
	}
	// The scroll area goes over the rows, since their click areas would
	// hide one beneath them, and passes clicks through to them.
	{
		pass := pointer.PassOp{}.Push(gtx.Ops)
		r := clip.Rect{Max: size}.Push(gtx.Ops)
		event.Op(gtx.Ops, l)
		r.Pop()
		pass.Pop()
	}
	l.sb.layout(gtx, size, contentH, l.offY, max(contentW, size.X), l.offX, &l.offY, &l.offX)
	return layout.Dimensions{Size: size}
}

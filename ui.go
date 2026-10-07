package main

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	gtfont "github.com/go-text/typesetting/font"
	gtopentype "github.com/go-text/typesetting/font/opentype"
)

// ---- Fonts ----

// shaper lays out every string EdMin draws; it uses the system's fonts.
// loadDesktopFonts replaces it once the desktop's fonts are known.
var shaper = text.NewShaper()

// The interface and monospace fonts follow the desktop's settings, as GTK
// does, with DejaVu-style fallbacks when they can't be read (and Windows's
// own fonts after those).
var (
	uiFont   = font.Font{Typeface: "Ubuntu Sans,Cantarell,Noto Sans,DejaVu Sans,Segoe UI,sans-serif"}
	monoFont = font.Font{Typeface: "DejaVu Sans Mono,Liberation Mono,Consolas,monospace"}
	uiSize   = unit.Sp(11 * 4.0 / 3)
	monoSize = unit.Sp(11 * 4.0 / 3)
)

// loadDesktopFonts reads GNOME's interface and monospace fonts, like
// "Ubuntu Sans 11", so EdMin's text matches GTK applications.
func loadDesktopFonts() {
	get := func(key string) (string, float32, bool) {
		out, err := exec.Command("gsettings", "get", "org.gnome.desktop.interface", key).Output()
		if err != nil {
			return "", 0, false
		}
		s := strings.Trim(strings.TrimSpace(string(out)), "'")
		i := strings.LastIndexByte(s, ' ')
		if i < 0 {
			return "", 0, false
		}
		pt, err := strconv.ParseFloat(s[i+1:], 32)
		if err != nil || pt <= 0 {
			return "", 0, false
		}
		return strings.TrimSpace(s[:i]), float32(pt), true
	}
	scale := float32(1)
	if out, err := exec.Command("gsettings", "get", "org.gnome.desktop.interface", "text-scaling-factor").Output(); err == nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 32); err == nil && f > 0 {
			scale = float32(f)
		}
	}
	var extra []font.FontFace
	if fam, pt, ok := get("font-name"); ok {
		uiFont.Typeface = font.Typeface(fam + "," + string(uiFont.Typeface))
		uiSize = unit.Sp(pt * scale * 4 / 3)
		if extra = variableWeights(fam); len(extra) > 0 {
			uiFont.Typeface = font.Typeface(varAlias(fam) + "," + string(uiFont.Typeface))
		}
	}
	// GTK's monospace text views use fontconfig's "monospace" family at the
	// interface font's size (the monospace-font-name setting is for
	// terminal applications).
	monoSize = uiSize
	if out, err := exec.Command("fc-match", "-f", "%{family}", "monospace").Output(); err == nil {
		if fam, _, _ := strings.Cut(string(out), ","); fam != "" {
			monoFont.Typeface = font.Typeface(fam + "," + string(monoFont.Typeface))
		}
	}
	shaper = text.NewShaper(text.WithCollection(extra))
}

// varFace is a weight of a variable font.
type varFace struct {
	f    *gtfont.Font
	vars []gtfont.Variation
}

func (v varFace) Face() *gtfont.Face {
	face := gtfont.NewFace(v.f)
	face.SetVariations(v.vars)
	return face
}

// varAlias is the family name the weights of a variable font are loaded
// under, so they aren't shadowed by the system's entries for the font.
func varAlias(family string) string { return family + " EdMin" }

// variableWeights returns the heavier weights of family when it is a
// variable font, which Gio's system font loading only sees in its default
// weight, so that bold text is bold as in GTK.
func variableWeights(family string) []font.FontFace {
	var out []font.FontFace
	for _, style := range []string{"", ":italic"} {
		path, err := exec.Command("fc-match", "-f", "%{file}", family+style).Output()
		if err != nil || len(path) == 0 {
			continue
		}
		data, err := os.ReadFile(string(path))
		if err != nil {
			continue
		}
		for _, w := range []font.Weight{font.Normal, font.Medium, font.SemiBold, font.Bold} {
			// Each weight needs a font of its own: the shaper tells faces
			// apart by their font.
			ld, err := gtopentype.NewLoader(bytes.NewReader(data))
			if err != nil {
				break
			}
			f, err := gtfont.NewFont(ld)
			if err != nil {
				break
			}
			v := varFace{f, []gtfont.Variation{{Tag: gtopentype.MustNewTag("wght"), Value: float32(400 + int(w))}}}
			if len(v.Face().Coords()) == 0 {
				break // not a variable font
			}
			md := font.Font{Typeface: font.Typeface(varAlias(family)), Weight: w}
			if style != "" {
				md.Style = font.Italic
			}
			out = append(out, font.FontFace{Font: md, Face: v})
		}
	}
	return out
}

// small is GTK's <small>: 1/1.2 of the normal size.
func small(s unit.Sp) unit.Sp { return s / 1.2 }

func bold(f font.Font) font.Font   { f.Weight = font.Bold; return f }
func italic(f font.Font) font.Font { f.Style = font.Italic; return f }

// ---- Colours ----

func rgb(hex string) color.NRGBA {
	hex = strings.TrimPrefix(hex, "#")
	v, _ := strconv.ParseUint(hex, 16, 32)
	if len(hex) == 8 {
		return color.NRGBA{uint8(v >> 24), uint8(v >> 16), uint8(v >> 8), uint8(v)}
	}
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
}

func withAlpha(c color.NRGBA, a float32) color.NRGBA {
	c.A = uint8(float32(c.A) * a)
	return c
}

// mix blends b into a by t (0..1).
func mix(a, b color.NRGBA, t float32) color.NRGBA {
	l := func(x, y uint8) uint8 { return uint8(float32(x)*(1-t) + float32(y)*t) }
	return color.NRGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), l(a.A, b.A)}
}

var (
	white = color.NRGBA{255, 255, 255, 255}
	black = color.NRGBA{0, 0, 0, 255}
)

// ---- Drawing ----

func fillRect(gtx layout.Context, r image.Rectangle, c color.NRGBA) {
	defer clip.Rect(r).Push(gtx.Ops).Pop()
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}

func fillRRect(gtx layout.Context, r image.Rectangle, radius int, c color.NRGBA) {
	defer clip.UniformRRect(r, radius).Push(gtx.Ops).Pop()
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}

type frect struct{ Min, Max f32.Point }

func f32Rect(r image.Rectangle) frect {
	return frect{Min: f32.Pt(float32(r.Min.X), float32(r.Min.Y)), Max: f32.Pt(float32(r.Max.X), float32(r.Max.Y))}
}

// strokeRect draws a 1-pixel-per-w outline just inside r.
func strokeRect(gtx layout.Context, r image.Rectangle, w int, c color.NRGBA) {
	fillRect(gtx, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+w), c)
	fillRect(gtx, image.Rect(r.Min.X, r.Max.Y-w, r.Max.X, r.Max.Y), c)
	fillRect(gtx, image.Rect(r.Min.X, r.Min.Y, r.Min.X+w, r.Max.Y), c)
	fillRect(gtx, image.Rect(r.Max.X-w, r.Min.Y, r.Max.X, r.Max.Y), c)
}

// colorMaterial records a paint operation for text.
func colorMaterial(gtx layout.Context, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}

// ---- Text ----

// Label draws one line of text.
type Label struct {
	Text      string
	Font      font.Font
	Size      unit.Sp
	Color     color.NRGBA
	Alignment text.Alignment
	MaxLines  int // 0 means one line
	Truncate  bool
}

func (l Label) Layout(gtx layout.Context) layout.Dimensions {
	if l.Size == 0 {
		l.Size = uiSize
	}
	if l.Font.Typeface == "" {
		f := uiFont
		f.Weight, f.Style = l.Font.Weight, l.Font.Style
		l.Font = f
	}
	// Labels are as tall as their text, so containers can centre them.
	gtx.Constraints.Min.Y = 0
	lines := l.MaxLines
	if lines == 0 {
		lines = 1
	}
	w := widget.Label{Alignment: l.Alignment, MaxLines: lines, LineHeightScale: 1.2}
	if l.Truncate {
		w.Truncator = "…"
	}
	if lines == 1 && !l.Truncate {
		// Don't wrap or cut: let it run past the constraint like GTK's
		// labels inside a clipped container.
		gtx.Constraints.Max.X = 1 << 20
		gtx.Constraints.Min.X = 0
	}
	return w.Layout(gtx, shaper, l.Font, l.Size, l.Text, colorMaterial(gtx, l.Color))
}

// textWidth measures s in pixels.
func textWidth(gtx layout.Context, s string, f font.Font, size unit.Sp) int {
	m := op.Record(gtx.Ops)
	gtx.Constraints.Min = image.Point{}
	d := Label{Text: s, Font: f, Size: size}.Layout(gtx)
	m.Stop()
	return d.Size.X
}

// lineHeight is the height of one line of text in f at size.
func lineHeight(gtx layout.Context, f font.Font, size unit.Sp) int {
	m := op.Record(gtx.Ops)
	gtx.Constraints.Min = image.Point{}
	d := Label{Text: "Mg", Font: f, Size: size}.Layout(gtx)
	m.Stop()
	return d.Size.Y
}

// TextSpan is a piece of rich text.
type TextSpan struct {
	Text   string
	Font   font.Font
	Size   unit.Sp
	Color  color.NRGBA
	BG     color.NRGBA // transparent for none
	Weight font.Weight
}

// layoutSpans draws spans side by side on one line, aligned on a common
// baseline-ish vertical centre, clipped to the available width.
func layoutSpans(gtx layout.Context, spans []TextSpan) layout.Dimensions {
	type piece struct {
		call op.CallOp
		dims layout.Dimensions
		bg   color.NRGBA
	}
	var pieces []piece
	h, base := 0, 0
	for _, s := range spans {
		if s.Text == "" {
			continue
		}
		f := s.Font
		if f.Typeface == "" {
			f = uiFont
			f.Style = s.Font.Style
		}
		f.Weight = s.Weight
		if s.Size == 0 {
			s.Size = uiSize
		}
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints.Min = image.Point{}
		d := Label{Text: s.Text, Font: f, Size: s.Size, Color: s.Color}.Layout(g)
		pieces = append(pieces, piece{m.Stop(), d, s.BG})
		h = max(h, d.Size.Y)
		base = max(base, d.Baseline)
	}
	x := 0
	for _, p := range pieces {
		// Align baselines (measured from the bottom).
		y := (h - p.dims.Size.Y) - (base - p.dims.Baseline)
		off := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		if p.bg.A != 0 {
			fillRect(gtx, image.Rectangle{Max: p.dims.Size}, p.bg)
		}
		p.call.Add(gtx.Ops)
		off.Pop()
		x += p.dims.Size.X
	}
	return layout.Dimensions{Size: image.Pt(min(x, max(gtx.Constraints.Max.X, 0)), h), Baseline: base}
}

// ---- Layout helpers ----

// spacer is an empty widget of the given size in dp.
func spacer(w, h unit.Dp) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(gtx.Dp(w), gtx.Dp(h))}
	}
}

// fill paints the whole constraint area.
func fill(gtx layout.Context, c color.NRGBA) {
	fillRect(gtx, image.Rectangle{Max: gtx.Constraints.Max}, c)
}

// background lays out w on top of a rectangle of colour c.
func background(gtx layout.Context, c color.NRGBA, w layout.Widget) layout.Dimensions {
	m := op.Record(gtx.Ops)
	d := w(gtx)
	call := m.Stop()
	fillRect(gtx, image.Rectangle{Max: d.Size}, c)
	call.Add(gtx.Ops)
	return d
}

// ---- Clickable areas ----

// Clicker tracks presses, clicks, double clicks and hover on an area.
type Clicker struct {
	hovered bool
	pressed bool
	clicks  []Click
}

// Click is one press (count 2 for a double click).
type Click struct {
	Pos       image.Point
	Button    pointer.Buttons
	Modifiers key.Modifiers
	Count     int
}

// Update processes pointer events for the area last laid out with Add.
func (c *Clicker) Update(gtx layout.Context) []Click {
	var out []Click
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: c, Kinds: pointer.Press | pointer.Release | pointer.Enter | pointer.Leave | pointer.Cancel})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch e.Kind {
		case pointer.Enter:
			c.hovered = true
		case pointer.Leave, pointer.Cancel:
			c.hovered = false
			c.pressed = false
		case pointer.Press:
			c.pressed = true
			n := 1
			if e.Buttons == pointer.ButtonPrimary && time.Since(lastPress.t) < 400*time.Millisecond &&
				lastPress.target == c && abs(lastPress.pos.X-int(e.Position.X)) < 6 && abs(lastPress.pos.Y-int(e.Position.Y)) < 6 {
				n = lastPress.count + 1
			}
			lastPress.t, lastPress.target, lastPress.count = time.Now(), c, n
			lastPress.pos = e.Position.Round()
			out = append(out, Click{Pos: e.Position.Round(), Button: e.Buttons, Modifiers: e.Modifiers, Count: n})
		case pointer.Release:
			c.pressed = false
		}
	}
	return out
}

// lastPress detects double clicks across frames.
var lastPress struct {
	t      time.Time
	target any
	pos    image.Point
	count  int
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Add registers the area r (in the current offset) for c's events.
func (c *Clicker) Add(gtx layout.Context, r image.Rectangle, cursor pointer.Cursor) {
	defer clip.Rect(r).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, c)
	if cursor != pointer.CursorDefault {
		cursor.Add(gtx.Ops)
	}
}

// Hovered reports whether the pointer is over the area.
func (c *Clicker) Hovered() bool { return c.hovered }

// ---- UI-thread plumbing ----

// uiMu serialises all window event handling, so EdMin's state is only ever
// touched by one goroutine at a time, as with GTK's main loop.
var uiMu sync.Mutex

// later runs fn on the UI after the current event has been handled (like
// glib.IdleAdd).
func (a *App) later(fn func()) {
	a.qmu.Lock()
	a.queue = append(a.queue, fn)
	a.qmu.Unlock()
	a.invalidate()
}

// after runs fn on the UI after d (like glib.TimeoutAdd).
func (a *App) after(d time.Duration, fn func()) {
	time.AfterFunc(d, func() { a.later(fn) })
}

func (a *App) drainQueue() {
	for {
		a.qmu.Lock()
		q := a.queue
		a.queue = nil
		a.qmu.Unlock()
		if len(q) == 0 {
			return
		}
		for _, fn := range q {
			fn()
		}
	}
}

func (a *App) invalidate() {
	if a.win != nil {
		a.win.Invalidate()
	}
}

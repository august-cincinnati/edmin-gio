package main

import (
	"image"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
)

// windowDeco draws a GNOME-style header bar, which doubles as the title bar
// since EdMin draws its own window decorations, as GTK does on GNOME.
type windowDeco struct {
	maximized bool
	min, max  Button
	close     Button
}

func (d *windowDeco) config(c app.Config) {
	d.maximized = c.Mode == app.Maximized || c.Mode == app.Fullscreen
}

const headerBarHeight = 46

// headerBar draws the bar across the top of the window and returns its
// height. left and right lay out the bar's own widgets; actions selects the
// window buttons (minimize, maximize, close).
func (d *windowDeco) headerBar(gtx layout.Context, w *app.Window, pal palette, title, subtitle string,
	left, right func(gtx layout.Context) layout.Dimensions, actions system.Action) int {
	width := gtx.Constraints.Max.X
	h := gtx.Dp(headerBarHeight)
	fillRect(gtx, image.Rect(0, 0, width, h), pal.Panel)
	fillRect(gtx, image.Rect(0, h, width, h+gtx.Dp(1)), pal.Border)

	// Dragging the bar moves the window.
	{
		r := clip.Rect{Max: image.Pt(width, h)}.Push(gtx.Ops)
		system.ActionInputOp(system.ActionMove).Add(gtx.Ops)
		event.Op(gtx.Ops, d)
		r.Pop()
	}

	// Window buttons, GNOME's round ones, at the right.
	if w != nil {
		if d.min.Clicked(gtx) {
			w.Perform(system.ActionMinimize)
		}
		if d.max.Clicked(gtx) {
			if d.maximized {
				w.Perform(system.ActionUnmaximize)
			} else {
				w.Perform(system.ActionMaximize)
			}
		}
		if d.close.Clicked(gtx) {
			w.Perform(system.ActionClose)
		}
	}
	type wb struct {
		btn  *Button
		icon string
		act  system.Action
	}
	maxIcon := "window-maximize-symbolic"
	if d.maximized {
		maxIcon = "window-restore-symbolic"
	}
	var wbs []wb
	for _, b := range []wb{{&d.min, "window-minimize-symbolic", system.ActionMinimize}, {&d.max, maxIcon, system.ActionMaximize}, {&d.close, "window-close-symbolic", system.ActionClose}} {
		if actions&b.act != 0 {
			wbs = append(wbs, b)
		}
	}
	bs := gtx.Dp(30)
	step := gtx.Dp(40)
	x := width - gtx.Dp(6) - bs - (len(wbs)-1)*step
	controlsX := x
	if len(wbs) == 0 {
		controlsX = width
	}
	for _, b := range wbs {
		func() {
			defer op.Offset(image.Pt(x, (h-bs)/2)).Push(gtx.Ops).Pop()
			b.btn.Layout(gtx, buttonStyle{Icon: b.icon, Pal: pal, Size: image.Pt(30, 30), Circle: true})
		}()
		x += step
	}

	// The bar's own widgets.
	leftW := 0
	if left != nil {
		func() {
			defer op.Offset(image.Pt(gtx.Dp(6), (h-gtx.Dp(34))/2)).Push(gtx.Ops).Pop()
			g := gtx
			g.Constraints.Min = image.Point{}
			leftW = left(g).Size.X + gtx.Dp(6)
		}()
	}
	rightX := controlsX
	if right != nil {
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints.Min = image.Point{}
		rd := right(g)
		call := m.Stop()
		rightX = controlsX - gtx.Dp(18) - rd.Size.X
		if len(wbs) == 0 {
			rightX = width - gtx.Dp(6) - rd.Size.X
		}
		func() {
			defer op.Offset(image.Pt(rightX, (h-rd.Size.Y)/2)).Push(gtx.Ops).Pop()
			call.Add(gtx.Ops)
		}()
	}

	// Title and subtitle, centred in the window as far as the widgets allow.
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints.Min = image.Point{}
	avail := max(rightX-leftW-gtx.Dp(24), 0)
	g.Constraints.Max.X = avail
	td := layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(g,
		layout.Rigid(Label{Text: title, Font: font.Font{Weight: font.Bold}, Color: pal.FG, Truncate: true, Alignment: 1}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if subtitle == "" {
				return layout.Dimensions{}
			}
			return Label{Text: subtitle, Size: uiSize * 0.9, Color: withAlpha(pal.FG, 0.55), Truncate: true, Alignment: 1}.Layout(gtx)
		}),
	)
	call := m.Stop()
	tx := (width - td.Size.X) / 2
	tx = max(leftW+gtx.Dp(12), min(tx, rightX-gtx.Dp(12)-td.Size.X))
	func() {
		defer op.Offset(image.Pt(tx, (h-td.Size.Y)/2)).Push(gtx.Ops).Pop()
		call.Add(gtx.Ops)
	}()
	return h + gtx.Dp(1)
}

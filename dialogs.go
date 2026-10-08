package main

import (
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
)

// Response codes, as in GTK.
const (
	RespNone = iota
	RespOK
	RespCancel
	RespYes
	RespNo
	RespClose
	RespAccept
)

type DialogButton struct {
	Label string
	Resp  int
	btn   Button
}

// Dialog is a modal window drawn over its parent, like GNOME's attached
// dialogs.
type Dialog struct {
	Title     string // in the dialog's title bar (not for message dialogs)
	Message   string // a message dialog's bold text
	Secondary string
	Body      func(gtx layout.Context) layout.Dimensions
	Buttons   []*DialogButton
	Default   int // the response for Enter
	Width     unit.Dp
	White     bool // black on white, like the Settings window
	// OnResponse runs when a button is pressed or the dialog is cancelled
	// (RespCancel for Escape and the close button).
	OnResponse func(resp int)
	// OnKey sees key presses first; return true to consume one.
	OnKey func(gtx layout.Context, e key.Event) bool
	// Init runs on the first frame, e.g. to focus an entry.
	Init func(gtx layout.Context)

	closeBtn Button
	inited   bool
	app      *App
	focusTag bool // focused when nothing else is, to receive keys
}

func (d *Dialog) addButtons(pairs ...any) {
	for i := 0; i+1 < len(pairs); i += 2 {
		d.Buttons = append(d.Buttons, &DialogButton{Label: pairs[i].(string), Resp: pairs[i+1].(int)})
	}
}

// showDialog opens d over the window.
func (a *App) showDialog(d *Dialog) {
	d.app = a
	a.dialogs = append(a.dialogs, d)
	a.invalidate()
}

// respond closes d with resp.
func (d *Dialog) respond(resp int) {
	a := d.app
	for i, x := range a.dialogs {
		if x == d {
			a.dialogs = append(a.dialogs[:i], a.dialogs[i+1:]...)
			break
		}
	}
	a.invalidate()
	if d.OnResponse != nil {
		d.OnResponse(resp)
	}
	a.restoreFocus()
}

func (d *Dialog) pal() palette {
	if d.White {
		return settingsPalette
	}
	return d.app.theme.palette()
}

func (d *Dialog) layout(gtx layout.Context) {
	pal := d.pal()
	// Keys.
	if !d.inited {
		d.inited = true
		gtx.Execute(key.FocusCmd{Tag: &d.focusTag})
		if d.Init != nil {
			d.Init(gtx)
		}
	}
	for {
		ev, ok := gtx.Event(
			key.Filter{Name: key.NameEscape},
			key.Filter{Name: key.NameReturn},
			key.Filter{Name: key.NameEnter},
			key.Filter{Name: "", Optional: key.ModShift | key.ModCtrl | key.ModAlt},
			key.FocusFilter{Target: &d.focusTag},
		)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		if d.OnKey != nil && d.OnKey(gtx, e) {
			continue
		}
		switch e.Name {
		case key.NameEscape:
			d.respond(RespCancel)
			return
		case key.NameReturn, key.NameEnter:
			if d.Default != RespNone {
				d.respond(d.Default)
				return
			}
		}
	}

	// Scrim blocking the window below.
	size := gtx.Constraints.Max
	fillRect(gtx, image.Rectangle{Max: size}, color.NRGBA{0, 0, 0, 0x60})
	{
		r := clip.Rect{Max: size}.Push(gtx.Ops)
		event.Op(gtx.Ops, d)
		for {
			if _, ok := gtx.Event(pointer.Filter{Target: d, Kinds: pointer.Press | pointer.Scroll}); !ok {
				break
			}
		}
		r.Pop()
	}

	w := gtx.Dp(d.Width)
	if w == 0 {
		w = gtx.Dp(360)
	}
	w = min(w, size.X-gtx.Dp(20))
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, size.Y-gtx.Dp(20))}
	dims := d.content(g, pal)
	call := m.Stop()

	x := (size.X - w) / 2
	y := max((size.Y-dims.Size.Y)/2-gtx.Dp(40), gtx.Dp(10))
	defer op.Offset(image.Pt(x, y)).Push(gtx.Ops).Pop()
	rad := gtx.Dp(8)
	r := image.Rectangle{Max: image.Pt(w, dims.Size.Y)}
	// A soft shadow.
	for i := 1; i <= 4; i++ {
		s := gtx.Dp(unit.Dp(i * 2))
		fillRRect(gtx, image.Rectangle{Min: image.Pt(-s, -s+gtx.Dp(2)), Max: r.Max.Add(image.Pt(s, s+gtx.Dp(2)))}, rad+s, color.NRGBA{0, 0, 0, 0x14})
	}
	func() {
		defer clip.UniformRRect(r, rad).Push(gtx.Ops).Pop()
		fill(gtx, pal.Panel)
		event.Op(gtx.Ops, &d.Buttons) // swallow clicks on the dialog body
		call.Add(gtx.Ops)
	}()
	strokeShape(gtx, clip.UniformRRect(r, rad), gtx.Dp(1), pal.Border)
}

// content lays out the dialog's inside: title bar, body and buttons.
func (d *Dialog) content(gtx layout.Context, pal palette) layout.Dimensions {
	w := gtx.Constraints.Min.X
	var children []layout.FlexChild
	if d.Message != "" {
		// A message dialog: centred bold text, then buttons across the bottom.
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 24, Bottom: 24, Left: 24, Right: 24}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return Label{Text: d.Message, Font: bold(uiFont), Size: uiSize * 1.2, Color: pal.FG, Alignment: 1, MaxLines: 4}.Layout(gtx)
					}),
					layout.Rigid(spacer(0, 10)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if d.Secondary == "" {
							return layout.Dimensions{}
						}
						return Label{Text: d.Secondary, Color: pal.FG, Alignment: 1, MaxLines: 6}.Layout(gtx)
					}),
				)
			})
		}))
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			n := len(d.Buttons)
			h := gtx.Dp(44)
			fillRect(gtx, image.Rect(0, 0, w, gtx.Dp(1)), pal.Border)
			for i, b := range d.Buttons {
				x0, x1 := w*i/n, w*(i+1)/n
				if i > 0 {
					fillRect(gtx, image.Rect(x0, 0, x0+gtx.Dp(1), h), pal.Border)
				}
				for range b.btn.Update(gtx) {
					resp := b.Resp
					defer d.respond(resp)
				}
				r := image.Rect(x0, gtx.Dp(1), x1, h)
				if b.btn.hovered {
					bg := pal.Hover
					rr := clip.RRect{Rect: r}
					if i == 0 {
						rr.SW = gtx.Dp(8)
					}
					if i == n-1 {
						rr.SE = gtx.Dp(8)
					}
					func() {
						defer rr.Push(gtx.Ops).Pop()
						fill(gtx, bg)
					}()
				}
				f := uiFont
				col := pal.FG
				if b.Resp == d.Default {
					f.Weight = 600
				}
				func() {
					g := gtx
					g.Constraints = layout.Exact(r.Size())
					defer op.Offset(r.Min).Push(gtx.Ops).Pop()
					layout.Center.Layout(g, Label{Text: b.Label, Font: f, Color: col}.Layout)
				}()
				b.btn.Add(gtx, r, pointer.CursorDefault)
			}
			return layout.Dimensions{Size: image.Pt(w, h)}
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	}

	// A normal dialog: title bar, body, buttons at the bottom right.
	children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(46)
		if d.closeBtn.Clicked(gtx) {
			defer d.respond(RespCancel)
		}
		g := gtx
		g.Constraints = layout.Exact(image.Pt(w, h))
		layout.Center.Layout(g, Label{Text: d.Title, Font: bold(uiFont), Color: pal.FG}.Layout)
		bs := gtx.Dp(24)
		func() {
			defer op.Offset(image.Pt(w-bs-gtx.Dp(11), (h-bs)/2)).Push(gtx.Ops).Pop()
			d.closeBtn.Layout(gtx, buttonStyle{Icon: "window-close-symbolic", Pal: pal, Size: image.Pt(24, 24), Circle: true})
		}()
		fillRect(gtx, image.Rect(0, h-gtx.Dp(1), w, h), pal.Border)
		return layout.Dimensions{Size: image.Pt(w, h)}
	}))
	if d.Body != nil {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return background(gtx, pal.Panel, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = w
				return d.Body(gtx)
			})
		}))
	}
	if len(d.Buttons) > 0 {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 6, Bottom: 6, Left: 6, Right: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				var items []layout.FlexChild
				items = append(items, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
				}))
				for i, b := range d.Buttons {
					if i > 0 {
						items = append(items, layout.Rigid(spacer(6, 0)))
					}
					items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if b.btn.Clicked(gtx) {
							resp := b.Resp
							defer d.respond(resp)
						}
						return b.btn.Layout(gtx, buttonStyle{Text: b.Label, Pal: pal, Suggest: b.Resp == d.Default && len(d.Buttons) > 1})
					}))
				}
				return layout.Flex{}.Layout(gtx, items...)
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// ---- Common dialogs ----

func (a *App) showError(title, msg string) {
	d := &Dialog{Message: title, Secondary: msg, Default: RespOK, Width: 400}
	d.addButtons("OK", RespOK)
	a.showDialog(d)
}

// askSave asks whether to save changes to name, then calls then with
// RespYes, RespNo or RespCancel.
func (a *App) askSave(name string, then func(resp int)) {
	d := &Dialog{Message: "Save changes to " + name + "?", Default: RespYes, Width: 400, OnResponse: then}
	d.addButtons("Don't Save", RespNo, "Cancel", RespCancel, "Save", RespYes)
	a.showDialog(d)
}

// prompt asks for a line of text and calls then with it if OK is pressed.
func (a *App) prompt(title, label, initial string, then func(string)) {
	e := NewEntry()
	e.SetText(initial)
	d := &Dialog{Title: title, Default: RespOK, Width: 420}
	d.Body = func(gtx layout.Context) layout.Dimensions {
		pal := d.pal()
		if e.Submitted() {
			defer d.respond(RespOK)
		}
		return layout.UniformInset(10).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return Label{Text: label, Color: pal.FG, MaxLines: 3}.Layout(gtx)
				}),
				layout.Rigid(spacer(0, 6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return e.Layout(gtx, pal, 0) }),
			)
		})
	}
	d.Init = func(gtx layout.Context) {
		e.Focus(gtx)
		e.SetCaret(e.Len(), 0)
	}
	d.addButtons("Cancel", RespCancel, "OK", RespOK)
	d.OnResponse = func(resp int) {
		if resp == RespOK {
			then(e.Text())
		}
	}
	a.showDialog(d)
}

// ---- Context menu ----

type menuItem struct {
	label string
	fn    func()
	click Clicker
}

// Menu is a popup menu, black on white whatever the theme.
type Menu struct {
	items []*menuItem
	pos   image.Point
}

func (a *App) popupMenu(pos image.Point, items []*menuItem) {
	a.menu = &Menu{items: items, pos: pos}
	a.invalidate()
}

func (m *Menu) layout(gtx layout.Context, a *App) {
	pal := settingsPalette
	size := gtx.Constraints.Max
	// Clicking outside closes the menu.
	{
		r := clip.Rect{Max: size}.Push(gtx.Ops)
		event.Op(gtx.Ops, m)
		r.Pop()
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: m, Kinds: pointer.Press}, key.Filter{Name: key.NameEscape})
			if !ok {
				break
			}
			switch e := ev.(type) {
			case pointer.Event:
				a.menu = nil
				return
			case key.Event:
				if e.State == key.Press {
					a.menu = nil
					return
				}
			}
		}
	}
	rowH := gtx.Dp(30)
	w := 0
	for _, it := range m.items {
		w = max(w, textWidth(gtx, it.label, uiFont, uiSize))
	}
	w += gtx.Dp(40)
	pad := gtx.Dp(4)
	h := len(m.items)*rowH + 2*pad
	x := min(m.pos.X, size.X-w-gtx.Dp(2))
	y := min(m.pos.Y, size.Y-h-gtx.Dp(2))
	defer op.Offset(image.Pt(x, y)).Push(gtx.Ops).Pop()
	r := image.Rect(0, 0, w, h)
	rad := gtx.Dp(6)
	for i := 1; i <= 3; i++ {
		s := gtx.Dp(unit.Dp(i * 2))
		fillRRect(gtx, image.Rectangle{Min: image.Pt(-s, -s+gtx.Dp(2)), Max: r.Max.Add(image.Pt(s, s+gtx.Dp(2)))}, rad+s, color.NRGBA{0, 0, 0, 0x16})
	}
	fillRRect(gtx, r, rad, pal.BG)
	strokeShape(gtx, clip.UniformRRect(r, rad), gtx.Dp(1), pal.Border)
	for i, it := range m.items {
		ir := image.Rect(pad, pad+i*rowH, w-pad, pad+(i+1)*rowH)
		for _, c := range it.click.Update(gtx) {
			if c.Button == pointer.ButtonPrimary {
				a.menu = nil
				fn := it.fn
				defer fn()
			}
		}
		if it.click.hovered {
			fillRRect(gtx, ir, gtx.Dp(4), pal.Hover)
		}
		func() {
			defer op.Offset(image.Pt(ir.Min.X+gtx.Dp(12), ir.Min.Y)).Push(gtx.Ops).Pop()
			g := gtx
			g.Constraints = layout.Exact(image.Pt(ir.Dx(), rowH))
			layout.W.Layout(g, Label{Text: it.label, Color: pal.FG}.Layout)
		}()
		it.click.Add(gtx, ir, pointer.CursorDefault)
	}
}

// ---- Folder chooser ----

// chooseFolder asks for a folder and calls then with it, unless cancelled.
// It uses GTK's own file chooser through zenity when that is installed, and
// otherwise a simple chooser of EdMin's own.
func (a *App) chooseFolder(title string, then func(string)) {
	if z, err := exec.LookPath("zenity"); err == nil {
		go func() {
			out, err := exec.Command(z, "--file-selection", "--directory", "--title="+title,
				"--filename="+a.root+string(filepath.Separator)).Output()
			dir := strings.TrimRight(string(out), "\n")
			if err != nil || dir == "" {
				return
			}
			a.later(func() { then(dir) })
		}()
		return
	}
	a.folderDialog(title, then)
}

// folderDialog is the built-in folder chooser: a path entry over a list of
// the folders inside it.
func (a *App) folderDialog(title string, then func(string)) {
	e := NewEntry()
	cur := a.root
	e.SetText(cur)
	var dirs []string
	var clicks []Clicker
	var list scrollList
	sel := -1
	load := func(dir string) {
		cur = dir
		e.SetText(dir)
		e.SetCaret(e.Len(), e.Len())
		dirs = []string{".."}
		if ents, err := os.ReadDir(dir); err == nil {
			for _, x := range ents {
				if x.IsDir() || x.Type()&os.ModeSymlink != 0 {
					p := filepath.Join(dir, x.Name())
					if st, err := os.Stat(p); err == nil && st.IsDir() && !strings.HasPrefix(x.Name(), ".") {
						dirs = append(dirs, x.Name())
					}
				}
			}
		}
		sort.Slice(dirs[1:], func(i, j int) bool { return strings.ToLower(dirs[i+1]) < strings.ToLower(dirs[j+1]) })
		clicks = make([]Clicker, len(dirs))
		sel = -1
		list.offY = 0
	}
	load(cur)
	d := &Dialog{Title: title, Default: RespAccept, Width: 560}
	d.Body = func(gtx layout.Context) layout.Dimensions {
		pal := d.pal()
		if e.Submitted() {
			p := filepath.Clean(e.Text())
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				load(p)
			}
		}
		return layout.UniformInset(10).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return e.Layout(gtx, pal, 0) }),
				layout.Rigid(spacer(0, 8)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					h := gtx.Dp(320)
					gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
					fill(gtx, pal.BG)
					rowH := gtx.Dp(28)
					d := list.layout(gtx, len(dirs), rowH, 0, func(gtx layout.Context, i int) {
						for _, c := range clicks[i].Update(gtx) {
							if c.Count >= 2 {
								name := dirs[i]
								defer load(filepath.Clean(filepath.Join(cur, name)))
							} else {
								sel = i
							}
						}
						if i == sel {
							fill(gtx, pal.Selection)
						}
						func() {
							defer op.Offset(image.Pt(gtx.Dp(8), (rowH-gtx.Dp(16))/2)).Push(gtx.Ops).Pop()
							drawIcon(gtx, "folder-symbolic", 16, pal.FG)
						}()
						func() {
							defer op.Offset(image.Pt(gtx.Dp(32), 0)).Push(gtx.Ops).Pop()
							g := gtx
							g.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, rowH))
							layout.W.Layout(g, Label{Text: dirs[i], Color: pal.FG}.Layout)
						}()
						clicks[i].Add(gtx, image.Rectangle{Max: gtx.Constraints.Max}, pointer.CursorDefault)
					})
					strokeRect(gtx, image.Rectangle{Max: d.Size}, gtx.Dp(1), pal.Border)
					return d
				}),
			)
		})
	}
	d.addButtons("Cancel", RespCancel, "Open", RespAccept)
	d.OnResponse = func(resp int) {
		if resp != RespAccept {
			return
		}
		dir := cur
		if sel > 0 {
			dir = filepath.Join(cur, dirs[sel])
		}
		then(dir)
	}
	a.showDialog(d)
}

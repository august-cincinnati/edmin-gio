package main

import (
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"strings"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
)

// BuildCommand is a named shell command stored per project.
type BuildCommand struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	// Params prompts for extra text to append to Command when run.
	Params bool `json:"params,omitempty"`
}

// BuildPanel lists named commands; double-click runs one in the terminal.
type BuildPanel struct {
	app     *App
	cmds    []BuildCommand
	sel     int
	clicks  []Clicker
	list    scrollList
	focused bool
	bg      Clicker
	btns    [4]Button
}

func NewBuildPanel(app *App) *BuildPanel { return &BuildPanel{app: app, sel: -1} }

func (b *BuildPanel) file() string {
	return filepath.Join(b.app.root, ".edmin", "commands.json")
}

func (b *BuildPanel) Load() {
	b.cmds = nil
	if data, err := os.ReadFile(b.file()); err == nil {
		json.Unmarshal(data, &b.cmds)
	}
	b.refresh()
}

func (b *BuildPanel) save() {
	data, _ := json.MarshalIndent(b.cmds, "", "  ")
	if err := os.MkdirAll(filepath.Dir(b.file()), 0755); err == nil {
		err = os.WriteFile(b.file(), append(data, '\n'), 0644)
		if err == nil {
			return
		}
		b.app.showError("Could not save build commands", err.Error())
	}
}

func (b *BuildPanel) refresh() {
	b.clicks = make([]Clicker, len(b.cmds))
	if b.sel >= len(b.cmds) {
		b.sel = -1
	}
	b.app.invalidate()
}

func (b *BuildPanel) selected() int { return b.sel }

func (b *BuildPanel) remove() {
	i := b.selected()
	if i < 0 {
		return
	}
	b.cmds = append(b.cmds[:i], b.cmds[i+1:]...)
	b.sel = -1
	b.save()
	b.refresh()
}

// run executes command idx in the terminal, first prompting for extra
// arguments when the command takes parameters.
func (b *BuildPanel) run(idx int) {
	c := b.cmds[idx]
	if !c.Params {
		b.runCommand(c.Command)
		return
	}
	e := NewEntry()
	e.Placeholder = "Parameters to append"
	d := &Dialog{Title: c.Name, Default: RespOK, Width: 420}
	d.Body = func(gtx layout.Context) layout.Dimensions {
		pal := d.pal()
		return layout.UniformInset(10).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(Label{Text: c.Command, Color: pal.FG, MaxLines: 4}.Layout),
				layout.Rigid(spacer(0, 6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return e.Layout(gtx, pal, 0) }),
			)
		})
	}
	d.Init = func(gtx layout.Context) { e.Focus(gtx) }
	d.addButtons("Cancel", RespCancel, "Run", RespOK)
	d.OnResponse = func(resp int) {
		if resp == RespOK {
			b.runCommand(withParams(c.Command, e.Text()))
		}
	}
	b.app.showDialog(d)
}

// runCommand runs command in the current terminal, then gives the focus
// back to the command list so another command can be picked straight away.
func (b *BuildPanel) runCommand(command string) {
	b.app.runInTerminal(command)
	b.app.focusTag(b)
}

// withParams appends extra to command, separated by a space.
func withParams(command, extra string) string {
	if extra = strings.TrimSpace(extra); extra == "" {
		return command
	}
	return strings.TrimRight(command, " ") + " " + extra
}

// edit shows the add/edit dialog; idx < 0 adds a new command.
func (b *BuildPanel) edit(idx int) {
	var cur BuildCommand
	title := "Add Build Command"
	if idx >= 0 {
		cur = b.cmds[idx]
		title = "Edit Build Command"
	}
	ne, ce := NewEntry(), NewEntry()
	ne.SetText(cur.Name)
	ce.SetText(cur.Command)
	ce.Placeholder = "e.g. go build ./..."
	var pc Toggle
	pc.On = cur.Params
	d := &Dialog{Title: title, Default: RespOK, Width: 420}
	d.Body = func(gtx layout.Context) layout.Dimensions {
		pal := d.pal()
		pc.Changed(gtx, false)
		row := func(label string, w layout.Widget) layout.FlexChild {
			return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = gtx.Dp(70)
						return Label{Text: label, Color: pal.FG}.Layout(gtx)
					}),
					layout.Rigid(spacer(8, 0)),
					layout.Flexed(1, w),
				)
			})
		}
		return layout.UniformInset(10).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				row("Name", func(gtx layout.Context) layout.Dimensions { return ne.Layout(gtx, pal, 0) }),
				layout.Rigid(spacer(0, 6)),
				row("Command", func(gtx layout.Context) layout.Dimensions { return ce.Layout(gtx, pal, 0) }),
				layout.Rigid(spacer(0, 6)),
				row("", func(gtx layout.Context) layout.Dimensions {
					return pc.Layout(gtx, pal, "Prompt for parameters when run", false)
				}),
			)
		})
	}
	d.Init = func(gtx layout.Context) { ne.Focus(gtx); ne.SetCaret(ne.Len(), 0) }
	d.addButtons("Cancel", RespCancel, "Save", RespOK)
	d.OnResponse = func(resp int) {
		name, command := ne.Text(), ce.Text()
		if resp != RespOK || command == "" {
			return
		}
		if name == "" {
			name = command
		}
		c := BuildCommand{Name: name, Command: command, Params: pc.On}
		if idx >= 0 && idx < len(b.cmds) {
			b.cmds[idx] = c
		} else {
			b.cmds = append(b.cmds, c)
		}
		b.save()
		b.refresh()
	}
	b.app.showDialog(d)
}

// ---- Layout ----

func (b *BuildPanel) handleKeys(gtx layout.Context) {
	keys := func() []event.Filter {
		fs := []event.Filter{key.FocusFilter{Target: b}}
		if b.focused {
			for _, n := range []key.Name{key.NameUpArrow, key.NameDownArrow, key.NameReturn, key.NameEnter, key.NameSpace, key.NameHome, key.NameEnd} {
				fs = append(fs, key.Filter{Focus: b, Name: n})
			}
		}
		return fs
	}
	filters := keys()
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.FocusEvent:
			b.focused = e.Focus
			// Take keys from now on, not from the next frame.
			filters = keys()
			if e.Focus && b.sel < 0 && len(b.cmds) > 0 {
				b.sel = 0
			}
		case key.Event:
			if e.State != key.Press || len(b.cmds) == 0 {
				continue
			}
			switch e.Name {
			case key.NameUpArrow:
				b.sel = max(b.sel-1, 0)
			case key.NameDownArrow:
				b.sel = min(b.sel+1, len(b.cmds)-1)
			case key.NameHome:
				b.sel = 0
			case key.NameEnd:
				b.sel = len(b.cmds) - 1
			case key.NameReturn, key.NameEnter, key.NameSpace:
				if b.sel >= 0 {
					defer b.run(b.sel)
				}
			}
		}
	}
}

func (b *BuildPanel) Layout(gtx layout.Context, pal palette) layout.Dimensions {
	b.handleKeys(gtx)
	size := gtx.Constraints.Max
	fill(gtx, pal.Panel)
	for range b.bg.Update(gtx) {
		b.app.focusTag(b)
	}
	{
		r := clip.Rect{Max: size}.Push(gtx.Ops)
		event.Op(gtx.Ops, &b.bg)
		event.Op(gtx.Ops, b)
		r.Pop()
	}
	specs := []struct {
		icon, tip string
		fn        func()
	}{
		{"list-add-symbolic", "Add command (Ctrl+Shift+T)", func() { b.edit(-1) }},
		{"document-edit-symbolic", "Edit selected command", func() {
			if i := b.selected(); i >= 0 {
				b.edit(i)
			}
		}},
		{"list-remove-symbolic", "Remove selected command", b.remove},
		{"media-playback-start-symbolic", "Run selected command", func() {
			if i := b.selected(); i >= 0 {
				b.run(i)
			}
		}},
	}
	return layout.Inset{Top: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = gtx.Constraints.Max
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: 6}.Layout(gtx, Label{Text: "Build Commands", Font: bold(uiFont), Color: pal.FG}.Layout)
			}),
			layout.Rigid(spacer(0, 4)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				fill(gtx, pal.BG)
				rowH := gtx.Dp(24)
				var run = -1
				d := b.list.layout(gtx, len(b.cmds), rowH, 0, func(gtx layout.Context, i int) {
					for _, c := range b.clicks[i].Update(gtx) {
						b.app.focusTag(b)
						if c.Button == pointer.ButtonPrimary {
							b.sel = i
							if c.Count == 2 {
								run = i
							}
						}
					}
					if i == b.sel {
						fill(gtx, pal.Selection)
					}
					name := b.cmds[i].Name
					if b.cmds[i].Params {
						name += " …"
					}
					func() {
						defer op.Offset(image.Pt(gtx.Dp(3), 0)).Push(gtx.Ops).Pop()
						g := gtx
						g.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, rowH))
						layout.W.Layout(g, Label{Text: name, Color: pal.FG}.Layout)
					}()
					r := image.Rect(0, 0, gtx.Constraints.Max.X, rowH)
					b.clicks[i].Add(gtx, r, pointer.CursorDefault)
					tooltipArea(gtx, &b.clicks[i], r, b.cmds[i].Command)
				})
				if run >= 0 {
					b.run(run)
				}
				return d
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Top: 4, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Center.Layout(gtx, Label{Text: "Double-click to run in the terminal", Size: small(uiSize), Color: pal.FG}.Layout)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: 4, Right: 4, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					var items []layout.FlexChild
					for i, s := range specs {
						if i > 0 {
							items = append(items, layout.Rigid(spacer(2, 0)))
						}
						items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							bt := &b.btns[i]
							bt.Tip = s.tip
							if bt.Clicked(gtx) {
								defer s.fn()
							}
							return bt.Layout(gtx, buttonStyle{Icon: s.icon, Pal: pal})
						}))
					}
					return layout.Flex{}.Layout(gtx, items...)
				})
			}),
		)
	})
}

package main

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
)

// notebookFrame is the colour of GTK's notebook outline in the Yaru theme.
var notebookFrame = rgb("#c7c7c7")

// Notebook draws a GTK-style tab strip: bordered tabs with the current one
// underlined in the accent colour, reorderable by dragging.
type Notebook struct {
	tabs    []*nbTab
	Current int
	scroll  int // horizontal scroll of the strip, in pixels

	drag     *nbTab
	dragX    float32 // pointer x within the strip
	dragFrom float32
	dragging bool
	stripTag bool

	// OnReorder runs after a tab is dragged to a new place.
	OnReorder func()
	// OnSwitch runs when a tab is clicked.
	OnSwitch func(i int)
}

type nbTab struct {
	key   any
	click Clicker
	close Button
	x, w  int // position in the strip at the last layout
}

// SetTabs makes the notebook's tabs match keys, keeping their state.
func (n *Notebook) SetTabs(keys []any) {
	old := map[any]*nbTab{}
	for _, t := range n.tabs {
		old[t.key] = t
	}
	n.tabs = n.tabs[:0]
	for _, k := range keys {
		t := old[k]
		if t == nil {
			t = &nbTab{key: k}
		}
		n.tabs = append(n.tabs, t)
	}
	n.Current = max(0, min(n.Current, len(n.tabs)-1))
}

type notebookStyle struct {
	Pal   palette
	Label func(gtx layout.Context, i int, active bool) layout.Dimensions
	Tip   func(i int) string
	// Closed is called when tab i's close button is pressed.
	Closed func(i int)
	// Action draws an optional widget at the end of the strip (the + button).
	Action func(gtx layout.Context) layout.Dimensions
	// DoubleClick is called on a double click on tab i's label.
	DoubleClick func(i int)
	// NoClose leaves out the close buttons.
	NoClose bool
}

// LayoutStrip draws the tab strip, gtx.Constraints.Max.X wide.
func (n *Notebook) LayoutStrip(gtx layout.Context, st notebookStyle) layout.Dimensions {
	pal := st.Pal
	w := gtx.Constraints.Max.X
	h := gtx.Dp(36)
	fillRect(gtx, image.Rect(0, 0, w, h+gtx.Dp(1)), pal.Panel)
	fillRect(gtx, image.Rect(0, h, w, h+gtx.Dp(1)), pal.Border)

	// The action widget at the end.
	actionW := 0
	if st.Action != nil {
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints.Min = image.Point{}
		ad := st.Action(g)
		call := m.Stop()
		actionW = ad.Size.X + gtx.Dp(4)
		func() {
			defer op.Offset(image.Pt(w-ad.Size.X-gtx.Dp(2), (h-ad.Size.Y)/2)).Push(gtx.Ops).Pop()
			call.Add(gtx.Ops)
		}()
	}

	// Tabs.
	area := image.Rect(0, 0, w-actionW, h)
	stack := clip.Rect(area).Push(gtx.Ops)
	pad := gtx.Dp(8)
	x := pad - n.scroll
	gap := gtx.Dp(8)
	for i, t := range n.tabs {
		active := i == n.Current
		for _, c := range t.click.Update(gtx) {
			if c.Button != pointer.ButtonPrimary {
				continue
			}
			if c.Count == 2 && st.DoubleClick != nil {
				st.DoubleClick(i)
				continue
			}
			n.Current = i
			if n.OnSwitch != nil {
				n.OnSwitch(i)
			}
			n.drag = t
		}
		if t.close.Clicked(gtx) && st.Closed != nil {
			idx := i
			defer st.Closed(idx)
		}
		// Content: label, gap, close button.
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints.Min = image.Point{}
		ld := st.Label(g, i, active)
		lcall := m.Stop()
		bw, bh := gtx.Dp(22), gtx.Dp(22)
		if st.NoClose {
			bw = -gtx.Dp(8)
		}
		inner := gtx.Dp(12)
		tw := 2*gtx.Dp(1) + inner + ld.Size.X + gtx.Dp(8) + bw + gtx.Dp(8)
		tx := x
		if n.dragging && n.drag == t {
			tx = int(n.dragX - n.dragFrom)
		}
		t.x, t.w = x+n.scroll, tw
		func() {
			defer op.Offset(image.Pt(tx, 0)).Push(gtx.Ops).Pop()
			bg := pal.Panel
			if active {
				bg = pal.BG
			}
			fillRect(gtx, image.Rect(0, 0, tw, h), bg)
			fillRect(gtx, image.Rect(0, 0, gtx.Dp(1), h), pal.Border)
			fillRect(gtx, image.Rect(tw-gtx.Dp(1), 0, tw, h), pal.Border)
			if active {
				fillRect(gtx, image.Rect(gtx.Dp(1), h-gtx.Dp(3), tw-gtx.Dp(1), h), accent)
			}
			func() {
				defer op.Offset(image.Pt(gtx.Dp(1)+inner, (h-ld.Size.Y)/2)).Push(gtx.Ops).Pop()
				lcall.Add(gtx.Ops)
			}()
			t.click.Add(gtx, image.Rect(0, 0, tw-bw-gtx.Dp(8), h), pointer.CursorDefault)
			if st.Tip != nil {
				tooltipArea(gtx, &t.click, image.Rect(0, 0, tw, h), st.Tip(i))
			}
			if !st.NoClose {
				defer op.Offset(image.Pt(tw-gtx.Dp(1)-gtx.Dp(8)-bw, (h-bh)/2)).Push(gtx.Ops).Pop()
				t.close.Layout(gtx, buttonStyle{Icon: "window-close-symbolic", Pal: pal, Size: image.Pt(22, 22)})
			}
		}()
		x += tw + gap
	}
	// Scroll the strip so the current tab is visible.
	total := x + n.scroll - gap + pad
	if n.Current < len(n.tabs) {
		t := n.tabs[n.Current]
		if t.x-n.scroll < 0 {
			n.scroll = t.x - pad
		} else if t.x+t.w-n.scroll > area.Dx() {
			n.scroll = t.x + t.w - area.Dx() + pad
		}
	}
	n.scroll = max(0, min(n.scroll, total-area.Dx()))
	n.dragEvents(gtx, area)
	stack.Pop()
	return layout.Dimensions{Size: image.Pt(w, h+gtx.Dp(1))}
}

// dragEvents lets tabs be dragged to reorder them.
func (n *Notebook) dragEvents(gtx layout.Context, area image.Rectangle) {
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &n.stripTag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch e.Kind {
		case pointer.Press:
			n.dragFrom = e.Position.X
			n.dragging = false
			if n.drag != nil {
				n.dragFrom = e.Position.X - float32(n.drag.x-n.scroll)
			}
			n.dragX = e.Position.X
		case pointer.Drag:
			if n.drag == nil {
				break
			}
			if !n.dragging && abs(int(e.Position.X-n.dragX)) < gtx.Dp(8) {
				break
			}
			n.dragging = true
			n.dragX = e.Position.X
			// Move the dragged tab past any neighbour whose middle it crosses.
			i := n.indexOf(n.drag)
			left := int(n.dragX-n.dragFrom) + n.scroll
			if i > 0 && left < n.tabs[i-1].x+n.tabs[i-1].w/2 {
				n.swap(i, i-1)
			} else if i+1 < len(n.tabs) && left+n.drag.w > n.tabs[i+1].x+n.tabs[i+1].w/2 {
				n.swap(i, i+1)
			}
		case pointer.Release, pointer.Cancel:
			if n.dragging && n.OnReorder != nil {
				n.OnReorder()
			}
			n.dragging = false
			n.drag = nil
		}
	}
	// The strip area takes pointer events that pass through the tabs.
	defer clip.Rect(area).Push(gtx.Ops).Pop()
	pass := pointer.PassOp{}.Push(gtx.Ops)
	event.Op(gtx.Ops, &n.stripTag)
	pass.Pop()
}

func (n *Notebook) indexOf(t *nbTab) int {
	for i, x := range n.tabs {
		if x == t {
			return i
		}
	}
	return -1
}

func (n *Notebook) swap(i, j int) {
	n.tabs[i], n.tabs[j] = n.tabs[j], n.tabs[i]
	if n.Current == i {
		n.Current = j
	} else if n.Current == j {
		n.Current = i
	}
	n.tabs[i].x, n.tabs[j].x = n.tabs[j].x, n.tabs[i].x
}

// Keys returns the tab keys in display order.
func (n *Notebook) Keys() []any {
	ks := make([]any, len(n.tabs))
	for i, t := range n.tabs {
		ks[i] = t.key
	}
	return ks
}

// frame draws GTK's notebook outline around r.
func notebookOutline(gtx layout.Context, r image.Rectangle) {
	strokeRect(gtx, r, gtx.Dp(1), notebookFrame)
}

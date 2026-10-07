package main

import (
	"image"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
)

// ignoredDirs are never shown or searched.
var ignoredDirs = map[string]bool{".git": true, ".hg": true, ".svn": true, "node_modules": true, ".edmin": true}

type treeNode struct {
	name, path string
	isDir      bool
	expanded   bool
	loaded     bool
	children   []*treeNode
	parent     *treeNode
	depth      int
	click      Clicker
}

// FileTree is the lazily loaded project explorer.
type FileTree struct {
	app      *App
	roots    []*treeNode
	rows     []*treeNode // the visible rows, top to bottom
	selected *treeNode
	list     scrollList
	revealed bool // scroll the selection into view on the next frame
	focused  bool

	typed    string // type-ahead search text
	typedAt  time.Time
	bgClick  Clicker
	toolbar  [5]Button
	emptyTag bool
}

func NewFileTree(app *App) *FileTree { return &FileTree{app: app} }

// toolbarButtons are the explorer's buttons in the window's header bar.
func (f *FileTree) toolbarSpecs() []struct {
	icon, tip string
	fn        func()
} {
	return []struct {
		icon, tip string
		fn        func()
	}{
		{"document-new-symbolic", "New file (Ctrl+N)", func() { f.app.showExplorer(); f.create(false) }},
		{"folder-new-symbolic", "New folder (Ctrl+Shift+N)", func() { f.app.showExplorer(); f.create(true) }},
		{"view-refresh-symbolic", "Refresh file tree", f.Reload},
		{"find-location-symbolic", "Jump to open file (Ctrl+Shift+E)", f.RevealCurrent},
		{"pan-up-symbolic", "Collapse all folders", f.CollapseAll},
	}
}

func (f *FileTree) CollapseAll() {
	var walk func(ns []*treeNode)
	walk = func(ns []*treeNode) {
		for _, n := range ns {
			n.expanded = false
			walk(n.children)
		}
	}
	walk(f.roots)
	f.rebuildRows()
}

// Reload rebuilds the tree from the project root, keeping expanded folders open.
func (f *FileTree) Reload() {
	expanded := map[string]bool{}
	var walk func(ns []*treeNode)
	walk = func(ns []*treeNode) {
		for _, n := range ns {
			if n.expanded {
				expanded[n.path] = true
				walk(n.children)
			}
		}
	}
	walk(f.roots)
	sel := ""
	if f.selected != nil {
		sel = f.selected.path
	}
	f.roots = f.fill(nil, f.app.root)
	var reopen func(ns []*treeNode)
	reopen = func(ns []*treeNode) {
		for _, n := range ns {
			if expanded[n.path] {
				f.expand(n)
				reopen(n.children)
			}
		}
	}
	reopen(f.roots)
	f.selected = nil
	f.rebuildRows()
	if sel != "" {
		for _, r := range f.rows {
			if r.path == sel {
				f.selected = r
			}
		}
	}
}

func (f *FileTree) fill(parent *treeNode, dir string) []*treeNode {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	sort.SliceStable(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	depth := 0
	if parent != nil {
		depth = parent.depth + 1
	}
	var out []*treeNode
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(dir, name)
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(full); err == nil {
				isDir = st.IsDir()
			}
		}
		if isDir && ignoredDirs[name] {
			continue
		}
		out = append(out, &treeNode{name: name, path: full, isDir: isDir, parent: parent, depth: depth})
	}
	return out
}

func (f *FileTree) expand(n *treeNode) {
	if !n.isDir {
		return
	}
	if !n.loaded {
		n.children = f.fill(n, n.path)
		n.loaded = true
	}
	n.expanded = true
}

func (f *FileTree) toggle(n *treeNode) {
	if n.expanded {
		n.expanded = false
	} else {
		f.expand(n)
	}
	f.rebuildRows()
}

func (f *FileTree) rebuildRows() {
	f.rows = f.rows[:0]
	var walk func(ns []*treeNode)
	walk = func(ns []*treeNode) {
		for _, n := range ns {
			f.rows = append(f.rows, n)
			if n.expanded {
				walk(n.children)
			}
		}
	}
	walk(f.roots)
	if f.selected != nil && !f.visible(f.selected) {
		// The selection was inside a collapsed folder: select that folder.
		p := f.selected.parent
		for p != nil && !f.visible(p) {
			p = p.parent
		}
		f.selected = p
	}
	f.app.invalidate()
}

func (f *FileTree) visible(n *treeNode) bool {
	for p := n.parent; p != nil; p = p.parent {
		if !p.expanded {
			return false
		}
	}
	return true
}

func (f *FileTree) rowIndex(n *treeNode) int {
	for i, r := range f.rows {
		if r == n {
			return i
		}
	}
	return -1
}

// nodeFor finds the node for p, expanding its ancestors so it is loaded.
func (f *FileTree) nodeFor(p string) *treeNode {
	rel, err := filepath.Rel(f.app.root, p)
	if err != nil || rel == "." || !insideRoot(rel) {
		return nil
	}
	nodes := f.roots
	cur := f.app.root
	var n *treeNode
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		if n != nil {
			f.expand(n)
			nodes = n.children
		}
		cur = filepath.Join(cur, name)
		n = nil
		for _, c := range nodes {
			if c.path == cur {
				n = c
				break
			}
		}
		if n == nil {
			return nil
		}
	}
	return n
}

// reveal expands the tree down to p, selects it and scrolls it into view.
func (f *FileTree) reveal(p string) bool {
	n := f.nodeFor(p)
	if n == nil {
		return false
	}
	f.rebuildRows()
	f.selected = n
	f.revealed = true
	return true
}

// RevealCurrent shows the explorer and selects the file open in the current
// tab, reloading once in case the file was created outside EdMin.
func (f *FileTree) RevealCurrent() {
	e := f.app.editors.Current()
	if e == nil || e.Path == "" {
		return
	}
	f.app.showExplorer()
	if !f.reveal(e.Path) {
		f.Reload()
		f.reveal(e.Path)
	}
}

// selectedDir returns the directory for "new file" actions: the selected
// folder, the selected file's folder, or the project root.
func (f *FileTree) selectedDir() string {
	n := f.selected
	if n == nil {
		return f.app.root
	}
	if n.isDir {
		return n.path
	}
	return filepath.Dir(n.path)
}

func (f *FileTree) activate(n *treeNode) {
	if n.isDir {
		f.toggle(n)
		return
	}
	f.app.editors.Open(n.path)
}

func (f *FileTree) contextMenu(pos image.Point) {
	items := []*menuItem{
		{label: "New File…", fn: func() { f.create(false) }},
		{label: "New Folder…", fn: func() { f.create(true) }},
		{label: "Refresh", fn: f.Reload},
	}
	if e := f.app.editors.Current(); e != nil && e.Path != "" {
		items = append(items, &menuItem{label: "Jump to Open File", fn: f.RevealCurrent})
	}
	f.app.popupMenu(pos, items)
}

func (f *FileTree) create(dir bool) {
	title := "New File"
	if dir {
		title = "New Folder"
	}
	base := f.selectedDir()
	f.app.prompt(title, "Name (relative to "+relPath(f.app.root, base)+", may include subfolders):", "", func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		full := filepath.Join(base, name)
		if rel, err := filepath.Rel(f.app.root, full); err != nil || rel == "." || !insideRoot(rel) {
			f.app.showError("Could not create "+name, "The path must be inside the project folder.")
			return
		}
		err := os.MkdirAll(filepath.Dir(full), 0755)
		if err == nil {
			if dir {
				err = os.Mkdir(full, 0755)
			} else {
				var fh *os.File
				fh, err = os.OpenFile(full, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
				if err == nil {
					fh.Close()
				}
			}
		}
		if err != nil {
			f.app.showError("Could not create "+name, err.Error())
			return
		}
		f.Reload()
		f.reveal(full)
		if !dir {
			f.app.editors.Open(full)
		}
	})
}

// arrowRight expands the selected folder.
func (f *FileTree) arrowRight() {
	if n := f.selected; n != nil && n.isDir && !n.expanded {
		f.expand(n)
		f.rebuildRows()
	}
}

// arrowLeft collapses the selected folder if it is open, else moves the
// selection to its parent folder.
func (f *FileTree) arrowLeft() {
	n := f.selected
	if n == nil {
		return
	}
	if n.isDir && n.expanded {
		n.expanded = false
		f.rebuildRows()
		return
	}
	if n.parent != nil {
		f.selected = n.parent
		f.revealed = true
	}
}

// deleteSelected asks before deleting the selected file or folder, then
// deletes it and closes any tabs open on it.
func (f *FileTree) deleteSelected() {
	n := f.selected
	if n == nil {
		return
	}
	p, isDir := n.path, n.isDir
	if rel, err := filepath.Rel(f.app.root, p); p == "" || err != nil || rel == "." || !insideRoot(rel) {
		return
	}
	title, detail := "Delete file?", " will be permanently deleted."
	if isDir {
		title, detail = "Delete folder?", " and everything in it will be permanently deleted."
	}
	d := &Dialog{Message: title, Secondary: relPath(f.app.root, p) + detail, Default: RespYes, Width: 400}
	d.addButtons("Cancel", RespNo, "Yes", RespYes)
	d.OnResponse = func(r int) {
		if r != RespYes {
			return
		}
		var err error
		if isDir {
			err = os.RemoveAll(p)
		} else {
			err = os.Remove(p)
		}
		// Close tabs on anything that was deleted, even if only partly.
		for _, e := range append([]*Editor(nil), f.app.editors.editors...) {
			if e.Path == p || isDir && strings.HasPrefix(e.Path, p+string(filepath.Separator)) {
				if _, serr := os.Stat(e.Path); os.IsNotExist(serr) {
					e.View.SetModified(false)
					f.app.editors.Close(e)
				}
			}
		}
		f.Reload()
		if err != nil {
			f.app.showError("Could not delete "+filepath.Base(p), err.Error())
		}
	}
	f.app.showDialog(d)
}

// typeAhead selects the first row starting with the typed text, like GTK's
// interactive search.
func (f *FileTree) typeAhead(s string) {
	if time.Since(f.typedAt) > 1500*time.Millisecond {
		f.typed = ""
	}
	f.typed += s
	f.typedAt = time.Now()
	low := strings.ToLower(f.typed)
	start := max(f.rowIndex(f.selected), 0)
	for k := 0; k < len(f.rows); k++ {
		r := f.rows[(start+k)%len(f.rows)]
		if strings.HasPrefix(strings.ToLower(r.name), low) {
			f.selected = r
			f.revealed = true
			return
		}
	}
}

// ---- Layout ----

func (f *FileTree) handleKeys(gtx layout.Context) {
	keys := func() []event.Filter {
		fs := []event.Filter{key.FocusFilter{Target: f}}
		if f.focused {
			for _, n := range []key.Name{key.NameUpArrow, key.NameDownArrow, key.NameLeftArrow, key.NameRightArrow,
				key.NameReturn, key.NameEnter, key.NameSpace, key.NameDeleteForward, key.NameHome, key.NameEnd,
				key.NamePageUp, key.NamePageDown, key.NameEscape} {
				fs = append(fs, key.Filter{Focus: f, Name: n})
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
			f.focused = e.Focus
			// Take keys from now on, not from the next frame.
			filters = keys()
			if e.Focus && f.selected == nil && len(f.rows) > 0 {
				f.selected = f.rows[0]
			}
		case key.EditEvent:
			if strings.TrimFunc(e.Text, unicode.IsSpace) != "" {
				f.typeAhead(e.Text)
			}
		case key.Event:
			if e.State != key.Press {
				continue
			}
			i := f.rowIndex(f.selected)
			page := max(f.list.view.Y/max(gtx.Dp(24), 1)-1, 1)
			move := func(j int) {
				if len(f.rows) == 0 {
					return
				}
				j = max(0, min(j, len(f.rows)-1))
				f.selected = f.rows[j]
				f.revealed = true
			}
			switch e.Name {
			case key.NameUpArrow:
				move(i - 1)
			case key.NameDownArrow:
				move(i + 1)
			case key.NameHome:
				move(0)
			case key.NameEnd:
				move(len(f.rows) - 1)
			case key.NamePageUp:
				move(i - page)
			case key.NamePageDown:
				move(i + page)
			case key.NameLeftArrow:
				f.arrowLeft()
			case key.NameRightArrow:
				f.arrowRight()
			case key.NameReturn, key.NameEnter, key.NameSpace:
				if f.selected != nil {
					f.activate(f.selected)
				}
			case key.NameDeleteForward:
				f.deleteSelected()
			case key.NameEscape:
				f.typed = ""
			}
		}
	}
}

func (f *FileTree) Layout(gtx layout.Context, pal palette) layout.Dimensions {
	f.handleKeys(gtx)
	size := gtx.Constraints.Max
	fill(gtx, pal.BG)
	rowH := gtx.Dp(24)
	indent := gtx.Dp(20)

	// Clicks on empty space.
	for _, c := range f.bgClick.Update(gtx) {
		f.app.focusTag(f)
		if c.Button == pointer.ButtonSecondary {
			f.selected = nil
			f.contextMenu(f.app.mouse)
		}
	}
	{
		r := clip.Rect{Max: size}.Push(gtx.Ops)
		event.Op(gtx.Ops, &f.bgClick)
		event.Op(gtx.Ops, f)
		r.Pop()
	}
	if f.revealed {
		f.revealed = false
		if i := f.rowIndex(f.selected); i >= 0 {
			// Like GTK's ScrollToCell with row alignment 0.3.
			if i*rowH < f.list.offY || (i+1)*rowH > f.list.offY+size.Y {
				f.list.offY = i*rowH - size.Y*3/10
			}
		}
	}
	contentW := 0
	for _, n := range f.rows {
		contentW = max(contentW, indent*(n.depth+1)+gtx.Dp(18)+len(n.name)*gtx.Dp(8))
	}
	var activate *treeNode
	d := f.list.layout(gtx, len(f.rows), rowH, contentW, func(gtx layout.Context, i int) {
		n := f.rows[i]
		w := gtx.Constraints.Max.X
		x0 := n.depth * indent
		for _, c := range n.click.Update(gtx) {
			f.app.focusTag(f)
			switch {
			case c.Button == pointer.ButtonSecondary:
				f.selected = n
				f.contextMenu(f.app.mouse)
			case c.Button == pointer.ButtonPrimary && n.isDir && c.Pos.X >= x0 && c.Pos.X < x0+indent:
				// The expander arrow.
				f.selected = n
				f.toggle(n)
			case c.Button == pointer.ButtonPrimary:
				f.selected = n
				if c.Count == 2 {
					activate = n
				}
			}
		}
		if n == f.selected {
			fill(gtx, pal.Selection)
		}
		if n.isDir {
			icon := "pan-end-symbolic"
			if n.expanded {
				icon = "pan-down-symbolic"
			}
			func() {
				defer op.Offset(image.Pt(x0+(indent-gtx.Dp(16))/2, (rowH-gtx.Dp(16))/2)).Push(gtx.Ops).Pop()
				drawIcon(gtx, icon, 16, pal.FG)
			}()
		}
		icon := "text-x-generic"
		if n.isDir {
			icon = "folder"
		}
		func() {
			defer op.Offset(image.Pt(x0+indent, (rowH-gtx.Dp(16))/2)).Push(gtx.Ops).Pop()
			drawIcon(gtx, icon, 16, pal.FG)
		}()
		func() {
			defer op.Offset(image.Pt(x0+indent+gtx.Dp(18), 0)).Push(gtx.Ops).Pop()
			g := gtx
			g.Constraints = layout.Exact(image.Pt(w, rowH))
			layout.W.Layout(g, Label{Text: n.name, Color: pal.FG}.Layout)
		}()
		n.click.Add(gtx, image.Rect(0, 0, w, rowH), pointer.CursorDefault)
	})
	if activate != nil {
		f.activate(activate)
	}
	// The type-ahead box, as GTK's search popup.
	if f.typed != "" && time.Since(f.typedAt) < 1500*time.Millisecond && f.focused {
		tw := max(textWidth(gtx, f.typed, uiFont, uiSize)+gtx.Dp(16), gtx.Dp(120))
		r := image.Rect(size.X-tw-gtx.Dp(6), size.Y-gtx.Dp(40), size.X-gtx.Dp(6), size.Y-gtx.Dp(6))
		fillRRect(gtx, r, gtx.Dp(5), pal.Panel)
		strokeShape(gtx, clip.UniformRRect(r, gtx.Dp(5)), gtx.Dp(2), accent)
		func() {
			defer op.Offset(image.Pt(r.Min.X+gtx.Dp(8), r.Min.Y)).Push(gtx.Ops).Pop()
			g := gtx
			g.Constraints = layout.Exact(image.Pt(r.Dx(), r.Dy()))
			layout.W.Layout(g, Label{Text: f.typed, Color: pal.FG}.Layout)
		}()
		gtx.Execute(op.InvalidateCmd{At: f.typedAt.Add(1500 * time.Millisecond)})
	}
	return d
}

// insideRoot reports whether a path relative to the project root stays inside it.
func insideRoot(rel string) bool {
	rel = filepath.Clean(rel)
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel) && filepath.VolumeName(rel) == "" && !os.IsPathSeparator(rel[0])
}

func relPath(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}

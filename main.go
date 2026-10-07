// EdMin is a minimal text editor built with Gio and tree-sitter.
package main

import (
	"encoding/json"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gioui.org/app"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
)

type App struct {
	win   *app.Window
	root  string
	theme *Theme
	shell []string // what the project's terminals run; nil means $SHELL

	editors *EditorArea
	tree    *FileTree
	search  *SearchDialog
	build   *BuildPanel

	terminals  []*Terminal
	termNB     Notebook
	termCount  int
	termLabels map[*Terminal]*renamable
	newTermBtn Button

	status string
	title  string

	// Panel visibility and sizes (in dp): the explorer's and build panel's
	// widths and the terminal panel's height.
	leftOn, termOn, buildOn bool
	leftW, rightW, termH    float32
	dragging                int // the divider being dragged: 1 left, 2 right, 3 terminal
	dragStart               image.Point
	dragFrom                float32

	deco                       windowDeco
	openBtn, newWinBtn         Button
	leftBtn, termBtn, buildBtn Button
	settingsBtn                Button

	settingsDlg  *Dialog // the open Settings window, if any
	dialogs      []*Dialog
	menu         *Menu
	pendingFocus event.Tag
	mouse        image.Point
	tip          tooltipState
	closeOK      bool // the window may close without asking

	qmu   sync.Mutex
	queue []func()

	divTags [3]bool
}

// apps holds every open window; the program exits when the last one closes.
var apps []*App

// quitting is set while every window is being closed at once, so the session
// keeps listing all of their projects.
var quitting bool

func main() {
	args, wait := parseArgs(os.Args[1:])
	detached := os.Getenv(detachedEnv) != ""
	// Don't pass the marker on to shells started in EdMin's terminals.
	os.Unsetenv(detachedEnv)
	if !wait && !detached && fromTerminal() && detach(args) {
		return
	}
	loadDesktopFonts()
	if len(args) == 0 {
		// Reopen the projects from last time, or else the current directory.
		for _, p := range loadSettings(settingsPath()).Open {
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				args = append(args, p)
			}
		}
	}
	if len(args) == 0 {
		cwd, _ := os.Getwd()
		args = []string{cwd}
	}
	uiMu.Lock()
	// Each argument (a folder or a file) opens in its own window.
	for _, arg := range args {
		root, _ := os.Getwd()
		var openFile string
		p, _ := filepath.Abs(arg)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			openFile = p
			root = filepath.Dir(p)
		} else if err == nil {
			root = p
		}
		a := newApp(root)
		if openFile != "" {
			a.editors.Open(openFile)
			a.focusWorkspace()
		}
	}
	uiMu.Unlock()
	app.Main()
}

// detachedEnv marks the background copy started by detach.
const detachedEnv = "EDMIN_DETACHED"

// parseArgs removes the --wait (-w) flag from args.
func parseArgs(in []string) (args []string, wait bool) {
	for _, a := range in {
		if a == "--wait" || a == "-w" {
			wait = true
		} else {
			args = append(args, a)
		}
	}
	return args, wait
}

// fromTerminal reports whether EdMin was started from an interactive shell.
func fromTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// detach restarts EdMin in its own session, disconnected from the terminal,
// so the shell prompt returns straight away. It reports whether that worked;
// if not, EdMin just runs in the foreground.
func detach(args []string) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), detachedEnv+"=1")
	// Stdin, stdout and stderr are left nil, so they go to /dev/null.
	cmd.SysProcAttr = detachedAttr()
	if err := cmd.Start(); err != nil {
		return false
	}
	cmd.Process.Release()
	return true
}

// newApp opens a new window with root as its project folder.
func newApp(root string) *App {
	a := newAppState(root)
	a.win = new(app.Window)
	a.win.Option(app.Title("EdMin"), app.Size(1300, 850), app.Decorated(false))
	go a.loop()
	return a
}

// newAppState sets up a window's state without the window itself.
func newAppState(root string) *App {
	a := &App{root: root, theme: projectTheme(root), leftOn: true, termOn: true, buildOn: true,
		leftW: 250, rightW: 239, termH: 266}
	apps = append(apps, a)
	a.editors = NewEditorArea(a)
	a.tree = NewFileTree(a)
	a.search = NewSearchDialog(a)
	a.build = NewBuildPanel(a)
	a.setRoot(root)
	a.restoreTerminals()
	return a
}

func (a *App) loop() {
	var ops op.Ops
	for {
		e := a.win.Event()
		uiMu.Lock()
		switch e := e.(type) {
		case app.DestroyEvent:
			a.destroyed()
			uiMu.Unlock()
			if len(apps) == 0 {
				os.Exit(0)
			}
			return
		case *app.ClosingEvent:
			if !a.closeOK {
				e.Abort()
				a.onClose(func() {
					a.closeOK = true
					a.win.Perform(system.ActionClose)
				})
			}
		case app.ConfigEvent:
			a.deco.config(e.Config)
		case app.FrameEvent:
			a.drainQueue()
			gtx := app.NewContext(&ops, e)
			a.layout(gtx)
			e.Frame(gtx.Ops)
		}
		uiMu.Unlock()
	}
}

// destroyed cleans up after the window has closed.
func (a *App) destroyed() {
	for _, t := range a.terminals {
		t.Close()
	}
	a.search.hide()
	for i, x := range apps {
		if x == a {
			apps = append(apps[:i], apps[i+1:]...)
			break
		}
	}
	// The last window closing ends the session; keep its list so the same
	// projects reopen next time.
	if len(apps) > 0 && !quitting {
		saveSession()
	}
}

// saveSession records the project folders open in windows.
func saveSession() {
	s := loadSettings(settingsPath())
	s.Open = nil
	seen := map[string]bool{}
	for _, a := range apps {
		if !seen[a.root] {
			seen[a.root] = true
			s.Open = append(s.Open, a.root)
		}
	}
	saveSettings(settingsPath(), s)
}

func (a *App) setRoot(root string) {
	a.root = root
	saveSession()
	a.setTheme(projectTheme(root))
	a.shell = projectShell(root)
	a.tree.Reload()
	a.build.Load()
	a.updateTitle()
}

func (a *App) updateTitle() {
	title := "EdMin — " + filepath.Base(a.root)
	if e := a.editors.Current(); e != nil {
		mod := ""
		if e.View.Modified() {
			mod = "● "
		}
		title = mod + relPath(a.root, e.Path) + " — EdMin"
	}
	if title != a.title {
		a.title = title
		if a.win != nil {
			a.win.Option(app.Title(title))
		}
	}
	a.invalidate()
}

func (a *App) updateStatus() {
	e := a.editors.Current()
	if e == nil {
		a.status = a.root
		return
	}
	cur := e.View.Cursor()
	lang := "Plain Text"
	if e.lang != nil {
		lang = e.lang.Name
	}
	col := len([]rune(e.View.Line(cur.Line)[:cur.Col]))
	a.status = fmt.Sprintf("%s    Ln %d, Col %d    %s", relPath(a.root, e.Path), cur.Line+1, col+1, lang)
	a.invalidate()
}

func (a *App) setStatusMsg(msg string) { a.status = msg; a.invalidate() }

// showExplorer opens the left panel.
func (a *App) showExplorer() { a.leftOn = true; a.invalidate() }

// focusTag gives the keyboard focus to tag on the next frame.
func (a *App) focusTag(tag event.Tag) {
	a.pendingFocus = tag
	a.invalidate()
}

// restoreFocus puts the focus back on the workspace after a dialog.
func (a *App) restoreFocus() {
	if len(a.dialogs) == 0 && a.pendingFocus == nil {
		a.focusWorkspace()
	}
}

// ---- Terminals ----

func (a *App) newTerminal() *Terminal { return a.newNamedTerminal("") }

// newNamedTerminal opens a terminal tab; an empty name gets "Terminal N".
func (a *App) newNamedTerminal(name string) *Terminal {
	a.termCount++
	var t *Terminal
	t = NewTerminal(a, a.root, a.shell, func() { a.closeTerminal(t) })
	r := &renamable{name: fmt.Sprintf("Terminal %d", a.termCount), onRename: a.saveTerminals}
	if name != "" {
		r.name = name
		r.custom = true
	}
	if a.termLabels == nil {
		a.termLabels = map[*Terminal]*renamable{}
	}
	a.termLabels[t] = r
	a.terminals = append(a.terminals, t)
	a.syncTermTabs()
	a.termNB.Current = len(a.terminals) - 1
	a.termOn = true
	t.Focus()
	return t
}

func (a *App) syncTermTabs() {
	keys := make([]any, len(a.terminals))
	for i, t := range a.terminals {
		keys[i] = t
	}
	a.termNB.SetTabs(keys)
	a.termNB.OnReorder = func() {
		var ts []*Terminal
		for _, k := range a.termNB.Keys() {
			ts = append(ts, k.(*Terminal))
		}
		a.terminals = ts
		a.saveTerminals()
	}
	a.termNB.OnSwitch = func(i int) { a.terminals[i].Focus() }
	a.invalidate()
}

// renamable is a tab label that turns into a text entry when double-clicked.
type renamable struct {
	name     string
	custom   bool // renamed by the user
	editing  bool
	entry    *Entry
	had      bool // the entry has had the focus
	after    func()
	onRename func()
}

// edit turns the label into an entry; then, if non-nil, runs when the edit ends.
func (r *renamable) edit(a *App, then func()) {
	if r.editing {
		return
	}
	r.editing, r.after, r.had = true, then, false
	r.entry = NewEntry()
	r.entry.SetText(r.name)
	r.entry.SetCaret(r.entry.Len(), 0)
	a.focusTag(&r.entry.Editor)
}

func (r *renamable) finish(commit bool) {
	if !r.editing {
		return
	}
	r.editing = false
	if name := strings.TrimSpace(r.entry.Text()); commit && name != "" {
		r.name = name
		r.custom = true
		r.onRename()
	}
	if r.after != nil {
		then := r.after
		r.after = nil
		then()
	}
}

func (r *renamable) layout(gtx layout.Context, pal palette) layout.Dimensions {
	if !r.editing {
		return Label{Text: r.name, Color: pal.FG}.Layout(gtx)
	}
	e := r.entry
	e.Update(gtx)
	switch {
	case e.Escaped():
		r.finish(false)
	case e.Submitted():
		r.finish(true)
	case r.had && !e.focused:
		r.finish(true)
	}
	if e.focused {
		r.had = true
	}
	if !r.editing {
		return Label{Text: r.name, Color: pal.FG}.Layout(gtx)
	}
	return e.Layout(gtx, pal, 110)
}

func (a *App) closeTerminal(t *Terminal) {
	a.dropTerminal(t)
	a.saveTerminals()
	if len(a.terminals) == 0 {
		a.termOn = false
	}
}

// dropTerminal closes t and removes its tab without touching the saved list.
func (a *App) dropTerminal(t *Terminal) {
	for i, x := range a.terminals {
		if x == t {
			a.terminals = append(a.terminals[:i], a.terminals[i+1:]...)
			delete(a.termLabels, t)
			t.Close()
			cur := a.termNB.Current
			if i < cur || cur >= len(a.terminals) {
				cur--
			}
			a.syncTermTabs()
			a.termNB.Current = max(cur, 0)
			break
		}
	}
}

func (a *App) terminalsFile() string {
	return filepath.Join(a.root, ".edmin", "terminals.json")
}

// saveTerminals stores the names of renamed terminal tabs, in tab order, so
// they reopen with the project. Tabs with default names aren't saved.
func (a *App) saveTerminals() {
	var names []string
	for _, t := range a.terminals {
		if r := a.termLabels[t]; r != nil && r.custom {
			names = append(names, r.name)
		}
	}
	f := a.terminalsFile()
	if len(names) == 0 {
		// Nothing to remember; don't leave an empty file behind.
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			a.setStatusMsg("Could not save terminal names: " + err.Error())
		}
		return
	}
	data, _ := json.MarshalIndent(names, "", "  ")
	err := os.MkdirAll(filepath.Dir(f), 0755)
	if err == nil {
		err = os.WriteFile(f, append(data, '\n'), 0644)
	}
	if err != nil {
		a.setStatusMsg("Could not save terminal names: " + err.Error())
	}
}

// restoreTerminals reopens the project's saved terminals (or one default
// terminal), leaving the keyboard focus on the editor or explorer rather
// than in a terminal, where plain Ctrl shortcuts would go to the shell.
func (a *App) restoreTerminals() {
	var names []string
	if data, err := os.ReadFile(a.terminalsFile()); err == nil {
		json.Unmarshal(data, &names)
	}
	if len(names) == 0 {
		a.newTerminal()
	}
	for _, n := range names {
		a.newNamedTerminal(n)
	}
	a.termNB.Current = 0
	a.focusWorkspace()
}

// focusWorkspace focuses the current editor, or the explorer if no file is open.
func (a *App) focusWorkspace() {
	if e := a.editors.Current(); e != nil {
		a.focusTag(e.View)
	} else {
		a.focusTag(a.tree)
	}
}

func (a *App) currentTerminal() *Terminal {
	if len(a.terminals) == 0 {
		return nil
	}
	return a.terminals[max(0, min(a.termNB.Current, len(a.terminals)-1))]
}

func (a *App) focusedTerminal() *Terminal {
	for _, t := range a.terminals {
		if t.HasFocus() {
			return t
		}
	}
	return nil
}

func (a *App) runInTerminal(cmd string) {
	a.termOn = true
	t := a.currentTerminal()
	if t == nil {
		t = a.newTerminal()
	}
	t.RunCommand(cmd)
	t.Focus()
}

// ---- Symbol navigation (Ctrl+Click) ----

// openBuffers returns the current text of open editors, keyed by path.
func (a *App) openBuffers() map[string]string {
	m := map[string]string{}
	for _, e := range a.editors.editors {
		m[e.Path] = e.Text()
	}
	return m
}

func (a *App) symbolAction(e *Editor, line, colByte int) {
	src := e.Text()
	lang := e.lang
	var name string
	if lang == nil {
		if name = wordAt(e); name == "" {
			return
		}
	}
	a.setStatusMsg("Looking up symbol…")
	open := a.openBuffers()
	root := a.root
	ext := filepath.Ext(e.Path)
	// Parsing (and a grammar's first query compilation) happens off the UI thread.
	go func() {
		clickedDef := false
		if lang != nil {
			sym, ok := lang.SymbolAt([]byte(src), line, colByte)
			if !ok {
				a.later(func() { a.setStatusMsg("No symbol under cursor") })
				return
			}
			if sym.Local != nil {
				// Scoped to one embedded region (e.g. a regex named group).
				for i := range sym.Local {
					sym.Local[i].Path = e.Path
				}
				a.later(func() { a.showSymbolResults(e, line, sym.Name, sym.IsDef, sym.Local) })
				return
			}
			name, clickedDef = sym.Name, sym.IsDef
		}
		var refs []SymbolRef
		walkProject(root, func(p string) {
			// Search every file of the same language family (e.g. .js/.ts/.tsx,
			// .c/.h/.cpp), parsing each with its own grammar.
			pl := languageFor(p)
			if lang != nil && (pl == nil || pl.Family != lang.Family) || lang == nil && filepath.Ext(p) != ext {
				return
			}
			var data []byte
			if txt, ok := open[p]; ok {
				data = []byte(txt)
			} else if data = readTextFile(p); data == nil {
				return
			}
			if pl != nil {
				refs = append(refs, pl.FindRefs(p, data, name)...)
			} else {
				refs = append(refs, findWordRefs(p, data, name)...)
			}
		})
		a.later(func() { a.showSymbolResults(e, line, name, clickedDef, refs) })
	}()
}

func (a *App) showSymbolResults(from *Editor, line int, name string, clickedDef bool, refs []SymbolRef) {
	var defs []SymbolRef
	for _, r := range refs {
		if r.IsDef {
			defs = append(defs, r)
		}
	}
	if !clickedDef && len(defs) > 0 {
		if target, ok := pickDefinition(from.Path, line, defs); ok {
			a.updateStatus()
			if ed := a.editors.Open(target.Path); ed != nil {
				ed.GotoLine(target.Line, target.ColByte)
			}
			a.setStatusMsg(fmt.Sprintf("Definition of “%s” — %s:%d", name, relPath(a.root, target.Path), target.Line+1))
			return
		}
	}
	// No single definition to jump to: list definitions (first) and usages.
	a.showRefs(fmt.Sprintf("%s, %s of “%s”", pluralize(len(defs), "definition", "definitions"),
		pluralize(len(refs)-len(defs), "usage", "usages"), name), refs, name)
}

// pickDefinition chooses the most likely definition: the nearest preceding
// one in the same file, any in the same file, or the only one elsewhere.
func pickDefinition(path string, line int, defs []SymbolRef) (SymbolRef, bool) {
	var same []SymbolRef
	for _, d := range defs {
		if d.Path == path {
			same = append(same, d)
		}
	}
	if len(same) > 0 {
		sort.Slice(same, func(i, j int) bool { return same[i].Line < same[j].Line })
		best := same[0]
		for _, d := range same {
			if d.Line <= line {
				best = d
			}
		}
		return best, true
	}
	if len(defs) == 1 {
		return defs[0], true
	}
	// All candidates in one file (e.g. a Haskell signature plus its
	// equations, or overloads): jump to the first.
	oneFile := true
	for _, d := range defs {
		oneFile = oneFile && d.Path == defs[0].Path
	}
	if oneFile {
		first := defs[0]
		for _, d := range defs {
			if d.Line < first.Line {
				first = d
			}
		}
		return first, true
	}
	// Prefer definitions in the same directory (e.g. the same Go package).
	var dir []SymbolRef
	for _, d := range defs {
		if filepath.Dir(d.Path) == filepath.Dir(path) {
			dir = append(dir, d)
		}
	}
	if len(dir) == 1 {
		return dir[0], true
	}
	return SymbolRef{}, false
}

func (a *App) showRefs(title string, refs []SymbolRef, name string) {
	ms := make([]match, len(refs))
	for i, r := range refs {
		ms[i] = match{path: r.Path, line: r.Line, colByte: r.ColByte, text: r.LineText, isDef: r.IsDef}
	}
	a.search.gen.Add(1) // cancel any running text search
	a.search.show(title, ms, name, true)
	a.search.present()
	a.setStatusMsg(title)
}

// ---- Closing ----

// confirmQuit offers to save each unsaved file, then calls then unless the
// user cancelled.
func (a *App) confirmQuit(then func()) {
	un := a.editors.Unsaved()
	if len(un) == 0 {
		then()
		return
	}
	e := un[0]
	a.editors.setCurrent(a.editors.indexOf(e))
	a.askSave(filepath.Base(e.Path), func(resp int) {
		switch resp {
		case RespYes:
			if !e.Save() {
				return
			}
		case RespNo:
			// Forget the changes so the next round moves on.
			e.View.SetModified(false)
		default:
			return
		}
		a.confirmQuit(then)
	})
}

// onClose handles the window's close button. With other windows open it asks
// whether to close just this one or all of them. It calls close if this
// window should close.
func (a *App) onClose(close func()) {
	if len(apps) < 2 {
		a.confirmQuit(close)
		return
	}
	d := &Dialog{Message: "Close all windows or just this one?",
		Secondary: fmt.Sprintf("%d EdMin windows are open.", len(apps)), Default: RespClose, Width: 460}
	d.addButtons("Cancel", RespCancel, "Close All Windows", RespAccept, "Close This Window", RespClose)
	d.OnResponse = func(r int) {
		switch r {
		case RespClose:
			a.confirmQuit(close)
		case RespAccept:
			a.closeAll(close)
		}
	}
	a.showDialog(d)
}

// closeAll closes every window, first offering to save each one's unsaved
// files; cancelling any of those leaves all windows open.
func (a *App) closeAll(close func()) {
	list := append([]*App(nil), apps...)
	var next func(i int)
	next = func(i int) {
		if i == len(list) {
			// Record every project now, so they all reopen next time.
			saveSession()
			quitting = true
			for _, x := range list {
				if x != a {
					x.closeOK = true
					x.win.Perform(system.ActionClose)
				}
			}
			close()
			return
		}
		x := list[i]
		if len(x.editors.Unsaved()) > 0 {
			x.win.Perform(system.ActionRaise)
		}
		x.confirmQuit(func() { next(i + 1) })
	}
	next(0)
}

// openFolderInNewWindow opens a chosen folder as a project in a new window,
// or raises the window that already has it open.
func (a *App) openFolderInNewWindow() {
	a.chooseFolder("Open Folder in New Window", func(dir string) {
		for _, x := range apps {
			if x.root == dir {
				x.win.Perform(system.ActionRaise)
				return
			}
		}
		newApp(dir)
	})
}

func (a *App) openFolder() {
	a.chooseFolder("Open Folder", func(dir string) {
		if dir == a.root {
			return
		}
		a.confirmQuit(func() {
			for len(a.editors.editors) > 0 {
				e := a.editors.editors[0]
				e.View.SetModified(false)
				a.editors.Close(e)
			}
			// The old project's terminals close with it; its saved names stay.
			for len(a.terminals) > 0 {
				a.dropTerminal(a.terminals[0])
			}
			a.termCount = 0
			a.setRoot(dir)
			a.restoreTerminals()
		})
	})
}

// ---- Keyboard shortcuts ----

// panelKey handles Ctrl+1-4, which open the explorer, terminal, build panel
// and settings (focusing the panel if it is already open), and with Shift
// held close them. Ctrl+5 focuses the file editor.
func (a *App) panelKey(n int, close bool) {
	if n == 5 && close {
		return
	}
	if d := a.settingsDlg; d != nil {
		if n == 4 && !close {
			return
		}
		// The modal Settings window is in the way of every panel.
		d.respond(RespClose)
		if n == 4 {
			return
		}
		a.later(func() { a.panelKey(n, close) })
		return
	}
	on := map[int]*bool{1: &a.leftOn, 2: &a.termOn, 3: &a.buildOn}[n]
	if close {
		if on != nil && *on {
			*on = false
			if e := a.editors.Current(); e != nil {
				a.focusTag(e.View)
			}
		}
		return
	}
	if on != nil {
		*on = true
	}
	switch n {
	case 1:
		a.focusTag(a.tree)
	case 2:
		if t := a.currentTerminal(); t != nil {
			t.Focus()
		} else {
			a.newTerminal()
		}
	case 3:
		a.focusTag(a.build)
	case 4:
		a.showSettings()
	case 5:
		if e := a.editors.Current(); e != nil {
			a.focusTag(e.View)
		}
	}
}

// resizeStep is how far one Alt+Shift+Arrow press moves a divider, in dp.
const resizeStep = 20

// focus regions, for resizing and tab cycling.
func (a *App) focusInTree() bool  { return a.tree.focused }
func (a *App) focusInBuild() bool { return a.build.focused }
func (a *App) focusInTerms() bool {
	if a.focusedTerminal() != nil {
		return true
	}
	for _, r := range a.termLabels {
		if r.editing && r.entry != nil && r.entry.focused {
			return true
		}
	}
	return false
}
func (a *App) focusInEditors() bool {
	for _, e := range a.editors.editors {
		if e.View.focused {
			return true
		}
	}
	return a.editors.findEnt.focused
}

// resizePane handles Alt+Shift+Arrow, which moves a divider of the focused pane
// in the arrow's direction, as tmux's resize-pane does: Left/Right move the
// pane's right edge if another panel is open to its right, else its left
// edge; Up/Down likewise move the bottom edge, else the top.
func (a *App) resizePane(name key.Name) bool {
	var dx, dy float32
	switch name {
	case key.NameLeftArrow:
		dx = -1
	case key.NameRightArrow:
		dx = 1
	case key.NameUpArrow:
		dy = -1
	case key.NameDownArrow:
		dy = 1
	default:
		return false
	}
	step := float32(resizeStep)
	switch {
	case a.focusInTree():
		if dx != 0 {
			a.leftW += dx * step
		}
	case a.focusInBuild():
		if dx != 0 {
			a.rightW -= dx * step
		}
	case a.focusInEditors(), a.focusInTerms():
		switch {
		case dy != 0 && a.termOn:
			a.termH -= dy * step
		case dx != 0 && a.buildOn:
			a.rightW -= dx * step
		case dx != 0 && a.leftOn:
			a.leftW += dx * step
		default:
			return false
		}
	default:
		return false
	}
	a.invalidate()
	return true
}

// closeCurrentTab handles Ctrl+Shift+W: it closes the current terminal tab if
// the terminal panel has focus, otherwise the current file tab.
func (a *App) closeCurrentTab() {
	if a.focusInTerms() {
		if t := a.currentTerminal(); t != nil {
			a.closeTerminal(t)
			if t := a.currentTerminal(); t != nil {
				t.Focus()
			} else if e := a.editors.Current(); e != nil {
				a.focusTag(e.View)
			}
		}
		return
	}
	if e := a.editors.Current(); e != nil {
		a.editors.Close(e, func() {
			if e := a.editors.Current(); e != nil {
				a.focusTag(e.View)
			}
		})
	}
}

// cycleTab handles Ctrl+Tab and Ctrl+Shift+Tab: it moves delta tabs through
// the terminal tabs if the terminal panel has focus, otherwise through the
// file tabs, wrapping at either end.
func (a *App) cycleTab(delta int) {
	if a.focusInTerms() {
		n := len(a.terminals)
		if n == 0 {
			return
		}
		a.termNB.Current = ((a.termNB.Current+delta)%n + n) % n
		if t := a.currentTerminal(); t != nil {
			t.Focus()
		}
		return
	}
	n := len(a.editors.editors)
	if n == 0 {
		return
	}
	a.editors.setCurrent(((a.editors.nb.Current+delta)%n + n) % n)
	if e := a.editors.Current(); e != nil {
		a.focusTag(e.View)
	}
}

// shortcutFilters are the keys the window handles itself, given whether a
// terminal has the focus (it then gets plain Ctrl combinations).
func (a *App) shortcutFilters(inTerm bool) []event.Filter {
	sc := key.ModShortcut
	var fs []event.Filter
	add := func(req, opt key.Modifiers, names ...key.Name) {
		for _, n := range names {
			fs = append(fs, key.Filter{Name: n, Required: req, Optional: opt})
		}
	}
	add(key.ModAlt|key.ModShift, 0, key.NameLeftArrow, key.NameRightArrow, key.NameUpArrow, key.NameDownArrow)
	add(sc, key.ModShift, "1", "2", "3", "4", "5", "!", "@", "#", "$", key.NameTab)
	add(sc|key.ModShift, 0, "R", "T", "F", "E", "O", "W", "X")
	if !inTerm {
		add(sc, key.ModShift, "S", "W", "F", "N", "O", "G")
		if a.focusInEditors() && !a.editors.findEnt.focused {
			add(sc, key.ModShift, "Z")
			add(sc, 0, "Y")
		}
	}
	return fs
}

func (a *App) onShortcut(e key.Event) {
	mods := shortcutMods(e.Modifiers)
	ctrl := mods == key.ModCtrl
	ctrlShift := mods == key.ModCtrl|key.ModShift
	inTerm := a.focusedTerminal() != nil
	name := e.Name

	// Shortcuts that work everywhere, including inside a terminal.
	switch {
	case mods == key.ModShift|key.ModAlt:
		a.resizePane(name)
		return
	case (ctrl || ctrlShift) && panelDigit(name) != 0:
		a.panelKey(panelDigit(name), ctrlShift)
		return
	case ctrlShift && name == "R" && inTerm:
		// Rename the focused terminal's tab, then return to the terminal.
		t := a.focusedTerminal()
		if r := a.termLabels[t]; r != nil {
			r.edit(a, t.Focus)
		}
		return
	case ctrlShift && name == "T" && a.focusInBuild():
		a.build.edit(-1)
		return
	case ctrlShift && name == "T":
		a.newTerminal()
		return
	case ctrlShift && name == "F":
		sel := ""
		if e := a.editors.Current(); e != nil {
			if s, en, ok := e.View.Selection(); ok && s.Line == en.Line {
				sel = e.View.SelectedText()
			}
		}
		a.search.Focus(sel)
		return
	case ctrlShift && name == "E":
		a.tree.RevealCurrent()
		return
	case ctrlShift && name == "O":
		a.openFolderInNewWindow()
		return
	case ctrlShift && name == "W":
		a.closeCurrentTab()
		return
	case (ctrl || ctrlShift) && name == key.NameTab:
		if ctrlShift {
			a.cycleTab(-1)
		} else {
			a.cycleTab(1)
		}
		return
	case ctrlShift && name == "X":
		// Goes through the closing check, like the title bar's close button.
		a.win.Perform(system.ActionClose)
		return
	}
	if inTerm || !ctrl && !ctrlShift {
		return
	}

	ed := a.editors.Current()
	editorFocused := ed != nil && ed.View.focused
	switch name {
	case "S":
		if ctrl && ed != nil {
			ed.Save()
		}
	case "W":
		if ctrl && ed != nil {
			a.editors.Close(ed)
		}
	case "F":
		if ctrl && ed != nil {
			a.editors.ShowFind()
		}
	case "N":
		a.showExplorer()
		a.tree.create(ctrlShift)
	case "O":
		if ctrl {
			a.openFolder()
		}
	case "Z":
		if editorFocused {
			if ctrl {
				ed.Undo()
			} else {
				ed.Redo()
			}
		}
	case "Y":
		if ctrl && editorFocused {
			ed.Redo()
		}
	case "G":
		if ctrl && ed != nil {
			a.prompt("Go to Line", "Line number:", "", func(s string) {
				var n int
				if _, err := fmt.Sscan(s, &n); err == nil && n > 0 {
					ed.GotoLine(n-1, 0)
				}
			})
		}
	}
}

// ---- Layout ----

func (a *App) layout(gtx layout.Context) {
	pal := a.theme.palette()
	curTip = &a.tip
	currentPalette = pal
	size := gtx.Constraints.Max

	// The root area tracks the pointer for tooltips and menus.
	root := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &a.mouse)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &a.mouse, Kinds: pointer.Move | pointer.Press | pointer.Drag})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok {
			a.mouse = e.Position.Round()
			a.tip.mouse = a.mouse
		}
	}

	// Window shortcuts, ahead of the focused widget.
	if len(a.dialogs) == 0 {
		fs := a.shortcutFilters(a.focusedTerminal() != nil)
		for {
			ev, ok := gtx.Event(fs...)
			if !ok {
				break
			}
			if e, ok := ev.(key.Event); ok && e.State == key.Press {
				a.onShortcut(e)
			}
		}
	}

	fill(gtx, pal.Panel)
	hb := a.deco.headerBar(gtx, a.win, pal, a.title, a.root, a.headerLeft, a.headerRight,
		system.ActionMinimize|system.ActionMaximize|system.ActionClose)

	// Status bar.
	statusH := lineHeight(gtx, uiFont, uiSize) + gtx.Dp(4)
	func() {
		defer op.Offset(image.Pt(0, size.Y-statusH)).Push(gtx.Ops).Pop()
		fillRect(gtx, image.Rect(0, 0, size.X, statusH), pal.Panel)
		defer op.Offset(image.Pt(gtx.Dp(8), gtx.Dp(2))).Push(gtx.Ops).Pop()
		g := gtx
		g.Constraints.Min = image.Point{}
		Label{Text: a.status, Color: pal.FG}.Layout(g)
	}()

	// Panes: [left | [[editor / terminal] | right]], 1dp dividers.
	body := image.Rect(0, hb, size.X, size.Y-statusH)
	a.layoutPanes(gtx, body, pal)

	root.Pop()

	// Overlays.
	if a.menu != nil {
		a.menu.layout(gtx, a)
	}
	for _, d := range append([]*Dialog(nil), a.dialogs...) {
		d.layout(gtx)
	}
	layoutTooltip(gtx, &a.tip)
	if a.pendingFocus != nil {
		gtx.Execute(key.FocusCmd{Tag: a.pendingFocus})
		a.pendingFocus = nil
	}
}

// hbButton is the size of a header bar button, in dp.
var hbButton = image.Pt(36, 34)

func (a *App) headerLeft(gtx layout.Context) layout.Dimensions {
	pal := a.theme.palette()
	a.openBtn.Tip = "Open folder (Ctrl+O)"
	a.newWinBtn.Tip = "Open folder in new window (Ctrl+Shift+O)"
	if a.openBtn.Clicked(gtx) {
		defer a.openFolder()
	}
	if a.newWinBtn.Clicked(gtx) {
		defer a.openFolderInNewWindow()
	}
	specs := a.tree.toolbarSpecs()
	items := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return a.openBtn.Layout(gtx, buttonStyle{Icon: "folder-open-symbolic", Pal: pal, Size: hbButton})
		}),
		layout.Rigid(spacer(6, 0)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return a.newWinBtn.Layout(gtx, buttonStyle{Icon: "window-new-symbolic", Pal: pal, Size: hbButton})
		}),
		layout.Rigid(spacer(6, 0)),
	}
	// The explorer's buttons, linked into one group.
	for i, s := range specs {
		items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := &a.tree.toolbar[i]
			b.Tip = s.tip
			if b.Clicked(gtx) {
				defer s.fn()
			}
			c := corners(0)
			if i == 0 {
				c |= roundLeft
			}
			if i == len(specs)-1 {
				c |= roundRight
			}
			if c == 0 {
				c = 1 << 7 // square
			}
			d := b.Layout(gtx, buttonStyle{Icon: s.icon, Pal: pal, Corners: c, Size: hbButton})
			if i < len(specs)-1 {
				d.Size.X -= gtx.Dp(1) // share the border with the next button
			}
			return d
		}))
	}
	return layout.Flex{}.Layout(gtx, items...)
}

func (a *App) headerRight(gtx layout.Context) layout.Dimensions {
	pal := a.theme.palette()
	type tog struct {
		b    *Button
		on   *bool
		icon string
		tip  string
	}
	togs := []tog{
		{&a.leftBtn, &a.leftOn, "view-list-symbolic", "Explorer (Ctrl+1, close Ctrl+Shift+1)"},
		{&a.termBtn, &a.termOn, "utilities-terminal-symbolic", "Terminal (Ctrl+2, close Ctrl+Shift+2)"},
		{&a.buildBtn, &a.buildOn, "system-run-symbolic", "Build panel (Ctrl+3, close Ctrl+Shift+3)"},
	}
	var items []layout.FlexChild
	for i, t := range togs {
		if i > 0 {
			items = append(items, layout.Rigid(spacer(6, 0)))
		}
		items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			t.b.Tip = t.tip
			if t.b.Clicked(gtx) {
				*t.on = !*t.on
				if t.on == &a.termOn {
					if a.termOn && len(a.terminals) == 0 {
						a.newTerminal()
					}
					if a.termOn {
						if term := a.currentTerminal(); term != nil {
							term.Focus()
						}
					}
				}
			}
			return t.b.Layout(gtx, buttonStyle{Icon: t.icon, Pal: pal, Checked: *t.on, Size: hbButton})
		}))
	}
	items = append(items, layout.Rigid(spacer(6, 0)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		a.settingsBtn.Tip = "Settings (Ctrl+4, close Ctrl+Shift+4)"
		if a.settingsBtn.Clicked(gtx) {
			defer a.showSettings()
		}
		return a.settingsBtn.Layout(gtx, buttonStyle{Icon: "open-menu-symbolic", Pal: pal, Size: hbButton})
	}))
	return layout.Flex{}.Layout(gtx, items...)
}

// layoutPanes lays out the explorer, editor, terminal and build panels in
// body, with draggable dividers between them.
func (a *App) layoutPanes(gtx layout.Context, body image.Rectangle, pal palette) {
	sep := gtx.Dp(1)
	minW := gtx.Dp(60)
	x0, x1 := body.Min.X, body.Max.X
	// Clamp the sizes to what fits, as GTK's panes do.
	a.leftW = max(a.leftW, 40)
	a.rightW = max(a.rightW, 40)
	a.termH = max(a.termH, 40)
	leftW, rightW := gtx.Dp(unit.Dp(a.leftW)), gtx.Dp(unit.Dp(a.rightW))
	avail := body.Dx()
	if a.leftOn && a.buildOn {
		leftW = min(leftW, avail-rightW-2*sep-minW)
		rightW = min(rightW, avail-leftW-2*sep-minW)
	} else if a.leftOn {
		leftW = min(leftW, avail-sep-minW)
	} else if a.buildOn {
		rightW = min(rightW, avail-sep-minW)
	}
	type div struct {
		r   image.Rectangle
		id  int
		dir int // 1 horizontal drag, 2 vertical
	}
	var divs []div
	if a.leftOn {
		r := image.Rect(x0, body.Min.Y, x0+leftW, body.Max.Y)
		a.layoutIn(gtx, r, func(gtx layout.Context) { a.tree.Layout(gtx, pal) })
		fillRect(gtx, image.Rect(r.Max.X, body.Min.Y, r.Max.X+sep, body.Max.Y), pal.Panel)
		divs = append(divs, div{image.Rect(r.Max.X-gtx.Dp(3), body.Min.Y, r.Max.X+sep+gtx.Dp(3), body.Max.Y), 1, 1})
		x0 = r.Max.X + sep
	}
	if a.buildOn {
		r := image.Rect(x1-rightW, body.Min.Y, x1, body.Max.Y)
		a.layoutIn(gtx, r, func(gtx layout.Context) { a.build.Layout(gtx, pal) })
		fillRect(gtx, image.Rect(r.Min.X-sep, body.Min.Y, r.Min.X, body.Max.Y), pal.Panel)
		divs = append(divs, div{image.Rect(r.Min.X-sep-gtx.Dp(3), body.Min.Y, r.Min.X+gtx.Dp(3), body.Max.Y), 2, 1})
		x1 = r.Min.X - sep
	}
	center := image.Rect(x0, body.Min.Y, x1, body.Max.Y)
	termH := gtx.Dp(unit.Dp(a.termH))
	termH = min(termH, center.Dy()-sep-gtx.Dp(80))
	edBottom := center.Max.Y
	if a.termOn {
		edBottom = center.Max.Y - termH - sep
		r := image.Rect(center.Min.X, edBottom+sep, center.Max.X, center.Max.Y)
		a.layoutIn(gtx, r, func(gtx layout.Context) { a.layoutTerminals(gtx, pal) })
		fillRect(gtx, image.Rect(center.Min.X, edBottom, center.Max.X, edBottom+sep), pal.Panel)
		divs = append(divs, div{image.Rect(center.Min.X, edBottom-gtx.Dp(3), center.Max.X, edBottom+sep+gtx.Dp(3)), 3, 2})
	}
	a.layoutIn(gtx, image.Rect(center.Min.X, center.Min.Y, center.Max.X, edBottom), func(gtx layout.Context) { a.editors.Layout(gtx) })

	// Divider drags.
	for _, d := range divs {
		tag := &a.divTags[d.id-1]
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
			if !ok {
				break
			}
			e, ok := ev.(pointer.Event)
			if !ok {
				continue
			}
			switch e.Kind {
			case pointer.Press:
				a.dragging = d.id
				a.dragStart = e.Position.Round().Add(d.r.Min)
				a.dragFrom = map[int]float32{1: a.leftW, 2: a.rightW, 3: a.termH}[d.id]
			case pointer.Drag:
				if a.dragging != d.id {
					break
				}
				p := e.Position.Round().Add(d.r.Min)
				dx := float32(p.X-a.dragStart.X) / gtx.Metric.PxPerDp
				dy := float32(p.Y-a.dragStart.Y) / gtx.Metric.PxPerDp
				switch d.id {
				case 1:
					a.leftW = a.dragFrom + dx
				case 2:
					a.rightW = a.dragFrom - dx
				case 3:
					a.termH = a.dragFrom - dy
				}
			case pointer.Release, pointer.Cancel:
				a.dragging = 0
			}
		}
		func() {
			defer op.Offset(d.r.Min).Push(gtx.Ops).Pop()
			defer clip.Rect{Max: d.r.Size()}.Push(gtx.Ops).Pop()
			event.Op(gtx.Ops, tag)
			if d.dir == 1 {
				pointer.CursorColResize.Add(gtx.Ops)
			} else {
				pointer.CursorRowResize.Add(gtx.Ops)
			}
		}()
	}
}

// layoutIn lays out w with constraints exactly r, at r's position.
func (a *App) layoutIn(gtx layout.Context, r image.Rectangle, w func(gtx layout.Context)) {
	if r.Dx() <= 0 || r.Dy() <= 0 {
		return
	}
	defer op.Offset(r.Min).Push(gtx.Ops).Pop()
	defer clip.Rect{Max: r.Size()}.Push(gtx.Ops).Pop()
	g := gtx
	g.Constraints = layout.Exact(r.Size())
	w(g)
}

func (a *App) layoutTerminals(gtx layout.Context, pal palette) {
	size := gtx.Constraints.Max
	fill(gtx, pal.Panel)
	if len(a.terminals) == 0 {
		notebookOutline(gtx, image.Rectangle{Max: size})
		return
	}
	shell := a.shell
	if len(shell) == 0 {
		shell = defaultShell()
	}
	tip := "Runs: " + strings.Join(shell, " ") + "\nDouble-click the name (or Ctrl+Shift+R) to rename"
	inner := image.Rect(gtx.Dp(1), gtx.Dp(1), size.X-gtx.Dp(1), size.Y-gtx.Dp(1))
	a.layoutIn(gtx, inner, func(gtx layout.Context) {
		sd := a.termNB.LayoutStrip(gtx, notebookStyle{
			Pal: pal,
			Label: func(gtx layout.Context, i int, active bool) layout.Dimensions {
				return a.termLabels[a.terminals[i]].layout(gtx, pal)
			},
			Tip:         func(int) string { return tip },
			Closed:      func(i int) { a.closeTerminal(a.terminals[i]) },
			DoubleClick: func(i int) { a.termLabels[a.terminals[i]].edit(a, nil) },
			Action: func(gtx layout.Context) layout.Dimensions {
				a.newTermBtn.Tip = "New terminal (Ctrl+Shift+T)"
				if a.newTermBtn.Clicked(gtx) {
					defer a.newTerminal()
				}
				return a.newTermBtn.Layout(gtx, buttonStyle{Icon: "list-add-symbolic", Pal: pal})
			},
		})
		// Every terminal stays laid out so they keep running at their size;
		// only the current one is drawn.
		t := a.currentTerminal()
		a.layoutIn(gtx, image.Rect(0, sd.Size.Y, gtx.Constraints.Max.X, gtx.Constraints.Max.Y), func(gtx layout.Context) {
			t.Layout(gtx)
		})
	})
	notebookOutline(gtx, image.Rectangle{Max: size})
}

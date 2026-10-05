package main

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// harness drives a window's layout without a display, feeding it input
// through Gio's router.
type harness struct {
	t      *testing.T
	a      *App
	router input.Router
	ops    op.Ops
	size   image.Point

	ime, lastSel key.Range // the window's view of the editor selection
}

func newHarness(t *testing.T, files map[string]string) *harness {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	for name, data := range files {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// No shells: the tests don't need them.
	os.MkdirAll(filepath.Join(root, ".edmin"), 0755)
	os.WriteFile(filepath.Join(root, ".edmin", "settings.json"), []byte(`{"shell":["/bin/cat"]}`), 0644)
	uiMu.Lock()
	a := newAppState(root)
	uiMu.Unlock()
	h := &harness{t: t, a: a, size: image.Pt(1300, 800)}
	t.Cleanup(func() {
		for _, x := range a.terminals {
			x.Close()
		}
		for i, x := range apps {
			if x == a {
				apps = append(apps[:i], apps[i+1:]...)
			}
		}
	})
	h.frames(2)
	return h
}

func (h *harness) frame() {
	uiMu.Lock()
	defer uiMu.Unlock()
	h.a.drainQueue()
	h.ops.Reset()
	gtx := layout.Context{
		Ops:         &h.ops,
		Now:         time.Now(),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(h.size),
		Source:      h.router.Source(),
	}
	h.a.layout(gtx)
	h.router.Frame(&h.ops)
}

func (h *harness) frames(n int) {
	for i := 0; i < n; i++ {
		h.frame()
	}
}

// send queues events and lays out frames so they are handled.
func (h *harness) send(evs ...event.Event) {
	for _, e := range evs {
		h.router.Queue(e)
		h.frames(2)
	}
}

func press(name key.Name, mods key.Modifiers) key.Event {
	return key.Event{Name: name, Modifiers: mods, State: key.Press}
}

func (h *harness) typeText(s string) {
	for _, r := range s {
		// As the window does: replace its idea of the focused editor's
		// selection, then move the selection past the new text.
		if st := h.router.EditorState().Selection.Range; st != h.lastSel {
			h.ime, h.lastSel = st, st
		}
		h.send(key.EditEvent{Range: h.ime, Text: string(r)})
		start := min(h.ime.Start, h.ime.End) + 1
		h.ime = key.Range{Start: start, End: start}
		h.send(key.SelectionEvent(h.ime))
	}
}

func (h *harness) click(p image.Point, mods key.Modifiers, count int) {
	for i := 0; i < count; i++ {
		pos := f32.Pt(float32(p.X), float32(p.Y))
		h.router.Queue(
			pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: pos},
			pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pos, Modifiers: mods},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: pos, Modifiers: mods},
		)
		h.frames(2)
	}
}

func TestEditingAndSave(t *testing.T) {
	h := newHarness(t, map[string]string{"a.go": "package main\n\nfunc main() {\n}\n"})
	a := h.a
	uiMu.Lock()
	e := a.editors.Open(filepath.Join(a.root, "a.go"))
	uiMu.Unlock()
	h.frames(3)
	if !e.View.focused {
		t.Fatal("new editor not focused")
	}
	// Move to the end of line 3 and type with auto-indent.
	h.send(press(key.NameDownArrow, 0), press(key.NameDownArrow, 0), press(key.NameEnd, 0))
	h.send(press(key.NameReturn, 0))
	h.send(press(key.NameTab, 0))
	h.typeText("x := 1")
	h.send(press(key.NameReturn, 0))
	h.typeText("y")
	want := "package main\n\nfunc main() {\n\tx := 1\n\ty\n}\n"
	if got := e.Text(); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	if !e.View.Modified() || !strings.HasPrefix(a.title, "● ") {
		t.Fatalf("not marked modified: title %q", a.title)
	}
	// Undo removes "y", then the newline and indent.
	h.send(press("Z", key.ModShortcut), press("Z", key.ModShortcut))
	if got := e.Text(); got != "package main\n\nfunc main() {\n\tx := 1\n}\n" {
		t.Fatalf("after undo: %q", got)
	}
	h.send(press("Z", key.ModShortcut|key.ModShift), press("Z", key.ModShortcut|key.ModShift))
	if got := e.Text(); got != want {
		t.Fatalf("after redo: %q", got)
	}
	h.send(press("S", key.ModShortcut))
	data, _ := os.ReadFile(e.Path)
	if string(data) != want || e.View.Modified() {
		t.Fatalf("save failed: %q modified=%v", data, e.View.Modified())
	}
	if !strings.Contains(a.status, "Ln 5, Col 3") || !strings.Contains(a.status, "Go") {
		t.Fatalf("status = %q", a.status)
	}
}

func TestFindBar(t *testing.T) {
	h := newHarness(t, map[string]string{"a.txt": "one foo\nfoo two\nFOO three\n"})
	a := h.a
	uiMu.Lock()
	e := a.editors.Open(filepath.Join(a.root, "a.txt"))
	uiMu.Unlock()
	h.frames(2)
	h.send(press("F", key.ModShortcut))
	if !a.editors.findOpen || !a.editors.findEnt.focused {
		t.Fatal("find bar not open and focused")
	}
	h.typeText("foo")
	if a.editors.findInfo != "3 matches" {
		t.Fatalf("info = %q", a.editors.findInfo)
	}
	if s, en, _ := e.View.Selection(); s != (Pos{0, 4}) || en != (Pos{0, 7}) {
		t.Fatalf("selection %v-%v", s, en)
	}
	h.send(press(key.NameReturn, 0))
	if s, _, _ := e.View.Selection(); s != (Pos{1, 0}) {
		t.Fatalf("next match at %v", s)
	}
	h.send(press(key.NameReturn, key.ModShift))
	if s, _, _ := e.View.Selection(); s != (Pos{0, 4}) {
		t.Fatalf("previous match at %v", s)
	}
	h.send(press(key.NameEscape, 0))
	h.frames(2)
	if a.editors.findOpen || !e.View.focused {
		t.Fatal("Escape should close the bar and refocus the editor")
	}
}

func TestPanelsAndDialogs(t *testing.T) {
	h := newHarness(t, map[string]string{"a.txt": "hello\n", "dir/b.txt": "b\n"})
	a := h.a
	// Ctrl+1 focuses the explorer; arrows and Enter open a file.
	h.send(press("1", key.ModShortcut))
	if !a.tree.focused {
		t.Fatal("Ctrl+1 did not focus the tree")
	}
	h.send(press(key.NameRightArrow, 0)) // expand "dir"
	if len(a.tree.rows) != 3 {
		t.Fatalf("rows after expand = %d", len(a.tree.rows))
	}
	h.send(press(key.NameDownArrow, 0), press(key.NameReturn, 0))
	if e := a.editors.Current(); e == nil || filepath.Base(e.Path) != "b.txt" {
		t.Fatal("Enter did not open dir/b.txt")
	}
	// Ctrl+Shift+1 closes the explorer; Ctrl+3 / Ctrl+Shift+3 the build panel.
	h.send(press("!", key.ModShortcut|key.ModShift))
	if a.leftOn {
		t.Fatal("explorer still open")
	}
	h.send(press("#", key.ModShortcut|key.ModShift))
	if a.buildOn {
		t.Fatal("build panel still open")
	}
	h.send(press("3", key.ModShortcut))
	if !a.buildOn || !a.build.focused {
		t.Fatal("Ctrl+3 did not open and focus the build panel")
	}
	// Ctrl+Shift+T in the build panel adds a command through a dialog.
	h.send(press("T", key.ModShortcut|key.ModShift))
	if len(a.dialogs) != 1 {
		t.Fatal("no add-command dialog")
	}
	h.typeText("Echo")
	// Tab moves the focus, as the window does when no widget takes it.
	h.router.MoveFocus(key.FocusForward)
	h.frames(2)
	h.typeText("echo hi")
	h.send(press(key.NameReturn, 0))
	if len(a.dialogs) != 0 || len(a.build.cmds) != 1 || a.build.cmds[0].Command != "echo hi" || a.build.cmds[0].Name != "Echo" {
		t.Fatalf("commands = %+v dialogs=%d", a.build.cmds, len(a.dialogs))
	}
	// Ctrl+4 opens Settings; picking a theme applies and saves it.
	h.send(press("4", key.ModShortcut))
	if a.settingsDlg == nil {
		t.Fatal("no settings")
	}
	uiMu.Lock()
	a.chooseTheme(themeByName("Tan"))
	uiMu.Unlock()
	h.send(press("$", key.ModShortcut|key.ModShift))
	if a.settingsDlg != nil || a.theme.Name != "Tan" {
		t.Fatal("settings not closed or theme not applied")
	}
	if s := loadSettings(projectSettingsPath(a.root)); s.Theme != "Tan" {
		t.Fatalf("theme not saved: %+v", s)
	}
	// Closing a modified tab asks first.
	e := a.editors.Current()
	h.send(press("5", key.ModShortcut))
	h.typeText("z")
	h.send(press("W", key.ModShortcut))
	if len(a.dialogs) != 1 || a.dialogs[0].Message == "" {
		t.Fatal("no save question")
	}
	h.send(press(key.NameEscape, 0))
	if a.editors.indexOf(e) < 0 {
		t.Fatal("cancel closed the tab")
	}
	h.send(press("W", key.ModShortcut))
	uiMu.Lock()
	a.dialogs[0].respond(RespNo)
	uiMu.Unlock()
	h.frames(2)
	if a.editors.indexOf(e) >= 0 {
		t.Fatal("Don't Save left the tab open")
	}
}

func TestTerminalTabs(t *testing.T) {
	h := newHarness(t, nil)
	a := h.a
	if len(a.terminals) != 1 {
		t.Fatalf("terminals = %d", len(a.terminals))
	}
	h.send(press("T", key.ModShortcut|key.ModShift))
	if len(a.terminals) != 2 || !a.terminals[1].focused {
		t.Fatal("Ctrl+Shift+T did not open and focus a terminal")
	}
	// Typing goes to the shell (cat echoes it back).
	h.typeText("hi")
	h.send(press(key.NameReturn, 0))
	time.Sleep(300 * time.Millisecond)
	h.frames(3)
	if got := a.terminals[1].vt.PlainText(); !strings.Contains(got, "hi") {
		t.Fatalf("terminal shows %q", got)
	}
	// Rename with Ctrl+Shift+R.
	h.send(press("R", key.ModShortcut|key.ModShift))
	r := a.termLabels[a.terminals[1]]
	if !r.editing {
		t.Fatal("not renaming")
	}
	h.send(press("A", key.ModShortcut))
	h.typeText("Build")
	h.send(press(key.NameReturn, 0))
	if r.name != "Build" || !r.custom {
		t.Fatalf("name = %q", r.name)
	}
	data, _ := os.ReadFile(a.terminalsFile())
	if !strings.Contains(string(data), "Build") {
		t.Fatalf("terminals.json = %q", data)
	}
	if !a.terminals[1].focused {
		t.Fatal("focus did not return to the terminal")
	}
	// Ctrl+Tab cycles terminal tabs while a terminal has focus.
	h.send(press(key.NameTab, key.ModShortcut))
	if a.termNB.Current != 0 {
		t.Fatalf("current terminal = %d", a.termNB.Current)
	}
	h.send(press("W", key.ModShortcut|key.ModShift))
	if len(a.terminals) != 1 {
		t.Fatal("Ctrl+Shift+W did not close the terminal")
	}
}

func TestCtrlClickDefinition(t *testing.T) {
	src := "package main\n\nfunc helper() {}\n\nfunc main() {\n\thelper()\n}\n"
	h := newHarness(t, map[string]string{"a.go": src})
	a := h.a
	uiMu.Lock()
	e := a.editors.Open(filepath.Join(a.root, "a.go"))
	uiMu.Unlock()
	h.frames(2)
	uiMu.Lock()
	e.View.OnCtrlClick(Pos{5, 2})
	uiMu.Unlock()
	for i := 0; i < 50 && e.View.Cursor().Line != 2; i++ {
		time.Sleep(50 * time.Millisecond)
		h.frames(1)
	}
	if c := e.View.Cursor(); c.Line != 2 || e.View.jump != 2 {
		t.Fatalf("cursor %v jump %d, status %q", c, e.View.jump, a.status)
	}
}

func TestPointerTrackingAndMenu(t *testing.T) {
	h := newHarness(t, map[string]string{"a.txt": "x\n", "b.txt": "y\n"})
	a := h.a
	// Hover the second explorer row, then right-click it.
	p := image.Pt(60, 47+24+12)
	h.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(p.X), float32(p.Y))})
	h.frames(2)
	if a.mouse != p {
		t.Fatalf("mouse = %v, want %v", a.mouse, p)
	}
	h.router.Queue(
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonSecondary, Position: f32.Pt(float32(p.X), float32(p.Y))},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(float32(p.X), float32(p.Y))},
	)
	h.frames(2)
	if a.menu == nil || a.tree.selected == nil || a.tree.selected.name != "b.txt" {
		t.Fatalf("menu %v selected %+v", a.menu != nil, a.tree.selected)
	}
	// Double-clicking a row opens the file.
	h.click(image.Pt(5, 5), 0, 1) // closes the menu
	h.click(p, 0, 2)
	if e := a.editors.Current(); e == nil || filepath.Base(e.Path) != "b.txt" {
		t.Fatal("double click did not open b.txt")
	}
}

func TestBuildRunKeepsFocus(t *testing.T) {
	h := newHarness(t, map[string]string{".edmin/commands.json": `[{"name":"Say","command":"say-hi"},{"name":"Args","command":"echo","params":true}]`})
	a := h.a
	uiMu.Lock()
	a.build.Load()
	uiMu.Unlock()
	h.send(press("3", key.ModShortcut))
	h.send(press(key.NameReturn, 0))
	time.Sleep(300 * time.Millisecond)
	h.frames(3)
	if !a.build.focused {
		t.Fatal("focus left the build panel after running a command")
	}
	for i := 0; i < 40 && !strings.Contains(a.currentTerminal().vt.PlainText(), "say-hi"); i++ {
		time.Sleep(50 * time.Millisecond)
		h.frames(1)
	}
	if got := a.currentTerminal().vt.PlainText(); !strings.Contains(got, "say-hi") {
		t.Fatalf("terminal shows %q", strings.TrimSpace(got))
	}
	// A command with parameters asks for them first, then refocuses too.
	h.send(press(key.NameDownArrow, 0), press(key.NameReturn, 0))
	if len(a.dialogs) != 1 {
		t.Fatal("no parameters dialog")
	}
	h.typeText("x")
	h.send(press(key.NameReturn, 0))
	if len(a.dialogs) != 0 || !a.build.focused {
		t.Fatalf("dialogs=%d build focused=%v", len(a.dialogs), a.build.focused)
	}
}

package main

import (
	"image"
	"image/png"
	"os"
	"strconv"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// TestScreenshot renders a window offscreen and saves it, for checking the
// look against the GTK version. It runs only when EDMIN_SHOT names the PNG
// to write; EDMIN_PROJ and EDMIN_FILE choose what to open.
func TestScreenshot(t *testing.T) {
	out := os.Getenv("EDMIN_SHOT")
	if out == "" {
		t.Skip("EDMIN_SHOT not set")
	}
	loadDesktopFonts()
	scale := float32(2)
	wdp, hdp := 1326, 832
	if v, err := strconv.Atoi(os.Getenv("EDMIN_W")); err == nil {
		wdp = v
	}
	if v, err := strconv.Atoi(os.Getenv("EDMIN_H")); err == nil {
		hdp = v
	}
	size := image.Pt(int(float32(wdp)*scale), int(float32(hdp)*scale))
	w, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Release()

	uiMu.Lock()
	a := newAppState(os.Getenv("EDMIN_PROJ"))
	if f := os.Getenv("EDMIN_FILE"); f != "" {
		a.editors.Open(f)
		a.focusWorkspace()
	}
	uiMu.Unlock()
	var router input.Router
	var ops op.Ops
	frame := func() {
		uiMu.Lock()
		defer uiMu.Unlock()
		a.drainQueue()
		ops.Reset()
		gtx := layout.Context{
			Ops:         &ops,
			Now:         time.Now(),
			Metric:      unit.Metric{PxPerDp: scale, PxPerSp: scale},
			Constraints: layout.Exact(size),
			Source:      router.Source(),
		}
		if os.Getenv("EDMIN_HOOK") == "search" {
			a.search.layout(gtx)
		} else {
			a.layout(gtx)
		}
		router.Frame(&ops)
		if err := w.Frame(&ops); err != nil {
			t.Fatal(err)
		}
	}
	steps := 6
	if v, err := strconv.Atoi(os.Getenv("EDMIN_STEPS")); err == nil {
		steps = v
	}
	for i := 0; i < steps; i++ {
		frame()
		if i == 2 {
			if fn := shotHook; fn != nil {
				uiMu.Lock()
				fn(a)
				uiMu.Unlock()
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := w.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	fh, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	png.Encode(fh, img)
	for _, x := range a.terminals {
		x.Close()
	}
}

// shotHook changes the window's state mid-way, as EDMIN_HOOK chooses.
var shotHook = func(a *App) {
	switch os.Getenv("EDMIN_HOOK") {
	case "settings":
		a.showSettings()
	case "find":
		a.editors.findEnt.SetText("gtk")
		a.editors.ShowFind()
	case "msg":
		a.askSave("editor.go", func(int) {})
	case "prompt":
		a.prompt("New File", "Name (relative to ., may include subfolders):", "", func(string) {})
	case "menu":
		a.tree.selected = a.tree.rows[2]
		a.tree.contextMenu(image.Pt(200, 300))
	case "search":
		a.search.entry.SetText("gotk3")
		a.search.run()
	case "symbol":
		e := a.editors.Current()
		a.symbolAction(e, 21, 6)
	}
}

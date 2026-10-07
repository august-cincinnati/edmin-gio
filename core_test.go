package main

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func screenLine(v *VT, y int) string {
	return strings.TrimRight(strings.Split(v.PlainText(), "\n")[y], " ")
}

func TestVTBasics(t *testing.T) {
	v := NewVT(5, 20)
	v.Write([]byte("hello\r\nworld"))
	if got := screenLine(v, 0); got != "hello" {
		t.Fatalf("line0 = %q", got)
	}
	if got := screenLine(v, 1); got != "world" {
		t.Fatalf("line1 = %q", got)
	}
	// Cursor movement, erase line, colors.
	v.Write([]byte("\x1b[1;1H\x1b[31mHE\x1b[0m\x1b[K"))
	if got := screenLine(v, 0); got != "HE" {
		t.Fatalf("after CUP/EL line0 = %q", got)
	}
	if v.lines[0][0].a.fg != 1 || v.lines[0][2].a.fg != colorDefault {
		t.Fatalf("SGR not applied: %+v", v.lines[0][:3])
	}
	// Split UTF-8 sequence across writes.
	v.Write([]byte("\x1b[3;1H\xe2\x82"))
	v.Write([]byte("\xac"))
	if got := screenLine(v, 2); got != "€" {
		t.Fatalf("utf8 line = %q", got)
	}
}

func TestVTScrollback(t *testing.T) {
	v := NewVT(3, 10)
	for i := 0; i < 5; i++ {
		v.Write([]byte{byte('a' + i), '\r', '\n'})
	}
	s := v.Snapshot()
	if len(s.Scrollback) != 3 {
		t.Fatalf("scrollback = %d lines, want 3", len(s.Scrollback))
	}
	if s.Scrollback[0][0].ch != 'a' || s.Lines[0][0].ch != 'd' {
		t.Fatalf("unexpected scroll state")
	}
	// Alternate screen does not push to scrollback and restores content.
	v.Write([]byte("\x1b[?1049hXYZ\r\n\r\n\r\n\r\n\x1b[?1049l"))
	if s := v.Snapshot(); len(s.Scrollback) != 0 || s.Lines[0][0].ch != 'd' {
		t.Fatalf("alt screen leaked: sb=%d first=%q", len(s.Scrollback), s.Lines[0][0].ch)
	}
	v.Resize(2, 5)
	v.Resize(4, 12)
	if v.rows != 4 || len(v.lines[0]) != 12 {
		t.Fatalf("resize failed")
	}
}

func TestVTClearScrollback(t *testing.T) {
	v := NewVT(3, 10)
	for i := 0; i < 5; i++ {
		v.Write([]byte{byte('a' + i), '\r', '\n'})
	}
	// What `clear` sends: home, erase screen, erase saved lines.
	v.Write([]byte("\x1b[H\x1b[2J\x1b[3J$ "))
	s := v.Snapshot()
	if !s.ClearScrollback || len(s.Scrollback) != 0 {
		t.Fatalf("clear kept scrollback: clear=%v sb=%d", s.ClearScrollback, len(s.Scrollback))
	}
	if got := v.PlainText(); got != "$         \n          \n          \n" {
		t.Fatalf("screen after clear = %q", got)
	}
	if v.Snapshot().ClearScrollback {
		t.Fatalf("ClearScrollback not reset after drain")
	}
}

func TestVTSynchronizedOutput(t *testing.T) {
	v := NewVT(3, 10)
	var reply []byte
	v.Reply = func(b []byte) { reply = append(reply, b...) }
	v.Write([]byte("\x1b[?2026$p"))
	if string(reply) != "\x1b[?2026;2$y" {
		t.Fatalf("DECRQM reply = %q", reply)
	}
	v.Write([]byte("\x1b[?2026hhalf"))
	if !v.Holding() {
		t.Fatalf("not holding during a synchronized frame")
	}
	v.Write([]byte(" done\x1b[?2026l"))
	if v.Holding() {
		t.Fatalf("still holding after the frame ended")
	}
}

func TestVTWrapAndReply(t *testing.T) {
	v := NewVT(3, 4)
	var reply string
	v.Reply = func(b []byte) { reply = string(b) }
	v.Write([]byte("abcdef"))
	if screenLine(v, 0) != "abcd" || screenLine(v, 1) != "ef" {
		t.Fatalf("wrap: %q", v.PlainText())
	}
	v.Write([]byte("\x1b[6n"))
	if reply != "\x1b[2;3R" {
		t.Fatalf("DSR reply = %q", reply)
	}
	v.Write([]byte("\x1bc"))
	if strings.TrimSpace(v.PlainText()) != "" {
		t.Fatalf("reset did not clear")
	}
}

const goSrc = `package demo

type Thing struct{ N int }

func helper(x int) int { return x + 1 }

func (t *Thing) Run() int {
	v := helper(t.N)
	return v
}

func main() {
	var th Thing
	th.Run()
	_ = helper(2)
}
`

func TestGoSymbols(t *testing.T) {
	lang := languageFor("x.go")
	sym, ok := lang.SymbolAt([]byte(goSrc), 7, 7) // helper call
	if !ok || sym.Name != "helper" || sym.IsDef {
		t.Fatalf("SymbolAt call = %+v ok=%v", sym, ok)
	}
	sym, ok = lang.SymbolAt([]byte(goSrc), 4, 6) // helper declaration
	if !ok || sym.Name != "helper" || !sym.IsDef {
		t.Fatalf("SymbolAt decl = %+v ok=%v", sym, ok)
	}
	refs := lang.FindRefs("x.go", []byte(goSrc), "helper")
	if len(refs) != 3 {
		t.Fatalf("helper refs = %d, want 3", len(refs))
	}
	for _, r := range refs {
		if r.IsDef != (r.Line == 4) {
			t.Fatalf("wrong def flag: %+v", r)
		}
	}
	for _, n := range []string{"Thing", "Run", "v", "th"} {
		found := false
		for _, r := range lang.FindRefs("x.go", []byte(goSrc), n) {
			found = found || r.IsDef
		}
		if !found {
			t.Errorf("no definition found for %s", n)
		}
	}
	// Parameter and result types are references, not definitions.
	src := "package p\ntype Ref struct{}\nfunc f(x Ref, ok bool) (Ref, bool) { return x, ok }\n"
	for _, n := range []string{"Ref", "bool"} {
		for _, r := range lang.FindRefs("x.go", []byte(src), n) {
			if r.IsDef && r.Line != 1 {
				t.Errorf("parameter type %s marked as definition: %+v", n, r)
			}
		}
	}
}

// Every official tree-sitter grammar: a definition and a usage of a symbol.
var langSamples = []struct {
	file, src, name string
	noDef           bool // language has no notion of a definition for this symbol
}{
	{"a.agda", "module Test where\ndata Nat : Set where\n  zero : Nat\n  suc : Nat -> Nat\ngreet : Nat -> Nat\ngreet n = suc n\ntwo : Nat\ntwo = greet zero\n", "greet", false},
	{"a.sh", "greet() {\n  echo hi\n}\ngreet\n", "greet", false},
	{"a.c", "int greet(int a) { return a; }\nint main(void) { return greet(1); }\n", "greet", false},
	{"a.cpp", "namespace n { int greet(int a) { return a; } }\nint main() { return n::greet(1); }\n", "greet", false},
	{"a.cs", "class A {\n  static int Greet(int a) { return a; }\n  static void Main() { Greet(1); }\n}\n", "Greet", false},
	{"a.css", ":root { --brand: red; }\na { color: var(--brand); }\n", "--brand", false},
	{"a.erb", "<% def greet(n) n end %>\n<p><%= greet(1) %></p>\n", "greet", false},
	{"a.ejs", "<% function greet(n) { return n } %>\n<p><%= greet(1) %></p>\n", "greet", false},
	{"a.go", goSrc, "helper", false},
	{"a.hs", "module Main where\ngreet :: Int -> Int\ngreet n = n\nmain = print (greet 1)\n", "greet", false},
	{"a.html", "<div><span>x</span></div>\n<span>y</span>\n", "span", true},
	{"a.java", "class A {\n  static int greet(int a) { return a; }\n  void m() { greet(1); }\n}\n", "greet", false},
	{"a.js", "function greet(a) { return a }\ngreet(1)\n", "greet", false},
	{"a.json", "{\"greet\": 1, \"other\": \"greet\"}\n", "greet", false},
	{"a.jl", "function greet(x)\n  x\nend\ngreet(1)\n", "greet", false},
	{"a.ml", "let greet x = x\nlet () = ignore (greet 1)\n", "greet", false},
	{"a.mli", "type t\nval greet : t -> t\nval twice : t -> t\n", "t", false},
	{"a.php", "<?php\nfunction greet($a) { return $a; }\ngreet(1);\n", "greet", false},
	{"a.py", "def greet(name):\n    return name\n\ngreet('x')\n", "greet", false},
	{"a.rb", "def greet(a)\n  a\nend\ngreet(1)\n", "greet", false},
	{"a.rs", "fn greet(a: i32) -> i32 { a }\nfn main() { greet(1); }\n", "greet", false},
	{"a.scala", "object A {\n  def greet(a: Int): Int = a\n  val x = greet(1)\n}\n", "greet", false},
	{"a.ts", "function greet(a: number): number { return a }\ngreet(1)\n", "greet", false},
	{"a.tsx", "const Greet = () => <div/>;\nconst x = <Greet/>;\n", "Greet", false},
	{"a.v", "module greet(input a);\nendmodule\nmodule top;\n  greet g(.a(1'b1));\nendmodule\n", "greet", false},
}

func TestAllLanguagesDefinitions(t *testing.T) {
	for _, tc := range langSamples {
		lang := languageFor(tc.file)
		if lang == nil {
			t.Errorf("%s: no language", tc.file)
			continue
		}
		refs := lang.FindRefs(tc.file, []byte(tc.src), tc.name)
		var def, use *SymbolRef
		for i := range refs {
			if refs[i].IsDef && def == nil {
				def = &refs[i]
			} else if !refs[i].IsDef {
				use = &refs[i]
			}
		}
		if use == nil || def == nil && !tc.noDef {
			t.Errorf("%s (%s): refs=%+v", tc.file, lang.Name, refs)
			continue
		}
		// Ctrl+Click on the usage must resolve to the same symbol.
		sym, ok := lang.SymbolAt([]byte(tc.src), use.Line, use.ColByte)
		if !ok || sym.Name != tc.name || sym.IsDef {
			t.Errorf("%s (%s): SymbolAt usage = %+v ok=%v", tc.file, lang.Name, sym, ok)
		}
		// pickDefinition must choose a definition for the usage.
		var defs []SymbolRef
		for _, r := range refs {
			if r.IsDef {
				defs = append(defs, r)
			}
		}
		if !tc.noDef {
			if _, ok := pickDefinition(tc.file, use.Line, defs); !ok {
				t.Errorf("%s: no definition picked from %+v", tc.file, defs)
			}
		}
	}
}

func TestQueriesCompile(t *testing.T) {
	for _, l := range append(languages, langJSDoc, langRegex) {
		l.init()
		if len(l.Highlights) > 0 && l.hl == nil {
			t.Errorf("%s: highlights query failed to compile", l.Name)
		}
		if len(l.Tags) > 0 && l.tags == nil {
			t.Errorf("%s: tags query failed to compile", l.Name)
		}
		if len(l.Locals) > 0 && l.locals == nil {
			t.Errorf("%s: locals query failed to compile", l.Name)
		}
	}
	for _, tc := range langSamples {
		if spans := languageFor(tc.file).Highlight([]byte(tc.src)); len(spans) == 0 {
			t.Errorf("%s: no highlight spans", tc.file)
		}
	}
}

func TestJSDocAndRegex(t *testing.T) {
	js := languageFor("a.js")
	src := "class Foo {}\n/** @param {Foo} x */\nfunction f(x) {}\n"
	sym, ok := js.SymbolAt([]byte(src), 1, strings.Index("/** @param {Foo} x */", "Foo")+1)
	if !ok || sym.Name != "Foo" {
		t.Fatalf("JSDoc symbol = %+v ok=%v", sym, ok)
	}
	src = "const r = /(?<year>\\d+)-\\k<year>/;\n"
	col := strings.LastIndex(src, "year")
	sym, ok = js.SymbolAt([]byte(src), 0, col)
	if !ok || sym.Name != "year" || len(sym.Local) != 2 || !sym.Local[0].IsDef || sym.Local[1].IsDef {
		t.Fatalf("regex group symbol = %+v ok=%v", sym, ok)
	}
	if sym.Local[0].ColByte != strings.Index(src, "year") {
		t.Fatalf("regex def column = %d", sym.Local[0].ColByte)
	}
}

func TestHighlight(t *testing.T) {
	spans := languageFor("x.go").Highlight([]byte(goSrc))
	classes := map[string]bool{}
	for _, s := range spans {
		classes[s.Class] = true
	}
	for _, c := range []string{hlKeyword, hlType, hlFunction, hlNumber} {
		if !classes[c] {
			t.Errorf("missing highlight class %s", c)
		}
	}
}

func TestWordRefs(t *testing.T) {
	refs := findWordRefs("f.txt", []byte("foo food foo_bar foo\n"), "foo")
	if len(refs) != 2 {
		t.Fatalf("word refs = %d", len(refs))
	}
}

// ptyInput makes the default shell print EDMIN_42 and exit, and
// ptyCommand prints it on its own; the 42 is computed so the echoed input
// doesn't count.
var ptyInput, ptyCommand = "echo EDMIN_$((40+2))\rexit\r", []string{"/bin/sh", "-c", "echo EDMIN_$((40+2))"}

func init() {
	if runtime.GOOS == "windows" {
		ptyInput = "'EDMIN_' + (40+2)\rexit\r"
		ptyCommand = []string{"powershell.exe", "-NoProfile", "-Command", "'EDMIN_' + (40+2)"}
	}
}

func TestPtyShell(t *testing.T) {
	master, err := startShell(t.TempDir(), nil, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer master.Kill()
	master.Write([]byte(ptyInput))
	var out []byte
	buf := make([]byte, 4096)
	for !strings.Contains(string(out), "EDMIN_42") {
		n, err := master.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			break
		}
	}
	if !strings.Contains(string(out), "EDMIN_42") {
		t.Fatalf("shell output missing: %q", out)
	}
}

func TestInsideRoot(t *testing.T) {
	cases := map[string]bool{
		"a.go":        true,
		"sub/a.go":    true,
		"..foo":       true,
		"..":          false,
		"../x":        false,
		"/etc/passwd": false,
	}
	if runtime.GOOS == "windows" {
		cases[`..\x`] = false
		cases[`C:\Windows`] = false
		cases[`C:x`] = false
		cases[`\Windows`] = false
	}
	for rel, want := range cases {
		if got := insideRoot(rel); got != want {
			t.Errorf("insideRoot(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestPtyCustomShell(t *testing.T) {
	master, err := startShell(t.TempDir(), ptyCommand, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer master.Kill()
	var out []byte
	buf := make([]byte, 4096)
	for !strings.Contains(string(out), "EDMIN_42") {
		n, err := master.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			break
		}
	}
	if !strings.Contains(string(out), "EDMIN_42") {
		t.Fatalf("custom shell output missing: %q", out)
	}
}

const testMountinfo = `22 1 8:2 / / rw,relatime shared:1 - ext4 /dev/sda2 rw
40 22 0:35 / /mnt/c rw,noatime - 9p C:\134 rw,dirsync,aname=drvfs;path=C:\;uid=1000
41 22 0:36 / /mnt/work rw,noatime - drvfs D:\134 rw
50 22 0:40 / /home/me/remote rw,nosuid shared:2 - fuse.sshfs me@build-box:/srv rw,user_id=1000
51 50 0:41 / /home/me/remote/nested\040dir rw - fuse.sshfs deploy@[fe80::1]: rw
52 22 0:42 / /home/me/home\040box rw - fuse.sshfs pi@raspberrypi:code rw
`

func TestDetectShell(t *testing.T) {
	found := func(name string) (string, error) {
		if name == "powershell.exe" {
			return "/mnt/c/WINDOWS/System32/WindowsPowerShell/v1.0/powershell.exe", nil
		}
		return "", os.ErrNotExist
	}
	wsl := shellEnv{inWSL: true, mountinfo: testMountinfo, lookPath: found}
	linux := shellEnv{mountinfo: testMountinfo, lookPath: found}
	ps := []string{"/mnt/c/WINDOWS/System32/WindowsPowerShell/v1.0/powershell.exe", "-NoLogo"}
	cases := []struct {
		root string
		env  shellEnv
		want []string
	}{
		{"/mnt/c/Users/me/proj", wsl, ps},
		{"/mnt/c", wsl, ps},
		{"/mnt/work/app", wsl, ps}, // drvfs mounted outside /mnt/<letter>
		{"/mnt/c/Users/me/proj", linux, nil},
		{"/mnt/cdrom/x", wsl, nil},
		{"/home/me/proj", wsl, nil},
		{"/home/me/remote", linux, []string{"ssh", "-t", "me@build-box", "cd '/srv' && exec $SHELL -l"}},
		{"/home/me/remote/app/it's", linux, []string{"ssh", "-t", "me@build-box", `cd '/srv/app/it'\''s' && exec $SHELL -l`}},
		{"/home/me/remote/nested dir/x", linux, []string{"ssh", "-t", "deploy@fe80::1", "cd 'x' && exec $SHELL -l"}},
		{"/home/me/home box", wsl, []string{"ssh", "-t", "pi@raspberrypi", "cd 'code' && exec $SHELL -l"}},
		{"/home/me/remotely", linux, nil},
	}
	for _, c := range cases {
		got := detectShell(c.root, c.env)
		if strings.Join(got, "\x00") != strings.Join(c.want, "\x00") {
			t.Errorf("detectShell(%q, wsl=%v) = %q, want %q", c.root, c.env.inWSL, got, c.want)
		}
	}
}

// Ctrl+clicking a method call resolves to the method, not the receiver,
// and the call itself is not mistaken for a definition.
func TestMethodCalls(t *testing.T) {
	cases := []struct{ file, src, call, name string }{
		{"a.go", "package p\ntype T struct{}\nfunc (t T) Run() {}\nfunc f() {\n  var obj T\n  obj.Run()\n}\n", "obj.Run()", "Run"},
		{"a.py", "class T:\n    def run(self): pass\n    def m(self):\n        self.run()\n", "self.run()", "run"},
		{"a.ts", "class T {\n  run(): void {}\n}\nconst obj = new T();\nobj.run();\n", "obj.run()", "run"},
		{"a.java", "class T {\n  void run() {}\n  void m() {\n    T obj = new T();\n    obj.run();\n  }\n}\n", "obj.run()", "run"},
		{"a.cpp", "struct T {\n  void run() {}\n};\nint main() {\n  T* obj;\n  obj->run();\n}\n", "obj->run()", "run"},
		{"a.rs", "struct T;\nimpl T {\n  fn run(&self) {}\n}\nfn main() {\n  let obj = T;\n  obj.run();\n}\n", "obj.run()", "run"},
		{"a.cs", "class T {\n  void Run() {}\n  void M() {\n    var obj = new T();\n    obj.Run();\n    obj?.Run();\n  }\n}\n", "obj.Run()", "Run"},
		{"a.php", "<?php\nclass T {\n  function run() {}\n}\n$obj = new T();\n$obj->run();\n$obj?->run();\nT::run();\n", "$obj->run()", "run"},
	}
	for _, c := range cases {
		lang := languageFor(c.file)
		lines := strings.Split(c.src, "\n")
		for i, l := range lines {
			j := strings.Index(l, c.call)
			if j < 0 {
				continue
			}
			sym, ok := lang.SymbolAt([]byte(c.src), i, j+strings.Index(c.call, c.name))
			if !ok || sym.Name != c.name || sym.IsDef {
				t.Errorf("%s: SymbolAt method call = %+v ok=%v", c.file, sym, ok)
			}
		}
		var defs []SymbolRef
		for _, r := range lang.FindRefs(c.file, []byte(c.src), c.name) {
			if r.IsDef {
				defs = append(defs, r)
			}
		}
		if len(defs) != 1 {
			t.Errorf("%s: want only the method declaration as a definition, got %+v", c.file, defs)
		}
	}
}

func TestWithParams(t *testing.T) {
	cases := []struct{ cmd, extra, want string }{
		{"go test", "", "go test"},
		{"go test", "  ", "go test"},
		{"go test", "./... -run Foo", "go test ./... -run Foo"},
		{"go test ", " -v ", "go test -v"},
	}
	for _, c := range cases {
		if got := withParams(c.cmd, c.extra); got != c.want {
			t.Errorf("withParams(%q, %q) = %q, want %q", c.cmd, c.extra, got, c.want)
		}
	}
}

# EdMin

EdMin is a minimal text editor written in Go, built with
[Gio](https://gioui.org). It uses the official
[tree-sitter Go binding](https://github.com/tree-sitter/go-tree-sitter) and
the official grammars from the [tree-sitter](https://github.com/tree-sitter)
organization for syntax highlighting and symbol navigation. Those are its only
dependencies; everything else, including the terminal emulator, is plain Go.

EdMin draws its whole window itself, in the style of a GTK 3 application on
GNOME (the Yaru theme): a header bar with the window buttons, the desktop's
interface font (read from GNOME's settings), fontconfig's `monospace` font for
code, and icons from the desktop's icon theme. On Windows, which has none of
those, it uses Segoe UI, Consolas and a built-in set of symbolic icons.

## Features

- **File explorer** on the left. Folders load when you expand them.
  The header bar has *New File*, *New Folder*, *Refresh* and *Collapse all*
  buttons, and the first three are also on the right-click menu. `.git` and
  `node_modules` are hidden.
- **Tabbed editor.** Tabs can be dragged to reorder, and a `●` marks
  unsaved changes. The editor has:
  - line numbers, with the current line emphasized
  - syntax highlighting for every supported language (see below)
  - auto-indent
  - undo and redo, grouped by word
- **Find in file** (`Ctrl+F`) highlights every match and has next/previous
  and a *Match case* option.
- **Find in project** (`Ctrl+Shift+F`) opens the Search dialog and searches
  every text file, with results grouped by file on its *Usages* tab. Click a result to open the file at that line.
- **Go to definition / Find usages** (`Ctrl+Click` on a symbol):
  - Clicking a usage jumps to its definition.
  - Otherwise (you clicked a definition, there are several candidates, or
    the definition is outside the project) the Search dialog opens with a
    **Definition** tab listing the candidates and a **Usages** tab listing
    every usage grouped by file.
  - Works in every language listed under [Languages](#languages).
- **Themes**: Light, Dark, Tan, Solarized Dark, Rust, Green and Purple. Choose one in Settings
  (the menu button in the header bar, or `Ctrl+4`). It applies immediately to
  that window only, so different projects can look different. The choice is
  saved for the project in `.edmin/settings.json`, and in
  `~/.config/edmin/settings.json` (`%AppData%\edmin\settings.json` on
  Windows) as the default for projects without one.
- **Terminals** in tabs along the bottom. Each runs the project's shell
  (see [Terminal shells](#terminal-shells)) through a
  pseudo-terminal and a built-in xterm-compatible emulator that supports
  colors, scrollback, and full-screen programs such as `vim` and `less`.
- **Build panel** on the right for saving named shell commands.
  Double-click a command, or select it and press ▶, to run it in the
  current terminal.

The explorer, terminal and build panels can each be hidden, using the
header-bar toggles or the keyboard shortcuts below.

## Languages

EdMin supports every language with an official tree-sitter parser:

| Language   | File types                              | Definitions come from            |
| ---------- | --------------------------------------- | -------------------------------- |
| Agda       | `.agda`                                 | structural rules                 |
| Bash       | `.sh` `.bash` `.bashrc` `.bash_profile` | structural rules                 |
| C          | `.c` `.h`                               | `tags.scm` + structural rules    |
| C++        | `.cc` `.cpp` `.cxx` `.hpp` `.hh` …      | `tags.scm` + structural rules    |
| C#         | `.cs`                                   | `tags.scm` + structural rules    |
| CSS        | `.css`                                  | structural rules (properties, custom properties, keyframes) |
| ERB / EJS  | `.erb` / `.ejs`                         | embedded Ruby / JavaScript       |
| Go         | `.go`                                   | `tags.scm` + structural rules    |
| Haskell    | `.hs` `.hs-boot`                        | `locals.scm` + structural rules  |
| HTML       | `.html` `.htm`                          | usages only (tag names)          |
| Java       | `.java`                                 | `tags.scm` + structural rules    |
| JavaScript | `.js` `.mjs` `.cjs` `.jsx`              | `tags.scm` + `locals.scm`        |
| JSDoc      | `/** … */` comments in JS/TS files      | type names resolve to JS/TS definitions |
| JSON       | `.json` `.jsonc`                        | object keys                      |
| Julia      | `.jl`                                   | `locals.scm` + structural rules  |
| OCaml      | `.ml` `.mli`                            | `tags.scm` + `locals.scm`        |
| PHP        | `.php` `.phtml`                         | `tags.scm` + structural rules    |
| Python     | `.py` `.pyi`                            | `tags.scm` + structural rules    |
| Regex      | regex literals in JS/TS files           | named groups and `\k<name>` backreferences |
| Ruby       | `.rb` `Rakefile` `Gemfile` …            | `tags.scm` + `locals.scm`        |
| Rust       | `.rs`                                   | `tags.scm` + structural rules    |
| Scala      | `.scala` `.sbt` `.sc`                   | `tags.scm` + `locals.scm`        |
| TypeScript | `.ts` `.tsx` `.mts` `.cts`              | `tags.scm` + `locals.scm`        |
| Verilog    | `.v` `.sv` `.vh` `.svh`                 | structural rules                 |

**Highlighting** uses each grammar's official `highlights.scm`. Verilog
ships no highlight query, so it gets a simpler built-in highlighter for
comments, strings, numbers and keywords. In ERB and EJS files, the code
inside `<% %>` is also highlighted as Ruby or JavaScript.

**Definitions** come from three sources:
- the grammar's `tags.scm` (the query format GitHub's code navigation uses)
- its `locals.scm`, where there is one
- structural rules for grammars that ship neither, such as a declaration's
  `name` field or the first identifier in a Verilog or Agda declaration

**Searching across files.** Ctrl+Click looks through every file in the
same language family, parsing each one with its own grammar. The families
are:
- `.js` `.jsx` `.ts` `.tsx` `.ejs`
- `.c` `.h` `.cpp` `.hpp`
- `.rb` `.erb`
- `.ml` `.mli`

**Vendored queries.** The query files are copied from the grammar
repositories into `queries/` (each with its MIT license) and embedded in the
binary. `queries/VERSIONS` records the grammar versions they came from. If
you upgrade a grammar in `go.mod`, copy its queries again from the module
cache.

## Requirements

- Linux, macOS, or Windows 10 version 1809 or newer
- Go 1.25 or newer
- A C compiler (tree-sitter uses cgo, as does Gio except on Windows) and, on
  Linux, the Wayland, X11, EGL and Vulkan development files Gio needs

On Debian/Ubuntu:

```sh
sudo apt install golang-go build-essential pkg-config libwayland-dev libx11-dev \
  libx11-xcb-dev libxkbcommon-x11-dev libgles2-mesa-dev libegl1-mesa-dev \
  libffi-dev libxcursor-dev libxfixes-dev libvulkan-dev
```

Optional, on Linux:
- `zenity`, for GTK's own folder chooser in *Open Folder*. Without it EdMin
  uses a simple chooser of its own.
- `gsettings` and `fc-match` (present on GNOME desktops), to pick up the
  desktop's fonts and icon theme. Without them EdMin falls back to Ubuntu
  Sans / DejaVu fonts and the Yaru or Adwaita icons.

On macOS, install Go and the Xcode command line tools.

On Windows, install Go and a MinGW-w64 C compiler, for example from
[MSYS2](https://www.msys2.org) (`pacman -S mingw-w64-ucrt-x86_64-gcc`), with
its `bin` folder on your `PATH`.

On macOS the shortcuts use Ctrl, as on Linux and Windows, not Cmd: Ctrl+S
saves, Ctrl+Click finds usages, and Ctrl+C/X/V/A/Z edit text. In the terminal,
Ctrl+Shift+C and Ctrl+Shift+V copy and paste, and Ctrl keeps its usual
terminal meaning (Ctrl+C interrupts).

## Building

```sh
go build -o edmin .
```

To build for Wayland only (without the X11 development files), add
`-tags nox11`:

```sh
go build -tags nox11 -o edmin .
```

On Windows, add `-H=windowsgui` so no console window opens alongside
EdMin:

```sh
go build -ldflags=-H=windowsgui -o edmin.exe .
```

The first build compiles Gio and the tree-sitter grammars, which takes a
few minutes. Later builds are fast.

## Usage

```sh
./edmin                 # reopen last session's projects (or the current directory)
./edmin path/to/project # open a folder
./edmin path/to/file.go # open a file (its folder becomes the project)
./edmin proj-a proj-b   # open each project in its own window
./edmin --wait file.txt # stay in the foreground until EdMin closes
```

Run from a terminal, EdMin starts itself in the background and gives the
prompt back straight away. Use `--wait` (or `-w`) to keep it in the foreground,
for example to see its error output.

Use the folder button in the header bar, or `Ctrl+O`, to switch projects.
To work on several projects at once, use the new-window button next to it, or
`Ctrl+Shift+O`, to open a folder in a new window. Each window has its own
tabs, terminals, build commands and colour theme. EdMin exits when the last
window is closed.

EdMin remembers which projects are open, in `~/.config/edmin/settings.json`
(`%AppData%\edmin\settings.json` on Windows).
Running `./edmin` with no arguments reopens them, one window each, skipping
any folder that no longer exists. Closing a window removes its project from
the list, except for the last window: the projects open when you quit are the
ones that come back.

## Keyboard shortcuts

| Shortcut                    | Action                                   |
| --------------------------- | ---------------------------------------- |
| `Ctrl+S`                    | Save current file                        |
| `Ctrl+W`                    | Close current tab                        |
| `Ctrl+Shift+W`              | Close current terminal tab if a terminal has focus, else current file tab |
| `Ctrl+Tab` / `Ctrl+Shift+Tab` | Next / previous terminal tab if a terminal has focus, else file tab |
| `Ctrl+Z` / `Ctrl+Shift+Z`   | Undo / redo (`Ctrl+Y` also redoes)       |
| `Ctrl+F`                    | Find in file (`Enter` / `Shift+Enter` to step, `Esc` to close) |
| `Ctrl+Shift+F`              | Find in project                          |
| `Ctrl+G`                    | Go to line                               |
| `Ctrl+Click`                | Go to definition / find usages           |
| `Ctrl+O`                    | Open folder                              |
| `Ctrl+Shift+O`              | Open folder in new window                |
| `Ctrl+Shift+X`              | Close window                             |
| `Ctrl+4`                    | Open / focus Settings (theme)            |
| `Ctrl+Shift+4`              | Close Settings                           |
| `Ctrl+1`                    | Open / focus file explorer               |
| `Ctrl+Shift+1`              | Close file explorer                      |
| `Ctrl+Shift+E`              | Jump to open file in explorer            |
| `Delete` (in the explorer)  | Delete the selected file or folder (asks first) |
| `Ctrl+2`                    | Open / focus terminal panel              |
| `Ctrl+Shift+2`              | Close terminal panel                     |
| `Ctrl+Shift+T`              | New terminal (in the build pane: add command) |
| `Ctrl+Shift+R` (in a terminal) | Rename the terminal's tab            |
| Double-click terminal tab  | Rename terminal (`Enter` to save, `Esc` to cancel); renamed tabs reopen with the project |
| `Ctrl+3`                    | Open / focus build panel                 |
| `Ctrl+Shift+3`              | Close build panel                        |
| `Ctrl+5`                    | Focus the file editor                    |
| `Ctrl+Shift+C` / `Ctrl+Shift+V` | Copy / paste in the terminal         |
| `Shift+PageUp` / `Shift+PageDown` | Scroll terminal history            |
| `Alt+Shift+Arrow`           | Resize the focused pane (like tmux)      |

When a terminal has focus, plain `Ctrl+<key>` combinations such as `Ctrl+C`
and `Ctrl+B` go to the shell. Only the `Ctrl+Shift` shortcuts and the panel
shortcuts `Ctrl+1`–`Ctrl+5`, `Ctrl+Tab` and `Alt+Shift+Arrow` are handled by the editor.

`Alt+Shift+Arrow` moves a divider next to the focused pane in the arrow's
direction. Left/Right move the pane's right edge if a panel is open to its
right, otherwise its left edge. Up/Down move the divider between the editor
and the terminal.

Renamed terminal tabs are saved per project in `.edmin/terminals.json`, in tab
order. When the project opens again, EdMin opens one terminal for each saved
name. Only the names are kept, not the shell sessions. Closing a renamed tab
removes it from the list.

## Terminal shells

Each project's terminals run a shell picked in this order:

1. **The project's `"shell"` setting** in `.edmin/settings.json`, as a list of
   arguments:

   ```json
   { "shell": ["ssh", "-t", "-p", "2222", "me@build-box", "cd /srv/app && exec $SHELL -l"] }
   ```

2. **Detected from where the project lives:**
   - Inside WSL, a project on a Windows drive (`/mnt/c/...`, or any other
     drvfs mount) gets PowerShell: `pwsh.exe` if it is installed, otherwise
     `powershell.exe`. It starts in the matching Windows folder.
   - A project on an sshfs mount gets `ssh -t [user@]host`, with a login
     shell in the same folder on the remote machine. The host comes from the
     mount's source, so aliases in `~/.ssh/config` work. The mount's port
     setting isn't visible to EdMin, so use the `"shell"` setting for a
     non-standard port, as in the example above.
3. **Your `$SHELL`**, or `/bin/sh` if it isn't set. On Windows it is
   PowerShell (`pwsh.exe` if installed, otherwise `powershell.exe`), or
   `cmd.exe` if neither is found. To use another shell, such as Git Bash,
   set `"shell"`, for example `["C:\\Program Files\\Git\\bin\\bash.exe", "-l"]`.

Hover over a terminal tab to see the command it runs. Build commands are
typed into that shell, so commands written for bash won't work in
PowerShell.

## Build commands

Build commands are saved per project in `.edmin/commands.json`:

```json
[
  { "name": "Run tests", "command": "go test ./..." },
  { "name": "Build", "command": "go build -o edmin ." }
]
```

You can edit this file by hand or with the panel's add, edit and remove
buttons.

## Project layout

| File          | Purpose                                                   |
| ------------- | --------------------------------------------------------- |
| `main.go`     | Window layout, panels, keyboard shortcuts, symbol lookup  |
| `window.go`   | Header bar and window buttons (EdMin draws its own decorations) |
| `editor.go`   | Editor tabs, highlighting, undo/redo, in-file search      |
| `codeview.go` | The text editing widget, with its line-number gutter      |
| `mono.go`     | Monospace text on a pixel grid, like GTK's hinted text    |
| `notebook.go` | GTK-style tab strips                                      |
| `widgets.go`  | Buttons, entries, check boxes, scrollbars, tooltips, lists |
| `dialogs.go`  | Modal dialogs, context menus, the folder chooser          |
| `ui.go`       | Fonts, colours, drawing helpers and the UI event queue    |
| `icons.go`    | Icons from the desktop's icon theme (SVG and PNG)         |
| `icons_builtin.go` | Built-in symbolic icons, for when there is no icon theme (Windows) |
| `filetree.go` | File explorer                                             |
| `search.go`   | Project-wide search and the results dialog                |
| `lang.go`     | Language table, highlighting and definition/usage analysis |
| `queries/`    | Official tree-sitter query files, embedded into the binary |
| `build.go`    | Build commands panel                                      |
| `terminal.go` | Terminal widget (rendering and keyboard input)            |
| `vt.go`       | VT100/xterm screen emulator (no UI code)                  |
| `pty_*.go`    | Pseudo-terminal support (Unix ptys with Linux and macOS ioctls; ConPTY on Windows) |
| `detach_*.go` | Starting EdMin in the background (Unix sessions, Windows detached processes) |
| `keys.go`     | Modifier handling (Ctrl shortcuts on every platform)      |
| `shell.go`    | Choosing each project's terminal shell (WSL, sshfs)     |
| `util.go`     | Word-based fallbacks for files without a grammar          |

## Testing

```sh
go test ./...
```

The tests cover:
- the window's behaviour, driven headlessly through Gio's input router:
  editing, undo and redo, saving, the find bar, the explorer, panel
  shortcuts, dialogs, settings, terminal tabs and Ctrl+Click
- the terminal emulator
- a real shell session through the pseudo-terminal
- for every supported language: that its queries compile, that it
  highlights, and that Ctrl+Click on a usage finds the definition
- JSDoc type names and regex named groups

`screenshot_test.go` renders a window offscreen to a PNG, for checking the
look (it is skipped unless `EDMIN_SHOT` is set):

```sh
EDMIN_SHOT=shot.png EDMIN_PROJ=$PWD EDMIN_FILE=main.go go test -run TestScreenshot .
```

## Limitations

- **Symbol navigation matches by name.** It is not type-aware. When several
  definitions share a name, EdMin prefers one in the same file, then the
  same directory, then the first one when all candidates are in one file,
  and otherwise lists them all. Files without a tree-sitter grammar fall
  back to whole-word text matching.
- **Query compatibility safeguard.** The Go binding compiles `#match?`
  predicates with Go's `regexp` package. If a future query uses a pattern
  Go can't compile, or a node type the grammar lacks, EdMin drops just that
  pattern rather than the whole query. With the current grammar versions,
  every pattern compiles.
- **The terminal emulator is not complete.** Mouse reporting is not
  supported, and wide characters (CJK, emoji) are treated as one column.
- **Untested platforms.** The macOS and Windows builds compile but are
  untested on real hardware, and are not packaged as an `.app` or installer.
  Windows terminals use ConPTY, so they need Windows 10 version 1809 or
  newer.
- **Dialogs are drawn inside the window**, like GNOME's attached dialogs,
  rather than as windows of their own. The Search window is a real window.
- **Input methods** can type text, but EdMin's code editor and terminal
  don't show pre-edit (composition) text.
- **Window edges.** Double-clicking the header bar doesn't maximize the
  window, and the window has square corners and no shadow.

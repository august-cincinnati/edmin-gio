# Project Name
- EdMin (Minimal Editor)

# Project Overview
- Our goal is to develop a minimal text editor
- We will use Golang
- The only dependencies should be Gio (gioui.org), the official Go tree-sitter binding (github.com/tree-sitter/go-tree-sitter) and the official grammar packages from the tree-sitter GitHub organization (no third-party grammars). Everything else should be pure Go.
- The look follows the original GTK 3 version on GNOME (Yaru theme); keep it that way. `go test -run TestScreenshot` (see README) renders the window offscreen for comparing.

# Features
- A collapsible file tree explorer on the left and central window for file text editing
- Tabbed view so that multiple files can be open at once
- Line numbers in a gutter on the left of the editor window, with the current line emphasized
- Ctrl + F and Ctrl + Shift + F for single-file and project-wide search
- Ability to Ctrl + Click on symbols to Find Usages and Jump to Definition in every language with an official tree-sitter parser: Agda, Bash, C, C++, C#, CSS, ERB / EJS, Go, Haskell, HTML, Java, JavaScript, JSDoc, JSON, Julia, OCaml, PHP, Python, Regex, Ruby, Rust, Scala, TypeScript, Verilog
- A collapsible, tabbed Terminal Window creator at the bottom
- A collapsible Build window on the right that allows you to create and store named CLI commands and double click them to execute in the open terminal window
- A Settings window (Ctrl + 4) with theme options: Light, Dark, Tan, Solarized Dark, Rust, Green and Purple

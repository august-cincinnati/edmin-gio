package main

import (
	"strings"
)

// wordAt returns the identifier-like word under the cursor (for files
// without a tree-sitter grammar).
func wordAt(e *Editor) string {
	cur := e.View.Cursor()
	line := e.View.Line(cur.Line)
	s, en := wordBounds(line, cur.Col)
	return line[s:en]
}

// findWordRefs finds whole-word occurrences of name in data.
func findWordRefs(path string, data []byte, name string) []SymbolRef {
	var refs []SymbolRef
	isW := func(b byte) bool {
		return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b >= 0x80
	}
	for i, line := range strings.Split(string(data), "\n") {
		off := 0
		for {
			idx := strings.Index(line[off:], name)
			if idx < 0 {
				break
			}
			p := off + idx
			end := p + len(name)
			if (p == 0 || !isW(line[p-1])) && (end >= len(line) || !isW(line[end])) {
				refs = append(refs, SymbolRef{Path: path, Line: i, ColByte: p, Name: name, LineText: line})
			}
			off = end
		}
	}
	return refs
}

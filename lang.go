package main

import (
	"embed"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unsafe"

	ts "github.com/tree-sitter/go-tree-sitter"
	tsagda "github.com/tree-sitter/tree-sitter-agda/bindings/go"
	tsbash "github.com/tree-sitter/tree-sitter-bash/bindings/go"
	tscsharp "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
	tsc "github.com/tree-sitter/tree-sitter-c/bindings/go"
	tscpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	tscss "github.com/tree-sitter/tree-sitter-css/bindings/go"
	tsembedded "github.com/tree-sitter/tree-sitter-embedded-template/bindings/go"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tshaskell "github.com/tree-sitter/tree-sitter-haskell/bindings/go"
	tshtml "github.com/tree-sitter/tree-sitter-html/bindings/go"
	tsjava "github.com/tree-sitter/tree-sitter-java/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tsjsdoc "github.com/tree-sitter/tree-sitter-jsdoc/bindings/go"
	tsjson "github.com/tree-sitter/tree-sitter-json/bindings/go"
	tsjulia "github.com/tree-sitter/tree-sitter-julia/bindings/go"
	tsocaml "github.com/tree-sitter/tree-sitter-ocaml/bindings/go"
	tsphp "github.com/tree-sitter/tree-sitter-php/bindings/go"
	tspython "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsregex "github.com/tree-sitter/tree-sitter-regex/bindings/go"
	tsruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
	tsrust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tsscala "github.com/tree-sitter/tree-sitter-scala/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
	tsverilog "github.com/tree-sitter/tree-sitter-verilog/bindings/go"
)

// queryFS holds the official highlights/tags/locals queries of each grammar,
// copied from the grammar repositories (see queries/VERSIONS).
//
//go:embed queries
var queryFS embed.FS

// Language describes an official tree-sitter grammar and how EdMin uses it.
type Language struct {
	Name   string
	Family string   // languages whose files can reference each other's symbols
	Exts   []string // file extensions, including the dot
	Files  []string // exact file names (e.g. "Gemfile")
	ptr    func() unsafe.Pointer

	Highlights []string // query files, in precedence order
	Tags       []string // tags.scm files: @definition.* with @name
	Locals     []string // locals.scm files: @local.definition

	// DefParents are node kinds whose first identifier is a definition name,
	// for grammars whose definitions have no "name" field.
	DefParents map[string]bool
	// StringIdents treats string contents as symbols (e.g. JSON keys).
	StringIdents bool
	// Inject names the language of embedded code regions (ERB, EJS).
	Inject *Language

	once   sync.Once
	lang   *ts.Language
	hl     *ts.Query
	tags   *ts.Query
	locals *ts.Query
}

func set(kinds ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range kinds {
		m[k] = true
	}
	return m
}

func jsHighlights(jsx bool) []string {
	h := []string{"javascript/highlights.scm"}
	if jsx {
		h = append(h, "javascript/highlights-jsx.scm")
	}
	return h
}

var (
	langJS = &Language{Name: "JavaScript", Family: "js", Exts: []string{".js", ".mjs", ".cjs", ".jsx"}, ptr: tsjs.Language,
		Highlights: []string{"javascript/highlights.scm", "javascript/highlights-jsx.scm", "javascript/highlights-params.scm"},
		Tags:       []string{"javascript/tags.scm"}, Locals: []string{"javascript/locals.scm"}}
	langRuby = &Language{Name: "Ruby", Family: "ruby", Exts: []string{".rb", ".rake", ".gemspec"},
		Files: []string{"Rakefile", "Gemfile", "Guardfile", "Vagrantfile"}, ptr: tsruby.Language,
		Highlights: []string{"ruby/highlights.scm"}, Tags: []string{"ruby/tags.scm"}, Locals: []string{"ruby/locals.scm"}}
	// JSDoc and Regex have no files of their own; they are used for text
	// embedded in JavaScript-family comments and regex literals.
	langJSDoc = &Language{Name: "JSDoc", Family: "js", ptr: tsjsdoc.Language, Highlights: []string{"jsdoc/highlights.scm"}}
	langRegex = &Language{Name: "Regex", Family: "regex", ptr: tsregex.Language, Highlights: []string{"regex/highlights.scm"}}
)

var languages = []*Language{
	{Name: "Agda", Family: "agda", Exts: []string{".agda"}, ptr: tsagda.Language,
		Highlights: []string{"agda/highlights.scm"},
		DefParents: set("type_signature", "signature", "function", "data", "record", "module", "postulate", "fields")},
	{Name: "Bash", Family: "bash", Exts: []string{".sh", ".bash", ".ebuild", ".eclass"},
		Files: []string{".bashrc", ".bash_profile", ".profile"}, ptr: tsbash.Language,
		Highlights: []string{"bash/highlights.scm"}},
	{Name: "C", Family: "c", Exts: []string{".c", ".h"}, ptr: tsc.Language,
		Highlights: []string{"c/highlights.scm"}, Tags: []string{"c/tags.scm"}},
	{Name: "C++", Family: "c", Exts: []string{".cc", ".cpp", ".cxx", ".c++", ".hpp", ".hxx", ".hh", ".h++"}, ptr: tscpp.Language,
		Highlights: []string{"c/highlights.scm", "cpp/highlights.scm"}, Tags: []string{"cpp/tags.scm"}},
	{Name: "C#", Family: "csharp", Exts: []string{".cs"}, ptr: tscsharp.Language,
		Highlights: []string{"c-sharp/highlights.scm"}, Tags: []string{"c-sharp/tags.scm"}},
	{Name: "CSS", Family: "css", Exts: []string{".css"}, ptr: tscss.Language,
		Highlights: []string{"css/highlights.scm"}, DefParents: set("declaration", "keyframes_statement")},
	{Name: "ERB", Family: "ruby", Exts: []string{".erb"}, ptr: tsembedded.Language,
		Highlights: []string{"embedded-template/highlights.scm"}, Inject: langRuby},
	{Name: "EJS", Family: "js", Exts: []string{".ejs"}, ptr: tsembedded.Language,
		Highlights: []string{"embedded-template/highlights.scm"}, Inject: langJS},
	{Name: "Go", Family: "go", Exts: []string{".go"}, ptr: tsgo.Language,
		Highlights: []string{"go/highlights.scm"}, Tags: []string{"go/tags.scm"}},
	{Name: "Haskell", Family: "haskell", Exts: []string{".hs", ".hs-boot"}, ptr: tshaskell.Language,
		Highlights: []string{"haskell/highlights.scm"}, Locals: []string{"haskell/locals.scm"}},
	{Name: "HTML", Family: "html", Exts: []string{".html", ".htm", ".xhtml"}, ptr: tshtml.Language,
		Highlights: []string{"html/highlights.scm"}},
	{Name: "Java", Family: "java", Exts: []string{".java"}, ptr: tsjava.Language,
		Highlights: []string{"java/highlights.scm"}, Tags: []string{"java/tags.scm"}},
	langJS,
	{Name: "JSON", Family: "json", Exts: []string{".json", ".jsonc"}, ptr: tsjson.Language,
		Highlights: []string{"json/highlights.scm"}, StringIdents: true, DefParents: set("pair")},
	{Name: "Julia", Family: "julia", Exts: []string{".jl"}, ptr: tsjulia.Language,
		Highlights: []string{"julia/highlights.scm"}, Locals: []string{"julia/locals.scm"},
		DefParents: set("function_definition", "macro_definition", "struct_definition", "abstract_definition",
			"primitive_definition", "module_definition", "const_statement", "assignment")},
	{Name: "OCaml", Family: "ocaml", Exts: []string{".ml"}, ptr: tsocaml.LanguageOCaml,
		Highlights: []string{"ocaml/highlights.scm"}, Tags: []string{"ocaml/tags.scm"}, Locals: []string{"ocaml/locals.scm"}},
	{Name: "OCaml Interface", Family: "ocaml", Exts: []string{".mli"}, ptr: tsocaml.LanguageOCamlInterface,
		Highlights: []string{"ocaml/highlights.scm"}, Tags: []string{"ocaml/tags.scm"}, Locals: []string{"ocaml/locals.scm"}},
	{Name: "PHP", Family: "php", Exts: []string{".php", ".phtml"}, ptr: tsphp.LanguagePHP,
		Highlights: []string{"php/highlights.scm"}, Tags: []string{"php/tags.scm"}},
	{Name: "Python", Family: "python", Exts: []string{".py", ".pyi", ".pyw"}, ptr: tspython.Language,
		Highlights: []string{"python/highlights.scm"}, Tags: []string{"python/tags.scm"}},
	langRuby,
	{Name: "Rust", Family: "rust", Exts: []string{".rs"}, ptr: tsrust.Language,
		Highlights: []string{"rust/highlights.scm"}, Tags: []string{"rust/tags.scm"}},
	{Name: "Scala", Family: "scala", Exts: []string{".scala", ".sbt", ".sc"}, ptr: tsscala.Language,
		Highlights: []string{"scala/highlights.scm"}, Tags: []string{"scala/tags.scm"}, Locals: []string{"scala/locals.scm"}},
	{Name: "TypeScript", Family: "js", Exts: []string{".ts", ".mts", ".cts"}, ptr: tsts.LanguageTypescript,
		Highlights: append([]string{"typescript/highlights.scm"}, jsHighlights(false)...),
		Tags:       []string{"typescript/tags.scm", "javascript/tags.scm"},
		Locals:     []string{"typescript/locals.scm", "javascript/locals.scm"}},
	{Name: "TSX", Family: "js", Exts: []string{".tsx"}, ptr: tsts.LanguageTSX,
		Highlights: append([]string{"typescript/highlights.scm"}, jsHighlights(true)...),
		Tags:       []string{"typescript/tags.scm", "javascript/tags.scm"},
		Locals:     []string{"javascript/locals.scm"}},
	{Name: "Verilog", Family: "verilog", Exts: []string{".v", ".vh", ".sv", ".svh"}, ptr: tsverilog.Language,
		DefParents: set("module_header", "module_ansi_header", "module_nonansi_header", "interface_ansi_header", "interface_nonansi_header",
			"program_ansi_header", "program_nonansi_header", "package_declaration", "class_declaration",
			"function_body_declaration", "function_prototype", "task_body_declaration", "task_prototype",
			"net_decl_assignment", "variable_decl_assignment", "param_assignment", "specparam_assignment",
			"ansi_port_declaration", "tf_port_item", "type_declaration", "enum_name_declaration",
			"genvar_declaration", "let_declaration", "modport_item", "clocking_declaration",
			"covergroup_declaration", "constraint_declaration", "checker_declaration", "udp_declaration",
			"list_of_port_identifiers", "list_of_variable_port_identifiers", "list_of_variable_identifiers",
			"list_of_genvar_identifiers", "list_of_interface_identifiers", "port")},
}

func languageFor(path string) *Language {
	base := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(path))
	for _, l := range languages {
		for _, f := range l.Files {
			if f == base {
				return l
			}
		}
	}
	for _, l := range languages {
		for _, e := range l.Exts {
			if e == ext {
				return l
			}
		}
	}
	return nil
}

func (l *Language) init() {
	l.once.Do(func() {
		l.lang = ts.NewLanguage(l.ptr())
		l.hl = compileQuery(l.lang, l.Highlights)
		l.tags = compileQuery(l.lang, l.Tags)
		l.locals = compileQuery(l.lang, l.Locals)
	})
}

// compileQuery concatenates query files and compiles them. Patterns that
// fail to compile (e.g. a #match? regex using syntax Go's regexp lacks)
// are dropped one at a time rather than discarding the whole query.
func compileQuery(lang *ts.Language, files []string) *ts.Query {
	if len(files) == 0 {
		return nil
	}
	var sb strings.Builder
	for _, f := range files {
		data, err := queryFS.ReadFile("queries/" + f)
		if err == nil {
			sb.Write(data)
			sb.WriteString("\n")
		}
	}
	src := sb.String()
	for tries := 0; tries < 200 && strings.TrimSpace(src) != ""; tries++ {
		q, qerr := ts.NewQuery(lang, src)
		if qerr == nil {
			return q
		}
		start, end, ok := topLevelForm(src, int(qerr.Offset))
		if !ok {
			return nil
		}
		src = src[:start] + src[end:]
	}
	return nil
}

// topLevelForm finds the top-level query pattern containing byte offset off.
func topLevelForm(src string, off int) (start, end int, ok bool) {
	var forms [][2]int
	depth, formStart := 0, -1
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == ';':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '"':
			for i++; i < len(src) && src[i] != '"'; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		case c == '(' || c == '[':
			if depth == 0 {
				if formStart >= 0 {
					forms = append(forms, [2]int{formStart, i})
				}
				formStart = i
			}
			depth++
		case c == ')' || c == ']':
			depth--
		}
	}
	if formStart >= 0 {
		forms = append(forms, [2]int{formStart, len(src)})
	}
	if off >= len(src) {
		off = len(src) - 1
	}
	for _, f := range forms {
		if off >= f[0] && off < f[1] {
			return f[0], f[1], true
		}
	}
	return 0, 0, false
}

// parse parses src; the caller must Close the tree.
func (l *Language) parse(src []byte, ranges []ts.Range) *ts.Tree {
	l.init()
	p := ts.NewParser()
	defer p.Close()
	if err := p.SetLanguage(l.lang); err != nil {
		return nil
	}
	if len(ranges) > 0 {
		if err := p.SetIncludedRanges(ranges); err != nil {
			return nil
		}
	}
	return p.Parse(src, nil)
}

// codeTree parses the embedded code of a template (ERB/EJS) with the
// injected language, keeping positions relative to the whole file. For
// other languages it returns the normal parse. The caller closes the tree.
func (l *Language) codeTree(src []byte) (*ts.Tree, *Language) {
	if l.Inject == nil {
		return l.parse(src, nil), l
	}
	tmpl := l.parse(src, nil)
	if tmpl == nil {
		return nil, l.Inject
	}
	defer tmpl.Close()
	var ranges []ts.Range
	walk(tmpl.RootNode(), func(n *ts.Node) bool {
		if n.Kind() == "code" {
			ranges = append(ranges, n.Range())
			return false
		}
		return true
	})
	if len(ranges) == 0 {
		return nil, l.Inject
	}
	return l.Inject.parse(src, ranges), l.Inject
}

// walk visits n and its descendants in document order; fn returns false
// to skip a node's children.
func walk(n *ts.Node, fn func(*ts.Node) bool) {
	if !fn(n) {
		return
	}
	for i := uint(0); i < n.ChildCount(); i++ {
		if c := n.Child(i); c != nil {
			walk(c, fn)
		}
	}
}

// ---- Highlighting ----

const (
	hlKeyword  = "hl-keyword"
	hlString   = "hl-string"
	hlComment  = "hl-comment"
	hlNumber   = "hl-number"
	hlType     = "hl-type"
	hlFunction = "hl-function"
	hlConstant = "hl-constant"
	hlProperty = "hl-property"
)

type Span struct {
	StartRow, StartCol, EndRow, EndCol int // tree-sitter points (column in bytes)
	Class                              string
}

// captureClass maps a highlights.scm capture name to an editor tag.
func captureClass(name string) string {
	first, rest, _ := strings.Cut(name, ".")
	switch first {
	case "keyword", "conditional", "repeat", "include", "exception", "storageclass", "tag":
		if first == "tag" {
			return hlType
		}
		return hlKeyword
	case "string", "character":
		if strings.HasPrefix(rest, "escape") || strings.HasPrefix(rest, "special.symbol") {
			return hlConstant
		}
		return hlString
	case "escape":
		return hlConstant
	case "comment":
		return hlComment
	case "number", "float":
		return hlNumber
	case "boolean", "constant":
		return hlConstant
	case "type", "constructor", "namespace", "module":
		return hlType
	case "function", "method", "attribute":
		return hlFunction
	case "property", "field":
		return hlProperty
	case "variable":
		if strings.HasPrefix(rest, "builtin") {
			return hlConstant
		}
		if strings.HasPrefix(rest, "member") {
			return hlProperty
		}
	case "label":
		return hlType
	}
	return ""
}

// Highlight computes highlight spans for src using the grammar's official
// highlights.scm (and the injected language's, for templates).
func (l *Language) Highlight(src []byte) []Span {
	l.init()
	var spans []Span
	if l.hl != nil {
		if tree := l.parse(src, nil); tree != nil {
			spans = append(spans, runHighlights(l.hl, tree, src)...)
			tree.Close()
		}
	} else if tree := l.parse(src, nil); tree != nil {
		spans = append(spans, genericHighlight(tree, src)...)
		tree.Close()
	}
	if l.Inject != nil {
		l.Inject.init()
		if tree, inj := l.codeTree(src); tree != nil {
			if inj.hl != nil {
				spans = append(spans, runHighlights(inj.hl, tree, src)...)
			}
			tree.Close()
		}
	}
	return spans
}

func runHighlights(q *ts.Query, tree *ts.Tree, src []byte) []Span {
	names := q.CaptureNames()
	classes := make([]string, len(names))
	for i, n := range names {
		if !strings.HasPrefix(n, "_") {
			classes[i] = captureClass(n)
		}
	}
	qc := ts.NewQueryCursor()
	defer qc.Close()
	claimed := map[[2]uint]bool{} // the first pattern to match a node wins
	var spans []Span
	caps := qc.Captures(q, tree.RootNode(), src)
	for {
		m, idx := caps.Next()
		if m == nil {
			break
		}
		c := m.Captures[idx]
		key := [2]uint{c.Node.StartByte(), c.Node.EndByte()}
		if claimed[key] {
			continue
		}
		claimed[key] = true
		if cls := classes[c.Index]; cls != "" {
			s, e := c.Node.StartPosition(), c.Node.EndPosition()
			spans = append(spans, Span{int(s.Row), int(s.Column), int(e.Row), int(e.Column), cls})
		}
	}
	return spans
}

var (
	genericString  = regexp.MustCompile(`string|char|text_block`)
	genericNumber  = regexp.MustCompile(`number|integer|float|decimal|_literal$`)
	genericComment = regexp.MustCompile(`comment`)
)

// genericHighlight is used for grammars that ship no highlights.scm
// (Verilog): comments, strings, numbers and keyword-like anonymous tokens.
func genericHighlight(tree *ts.Tree, src []byte) []Span {
	var spans []Span
	add := func(n *ts.Node, cls string) {
		s, e := n.StartPosition(), n.EndPosition()
		spans = append(spans, Span{int(s.Row), int(s.Column), int(e.Row), int(e.Column), cls})
	}
	walk(tree.RootNode(), func(n *ts.Node) bool {
		k := n.Kind()
		switch {
		case n.IsNamed() && genericComment.MatchString(k):
			add(n, hlComment)
			return false
		case n.IsNamed() && genericString.MatchString(k):
			add(n, hlString)
			return false
		case n.IsNamed() && genericNumber.MatchString(k):
			add(n, hlNumber)
			return false
		case !n.IsNamed() && n.ChildCount() == 0 && isKeywordToken(k):
			add(n, hlKeyword)
		}
		return true
	})
	return spans
}

func isKeywordToken(s string) bool {
	if len(s) < 2 {
		return false
	}
	for _, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// ---- Symbols ----

// SymbolRef is one occurrence of an identifier.
type SymbolRef struct {
	Path     string
	Line     int // 0-based
	ColByte  int // 0-based byte column
	Name     string
	IsDef    bool
	LineText string
}

// Symbol is the result of looking up the identifier under the cursor.
type Symbol struct {
	Name  string
	IsDef bool
	// Local, when non-nil, holds complete results for symbols that are
	// scoped to a single embedded region (regex named groups) and need no
	// project-wide search.
	Local []SymbolRef
}

var nonSymbolKind = regexp.MustCompile(`comment|string|char|number|integer|float|literal|escape|content|fragment|regex|text|heredoc`)

// isSymbolNode reports whether n is an identifier-like leaf.
func (l *Language) isSymbolNode(n *ts.Node, src []byte) bool {
	if !n.IsNamed() || n.ChildCount() != 0 {
		return false
	}
	k := n.Kind()
	if nonSymbolKind.MatchString(k) && !(l.StringIdents && k == "string_content") {
		return false
	}
	return isIdentText(n.Utf8Text(src))
}

// isIdentText accepts identifier-like text in any language: no spaces,
// brackets or quotes, and at least one letter or underscore.
func isIdentText(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	letter := false
	for _, r := range s {
		if unicode.IsSpace(r) || strings.ContainsRune("()[]{}\"`,;", r) {
			return false
		}
		if r == '_' || unicode.IsLetter(r) {
			letter = true
		}
	}
	return letter
}

// definitions returns the start bytes of every definition name in tree,
// combining the grammar's tags.scm and locals.scm with structural rules.
func (l *Language) definitions(tree *ts.Tree, src []byte) map[uint]bool {
	defs := map[uint]bool{}
	root := tree.RootNode()
	if l.tags != nil {
		names := l.tags.CaptureNames()
		qc := ts.NewQueryCursor()
		ms := qc.Matches(l.tags, root, src)
		for m := ms.Next(); m != nil; m = ms.Next() {
			isDef := false
			for _, c := range m.Captures {
				if strings.HasPrefix(names[c.Index], "definition.") {
					isDef = true
				}
			}
			if isDef {
				for _, c := range m.Captures {
					if names[c.Index] == "name" {
						defs[c.Node.StartByte()] = true
					}
				}
			}
		}
		qc.Close()
	}
	if l.locals != nil {
		names := l.locals.CaptureNames()
		qc := ts.NewQueryCursor()
		caps := qc.Captures(l.locals, root, src)
		for {
			m, idx := caps.Next()
			if m == nil {
				break
			}
			c := m.Captures[idx]
			if strings.HasPrefix(names[c.Index], "local.definition") {
				// The capture may wrap the identifier (e.g. a pattern node).
				n := c.Node
				walk(&n, func(x *ts.Node) bool {
					if l.isSymbolNode(x, src) {
						defs[x.StartByte()] = true
						return false
					}
					return true
				})
			}
		}
		qc.Close()
	}
	walk(root, func(n *ts.Node) bool {
		if l.isSymbolNode(n, src) && l.structuralDef(n, src) {
			defs[n.StartByte()] = true
		}
		return true
	})
	return defs
}

func sameNode(a, b *ts.Node) bool {
	return a != nil && b != nil && a.StartByte() == b.StartByte() && a.EndByte() == b.EndByte() && a.KindId() == b.KindId()
}

func fieldOf(parent, child *ts.Node) string {
	for i := uint(0); i < parent.ChildCount(); i++ {
		if sameNode(parent.Child(i), child) {
			return parent.FieldNameForChild(uint32(i))
		}
	}
	return ""
}

// nameFieldExcluded lists node kinds whose "name" field is a reference.
var nameFieldExcluded = set("call_expression", "selector_expression", "keyed_element", "method_invocation",
	"field_access", "labeled_statement", "attribute", "member_expression", "command", "call",
	"generic_name", "scoped_identifier", "qualified_name", "named_argument", "argument",
	"qualified_identifier", "jsx_opening_element", "jsx_closing_element", "jsx_self_closing_element",
	"member_access_expression", "member_binding_expression", "member_call_expression",
	"nullsafe_member_call_expression", "nullsafe_member_access_expression", "scoped_call_expression")

// structuralDef recognises definitions from the syntax tree shape.
func (l *Language) structuralDef(n *ts.Node, src []byte) bool {
	p := n.Parent()
	if p == nil {
		return false
	}
	switch fieldOf(p, n) {
	case "name":
		if !nameFieldExcluded[p.Kind()] {
			return true
		}
	case "declarator":
		return true
	case "left":
		switch p.Kind() {
		case "assignment", "for_statement", "for_in_statement", "variable_assignment":
			return true
		}
	case "pattern":
		switch p.Kind() {
		case "let_declaration", "parameter", "closure_parameters":
			return true
		}
	}
	switch p.Kind() {
	case "expression_list", "pattern_list", "identifier_list":
		gp := p.Parent()
		if gp != nil && fieldOf(gp, p) == "left" {
			switch gp.Kind() {
			case "short_var_declaration", "range_clause", "assignment":
				return true
			}
		}
	case "parameters", "typed_parameter", "default_parameter", "typed_default_parameter", "formal_parameters",
		"pointer_declarator", "array_declarator", "reference_declarator", "parameter_declaration", "variadic_parameter_declaration":
		// A parameter's type (e.g. Go's `x bool` or an unnamed result type) is a reference.
		return fieldOf(p, n) != "type"
	}
	if len(l.DefParents) > 0 {
		for a, depth := p, 0; a != nil && depth < 5; a, depth = a.Parent(), depth+1 {
			if !l.DefParents[a.Kind()] {
				continue
			}
			if strings.HasPrefix(a.Kind(), "list_of_") {
				return true
			}
			// The first identifier inside the declaration is its name.
			var first *ts.Node
			walk(a, func(x *ts.Node) bool {
				if first != nil {
					return false
				}
				if l.isSymbolNode(x, src) {
					c := *x
					first = &c
					return false
				}
				return true
			})
			return first != nil && sameNode(first, n)
		}
	}
	return false
}

// SymbolAt returns the symbol at a 0-based line and byte column.
func (l *Language) SymbolAt(src []byte, row, colByte int) (Symbol, bool) {
	tree, lang := l.codeTree(src)
	if tree == nil {
		return Symbol{}, false
	}
	defer tree.Close()
	lang.init()
	root := tree.RootNode()
	at := func(r, c int) *ts.Node {
		pt := ts.Point{Row: uint(r), Column: uint(max(c, 0))}
		return root.NamedDescendantForPointRange(pt, pt)
	}
	n := at(row, colByte)
	if n != nil && !lang.isSymbolNode(n, src) && colByte > 0 {
		// The click may sit just past the end of the word.
		if prev := at(row, colByte-1); prev != nil && lang.isSymbolNode(prev, src) {
			n = prev
		}
	}
	if n == nil {
		return Symbol{}, false
	}
	if lang.Family == "js" {
		if s, ok := jsEmbeddedSymbol(n, src, row, colByte); ok {
			return s, true
		}
	}
	if !lang.isSymbolNode(n, src) {
		return Symbol{}, false
	}
	defs := lang.definitions(tree, src)
	return Symbol{Name: n.Utf8Text(src), IsDef: defs[n.StartByte()]}, true
}

// FindRefs lists every occurrence of name in src, marking definitions.
func (l *Language) FindRefs(path string, src []byte, name string) []SymbolRef {
	if !strings.Contains(string(src), name) {
		return nil
	}
	tree, lang := l.codeTree(src)
	if tree == nil {
		return nil
	}
	defer tree.Close()
	lang.init()
	defs := lang.definitions(tree, src)
	lines := strings.Split(string(src), "\n")
	var refs []SymbolRef
	walk(tree.RootNode(), func(n *ts.Node) bool {
		if lang.isSymbolNode(n, src) && n.Utf8Text(src) == name {
			refs = append(refs, makeRef(path, lines, n.StartPosition(), name, defs[n.StartByte()]))
		}
		return true
	})
	return refs
}

func makeRef(path string, lines []string, p ts.Point, name string, isDef bool) SymbolRef {
	lt := ""
	if int(p.Row) < len(lines) {
		lt = lines[p.Row]
	}
	return SymbolRef{Path: path, Line: int(p.Row), ColByte: int(p.Column), Name: name, IsDef: isDef, LineText: lt}
}

// ---- Embedded languages in JavaScript-family files ----

// jsEmbeddedSymbol handles clicks inside JSDoc comments (a type name,
// which is then looked up across the project) and inside regex literals
// (named capture groups, resolved within the regex).
func jsEmbeddedSymbol(n *ts.Node, src []byte, row, colByte int) (Symbol, bool) {
	switch {
	case n.Kind() == "comment" && strings.HasPrefix(n.Utf8Text(src), "/**"):
		sub, ok := subNodeAt(langJSDoc, n, src, row, colByte)
		if !ok {
			return Symbol{}, false
		}
		defer sub.tree.Close()
		switch sub.node.Kind() {
		case "identifier":
			return Symbol{Name: sub.node.Utf8Text(sub.src)}, true
		case "type":
			// A type expression such as "Array<Foo>": take the word clicked.
			start := sub.toFile(sub.node.StartPosition())
			off := colByte - int(start.Column)
			if uint(row) != start.Row {
				return Symbol{}, false
			}
			if w := wordAround(sub.node.Utf8Text(sub.src), off); w != "" {
				return Symbol{Name: w}, true
			}
		}
		return Symbol{}, false
	case n.Kind() == "regex_pattern":
		sub, ok := subNodeAt(langRegex, n, src, row, colByte)
		if !ok {
			return Symbol{}, false
		}
		defer sub.tree.Close()
		if sub.node.Kind() != "group_name" {
			return Symbol{}, false
		}
		name := sub.node.Utf8Text(sub.src)
		lines := strings.Split(string(src), "\n")
		var refs []SymbolRef
		clickedDef := false
		walk(sub.tree.RootNode(), func(x *ts.Node) bool {
			if x.Kind() == "group_name" && x.Utf8Text(sub.src) == name {
				isDef := x.Parent() != nil && x.Parent().Kind() == "named_capturing_group"
				if sameNode(x, sub.node) {
					clickedDef = isDef
				}
				refs = append(refs, makeRef("", lines, sub.toFile(x.StartPosition()), name, isDef))
			}
			return true
		})
		return Symbol{Name: name, IsDef: clickedDef, Local: refs}, true
	}
	return Symbol{}, false
}

// wordAround returns the identifier word in s containing byte offset off.
func wordAround(s string, off int) string {
	isW := func(b byte) bool {
		return b == '_' || b == '$' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b >= 0x80
	}
	if off < 0 || off > len(s) {
		return ""
	}
	a, b := off, off
	for a > 0 && isW(s[a-1]) {
		a--
	}
	for b < len(s) && isW(s[b]) {
		b++
	}
	if w := s[a:b]; isIdentText(w) {
		return w
	}
	return ""
}

type subParse struct {
	tree   *ts.Tree
	node   *ts.Node
	src    []byte
	origin ts.Point
}

// toFile converts a position in the embedded text to a file position.
func (s subParse) toFile(p ts.Point) ts.Point {
	if p.Row == 0 {
		return ts.Point{Row: s.origin.Row, Column: s.origin.Column + p.Column}
	}
	return ts.Point{Row: s.origin.Row + p.Row, Column: p.Column}
}

// subNodeAt parses the text of host with lang and returns the named node
// at the given file position.
func subNodeAt(lang *Language, host *ts.Node, src []byte, row, colByte int) (subParse, bool) {
	text := []byte(host.Utf8Text(src))
	origin := host.StartPosition()
	tree := lang.parse(text, nil)
	if tree == nil {
		return subParse{}, false
	}
	r, c := uint(row)-origin.Row, uint(colByte)
	if r == 0 {
		c -= origin.Column
	}
	pt := ts.Point{Row: r, Column: c}
	n := tree.RootNode().NamedDescendantForPointRange(pt, pt)
	if n == nil {
		tree.Close()
		return subParse{}, false
	}
	return subParse{tree: tree, node: n, src: text, origin: origin}, true
}

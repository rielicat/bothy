package knowledge

import (
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const (
	// chunkerVersion prefixes stored file hashes: bumping it re-chunks every
	// file at the next index although its content did not change.
	chunkerVersion = "c1"

	chunkMaxLines = 120 // longer chunks are split into windows
	chunkWindow   = 80
	chunkMaxBytes = 8 << 10
	textWindow    = 60 // plain-text chunks close at a blank line after half of this
	tinyLines     = 3  // chunks with fewer non-blank lines merge into the previous one
	mergeMaxLines = 40
	lineMaxBytes  = 2048 // longer lines (minified code) are cut in the stored body
	titleMax      = 120
	termsMax      = 4 << 10
)

// span is a chunk before rendering: lines [start, end), 0-based.
type span struct {
	start, end  int
	kind, title string
}

// declRule recognises a declaration line. A parent rule (class, impl,
// module) opens a container whose member rules (methods) apply until a
// top-level line closes it; members are titled "Parent: signature".
type declRule struct {
	re     *regexp.Regexp
	kind   string
	name   int // submatch holding the declared name
	parent bool
	member bool
	guard  bool // keyword-less pattern: control-flow words are not names
}

type langSpec struct {
	name   string
	decls  []declRule
	attach *regexp.Regexp // comment, decorator and attribute lines that belong to the next declaration
	split  func(lines []string) []span
}

func re(s string) *regexp.Regexp { return regexp.MustCompile(s) }

// notName are control-flow words a keyword-less signature pattern can catch.
var notName = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true, "return": true, "else": true, "do": true,
	"using": true, "lock": true, "foreach": true, "synchronized": true, "when": true, "guard": true, "with": true,
	"sizeof": true, "case": true, "function": true, "new": true, "throw": true, "await": true, "yield": true,
}

var (
	goSpec = &langSpec{name: "go", attach: re(`^\s*(//|/\*|\*)`), decls: []declRule{
		{re: re(`^func\s*\([^)]*\)\s*([A-Za-z_]\w*)`), kind: "method", name: 1},
		{re: re(`^func\s+([A-Za-z_]\w*)`), kind: "func", name: 1},
		{re: re(`^type\s+([A-Za-z_]\w*)`), kind: "type", name: 1},
		{re: re(`^type\s*\(`), kind: "type"},
		{re: re(`^(?:var|const)\s*\(`), kind: "var"},
		{re: re(`^(?:var|const)\s+([A-Za-z_]\w*)`), kind: "var", name: 1},
	}}
	pythonSpec = &langSpec{name: "python", attach: re(`^\s*(@|#)`), decls: []declRule{
		{re: re(`^(?:async\s+)?def\s+([A-Za-z_]\w*)`), kind: "func", name: 1},
		{re: re(`^class\s+([A-Za-z_]\w*)`), kind: "class", name: 1, parent: true},
		{re: re(`^(?: {2}| {4}|\t)(?:async\s+)?def\s+([A-Za-z_]\w*)`), kind: "method", name: 1, member: true},
	}}
	jsDecls = []declRule{
		{re: re(`^(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)`), kind: "func", name: 1},
		{re: re(`^(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+([A-Za-z_$][\w$]*)`), kind: "class", name: 1, parent: true},
		{re: re(`^(?:export\s+)?(?:declare\s+)?(?:interface|type|enum)\s+([A-Za-z_$][\w$]*)`), kind: "type", name: 1},
		{re: re(`^(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)`), kind: "const", name: 1},
		{re: re(`^export\s+default\b`), kind: "export"},
		{re: re(`^(?: {2}| {4}|\t)(?:(?:public|private|protected|static|async|readonly|override|get|set)\s+)*(#?[A-Za-z_$][\w$]*)\s*(?:<[^>]*>)?\s*\([^;]*\)\s*(?::[^;{]*)?\{\s*$`), kind: "method", name: 1, member: true, guard: true},
	}
	rustSpec = &langSpec{name: "rust", attach: re(`^\s*(//|#\[|#!\[)`), decls: []declRule{
		{re: re(`^(?:pub(?:\([^)]*\))?\s+)?(?:const\s+)?(?:async\s+)?(?:unsafe\s+)?(?:extern\s+"[^"]*"\s+)?fn\s+([A-Za-z_]\w*)`), kind: "func", name: 1},
		{re: re(`^(?:pub(?:\([^)]*\))?\s+)?(?:struct|enum|union|type)\s+([A-Za-z_]\w*)`), kind: "type", name: 1},
		{re: re(`^(?:pub(?:\([^)]*\))?\s+)?(?:unsafe\s+)?trait\s+([A-Za-z_]\w*)`), kind: "trait", name: 1, parent: true},
		{re: re(`^(?:unsafe\s+)?impl\b(?:\s*<[^>]*>)?\s*([^{]*?)\s*(?:\{.*)?$`), kind: "impl", name: 1, parent: true},
		{re: re(`^(?:pub(?:\([^)]*\))?\s+)?mod\s+([A-Za-z_]\w*)`), kind: "mod", name: 1},
		{re: re(`^macro_rules!\s*([A-Za-z_]\w*)`), kind: "macro", name: 1},
		{re: re(`^(?:pub(?:\([^)]*\))?\s+)?(?:const|static)\s+([A-Za-z_]\w*)`), kind: "const", name: 1},
		{re: re(`^(?: {4}|\t)(?:pub(?:\([^)]*\))?\s+)?(?:const\s+)?(?:async\s+)?(?:unsafe\s+)?fn\s+([A-Za-z_]\w*)`), kind: "method", name: 1, member: true},
	}}
	classDecls = []declRule{
		{re: re(`^(?:(?:public|private|protected|internal|abstract|final|sealed|static|open|data|partial|inline|value|enum|annotation|fileprivate|export)\s+)*(?:class|interface|enum|record|struct|object|trait|protocol|extension)\s+([A-Za-z_]\w*)`), kind: "class", name: 1, parent: true},
		{re: re(`^(?:(?:public|private|internal|protected|static|suspend|inline|override|open|final|async)\s+)*(?:fun|func|def|function)\s+(?:<[^>]*>\s*)?([A-Za-z_][\w.]*)`), kind: "func", name: 1},
		{re: re(`^(?: {2}| {4}|\t)(?:(?:public|private|protected|internal|static|final|abstract|sealed|data|open|inner)\s+)*(?:class|interface|enum|record|struct|object)\s+([A-Za-z_]\w*)`), kind: "class", name: 1, member: true},
		{re: re(`^(?: {2}| {4}|\t)(?:(?:public|private|protected|internal|static|final|abstract|override|virtual|async|suspend|open|inline|synchronized|native|operator|fileprivate|mutating|sealed|new|extern|unsafe|partial|default)\s+)*(?:fun\s+|func\s+|def\s+|function\s+)?(?:<[^>]*>\s*)?(?:[\w<>\[\],.?]+\s+)?([A-Za-z_]\w*)\s*\([^;]*$`), kind: "method", name: 1, member: true, guard: true},
	}
	classAttach = re(`^\s*(//|/\*|\*|@|#\[|\[[A-Z])`)
	cSpec       = &langSpec{name: "c", attach: re(`^\s*(//|/\*|\*|template\s*<)`), decls: []declRule{
		{re: re(`^(?:typedef\s+)?(?:struct|union|enum|class)\s+([A-Za-z_]\w*)[^;]*$`), kind: "type", name: 1},
		{re: re(`^namespace\s+([A-Za-z_][\w:]*)`), kind: "namespace", name: 1},
		{re: re(`^#define\s+([A-Za-z_]\w*)`), kind: "macro", name: 1},
		{re: re(`^(?:(?:static|inline|extern|virtual|constexpr|const|unsigned|signed|struct|enum)\s+)*[A-Za-z_][\w:<>,\s*&]*?[\s*&]([A-Za-z_~][\w:~]*)\s*\([^;]*$`), kind: "func", name: 1, guard: true},
	}}
	rubySpec = &langSpec{name: "ruby", attach: re(`^\s*#`), decls: []declRule{
		{re: re(`^(?:class|module)\s+([A-Z][\w:]*)`), kind: "class", name: 1, parent: true},
		{re: re(`^def\s+(?:self\.)?([\w?!=]+)`), kind: "func", name: 1},
		{re: re(`^(?: {2}|\t)def\s+(?:self\.)?([\w?!=]+)`), kind: "method", name: 1, member: true},
		{re: re(`^(?: {2}|\t)(?:class|module)\s+([A-Z][\w:]*)`), kind: "class", name: 1, member: true},
	}}
	phpSpec = &langSpec{name: "php", attach: re(`^\s*(//|/\*|\*|#\[)`), decls: []declRule{
		{re: re(`^(?:(?:abstract|final|readonly)\s+)*(?:class|interface|trait|enum)\s+(\w+)`), kind: "class", name: 1, parent: true},
		{re: re(`^function\s+&?(\w+)`), kind: "func", name: 1},
		{re: re(`^(?: {4}|\t)(?:(?:public|private|protected|static|abstract|final)\s+)*function\s+&?(\w+)`), kind: "method", name: 1, member: true},
	}}
	shellSpec = &langSpec{name: "shell", attach: re(`^\s*#($|[^!])`), decls: []declRule{
		{re: re(`^function\s+([A-Za-z_][\w:.-]*)`), kind: "func", name: 1},
		{re: re(`^([A-Za-z_][\w:.-]*)\s*\(\)`), kind: "func", name: 1},
	}}
	luaSpec = &langSpec{name: "lua", attach: re(`^\s*--`), decls: []declRule{
		{re: re(`^(?:local\s+)?function\s+([\w.:]+)`), kind: "func", name: 1},
	}}
	elixirSpec = &langSpec{name: "elixir", attach: re(`^\s*(#|@doc|@moduledoc|@spec|@impl)`), decls: []declRule{
		{re: re(`^defmodule\s+([\w.]+)`), kind: "module", name: 1, parent: true},
		{re: re(`^(?: {2}|\t)(?:defp?|defmacrop?)\s+([\w?!]+)`), kind: "func", name: 1, member: true},
	}}
	sqlSpec = &langSpec{name: "sql", attach: re(`^\s*--`), decls: []declRule{
		{re: re(`(?i)^\s*(?:create|alter)\s+(?:or\s+replace\s+)?(?:temp(?:orary)?\s+)?(?:unique\s+)?(?:virtual\s+)?(?:table|view|index|trigger|function|procedure|type|schema|sequence)\s+(?:if\s+not\s+exists\s+)?([\w."]+)`), kind: "ddl", name: 1},
	}}
	makefileSpec = &langSpec{name: "make", attach: re(`^#`), decls: []declRule{
		{re: re(`^([A-Za-z0-9_./%$(){}-][A-Za-z0-9_./%$(){} -]*?)\s*::?(?:\s|$)`), kind: "target", name: 1},
	}}
	dockerSpec   = &langSpec{name: "dockerfile", attach: re(`^#`), decls: []declRule{{re: re(`(?i)^FROM\s+(\S+)`), kind: "stage", name: 1}}}
	markdownSpec = &langSpec{name: "markdown", split: markdownSpans}
	iniSpec      = &langSpec{name: "ini", attach: re(`^\s*[#;]`), decls: []declRule{{re: re(`^\s*\[([^\]]+)\]\s*$`), kind: "section", name: 1}}}
	yamlSpec     = &langSpec{name: "yaml", attach: re(`^#`), decls: []declRule{
		{re: re(`^---`), kind: "document"},
		{re: re(`^([A-Za-z0-9_"'][^#]*?):(?:\s|$)`), kind: "section", name: 1},
	}}
	textSpec = &langSpec{name: "text", split: windowSpans}
)

func lang(name string, decls []declRule, attach *regexp.Regexp) *langSpec {
	return &langSpec{name: name, decls: decls, attach: attach}
}

var byExt = map[string]*langSpec{
	".go": goSpec, ".py": pythonSpec, ".pyi": pythonSpec,
	".js": lang("javascript", jsDecls, re(`^\s*(//|/\*|\*|@)`)), ".jsx": lang("javascript", jsDecls, re(`^\s*(//|/\*|\*|@)`)),
	".mjs": lang("javascript", jsDecls, re(`^\s*(//|/\*|\*|@)`)), ".cjs": lang("javascript", jsDecls, re(`^\s*(//|/\*|\*|@)`)),
	".ts": lang("typescript", jsDecls, re(`^\s*(//|/\*|\*|@)`)), ".tsx": lang("typescript", jsDecls, re(`^\s*(//|/\*|\*|@)`)),
	".mts": lang("typescript", jsDecls, re(`^\s*(//|/\*|\*|@)`)), ".cts": lang("typescript", jsDecls, re(`^\s*(//|/\*|\*|@)`)),
	".rs": rustSpec, ".java": lang("java", classDecls, classAttach), ".kt": lang("kotlin", classDecls, classAttach),
	".kts": lang("kotlin", classDecls, classAttach), ".cs": lang("csharp", classDecls, classAttach),
	".scala": lang("scala", classDecls, classAttach), ".swift": lang("swift", classDecls, classAttach),
	".dart": lang("dart", classDecls, classAttach), ".groovy": lang("groovy", classDecls, classAttach),
	".c": cSpec, ".h": cSpec, ".cc": lang("cpp", cSpec.decls, cSpec.attach), ".cpp": lang("cpp", cSpec.decls, cSpec.attach),
	".cxx": lang("cpp", cSpec.decls, cSpec.attach), ".hpp": lang("cpp", cSpec.decls, cSpec.attach),
	".hh": lang("cpp", cSpec.decls, cSpec.attach), ".hxx": lang("cpp", cSpec.decls, cSpec.attach),
	".m": lang("objc", cSpec.decls, cSpec.attach), ".mm": lang("objc", cSpec.decls, cSpec.attach),
	".rb": rubySpec, ".rake": rubySpec, ".gemspec": rubySpec, ".php": phpSpec,
	".sh": shellSpec, ".bash": shellSpec, ".zsh": shellSpec, ".lua": luaSpec, ".ex": elixirSpec, ".exs": elixirSpec,
	".sql": sqlSpec, ".mk": makefileSpec, ".dockerfile": dockerSpec,
	".md": markdownSpec, ".markdown": markdownSpec, ".mdx": markdownSpec, ".mdown": markdownSpec,
	".toml": lang("toml", iniSpec.decls, iniSpec.attach), ".ini": iniSpec, ".cfg": iniSpec, ".conf": iniSpec,
	".editorconfig": iniSpec, ".yaml": yamlSpec, ".yml": yamlSpec,
	".json": {name: "json", split: windowSpans}, ".html": {name: "html", split: windowSpans},
	".css": {name: "css", split: windowSpans}, ".scss": {name: "scss", split: windowSpans},
	".svelte": {name: "svelte", split: windowSpans}, ".vue": {name: "vue", split: windowSpans},
}

var byName = map[string]*langSpec{
	"Makefile": makefileSpec, "makefile": makefileSpec, "GNUmakefile": makefileSpec, "Dockerfile": dockerSpec,
	"Containerfile": dockerSpec, "Rakefile": rubySpec, "Gemfile": rubySpec, "Justfile": makefileSpec, "justfile": makefileSpec,
}

// langOf picks the chunker for a slash path; unknown text gets windows.
func langOf(p string) *langSpec {
	base := path.Base(p)
	if l, ok := byName[base]; ok {
		return l
	}
	if l, ok := byExt[strings.ToLower(path.Ext(base))]; ok {
		return l
	}
	if strings.HasPrefix(base, "Dockerfile.") {
		return dockerSpec
	}
	return textSpec
}

// splitLines splits on "\n" (dropping a trailing "\r") without a final empty
// line for a trailing newline, so index i is line i+1 of the file.
func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }

func indented(s string) bool { return s != "" && (s[0] == ' ' || s[0] == '\t') }

// lineTitle turns a declaration or heading line into a one-line title.
func lineTitle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSpace(strings.TrimSuffix(s, "{"))
	s = strings.TrimSpace(strings.TrimSuffix(s, ":"))
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > titleMax {
		r := []rune(s)
		s = string(r[:titleMax-1]) + "…"
	}
	return s
}

func firstLine(lines []string) string {
	for _, l := range lines {
		if !blank(l) {
			return lineTitle(l)
		}
	}
	return ""
}

// chunkFile renders data as chunks; the language label comes along.
func chunkFile(p string, data []byte) (string, []core.KnowledgeChunk) {
	spec := langOf(p)
	lines := splitLines(string(data))
	var spans []span
	if spec.split != nil {
		spans = spec.split(lines)
	} else {
		spans = declSpans(lines, spec)
	}
	spans = finish(lines, spans)
	out := make([]core.KnowledgeChunk, 0, len(spans))
	for _, s := range spans {
		body := renderBody(lines[s.start:s.end])
		out = append(out, core.KnowledgeChunk{Path: p, StartLine: s.start + 1, EndLine: s.end, Kind: s.kind,
			Lang: spec.name, Title: s.title, Body: body, Terms: termsOf(s.title + "\n" + body)})
	}
	return spec.name, out
}

func renderBody(lines []string) string {
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		if len(l) > lineMaxBytes {
			cut := lineMaxBytes
			for cut > 0 && !utf8.RuneStart(l[cut]) {
				cut--
			}
			l = l[:cut] + "…"
		}
		b.WriteString(l)
	}
	return b.String()
}

type declStart struct {
	line        int
	kind, title string
}

// declSpans cuts at declaration lines, each pulled up over the comment and
// decorator lines directly above it; text before the first one is the header.
func declSpans(lines []string, spec *langSpec) []span {
	var starts []declStart
	parent := ""
	attached := func(l string) bool { return spec.attach != nil && spec.attach.MatchString(l) }
	for i, l := range lines {
		if blank(l) {
			continue
		}
		matched := false
		for _, r := range spec.decls {
			if r.member && parent == "" {
				continue
			}
			m := r.re.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			name := ""
			if r.name > 0 && r.name < len(m) {
				name = strings.TrimSpace(m[r.name])
			}
			if r.guard && notName[name] {
				continue
			}
			title := lineTitle(l)
			switch {
			case r.member:
				title = parent + ": " + title
			case r.parent:
				parent = cmpOr(name, title)
			default:
				parent = ""
			}
			starts = append(starts, declStart{i, r.kind, title})
			matched = true
			break
		}
		// A top-level line closes the container; an opening brace on its
		// own line (Allman style) does not.
		if !matched && parent != "" && !indented(l) && !attached(l) && strings.TrimSpace(l) != "{" {
			parent = ""
		}
	}
	if len(starts) == 0 {
		return windowSpans(lines)
	}
	for k := range starts {
		floor := 0
		if k > 0 {
			floor = starts[k-1].line + 1
		}
		for starts[k].line > floor && !blank(lines[starts[k].line-1]) && attached(lines[starts[k].line-1]) {
			starts[k].line--
		}
	}
	var spans []span
	if first := starts[0].line; first > 0 {
		spans = append(spans, span{0, first, "header", firstLine(lines[:first])})
	}
	for k, s := range starts {
		end := len(lines)
		if k+1 < len(starts) {
			end = starts[k+1].line
		}
		spans = append(spans, span{s.line, end, s.kind, s.title})
	}
	return spans
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var (
	atxRE    = re(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*$`)
	fenceRE  = re("^ {0,3}(`{3,}|~{3,})")
	setextRE = re(`^ {0,3}(=+|-+)[ \t]*$`)
	listRE   = re(`^\s*([-*+>|]|\d+[.)])(\s|$)`)
)

// markdownSpans cuts at headings of level 1-3 outside fenced code; a title
// is the heading path ("Install > Linux").
func markdownSpans(lines []string) []span {
	var stack [7]string
	var starts []declStart
	fence := ""
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if m := fenceRE.FindStringSubmatch(l); m != nil {
			switch {
			case fence == "":
				fence = m[1][:3]
			case strings.HasPrefix(strings.TrimSpace(l), fence):
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		level, text, setext := 0, "", false
		if m := atxRE.FindStringSubmatch(l); m != nil {
			level, text = len(m[1]), strings.TrimSpace(strings.TrimRight(m[2], "# \t"))
		} else if i+1 < len(lines) && !blank(l) && !indented(l) && !listRE.MatchString(l) {
			if m := setextRE.FindStringSubmatch(lines[i+1]); m != nil {
				level, text, setext = 1, strings.TrimSpace(l), true
				if m[1][0] == '-' {
					level = 2
				}
			}
		}
		if level == 0 {
			continue
		}
		stack[level] = text
		for j := level + 1; j < len(stack); j++ {
			stack[j] = ""
		}
		if setext {
			i++
		}
		if level > 3 {
			continue
		}
		var parts []string
		for _, s := range stack[1 : level+1] {
			if s != "" {
				parts = append(parts, s)
			}
		}
		starts = append(starts, declStart{i - btoi(setext), "heading", lineTitle(strings.Join(parts, " > "))})
	}
	if len(starts) == 0 {
		return windowSpans(lines)
	}
	var spans []span
	if first := starts[0].line; first > 0 {
		spans = append(spans, span{0, first, "text", firstLine(lines[:first])})
	}
	for k, s := range starts {
		end := len(lines)
		if k+1 < len(starts) {
			end = starts[k+1].line
		}
		spans = append(spans, span{s.line, end, s.kind, s.title})
	}
	return spans
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// windowSpans groups paragraphs into chunks of up to textWindow lines,
// closing at a blank line once half of that is reached.
func windowSpans(lines []string) []span {
	var spans []span
	start, size := -1, 0
	flush := func(end int) {
		if start >= 0 {
			spans = append(spans, span{start, end, "text", firstLine(lines[start:end])})
			start, size = -1, 0
		}
	}
	for i, l := range lines {
		if blank(l) {
			if start >= 0 && i-start >= textWindow/2 {
				flush(i)
			}
			continue
		}
		if start < 0 {
			start = i
		}
		size += len(l) + 1
		if i-start+1 >= textWindow || size >= chunkMaxBytes {
			flush(i + 1)
		}
	}
	flush(len(lines))
	return spans
}

func nonBlank(lines []string) int {
	n := 0
	for _, l := range lines {
		if !blank(l) {
			n++
		}
	}
	return n
}

// minorKinds fold into the chunk before them when tiny; declarations and
// headings stay separate unless they follow one of their own kind.
var minorKinds = map[string]bool{"var": true, "const": true, "macro": true, "text": true, "section": true,
	"document": true, "export": true}

// finish trims blank edges, folds tiny chunks into their predecessor and
// splits oversized ones into windows.
func finish(lines []string, spans []span) []span {
	var out []span
	for _, s := range spans {
		for s.start < s.end && blank(lines[s.start]) {
			s.start++
		}
		for s.end > s.start && blank(lines[s.end-1]) {
			s.end--
		}
		if s.start >= s.end {
			continue
		}
		if n := len(out); n > 0 && nonBlank(lines[s.start:s.end]) < tinyLines && s.end-out[n-1].start <= mergeMaxLines &&
			(minorKinds[s.kind] || s.kind == out[n-1].kind) {
			out[n-1].end = s.end
			continue
		}
		out = append(out, s)
	}
	var split []span
	for _, s := range out {
		split = append(split, splitBig(lines, s)...)
	}
	return split
}

func splitBig(lines []string, s span) []span {
	size := 0
	for _, l := range lines[s.start:s.end] {
		size += min(len(l), lineMaxBytes) + 1
	}
	if s.end-s.start <= chunkMaxLines && size <= chunkMaxBytes {
		return []span{s}
	}
	var out []span
	start, size := s.start, 0
	for i := s.start; i < s.end; i++ {
		size += min(len(lines[i]), lineMaxBytes) + 1
		if i+1-start >= chunkWindow || size >= chunkMaxBytes || i+1 == s.end {
			title := s.title
			if len(out) > 0 {
				title = lineTitle(s.title + " (cont.)")
			}
			out = append(out, span{start, i + 1, s.kind, title})
			start, size = i+1, 0
		}
	}
	return slices.DeleteFunc(out, func(x span) bool { return nonBlank(lines[x.start:x.end]) == 0 })
}

var identRE = re(`[A-Za-z][A-Za-z0-9]*`)

// termsOf lists the parts of camelCase and digit-joined identifiers
// ("NewIndexer" -> "new indexer"), which unicode61 indexes only whole.
func termsOf(text string) string {
	seen := map[string]bool{}
	var b strings.Builder
	for _, tok := range identRE.FindAllString(text, -1) {
		parts := splitIdent(tok)
		if len(parts) < 2 {
			continue
		}
		for _, p := range parts {
			p = strings.ToLower(p)
			if len(p) < 2 || seen[p] {
				continue
			}
			if b.Len()+len(p)+1 > termsMax {
				return strings.TrimSpace(b.String())
			}
			seen[p] = true
			b.WriteString(p)
			b.WriteByte(' ')
		}
	}
	return strings.TrimSpace(b.String())
}

func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }
func isLower(c byte) bool { return c >= 'a' && c <= 'z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// splitIdent splits an ASCII identifier at case and digit boundaries
// ("HTTPServerError2" -> HTTP, Server, Error, 2).
func splitIdent(s string) []string {
	var parts []string
	start := 0
	for i := 1; i < len(s); i++ {
		prev, cur := s[i-1], s[i]
		var next byte
		if i+1 < len(s) {
			next = s[i+1]
		}
		if isLower(prev) && isUpper(cur) || isUpper(prev) && isUpper(cur) && isLower(next) || isDigit(prev) != isDigit(cur) {
			parts = append(parts, s[start:i])
			start = i
		}
	}
	return append(parts, s[start:])
}

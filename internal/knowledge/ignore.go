package knowledge

import (
	"bytes"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	ignoreFileMax   = 64 << 10 // .gitignore / .gitattributes bytes read
	ignorePatterns  = 2000     // per file; a hostile repo cannot make matching quadratic in patterns
	ignoreTotal     = 10000    // across every ignore file of one walk
	ignoreSegments  = 64       // per pattern
	// ignoreWork bounds the matching cells (pattern × path segments) one
	// walk may spend; ordinary repositories use well under a tenth of it.
	ignoreWork = 50_000_000
	sniffBytes      = 8 << 10  // binary detection window
	generatedLines  = 10       // header lines searched for generated-code markers
	maxWalkDepth    = 32
	maxIndexedPath  = 1024
	attrGenerated   = "linguist-generated"
	attrVendored    = "linguist-vendored"
	attrUnsetPrefix = "-"
)

// skipDirs are vendored code, VCS metadata, caches and build output that a
// repository often commits or that a checkout may contain; .gitignore covers
// the rest.
var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, ".bzr": true, ".bothy": true,
	"node_modules": true, "vendor": true, "third_party": true, "bower_components": true, "jspm_packages": true,
	"Pods": true, "Carthage": true, ".yarn": true, ".pnpm-store": true,
	".venv": true, "venv": true, "__pycache__": true, ".tox": true, ".nox": true, ".mypy_cache": true,
	".pytest_cache": true, ".ruff_cache": true, ".gradle": true, ".cache": true, ".terraform": true, ".direnv": true,
	".dart_tool": true, ".next": true, ".nuxt": true, ".svelte-kit": true, ".turbo": true, ".parcel-cache": true,
	".idea": true, ".vs": true, "dist": true, "target": true, "coverage": true, "DerivedData": true, "_build": true,
}

// skipNames are lockfiles and other machine-written files matched by name.
var skipNames = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true, "bun.lockb": true, "go.sum": true,
	"Cargo.lock": true, "poetry.lock": true, "Pipfile.lock": true, "composer.lock": true, "Gemfile.lock": true,
	"mix.lock": true, "flake.lock": true, "packages.lock.json": true, "pubspec.lock": true, ".DS_Store": true,
}

// skipSuffixes are generated or binary files recognised by name alone.
var skipSuffixes = []string{
	".min.js", ".min.css", ".map", ".pb.go", ".pb.gw.go", "_pb2.py", "_pb2_grpc.py", ".g.dart", ".freezed.dart",
	"_generated.go", ".gen.go", ".gen.ts", ".designer.cs", ".snap",
	".png", ".jpg", ".jpeg", ".gif", ".bmp", ".ico", ".webp", ".tiff", ".psd", ".pdf", ".zip", ".gz", ".tgz", ".bz2",
	".xz", ".zst", ".7z", ".rar", ".tar", ".jar", ".war", ".class", ".exe", ".dll", ".so", ".dylib", ".a", ".o", ".obj",
	".lib", ".wasm", ".woff", ".woff2", ".ttf", ".otf", ".eot", ".mp3", ".mp4", ".mov", ".avi", ".wav", ".flac",
	".ogg", ".webm", ".sqlite", ".db", ".bin", ".pyc", ".pyo", ".iso", ".dmg",
}

var generatedRE = regexp.MustCompile(`(?i)(code generated .*do not edit|@generated\b|<auto-?generated|this file (is|was) (auto(matically)?[- ]?)?generated)`)

func skipByName(name string) bool {
	if skipNames[name] || strings.HasPrefix(name, "zz_generated") {
		return true
	}
	lower := strings.ToLower(name)
	for _, s := range skipSuffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}

// binary reports content that is not UTF-8 text: a NUL byte or an invalid
// sequence in the first sniffBytes (a rune cut at the window edge is fine).
func binary(data []byte) bool {
	head := data[:min(len(data), sniffBytes)]
	if bytes.IndexByte(head, 0) >= 0 {
		return true
	}
	if len(data) > sniffBytes {
		for i := 0; i < utf8.UTFMax-1 && len(head) > 0 && !utf8.Valid(head); i++ {
			if r, size := utf8.DecodeLastRune(head); r != utf8.RuneError || size != 1 {
				break
			}
			head = head[:len(head)-1]
		}
	}
	return !utf8.Valid(head)
}

// generated looks for the usual "generated, do not edit" header markers.
func generated(data []byte) bool {
	n := 0
	for line := range bytes.Lines(data[:min(len(data), 4096)]) {
		if generatedRE.Match(line) {
			return true
		}
		if n++; n == generatedLines {
			break
		}
	}
	return false
}

// pattern is one gitignore line (also used for .gitattributes paths).
type pattern struct {
	segs     []string // split on "/", runs of "**" collapsed
	negate   bool
	dirOnly  bool
	anchored bool // had a "/" other than a trailing one: matched against the whole relative path
}

// ruleSet holds the patterns of one ignore file; base is the directory it
// sits in, relative to the indexed root ("" for the root).
type ruleSet struct {
	base string
	pats []pattern
}

func parsePattern(line string) (pattern, bool) {
	line = strings.TrimSuffix(line, "\r")
	// Trailing spaces are ignored unless escaped with a backslash.
	for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, `\ `) {
		line = line[:len(line)-1]
	}
	if line == "" || strings.HasPrefix(line, "#") {
		return pattern{}, false
	}
	var p pattern
	switch {
	case strings.HasPrefix(line, "!"):
		p.negate, line = true, line[1:]
	case strings.HasPrefix(line, `\!`), strings.HasPrefix(line, `\#`):
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		p.dirOnly, line = true, strings.TrimRight(line, "/")
	}
	if strings.Contains(line, "/") {
		p.anchored = true
		line = strings.TrimLeft(line, "/")
	}
	if line == "" {
		return pattern{}, false
	}
	for _, s := range strings.Split(line, "/") {
		if s == "" {
			continue
		}
		if s == "**" && len(p.segs) > 0 && p.segs[len(p.segs)-1] == "**" {
			continue
		}
		// gitignore negates a bracket class with "!", path.Match with "^".
		p.segs = append(p.segs, strings.ReplaceAll(s, "[!", "[^"))
		if len(p.segs) > ignoreSegments {
			return pattern{}, false
		}
	}
	if len(p.segs) == 0 {
		return pattern{}, false
	}
	return p, true
}

func parseIgnore(base string, data []byte) ruleSet {
	rs := ruleSet{base: base}
	for line := range strings.Lines(string(data[:min(len(data), ignoreFileMax)])) {
		if p, ok := parsePattern(strings.TrimSuffix(line, "\n")); ok {
			rs.pats = append(rs.pats, p)
			if len(rs.pats) == ignorePatterns {
				break
			}
		}
	}
	return rs
}

// parseAttributes turns the linguist-generated and linguist-vendored entries
// of a .gitattributes file into ignore patterns ("-attr" or "attr=false"
// re-include).
func parseAttributes(data []byte) ruleSet {
	var rs ruleSet
	for line := range strings.Lines(string(data[:min(len(data), ignoreFileMax)])) {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		set := 0
		for _, a := range fields[1:] {
			name, val, _ := strings.Cut(a, "=")
			unset := strings.HasPrefix(name, attrUnsetPrefix)
			name = strings.TrimPrefix(name, attrUnsetPrefix)
			if name != attrGenerated && name != attrVendored {
				continue
			}
			if unset || val == "false" {
				set = -1
			} else {
				set = 1
			}
		}
		if set == 0 {
			continue
		}
		if p, ok := parsePattern(fields[0]); ok {
			p.negate = set < 0
			rs.pats = append(rs.pats, p)
			if len(rs.pats) == ignorePatterns {
				break
			}
		}
	}
	return rs
}

// match reports whether p matches rel (relative to the rule set's base).
func (p pattern) match(rel string, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	if !p.anchored {
		return matchSegs(p.segs, []string{path.Base(rel)})
	}
	return matchSegs(p.segs, strings.Split(rel, "/"))
}

// matchSegs matches pattern segments against path segments. "**" spans zero
// or more segments, except at the end, where it needs at least one (so
// "dir/**" matches what is inside dir, not dir itself). It is a dynamic
// programme, so hostile patterns stay O(len(p) × len(s)).
func matchSegs(p, s []string) bool {
	lp, ls := len(p), len(s)
	next := make([]bool, ls+1) // row i+1
	cur := make([]bool, ls+1)
	next[ls] = true
	for i := lp - 1; i >= 0; i-- {
		for j := ls; j >= 0; j-- {
			switch {
			case p[i] == "**" && i == lp-1:
				cur[j] = j < ls
			case p[i] == "**":
				cur[j] = next[j] || j < ls && cur[j+1]
			default:
				ok := false
				if j < ls {
					m, err := path.Match(p[i], s[j])
					ok = err == nil && m && next[j+1]
				}
				cur[j] = ok
			}
		}
		next, cur = cur, next
	}
	return next[0]
}

// ignorer evaluates the rule sets that apply to a path: the root
// .gitattributes first, then every .gitignore from the root down; the last
// matching pattern wins.
type ignorer struct {
	attrs ruleSet
	dirs  map[string]ruleSet // by base
	// loaded counts patterns across the walk's ignore files; limit > 0 caps
	// the matching work and spent tracks it, so hostile rules (thousands of
	// "**" patterns in every directory) end the walk instead of stalling it.
	loaded       int
	limit, spent int64
}

// add keeps the rule set of one ignore file within the walk's pattern cap.
func (ig *ignorer) add(rs ruleSet) {
	if room := ignoreTotal - ig.loaded; len(rs.pats) > room {
		rs.pats = rs.pats[:max(room, 0)]
	}
	ig.loaded += len(rs.pats)
	ig.dirs[rs.base] = rs
}

// exhausted reports that the work cap was reached; ignored answers true from
// then on.
func (ig *ignorer) exhausted() bool { return ig.limit > 0 && ig.spent >= ig.limit }

func (ig *ignorer) ignored(rel string, isDir bool) bool {
	ignored := false
	depth := int64(strings.Count(rel, "/") + 2)
	apply := func(rs ruleSet, sub string) {
		for _, p := range rs.pats {
			if ig.limit > 0 {
				if ig.exhausted() {
					return
				}
				ig.spent += int64(len(p.segs)) * depth
			}
			if p.match(sub, isDir) {
				ignored = !p.negate
			}
		}
	}
	apply(ig.attrs, rel)
	if rs, ok := ig.dirs[""]; ok {
		apply(rs, rel)
	}
	for i := 0; i < len(rel); i++ {
		if rel[i] != '/' {
			continue
		}
		if rs, ok := ig.dirs[rel[:i]]; ok {
			apply(rs, rel[i+1:])
		}
	}
	return ignored || ig.exhausted()
}

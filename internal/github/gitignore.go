package github

import (
	"path"
	"strings"
)

// ignorer applies .gitignore files the way git does for untracked files:
// patterns are relative to the directory of their file, deeper files and
// later lines win, a leading ! re-includes, a trailing / matches
// directories only, a pattern with an inner / is anchored to its directory,
// and ** spans directories. Callers do not descend into an ignored
// directory, so nothing below it can be re-included, as in git.
type ignorer struct{ rules []ignoreRule }

type ignoreRule struct {
	base     string // directory of the .gitignore, "" for the checkout root
	negate   bool
	dirOnly  bool
	anchored bool
	parts    []string
}

// add parses one .gitignore found in directory base.
func (ig *ignorer) add(base string, content []byte) {
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = trimUnescapedSpace(line)
		r := ignoreRule{base: base}
		switch {
		case strings.HasPrefix(line, "!"):
			r.negate, line = true, line[1:]
		case strings.HasPrefix(line, `\!`), strings.HasPrefix(line, `\#`):
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly, line = true, strings.TrimRight(line, "/")
		}
		if line == "" {
			continue
		}
		if strings.Contains(line, "/") {
			r.anchored = true
		}
		r.parts = strings.Split(strings.TrimPrefix(line, "/"), "/")
		ig.rules = append(ig.rules, r)
	}
}

// trimUnescapedSpace drops trailing spaces unless the last is escaped.
func trimUnescapedSpace(s string) string {
	for strings.HasSuffix(s, " ") && !strings.HasSuffix(s, `\ `) {
		s = s[:len(s)-1]
	}
	return s
}

// ignored reports whether rel (slash-separated, relative to the checkout
// root) is ignored; the last matching rule decides.
func (ig *ignorer) ignored(rel string, isDir bool) bool {
	out := false
	for _, r := range ig.rules {
		if r.dirOnly && !isDir {
			continue
		}
		sub := rel
		if r.base != "" {
			var ok bool
			if sub, ok = strings.CutPrefix(rel, r.base+"/"); !ok {
				continue
			}
		}
		if r.match(sub) {
			out = !r.negate
		}
	}
	return out
}

func (r ignoreRule) match(sub string) bool {
	segs := strings.Split(sub, "/")
	if !r.anchored {
		ok, _ := path.Match(r.parts[0], segs[len(segs)-1])
		return ok
	}
	return matchSegments(r.parts, segs)
}

func matchSegments(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		if len(pat) == 1 {
			return len(segs) > 0
		}
		for i := 0; i <= len(segs); i++ {
			if matchSegments(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], segs[1:])
}

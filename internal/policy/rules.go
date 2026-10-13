package policy

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// shellMeta are the characters that make a command line more than one plain
// command (SPEC §5.8): an argv0 rule never matches a command containing them.
const shellMeta = ";|&$`<>(){}*?\n"

var (
	shells      = []string{"bash", "sh", "zsh", "dash"}
	progNameRe  = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)
	trustedBins = []string{"/bin", "/usr/bin", "/usr/local/bin", "/sbin", "/usr/sbin"}
)

func firstRule(x *input) *config.Rule {
	for i := range x.cfg.Policy.Rules {
		if r := &x.cfg.Policy.Rules[i]; ruleMatches(r, x) {
			return r
		}
	}
	return nil
}

func ruleMatches(r *config.Rule, x *input) bool {
	if r.Verdict == "" || r.Tool != "" && !globMatch(r.Tool, x.a.Tool) {
		return false
	}
	if r.SideEffect != "" {
		if se, err := core.ParseSideEffect(r.SideEffect); err != nil || se != x.se {
			return false
		}
	}
	if len(r.Triggers) > 0 && !slices.Contains(r.Triggers, string(x.Trigger)) {
		return false
	}
	// A relaxing rule must cover every destination and path the action
	// touches; a tightening one fires when any of them matches.
	all := r.Verdict == string(core.VerdictAuto)
	if len(r.Argv0) > 0 {
		name, args, ok := commandLine(x.argv)
		if !ok || !slices.Contains(r.Argv0, name) || all && !readOnlyArgs(name, args) {
			return false
		}
	}
	if len(r.Domains) > 0 && !matchHosts(r.Domains, x.hosts, all) {
		return false
	}
	if len(r.Paths) > 0 && !matchPaths(r.Paths, x.a.Paths, all) {
		return false
	}
	for ptr, g := range r.Match {
		v, ok := pointerText(x.payload, ptr)
		if !ok || !globMatch(g, v) {
			return false
		}
	}
	return true
}

func matchHosts(patterns []string, hosts []hostPort, all bool) bool {
	if len(hosts) == 0 {
		return false
	}
	pats := make([]hostPattern, 0, len(patterns))
	for _, s := range patterns {
		if p, ok := parseHostPattern(s); ok {
			pats = append(pats, p)
		}
	}
	hit := func(h hostPort) bool {
		return slices.ContainsFunc(pats, func(p hostPattern) bool { return p.match(h) })
	}
	if all {
		return !slices.ContainsFunc(hosts, func(h hostPort) bool { return !hit(h) })
	}
	return slices.ContainsFunc(hosts, hit)
}

func matchPaths(patterns, paths []string, all bool) bool {
	if len(paths) == 0 {
		return false
	}
	hit := func(p string) bool {
		return slices.ContainsFunc(patterns, func(g string) bool { return pathGlob(g, p) })
	}
	if all {
		return !slices.ContainsFunc(paths, func(p string) bool { return !hit(p) })
	}
	return slices.ContainsFunc(paths, hit)
}

// commandName returns the program a command line starts, the way an argv0
// rule sees it: for ["bash","-lc",cmd] the first word of cmd, otherwise
// argv[0]. ok is false when that cannot be decided safely.
func commandName(argv []string) (string, bool) {
	name, _, ok := commandLine(argv)
	return name, ok
}

// commandLine is commandName plus the words after the program.
func commandLine(argv []string) (string, []string, bool) {
	if len(argv) == 0 {
		return "", nil, false
	}
	words := argv
	if len(argv) == 3 && (argv[1] == "-c" || argv[1] == "-lc") {
		if sh, ok := programName(argv[0]); ok && slices.Contains(shells, sh) {
			cmd := argv[2]
			if strings.ContainsAny(cmd, shellMeta) {
				return "", nil, false
			}
			if words = strings.Fields(cmd); len(words) == 0 {
				return "", nil, false
			}
		}
	}
	name, ok := programName(words[0])
	return name, words[1:], ok
}

// writingFlags are the options that make a tool of the default read-only
// shell rule delete or write files or run another program.
var writingFlags = map[string][]string{
	"find": {"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls"},
	"rg":   {"--pre", "--pre-glob", "--search-zip", "--hostname-bin"},
}

// readOnlyArgs reports whether args keep name read-only, so that a relaxing
// argv0 rule may match. Quotes and backslashes are dropped first, as the
// shell would ("-de'le'te" is -delete).
func readOnlyArgs(name string, args []string) bool {
	bad := writingFlags[name]
	if bad == nil {
		return true
	}
	for _, a := range args {
		a = strings.Map(func(r rune) rune {
			if r == '\'' || r == '"' || r == '\\' {
				return -1
			}
			return r
		}, a)
		flag, _, _ := strings.Cut(a, "=")
		if slices.Contains(bad, flag) {
			return false
		}
		// rg's -z (--search-zip) runs decompressors and may share a cluster
		// with other short flags.
		if name == "rg" && len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.ContainsRune(a, 'z') {
			return false
		}
	}
	return true
}

// programName accepts a bare command name or an absolute path in a system
// bin directory. Relative or workspace paths ("./ls") are rejected: a script
// the agent wrote must not ride on a rule meant for the system tool.
func programName(word string) (string, bool) {
	if strings.Contains(word, "/") {
		dir, base := path.Split(word)
		if !slices.Contains(trustedBins, path.Clean(dir)) {
			return "", false
		}
		word = base
	}
	if !progNameRe.MatchString(word) {
		return "", false
	}
	return word, true
}

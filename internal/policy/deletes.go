package policy

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

var (
	// deleters delete files or destroy their content whatever their
	// arguments.
	deleters = []string{"rm", "rmdir", "unlink", "shred", "truncate"}
	// runners run the command their arguments name (sudo rm, xargs rm).
	runners = []string{"sudo", "doas", "env", "command", "builtin", "exec", "nohup", "nice", "ionice", "time", "timeout",
		"stdbuf", "setsid", "xargs", "parallel", "busybox", "watch", "flock", "chroot", "unbuffer", "strace"}
	// preCommand are the reserved words after which a command starts.
	preCommand = []string{"!", "{", "}", "if", "then", "else", "elif", "fi", "do", "done", "while", "until", "esac"}
	// runnerValueOpts are runner options whose value is the next word, so
	// that word is not the command.
	runnerValueOpts = map[string][]string{
		"sudo": {"-u", "-g", "-h", "-p", "-C", "-U", "-r", "-t", "-T", "-D", "--user", "--group", "--host", "--prompt", "--chdir"},
		"doas": {"-u", "-C"}, "env": {"-u", "-C", "--unset", "--chdir"}, "nice": {"-n", "--adjustment"},
		"ionice": {"-c", "-n", "-p", "--class", "--classdata"}, "timeout": {"-s", "-k", "--signal", "--kill-after"},
		"xargs":    {"-I", "-n", "-P", "-L", "-s", "-d", "-E", "-a", "--max-args", "--max-procs", "--delimiter", "--arg-file", "--max-lines"},
		"parallel": {"-j", "--jobs", "-S"}, "flock": {"-w", "-E", "--timeout", "--conflict-exit-code"}, "watch": {"-n", "--interval"},
		"chroot": {"--userspec", "--groups"}, "strace": {"-o", "-e", "-p", "-s", "-u"},
	}
	// runnerOperands counts the operands a runner takes before its command:
	// timeout DURATION, flock FILE, chroot NEWROOT.
	runnerOperands = map[string]int{"timeout": 1, "flock": 1, "chroot": 1}
	// gitValueOpts are git's global options that take the next word.
	gitValueOpts = []string{"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--super-prefix", "--config-env"}
	assignRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\+?=`)
	// numberRe is a runner's numeric value or duration ("10", "5s", "1.5m").
	numberRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[smhd]?$`)
)

// maxScriptDepth bounds nested scripts (bash -c "sh -c '…'", find -exec,
// sudo); a deeper one cannot be checked and counts as deleting.
const maxScriptDepth = 8

// deletesFiles reports whether an exec argv deletes files or destroys their
// content: rm, rmdir, unlink, shred, truncate, find -delete, git clean, git
// rm, rsync --delete, also behind sudo, xargs, find -exec, bash -c, eval or
// a here-document fed to a shell, after quote and backslash removal.
// Writing new content over a file (>, mv, cp) counts as a change, as
// fs_write does. It knows programs by name only: a deletion by a program
// named at run time or by python code is not seen.
func deletesFiles(argv []string) bool { return commandDeletes(argv, 0) }

func commandDeletes(words []string, depth int) bool {
	if depth > maxScriptDepth {
		return true
	}
	words = commandWords(words)
	if len(words) == 0 {
		return false
	}
	name, args := path.Base(words[0]), words[1:]
	switch {
	case slices.Contains(deleters, name):
		return true
	case name == "find":
		return findDeletes(args, depth)
	case name == "git":
		return gitDeletes(args)
	case name == "rsync":
		return slices.ContainsFunc(args, func(a string) bool { return a == "--del" || strings.HasPrefix(a, "--delete") })
	case name == "eval":
		return scriptDeletes(strings.Join(args, " "), depth+1)
	case slices.Contains(shells, name):
		return shellDeletes(args, depth)
	case slices.Contains(runners, name):
		// "command -v rm" only says where rm is.
		if name == "command" && slices.ContainsFunc(args, func(a string) bool { return a == "-v" || a == "-V" }) {
			return false
		}
		return commandDeletes(runnerCommand(name, args), depth+1)
	}
	return false
}

// runnerCommand returns the command a runner runs: its arguments after the
// runner's own options, their values, variable assignments and leading
// operands. "xargs grep -n unlink" runs grep, not unlink.
func runnerCommand(name string, args []string) []string {
	operands := runnerOperands[name]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return args[i+1:]
		case slices.Contains(runnerValueOpts[name], a):
			i++
		case len(a) > 1 && a[0] == '-', assignRe.MatchString(a):
		case operands > 0:
			operands--
		case numberRe.MatchString(a):
		default:
			return args[i:]
		}
	}
	return nil
}

// commandWords drops what comes before a command's program: variable
// assignments, reserved words and a function definition's name.
func commandWords(words []string) []string {
	for len(words) > 0 {
		switch w := words[0]; {
		case w == "function" && len(words) > 1:
			words = words[2:]
		case slices.Contains(preCommand, w) || assignRe.MatchString(w):
			words = words[1:]
		default:
			return words
		}
	}
	return nil
}

// findDeletes checks -delete and the command of each -exec, -execdir, -ok
// and -okdir, which runs up to its ";" or "+".
func findDeletes(args []string, depth int) bool {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-delete":
			return true
		case "-exec", "-execdir", "-ok", "-okdir":
			end := i + 1
			for end < len(args) && args[end] != ";" && args[end] != "+" {
				end++
			}
			if commandDeletes(args[i+1:end], depth+1) {
				return true
			}
			i = end
		}
	}
	return false
}

// gitDeletes reports git clean (not a dry run) and git rm (not --cached).
// Commands that put back committed content (reset --hard, checkout,
// restore) change files the way fs_write does and are not counted.
func gitDeletes(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case slices.Contains(gitValueOpts, a):
			i++
		case strings.HasPrefix(a, "-"):
		case a == "clean":
			return !slices.ContainsFunc(args[i+1:], func(o string) bool {
				return o == "--dry-run" || len(o) > 1 && o[0] == '-' && o[1] != '-' && strings.ContainsRune(o, 'n')
			})
		case a == "rm":
			return !slices.Contains(args[i+1:], "--cached")
		default:
			return false
		}
	}
	return false
}

// shellDeletes checks the script of sh -c (in any option cluster, such as
// -lc); a script file it runs cannot be read here.
func shellDeletes(args []string, depth int) bool {
	script := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			if script && i+1 < len(args) {
				return scriptDeletes(args[i+1], depth+1)
			}
			return false
		case a == "--rcfile" || a == "--init-file":
			i++
		case strings.HasPrefix(a, "--"):
		case len(a) > 1 && (a[0] == '-' || a[0] == '+'):
			script = script || a[0] == '-' && strings.ContainsRune(a[1:], 'c')
			if strings.HasSuffix(a, "o") || strings.HasSuffix(a, "O") {
				i++
			}
		default:
			return script && scriptDeletes(a, depth+1)
		}
	}
	return false
}

// scriptDeletes checks every command of a shell script, the command
// substitutions inside its double quotes and the here-documents it feeds
// to a shell.
func scriptDeletes(s string, depth int) bool {
	if depth > maxScriptDepth {
		return true
	}
	cmds, nested := splitScript(s)
	for i, c := range cmds {
		if commandDeletes(c.words, depth) {
			return true
		}
		// A script piped into a shell is what the command before it
		// writes: a here-document ("cat <<EOF | bash") or echo's words.
		if c.piped && i > 0 && readsScript(c.words) {
			for _, in := range written(cmds[i-1]) {
				if scriptDeletes(in, depth+1) {
					return true
				}
			}
		}
		if len(c.input) > 0 && slices.ContainsFunc(c.words, func(w string) bool { n := path.Base(w); return n == "eval" || slices.Contains(shells, n) }) {
			for _, in := range c.input {
				if scriptDeletes(in, depth+1) {
					return true
				}
			}
		}
	}
	for _, n := range nested {
		if scriptDeletes(n, depth+1) {
			return true
		}
	}
	return false
}

// simpleCommand is one command of a script: its words without quotes and
// backslashes, the here-documents and here-strings it reads, and whether a
// pipe feeds it.
type simpleCommand struct {
	words []string
	input []string
	piped bool
}

// written returns the text a command writes that a shell after a pipe would
// run: its here-documents and here-strings, or the words echo and printf
// print. A file it reads (cat script.sh) cannot be checked here.
func written(c *simpleCommand) []string {
	out := slices.Clone(c.input)
	if words := commandWords(c.words); len(words) > 1 {
		if n := path.Base(words[0]); n == "echo" || n == "printf" {
			out = append(out, strings.Join(words[1:], " "))
		}
	}
	return out
}

// readsScript reports whether a shell reads its script from stdin: no -c
// and no script file, or -s.
func readsScript(words []string) bool {
	words = commandWords(words)
	if len(words) == 0 || !slices.Contains(shells, path.Base(words[0])) {
		return false
	}
	for _, a := range words[1:] {
		switch {
		case len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.ContainsRune(a[1:], 's'):
			return true
		case len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.ContainsRune(a[1:], 'c'):
			return false
		case !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+"):
			return false
		}
	}
	return true
}

type redirect int

const (
	noRedirect redirect = iota
	toFile
	heredoc
	heredocTabs // <<-: the body's leading tabs are dropped
	hereString
)

// splitScript cuts a script into simple commands, as close to bash as
// finding program names needs: ; & | ( ) newlines, backticks and $( end a
// command; quotes, backslashes and $'…' are removed from words; comments,
// redirection targets and the file descriptor before a redirection are
// dropped; a here-document's body becomes its command's input instead of
// being read as commands. Command substitutions inside double quotes are
// returned as nested scripts.
func splitScript(s string) (cmds []*simpleCommand, nested []string) {
	type pending struct {
		delim  string
		tabs   bool
		expand bool // an unquoted delimiter: the body's substitutions run
		cmd    *simpleCommand
	}
	var (
		cur    = &simpleCommand{}
		word   strings.Builder
		inWord bool
		quoted bool // the word being read had quotes or backslashes
		target redirect
		docs   []pending
		piped  bool // the next command reads this one's output
	)
	endWord := func() {
		if !inWord {
			return
		}
		w := word.String()
		word.Reset()
		inWord = false
		q := quoted
		quoted = false
		switch target {
		case noRedirect:
			cur.words = append(cur.words, w)
		case heredoc, heredocTabs:
			docs = append(docs, pending{delim: w, tabs: target == heredocTabs, expand: !q, cmd: cur})
		case hereString:
			cur.input = append(cur.input, w)
		}
		target = noRedirect
	}
	endCommand := func() {
		endWord()
		target = noRedirect
		if len(cur.words) > 0 || len(cur.input) > 0 {
			cmds = append(cmds, cur)
		}
		cur = &simpleCommand{piped: piped}
		piped = false
	}
	// substitutions adds the command substitutions in t as nested scripts.
	substitutions := func(t string) {
		for j := 0; j < len(t); j++ {
			switch {
			case t[j] == '\\':
				j++
			case t[j] == '`':
				end := closing(t, j+1, '`')
				nested = append(nested, t[j+1:min(end, len(t))])
				j = end
			case t[j] == '$' && j+1 < len(t) && t[j+1] == '(':
				end := closing(t, j+2, ')')
				nested = append(nested, t[j+2:min(end, len(t))])
				j = end
			}
		}
	}
	// readBodies consumes the here-documents started on the line that
	// ends before s[from] and returns where the next line starts.
	readBodies := func(from int) int {
		for _, d := range docs {
			var body strings.Builder
			for from < len(s) {
				line, _, _ := strings.Cut(s[from:], "\n")
				from += len(line) + 1
				if line == d.delim || d.tabs && strings.TrimLeft(line, "\t") == d.delim {
					break
				}
				body.WriteString(line)
				body.WriteByte('\n')
			}
			d.cmd.input = append(d.cmd.input, body.String())
			if d.expand {
				substitutions(body.String())
			}
		}
		docs = nil
		return min(from, len(s))
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			if i+1 < len(s) {
				i++
				if s[i] != '\n' {
					word.WriteByte(s[i])
					inWord, quoted = true, true
				}
			}
		case c == '\'':
			inWord, quoted = true, true
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				end = len(s) - i - 1
			}
			word.WriteString(s[i+1 : i+1+end])
			i += end + 1
		case c == '$' && i+1 < len(s) && s[i+1] == '\'':
			inWord, quoted = true, true
			for i += 2; i < len(s) && s[i] != '\''; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				word.WriteByte(s[i])
			}
		case c == '"':
			inWord, quoted = true, true
			for i++; i < len(s) && s[i] != '"'; i++ {
				switch {
				case s[i] == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", s[i+1]) >= 0:
					i++
					if s[i] != '\n' {
						word.WriteByte(s[i])
					}
				case s[i] == '`':
					end := closing(s, i+1, '`')
					nested = append(nested, s[i+1:end])
					i = end
				case s[i] == '$' && i+1 < len(s) && s[i+1] == '(':
					end := closing(s, i+2, ')')
					nested = append(nested, s[i+2:end])
					i = end
				default:
					word.WriteByte(s[i])
				}
			}
		case c == '#' && !inWord:
			for i+1 < len(s) && s[i+1] != '\n' {
				i++
			}
		case c == ' ' || c == '\t':
			endWord()
		case c == '\n':
			endCommand()
			if len(docs) > 0 {
				i = readBodies(i+1) - 1
			}
		case c == '$' && i+1 < len(s) && s[i+1] == '(' || c == '`':
			// The substitution's output is part of this word, and what
			// follows it belongs to the same command.
			start := i
			if c == '`' {
				i = closing(s, i+1, '`')
			} else {
				i = closing(s, i+2, ')')
			}
			substitutions(s[start:min(i+1, len(s))])
			word.WriteString("$()")
			inWord = true
		case c == '&' && i+1 < len(s) && s[i+1] == '>':
			endWord()
			i++
			if i+1 < len(s) && s[i+1] == '>' {
				i++
			}
			target = toFile
		case c == '<' || c == '>':
			// "2>" redirects a file descriptor; the digits are not a word.
			if w := word.String(); inWord && w != "" && strings.Trim(w, "0123456789") == "" {
				word.Reset()
				inWord = false
			}
			endWord()
			target = toFile
			switch rest := s[i+1:]; {
			case c == '<' && strings.HasPrefix(rest, "<<"):
				target, i = hereString, i+2
			case c == '<' && strings.HasPrefix(rest, "<-"):
				target, i = heredocTabs, i+2
			case c == '<' && strings.HasPrefix(rest, "<"):
				target, i = heredoc, i+1
			case strings.HasPrefix(rest, ">") || strings.HasPrefix(rest, "&") || strings.HasPrefix(rest, "|"):
				i++
			}
		case c == '|' && i+1 < len(s) && s[i+1] == '|':
			endCommand()
			i++
		case c == '|':
			endCommand()
			piped = true
			cur.piped = true
		case strings.IndexByte(";&()", c) >= 0:
			endCommand()
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	endCommand()
	return cmds, nested
}

// closing returns the index of the delimiter that ends a command
// substitution starting at s[from], or len(s). Parentheses nest; quotes are
// not followed, which only ever makes the nested script longer.
func closing(s string, from int, delim byte) int {
	depth := 1
	for j := from; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '(':
			if delim == ')' {
				depth++
			}
		case delim:
			if depth--; depth == 0 || delim == '`' {
				return j
			}
		}
	}
	return len(s)
}

package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const (
	notesFile     = "NOTES.md"
	notesReadMax  = 1 << 20 // a file left over a lowered cap still reads
	notesTextMax  = 4 << 10 // per agent edit
	notesDiffMax  = 64 << 10
	sectionMaxLen = 80
)

// Notes ops of the notes tool.
const (
	opRead    = "read"
	opAppend  = "append"
	opReplace = "replace"
)

type notesOp struct {
	Repo    string `json:"repo"`
	Op      string `json:"op"`
	Section string `json:"section,omitempty"`
	Text    string `json:"text,omitempty"`
	Old     string `json:"old,omitempty"`
	New     string `json:"new,omitempty"`
}

func etagOf(content string) string {
	if content == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:16])
}

func (s *Service) notesPath(r *core.IndexedRepo) (string, error) {
	dir, err := notesDir(s.d.DataDir, r.Principal, r.Repo)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, notesFile), nil
}

// readNotes returns "" when the repo has no notes yet.
func (s *Service) readNotes(r *core.IndexedRepo) (string, error) {
	p, err := s.notesPath(r)
	if err != nil {
		return "", err
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, notesReadMax))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// writeNotesFile replaces the file atomically (temp file, fsync, rename;
// 0600 in 0700 directories); empty content removes it.
func (s *Service) writeNotesFile(r *core.IndexedRepo, content string) error {
	p, err := s.notesPath(r)
	if err != nil {
		return err
	}
	if content == "" {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".notes-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// cleanNotes normalises text written into notes: UTF-8, LF line ends, no
// control characters other than tab and newline.
func cleanNotes(s string) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\r\n", "\n"), "�")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

func notesTitle(repo string) string { return "# " + repo + "\n" }

// applyNotesOp computes the notes after an agent edit. append adds text at
// the end of the named section (creating it); replace swaps the one exact
// occurrence of old for new.
func applyNotesOp(content string, op notesOp) (string, error) {
	switch op.Op {
	case opAppend:
		text := strings.Trim(cleanNotes(op.Text), "\n")
		section := strings.TrimSpace(strings.TrimLeft(cleanNotes(op.Section), "# "))
		switch {
		case strings.TrimSpace(text) == "":
			return "", invalidf("text is empty")
		case len(text) > notesTextMax:
			return "", invalidf("text is longer than %d bytes", notesTextMax)
		case section == "" || strings.Contains(section, "\n") || utf8.RuneCountInString(section) > sectionMaxLen:
			return "", invalidf("section must be a single heading of at most %d characters, e.g. \"Conventions\"", sectionMaxLen)
		}
		if content == "" {
			content = notesTitle(op.Repo)
		}
		return appendToSection(content, section, text), nil
	case opReplace:
		old, repl := cleanNotes(op.Old), cleanNotes(op.New)
		switch n := strings.Count(content, old); {
		case strings.TrimSpace(old) == "":
			return "", invalidf("old is empty: quote the exact text to change")
		case len(repl) > notesTextMax:
			return "", invalidf("new is longer than %d bytes", notesTextMax)
		case n == 0:
			return "", invalidf("old was not found in the notes; read them first and quote the text exactly")
		case n > 1:
			return "", invalidf("old appears %d times in the notes; quote more of it so it matches once", n)
		}
		return strings.Replace(content, old, repl, 1), nil
	}
	return "", invalidf("op must be read, append or replace")
}

func headingLevel(line string) (int, string) {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return 0, ""
	}
	n := len(t) - len(strings.TrimLeft(t, "#"))
	if n == 0 || n > 6 || len(t) > n && t[n] != ' ' && t[n] != '\t' {
		return 0, ""
	}
	return n, strings.TrimSpace(strings.TrimRight(t[n:], "# \t"))
}

func appendToSection(content, section, text string) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	at, level := -1, 0
	for i, l := range lines {
		if n, title := headingLevel(l); n >= 2 && strings.EqualFold(title, section) {
			at, level = i, n
			break
		}
	}
	if at < 0 {
		return strings.Join(lines, "\n") + "\n\n## " + section + "\n" + text + "\n"
	}
	end := len(lines)
	for i := at + 1; i < len(lines); i++ {
		if n, _ := headingLevel(lines[i]); n > 0 && n <= level {
			end = i
			break
		}
	}
	next := end
	for end > at+1 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	out := append([]string{}, lines[:end]...)
	out = append(out, strings.Split(text, "\n")...)
	if next < len(lines) {
		out = append(out, "")
		out = append(out, lines[next:]...)
	}
	return strings.Join(out, "\n") + "\n"
}

// notesDiff is a one-hunk unified diff of the changed lines, for cards.
func notesDiff(before, after string) string {
	a, b := splitLines(before), splitLines(after)
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n", notesFile, notesFile, pre+1, len(a)-pre-suf, pre+1, len(b)-pre-suf)
	for _, l := range a[pre : len(a)-suf] {
		sb.WriteString("-" + l + "\n")
	}
	for _, l := range b[pre : len(b)-suf] {
		sb.WriteString("+" + l + "\n")
	}
	d := sb.String()
	if len(d) > notesDiffMax {
		cut := notesDiffMax
		for cut > 0 && !utf8.RuneStart(d[cut]) {
			cut--
		}
		d = d[:cut] + "\n… (diff truncated)\n"
	}
	return d
}

// summarize keeps the notes, minus their title, up to budget bytes, cut at a
// line boundary; more reports that something was left out.
func summarize(content string, budget int) (text string, more bool) {
	lines := splitLines(content)
	for len(lines) > 0 && (strings.TrimSpace(lines[0]) == "" || strings.HasPrefix(lines[0], "# ")) {
		lines = lines[1:]
	}
	var b strings.Builder
	for _, l := range lines {
		if b.Len()+len(l)+1 > budget {
			if b.Len() == 0 && budget > 0 {
				// A first line longer than the budget is cut rather than lost.
				cut := min(len(l), budget-1)
				for cut > 0 && !utf8.RuneStart(l[cut]) {
					cut--
				}
				b.WriteString(l[:cut])
			}
			more = true
			break
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n"), more
}

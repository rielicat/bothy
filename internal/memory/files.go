package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

// userError carries a message meant for the model or the web UI verbatim while
// still matching a core sentinel with errors.Is.
type userError struct {
	msg  string
	kind error
}

func (e *userError) Error() string { return e.msg }
func (e *userError) Unwrap() error { return e.kind }

func invalidf(format string, args ...any) error {
	return &userError{msg: fmt.Sprintf(format, args...), kind: core.ErrInvalid}
}

var files = []core.MemoryFile{core.FileMemory, core.FileUser, core.FileHeartbeat}

func validFile(f core.MemoryFile) bool {
	return f == core.FileMemory || f == core.FileUser || f == core.FileHeartbeat
}

func fileName(f core.MemoryFile) string { return string(f) + ".md" }

// teamDir is the folder of TEAM.md under <data>/memory. Principal ids start
// with usr_ or spc_, so no principal's folder can be it, and Purge, which
// only takes those ids, never removes it.
const teamDir core.PrincipalID = "team"

// kindOf returns p's kind for spc_ ids, looked up once: a principal's kind
// never changes.
func (s *Service) kindOf(ctx context.Context, p core.PrincipalID) (core.PrincipalKind, error) {
	if k, ok := s.kinds.Load(p); ok {
		return k.(core.PrincipalKind), nil
	}
	pr, err := s.store.Principals().Get(ctx, core.SystemViewer(), p)
	if err != nil {
		return "", fmt.Errorf("memory of %s: %w", p, err)
	}
	s.kinds.Store(p, pr.Kind)
	return pr.Kind, nil
}

// checkPrincipal accepts accounts and teammates. A channel gets
// ErrForbidden (channels have no memory); anything else is rejected before
// it can reach a file path. It returns the principal's kind.
func (s *Service) checkPrincipal(ctx context.Context, p core.PrincipalID) (core.PrincipalKind, error) {
	switch {
	case core.ValidID(core.PrefixUser, string(p)):
		return core.PrincipalUser, nil
	case core.ValidID(core.PrefixSpace, string(p)):
		k, err := s.kindOf(ctx, p)
		if err != nil {
			return "", err
		}
		if k != core.PrincipalPerson {
			return "", fmt.Errorf("memory of %s: channels have no memory: %w", p, core.ErrForbidden)
		}
		return k, nil
	}
	return "", fmt.Errorf("memory: principal %q: %w", p, core.ErrInvalid)
}

// isChannel reports a channel's principal, whose snapshot is empty.
func (s *Service) isChannel(ctx context.Context, p core.PrincipalID) bool {
	if !core.ValidID(core.PrefixSpace, string(p)) {
		return false
	}
	k, err := s.kindOf(ctx, p)
	return err == nil && k == core.PrincipalSpace
}

// errTeammateHeartbeat: teammates have no check-ins, so no checklist.
var errTeammateHeartbeat = invalidf("HEARTBEAT is for check-ins, which teammates don't have.")

// etag is the hex sha256 prefix of a file's content; a missing file has the
// ETag of "".
func etag(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:16])
}

func (s *Service) capOf(f core.MemoryFile) int {
	m := s.cfg.Current().Memory
	switch f {
	case core.FileMemory:
		return m.MemoryMaxBytes
	case core.FileUser:
		return m.UserMaxBytes
	case core.FileTeam:
		return m.TeamMaxBytes
	}
	return m.HeartbeatMaxBytes
}

// checkCap refuses an op that would leave the file over its cap. An op that
// does not grow the file always passes, so a file left over the cap (after the
// cap was lowered) can still be shortened entry by entry.
func (s *Service) checkCap(f core.MemoryFile, before, after string) error {
	limit := s.capOf(f)
	if len(after) <= limit || len(after) <= len(before) {
		return nil
	}
	return capError(f, len(before), len(after), limit)
}

func (s *Service) path(p core.PrincipalID, f core.MemoryFile) string {
	return filepath.Join(s.dir, string(p), fileName(f))
}

func (s *Service) readFile(p core.PrincipalID, f core.MemoryFile) (string, error) {
	b, err := os.ReadFile(s.path(p, f))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("memory: read %s: %w", fileName(f), err)
	}
	return string(b), nil
}

// writeFile replaces the file atomically: readers see the old or the new
// content, never a torn write.
func (s *Service) writeFile(p core.PrincipalID, f core.MemoryFile, content string) error {
	dir := filepath.Join(s.dir, string(p))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("memory: create dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+fileName(f)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("memory: write %s: %w", fileName(f), err)
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("memory: write %s: %w", fileName(f), err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("memory: write %s: %w", fileName(f), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("memory: write %s: %w", fileName(f), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("memory: write %s: %w", fileName(f), err)
	}
	if err := os.Rename(name, s.path(p, f)); err != nil {
		return fmt.Errorf("memory: write %s: %w", fileName(f), err)
	}
	syncDir(dir)
	return nil
}

// restoreFile puts back content read before a failed change. Empty content
// means the file may not have existed (a missing file reads as ""), so it is
// removed rather than left behind empty, e.g. after a racing purge.
func (s *Service) restoreFile(p core.PrincipalID, f core.MemoryFile, content string) error {
	if content != "" {
		return s.writeFile(p, f, content)
	}
	if err := os.Remove(s.path(p, f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("memory: restore %s: %w", fileName(f), err)
	}
	return nil
}

// syncDir makes the rename durable where the platform supports fsync on a
// directory; elsewhere (Windows) it is a no-op.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// checkText rejects content that cannot be stored as a text file.
func checkText(what, s string) error {
	if !utf8.ValidString(s) {
		return invalidf("%s is not valid UTF-8", what)
	}
	if strings.ContainsRune(s, 0) {
		return invalidf("%s contains a NUL byte", what)
	}
	return nil
}

// Entries are the non-blank lines of a memory file, compared after trimming
// surrounding whitespace. Agent ops never touch blank lines or line order, so
// a file edited by hand in the web UI keeps its layout.

func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

func joinLines(lines []string) string {
	out := strings.Join(lines, "\n")
	if strings.TrimSpace(out) == "" {
		return ""
	}
	return strings.TrimRight(out, "\n") + "\n"
}

func findEntry(lines []string, entry string) int {
	for i, l := range lines {
		if strings.TrimSpace(l) == entry {
			return i
		}
	}
	return -1
}

// normalizeOp trims the entry texts and checks that the op is complete.
func normalizeOp(op core.MemoryOp) (core.MemoryOp, error) {
	op.Old = strings.TrimSpace(op.Old)
	op.New = strings.TrimSpace(op.New)
	if !validFile(op.File) && op.File != core.FileTeam {
		return op, invalidf("file must be MEMORY, USER, HEARTBEAT or TEAM, got %q", op.File)
	}
	if err := checkText("old", op.Old); err != nil {
		return op, err
	}
	if err := checkText("new", op.New); err != nil {
		return op, err
	}
	switch op.Op {
	case "add":
		if op.New == "" {
			return op, invalidf("op=add needs new: the entry to save")
		}
		op.Old = ""
	case "replace":
		if op.Old == "" || op.New == "" {
			return op, invalidf("op=replace needs old (the exact existing entry) and new (its replacement)")
		}
		if op.Old == op.New {
			return op, invalidf("old and new are identical; nothing to change")
		}
	case "remove":
		if op.Old == "" {
			return op, invalidf("op=remove needs old: the exact entry to delete")
		}
		op.New = ""
	default:
		return op, invalidf("op must be add, replace or remove, got %q", op.Op)
	}
	if strings.ContainsAny(op.New, "\r\n") {
		return op, invalidf("one entry per call: new must be a single line")
	}
	return op, nil
}

// applyOp returns the file content after a normalized op. reveal lets a
// missing-entry error quote the file's entries (see notFoundError).
func applyOp(before string, op core.MemoryOp, reveal bool) (string, error) {
	lines := splitLines(before)
	name := fileName(op.File)
	switch op.Op {
	case "add":
		if findEntry(lines, op.New) >= 0 {
			return "", invalidf("%s already has the entry %q; nothing changed", name, op.New)
		}
		return joinLines(append(lines, op.New)), nil
	case "replace", "remove":
		i := findEntry(lines, op.Old)
		if i < 0 {
			return "", notFoundError(op.File, before, op.Old, reveal)
		}
		if op.Op == "remove" {
			return joinLines(append(lines[:i:i], lines[i+1:]...)), nil
		}
		out := append([]string(nil), lines...)
		out[i] = op.New
		return joinLines(out), nil
	}
	return "", invalidf("op must be add, replace or remove, got %q", op.Op)
}

// notFoundError helps the model retry: with reveal it names entries that
// contain the text and, for HEARTBEAT.md (which is not in the model's
// context), the whole file. The text is a tool result that no policy step
// sees, so callers reveal only into a session already tainted private;
// otherwise it would bring private data in without the taint.
func notFoundError(f core.MemoryFile, content, old string, reveal bool) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s has no entry exactly equal to %q.", fileName(f), old)
	if !reveal {
		return invalidf("%s", b.String())
	}
	var near []string
	needle := strings.ToLower(old)
	for _, l := range splitLines(content) {
		if t := strings.TrimSpace(l); t != "" && strings.Contains(strings.ToLower(t), needle) {
			near = append(near, t)
			if len(near) == 3 {
				break
			}
		}
	}
	if len(near) > 0 {
		b.WriteString(" Entries containing it:")
		for _, n := range near {
			fmt.Fprintf(&b, " %q", n)
		}
		b.WriteString(". Pass the whole entry as old.")
	}
	if f == core.FileHeartbeat {
		if strings.TrimSpace(content) == "" {
			b.WriteString(" HEARTBEAT.md is empty.")
		} else {
			b.WriteString(" Current HEARTBEAT.md:\n" + content)
		}
	}
	return invalidf("%s", b.String())
}

// capError tells the model how many bytes it has left (SPEC §4.14).
func capError(f core.MemoryFile, before, after, limit int) error {
	free := max(limit-before, 0)
	return invalidf("%s is capped at %d bytes: this change would make it %d bytes. %d bytes are free now; remove or shorten entries first.",
		fileName(f), limit, after, free)
}

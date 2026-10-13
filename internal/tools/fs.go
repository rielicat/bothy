package tools

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const (
	readCap      = 256 << 10 // fs_read output and binary-sniff budget
	maxWriteSize = 1 << 20   // fs_write content per call; larger files are written in appends
	maxDiffInput = 4 << 20
	maxDiff      = 64 << 10
	listCap      = 1000
	sniffLen     = 8 << 10
	// Workspace files are shared with the box user through group 10001
	// (umask 007); directories inherit the setgid bit from the workspace.
	fileMode fs.FileMode = 0o660
	dirMode  fs.FileMode = 0o770
)

// cleanPath turns a model-supplied path into a clean workspace-relative one.
// os.Root enforces confinement (including symlinks) at use; this rejects
// absolute and escaping paths early with a clear message.
func cleanPath(p string) (string, error) {
	switch {
	case p == "":
		return "", fmt.Errorf("%w: path is required", core.ErrInvalid)
	case strings.ContainsRune(p, 0):
		return "", fmt.Errorf("%w: path contains a NUL byte", core.ErrInvalid)
	case path.IsAbs(p) || filepath.IsAbs(p) || filepath.VolumeName(p) != "":
		return "", fmt.Errorf("%w: path %q must be relative to the workspace", core.ErrInvalid, p)
	}
	c := path.Clean(filepath.ToSlash(p))
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%w: path %q is outside the workspace", core.ErrInvalid, p)
	}
	return c, nil
}

func openRoot(env *core.ToolEnv) (*os.Root, error) {
	if env == nil || env.Workspace == "" {
		return nil, errors.New("this run has no workspace")
	}
	r, err := os.OpenRoot(env.Workspace)
	if err != nil {
		// The model sees this error; it must not learn the host path.
		return nil, fsErr("workspace", err)
	}
	return r, nil
}

var errNotRegular = errors.New("not a regular file")

// openRegular opens a workspace file for the gateway's own fs_* tools. Box
// processes write the same tree and keep running between a tool's Plan and
// Exec, so a checked path can turn into a FIFO or device: O_NONBLOCK keeps
// open(2) from blocking on it, and the type is checked on the open descriptor.
func openRegular(root *os.Root, rel string, flag int, perm fs.FileMode) (*os.File, fs.FileInfo, error) {
	f, err := root.OpenFile(rel, flag|syscall.O_NONBLOCK, perm)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, st, errNotRegular
	}
	return f, st, nil
}

// readRegular reads at most limit bytes of a regular workspace file; ok is
// false when the file is larger.
func readRegular(root *os.Root, rel string, limit int64) (data []byte, st fs.FileInfo, ok bool, err error) {
	f, st, err := openRegular(root, rel, os.O_RDONLY, 0)
	if err != nil {
		return nil, st, false, err
	}
	defer f.Close()
	if st.Size() > limit {
		return nil, st, false, nil
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, st, false, err
	}
	if int64(len(b)) > limit {
		return nil, st, false, nil
	}
	return b, st, true, nil
}

// readDir lists a workspace folder in name order without blocking on a FIFO
// that replaced it (see openRegular).
func readDir(root *os.Root, rel string) ([]fs.DirEntry, error) {
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, &fs.PathError{Op: "readdir", Path: rel, Err: syscall.ENOTDIR}
	}
	entries, err := f.ReadDir(-1)
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, err
}

// fsErr rewrites os.Root errors into messages for the model without host paths.
func fsErr(rel string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s: no such file or directory", rel)
	case errors.Is(err, fs.ErrExist):
		return fmt.Errorf("%s: already exists", rel)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%s: permission denied", rel)
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %s: %w", rel, pe.Op, pe.Err)
	}
	return fmt.Errorf("%s: %w", rel, err)
}

func isBinary(b []byte) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return true
	}
	// A rune cut at the end of the sniffed window is not a sign of binary.
	for i := 0; i < utf8.UTFMax && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	return !utf8.Valid(b)
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func objectSchema(props string, required ...string) []byte {
	req := ""
	if len(required) > 0 {
		req = `,"required":["` + strings.Join(required, `","`) + `"]`
	}
	return []byte(`{"type":"object","properties":{` + props + `}` + req + `,"additionalProperties":false}`)
}

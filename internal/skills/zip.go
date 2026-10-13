package skills

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/skills/treehash"
)

// Upload limits (SPEC §8.3: ≤ 5 MB, ≤ 200 files). The unpacked caps bound
// what a small but highly compressed archive may expand to.
const (
	maxZipBytes    = 5 << 20
	maxZipFiles    = 200
	maxZipEntries  = 2 * maxZipFiles
	maxFileBytes   = 5 << 20
	maxUnpackBytes = 20 << 20

	installTempPrefix = ".install-"
)

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{core.ErrInvalid}, args...)...)
}

// Install unpacks a skill archive into <dir>/<name> as a pending skill. The
// archive holds SKILL.md at its root or inside one top-level folder. Entry
// names are validated (no absolute paths, backslashes or ".." segments) and
// written through os.Root, symlinks and special files are refused, and
// nothing from the archive is ever executed.
func (l *Library) Install(ctx context.Context, zr io.ReaderAt, size int64, by core.UserID) (*core.SkillMeta, error) {
	if size <= 0 || size > maxZipBytes {
		return nil, invalidf("skill archive must be between 1 byte and %d MB", maxZipBytes>>20)
	}
	z, err := zip.NewReader(zr, size)
	if err != nil {
		return nil, invalidf("not a zip archive: %v", err)
	}
	entries, prefix, err := zipEntries(z)
	if err != nil {
		return nil, err
	}
	fm, err := zipFrontmatter(entries[prefix+skillFile])
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	target := l.skillDir(fm.Name)
	if _, err := os.Lstat(target); err == nil {
		return nil, fmt.Errorf("skill %s already exists; remove it first: %w", fm.Name, core.ErrConflict)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if _, err := l.store.Skills().Get(ctx, fm.Name); err == nil {
		return nil, fmt.Errorf("skill %s already exists; remove it first: %w", fm.Name, core.ErrConflict)
	} else if !errors.Is(err, core.ErrNotFound) {
		return nil, err
	}

	tmp, err := os.MkdirTemp(l.dir, installTempPrefix)
	if err != nil {
		return nil, err
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.RemoveAll(tmp)
		}
	}()
	// MkdirTemp creates 0700; boxes read /skills through group 10001, and the
	// setgid bit inherited from the skills directory must survive the chmod.
	if fi, err := os.Stat(tmp); err != nil {
		return nil, err
	} else if err := os.Chmod(tmp, fi.Mode()&os.ModeSetgid|0o750); err != nil {
		return nil, err
	}
	files, err := unpack(tmp, prefix, entries)
	if err != nil {
		return nil, err
	}
	sum, err := treehash.Sum(tmp)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(tmp, skillFile))
	if err != nil {
		return nil, err
	}
	_, body, err := parseSkill(data)
	if err != nil {
		return nil, invalidf("%v", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return nil, err
	}
	installed = true
	now := l.clock.Now()
	meta := &core.SkillMeta{
		Name: fm.Name, Description: fm.Description, Path: "skills/" + fm.Name, Hash: sum, Status: core.SkillPending,
		Tokens: estimateTokens(body), InstalledBy: by, InstalledAt: now, UpdatedAt: now,
	}
	if err := l.store.Skills().Upsert(ctx, meta); err != nil {
		_ = os.RemoveAll(target)
		return nil, err
	}
	l.record(ctx, "user:"+string(by), "skill.install", fm.Name, map[string]any{"hash": sum, "files": files})
	if err := l.refreshLocked(ctx); err != nil {
		return nil, err
	}
	return meta, nil
}

// zipEntries validates every entry name and type and returns the entries by
// name plus the folder prefix ("" or "<top>/") that holds SKILL.md.
func zipEntries(z *zip.Reader) (map[string]*zip.File, string, error) {
	if len(z.File) > maxZipEntries {
		return nil, "", invalidf("archive has more than %d entries", maxZipEntries)
	}
	entries := map[string]*zip.File{}
	files := 0
	tops := map[string]bool{}
	for _, f := range z.File {
		name := f.Name
		if strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store" {
			continue
		}
		mode := f.Mode()
		isDir := mode.IsDir() || strings.HasSuffix(name, "/")
		clean := strings.TrimSuffix(name, "/")
		switch {
		case clean == "" || strings.ContainsAny(clean, "\\\x00") || path.IsAbs(clean) ||
			path.Clean(clean) != clean || !fs.ValidPath(clean):
			return nil, "", invalidf("unsafe archive entry name %q", name)
		case mode&fs.ModeSymlink != 0:
			return nil, "", invalidf("archive entry %q is a symlink; skills cannot contain symlinks", name)
		case !isDir && !mode.IsRegular():
			return nil, "", invalidf("archive entry %q is not a regular file", name)
		case !isDir && f.UncompressedSize64 > maxFileBytes:
			return nil, "", invalidf("archive entry %q is larger than %d MB", name, maxFileBytes>>20)
		}
		if _, dup := entries[clean]; dup {
			return nil, "", invalidf("archive entry %q appears twice", name)
		}
		if !isDir {
			if files++; files > maxZipFiles {
				return nil, "", invalidf("archive has more than %d files", maxZipFiles)
			}
		}
		entries[clean] = f
		top, _, _ := strings.Cut(clean, "/")
		tops[top] = true
	}
	if f, ok := entries[skillFile]; ok && !f.Mode().IsDir() {
		return entries, "", nil
	}
	if len(tops) == 1 {
		for top := range tops {
			if f, ok := entries[top+"/"+skillFile]; ok && !f.Mode().IsDir() {
				return entries, top + "/", nil
			}
		}
	}
	return nil, "", invalidf("archive must hold %s at its root or inside a single top-level folder", skillFile)
}

func zipFrontmatter(f *zip.File) (frontmatter, error) {
	rc, err := f.Open()
	if err != nil {
		return frontmatter{}, invalidf("%s: %v", skillFile, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxReadBytes+1))
	if err != nil {
		return frontmatter{}, invalidf("%s: %v", skillFile, err)
	}
	if len(data) > maxReadBytes {
		return frontmatter{}, invalidf("%s is larger than %d KB", skillFile, maxReadBytes>>10)
	}
	fm, _, err := parseSkill(data)
	if err != nil {
		return frontmatter{}, invalidf("%v", err)
	}
	return fm, nil
}

// unpack writes the entries below prefix into dir and returns the number of
// files written.
func unpack(dir, prefix string, entries map[string]*zip.File) (int, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	var total int64
	files := 0
	for name, f := range entries {
		rel, ok := strings.CutPrefix(name, prefix)
		if !ok || rel == "" {
			continue
		}
		if f.Mode().IsDir() || strings.HasSuffix(f.Name, "/") {
			if err := root.MkdirAll(rel, 0o750); err != nil {
				return 0, err
			}
			continue
		}
		if parent := path.Dir(rel); parent != "." {
			if err := root.MkdirAll(parent, 0o750); err != nil {
				return 0, err
			}
		}
		n, err := writeEntry(root, rel, f, maxUnpackBytes-total)
		if err != nil {
			return 0, err
		}
		total += n
		files++
	}
	return files, nil
}

func writeEntry(root *os.Root, rel string, f *zip.File, budget int64) (int64, error) {
	limit := min(int64(maxFileBytes), budget)
	rc, err := f.Open()
	if err != nil {
		return 0, invalidf("%s: %v", rel, err)
	}
	defer rc.Close()
	dst, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(dst, io.LimitReader(rc, limit+1))
	closeErr := dst.Close()
	switch {
	case copyErr != nil:
		return 0, invalidf("%s: %v", rel, copyErr)
	case n > limit:
		return 0, invalidf("archive expands past the size limit at %s", rel)
	case closeErr != nil:
		return 0, closeErr
	}
	return n, nil
}

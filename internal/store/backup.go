package store

import (
	"archive/tar"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// backupFiles are the top-level files of the data directory a backup carries
// next to the database snapshot and backupTrees. The KEK and workspaces are
// deliberately absent.
var backupFiles = []string{"SOUL.md", "picture.png", "bothy.yaml", "bothy.ui.yaml"}

// backupTrees are data-directory trees copied whole: memory files and the
// knowledge module's project notes.
var backupTrees = []string{"memory", "knowledge"}

// Backup writes <dir>/bothy-<UTC yyyymmddThhmmss>.tar (0600) holding a VACUUM
// INTO snapshot of the database plus memory/**, knowledge/**, SOUL.md,
// picture.png, bothy.yaml and bothy.ui.yaml from the database's directory.
// Data-directory files are read through os.Root and symlinks are skipped, so
// nothing outside it is copied.
func (db *DB) Backup(ctx context.Context, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("store: backup: %w", err)
	}
	name := fmt.Sprintf("bothy-%s.tar", db.clock.Now().UTC().Format("20060102T150405"))
	final := filepath.Join(dir, name)
	if _, err := os.Lstat(final); err == nil {
		return "", conflict("backup " + name + " already exists")
	}
	snap, err := db.snapshot(ctx, dir)
	if err != nil {
		return "", err
	}
	defer os.Remove(snap)

	tmp, err := os.CreateTemp(dir, ".bothy-*.tar.tmp")
	if err != nil {
		return "", fmt.Errorf("store: backup: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", fmt.Errorf("store: backup: %w", err)
	}
	if err := db.writeBackup(tmp, snap); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("store: backup: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("store: backup: %w", err)
	}
	if err := os.Link(tmp.Name(), final); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", conflict("backup " + name + " already exists")
		}
		return "", fmt.Errorf("store: backup: %w", err)
	}
	return final, nil
}

// snapshot writes a consistent copy of the database into dir with VACUUM INTO,
// on a connection of its own: on the writer it would stall every write for as
// long as the copy takes, and the readers are query_only, which VACUUM INTO
// refuses. In WAL mode its read transaction does not block the writer.
func (db *DB) snapshot(ctx context.Context, dir string) (string, error) {
	f, err := os.CreateTemp(dir, ".bothy-*.db.tmp")
	if err != nil {
		return "", fmt.Errorf("store: backup: %w", err)
	}
	p := f.Name()
	// VACUUM INTO accepts an existing empty file, which keeps it 0600.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(p)
		return "", fmt.Errorf("store: backup: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(p)
		return "", fmt.Errorf("store: backup: %w", err)
	}
	conn, err := sql.Open("sqlite", dsn(db.path, false))
	if err != nil {
		os.Remove(p)
		return "", fmt.Errorf("store: backup: %w", err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	if _, err := conn.ExecContext(ctx, "VACUUM INTO ?", p); err != nil {
		os.Remove(p)
		return "", dbErr("backup snapshot", err)
	}
	return p, nil
}

func (db *DB) writeBackup(w io.Writer, snap string) error {
	tw := tar.NewWriter(w)
	now := db.clock.Now().UTC()
	if err := addFile(tw, "bothy.db", snap, now); err != nil {
		return err
	}
	root, err := os.OpenRoot(filepath.Dir(db.path))
	if err != nil {
		return fmt.Errorf("store: backup: %w", err)
	}
	defer root.Close()
	for _, name := range backupFiles {
		if err := addRootFile(tw, root, name); err != nil {
			return err
		}
	}
	for _, top := range backupTrees {
		err = fs.WalkDir(root.FS(), top, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return fs.SkipDir // nothing yet, or a principal's directory removed mid-walk
				}
				return err
			}
			if d.Type().IsRegular() {
				return addRootFile(tw, root, p)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("store: backup %s: %w", top, err)
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("store: backup: %w", err)
	}
	return nil
}

func addFile(tw *tar.Writer, name, path string, mod time.Time) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("store: backup: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("store: backup: %w", err)
	}
	return copyEntry(tw, name, fi.Size(), mod, f)
}

// addRootFile copies a regular file from the data directory; a missing file
// or anything that is not a regular file (symlinks included) is skipped. The
// memory service replaces files by rename while a backup runs, so a file may
// vanish after the walk saw it, and the size comes from the opened file.
func addRootFile(tw *tar.Writer, root *os.Root, name string) error {
	fi, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: backup: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	f, err := root.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: backup: %w", err)
	}
	defer f.Close()
	if fi, err = f.Stat(); err != nil {
		return fmt.Errorf("store: backup: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	return copyEntry(tw, filepath.ToSlash(name), fi.Size(), fi.ModTime(), f)
}

func copyEntry(tw *tar.Writer, name string, size int64, mod time.Time, r io.Reader) error {
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: size, ModTime: mod, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("store: backup %s: %w", name, err)
	}
	if _, err := io.CopyN(tw, r, size); err != nil {
		return fmt.Errorf("store: backup %s: %w", name, err)
	}
	return nil
}

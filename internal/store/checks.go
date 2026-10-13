package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

// backupMaxAge is when backups.none_recent starts to fire.
const backupMaxAge = 7 * 24 * time.Hour

// maxListed caps how many offending paths a finding names.
const maxListed = 10

type check struct {
	id  string
	run func(ctx context.Context) []core.Finding
	fix func(ctx context.Context, f core.Finding) error
}

func (c check) ID() string                             { return c.id }
func (c check) Run(ctx context.Context) []core.Finding { return c.run(ctx) }
func (c check) Fix(ctx context.Context, f core.Finding) error {
	if c.fix == nil || !f.AutoFix {
		return core.ErrNotImplemented
	}
	return c.fix(ctx, f)
}

// Checks returns sqlite.version, data.permissions (auto-fixable) and
// backups.none_recent.
func (db *DB) Checks() []core.Check {
	return []core.Check{
		check{id: "sqlite.version", run: db.checkVersion},
		check{id: "data.permissions", run: db.checkPermissions, fix: db.fixPermissions},
		check{id: "backups.none_recent", run: db.checkBackups},
	}
}

func (db *DB) checkVersion(context.Context) []core.Finding {
	if err := checkVersion(db.version); err != nil {
		return []core.Finding{{
			ID: "sqlite.version", Area: "sqlite", Severity: core.SevCritical,
			Title:  "SQLite is too old",
			Detail: err.Error(),
			Fix:    "Upgrade Bothy: the release binary links SQLite " + minSQLiteVersion + " or newer.",
		}}
	}
	return nil
}

// loosePaths lists data-directory entries that grant any group or other
// permission. The workspace tree (<data>/work in single-container mode) is
// skipped: it is shared with the sandbox uid on purpose (0660/2770).
func (db *DB) loosePaths() ([]string, error) {
	dataDir := filepath.Dir(db.path)
	var out []string
	err := filepath.WalkDir(dataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() && p == filepath.Join(dataDir, "work") {
			return fs.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if fi.Mode().Perm()&0o077 != 0 {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

func (db *DB) checkPermissions(context.Context) []core.Finding {
	paths, err := db.loosePaths()
	if err != nil {
		return []core.Finding{{
			ID: "data.permissions", Area: "data", Severity: core.SevHigh, Security: true,
			Title:  "Data directory permissions could not be checked",
			Detail: err.Error(),
		}}
	}
	if len(paths) == 0 {
		return nil
	}
	listed := paths[:min(len(paths), maxListed)]
	detail := fmt.Sprintf("%d path(s) under %s are readable by other users (files must be 0600, directories 0700): %s",
		len(paths), filepath.Dir(db.path), strings.Join(listed, ", "))
	if len(paths) > len(listed) {
		detail += ", …"
	}
	return []core.Finding{{
		ID: "data.permissions", Area: "data", Severity: core.SevHigh, Security: true, AutoFix: true,
		Title:  "Data files are readable by other users",
		Detail: detail,
		Fix:    "bothy security-audit --fix",
	}}
}

func (db *DB) fixPermissions(context.Context, core.Finding) error {
	paths, err := db.loosePaths()
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		mode := os.FileMode(0o600)
		if fi.IsDir() {
			mode = 0o700
		}
		if err := os.Chmod(p, mode); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (db *DB) checkBackups(context.Context) []core.Finding {
	var newest time.Time
	entries, err := os.ReadDir(db.backupDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return []core.Finding{{ID: "backups.none_recent", Area: "backups", Severity: core.SevLow,
			Title: "Backups could not be listed", Detail: err.Error()}}
	}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, "bothy-") || !strings.HasSuffix(n, ".tar") {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	if !newest.IsZero() && db.clock.Now().Sub(newest) < backupMaxAge {
		return nil
	}
	detail := "No backup has been written to " + db.backupDir + "."
	if !newest.IsZero() {
		detail = fmt.Sprintf("The newest backup in %s is from %s.", db.backupDir, newest.UTC().Format(time.DateOnly))
	}
	return []core.Finding{{
		ID: "backups.none_recent", Area: "backups", Severity: core.SevLow,
		Title:  "No recent backup",
		Detail: detail,
		Fix:    "bothy backup",
	}}
}

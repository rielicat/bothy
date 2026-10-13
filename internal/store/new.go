package store

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/rielicat/bothy/internal/core"
)

type Options struct {
	Path        string // <data>/bothy.db
	BackupDir   string // <data>/backups
	WorkDir     string // resolved storage.work_dir; data.permissions skips it (shared with the sandbox uid)
	BackupsKeep int
	Clock       core.Clock
	Log         *slog.Logger
}

// DB implements core.Store and core.CheckProvider.
type DB struct {
	handle

	w         *sql.DB // the single writer connection
	r         *sql.DB // two query_only reader connections
	version   string
	path      string
	backupDir string
	workDir   string
	clock     core.Clock
	log       *slog.Logger
}

var (
	_ core.Store         = (*DB)(nil)
	_ core.CheckProvider = (*DB)(nil)
)

// Open sets pragmas, refuses SQLite < 3.53.4, takes a VACUUM INTO backup when
// migrations are pending, then applies embedded migrations.
func Open(ctx context.Context, o Options) (*DB, error) {
	return open(ctx, o, migrationsFS, queryVersion)
}

func (db *DB) SQLiteVersion() string { return db.version }

func (db *DB) Close() error {
	var err error
	if db.r != nil {
		err = db.r.Close()
	}
	if db.w != nil {
		if werr := db.w.Close(); err == nil {
			err = werr
		}
	}
	return err
}

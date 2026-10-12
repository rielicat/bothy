package store

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Options struct {
	Path        string // <data>/bothy.db
	BackupDir   string // <data>/backups
	BackupsKeep int
	Clock       core.Clock
	Log         *slog.Logger
}

// DB implements core.Store and core.CheckProvider.
type DB struct {
	core.Store // skeleton stub: nil; M1 replaces with real fields

	w       *sql.DB // the single writer connection
	r       *sql.DB // two query_only reader connections
	version string
}

// Open sets pragmas, refuses SQLite < 3.53.4, takes a VACUUM INTO backup when
// migrations are pending, then applies embedded migrations (skeleton-implemented).
func Open(ctx context.Context, o Options) (*DB, error) { return open(ctx, o, migrationsFS) }

// RunRetention prunes egress_log, inbound_dedup, expired auth sessions and old
// runs per config every hour. Blocks until ctx is done.
func (db *DB) RunRetention(ctx context.Context, cfg config.View) error {
	<-ctx.Done()
	return nil
}

func (db *DB) Checks() []core.Check { return nil }

// Boot-path methods implemented in the skeleton (the rest of core.Store is M1's).
func (db *DB) Meta() core.MetaRepo { return nil }

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

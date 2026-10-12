package store

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

// TestBackupDoesNotWaitForWriter: a backup taken while a transaction holds
// the single writer connection completes without waiting for it, and its
// snapshot holds only committed data.
func TestBackupDoesNotWaitForWriter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	db := openAt(t, dir, fixedClock{time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)})
	ctx := context.Background()
	if err := db.Meta().Set(ctx, "committed", "yes"); err != nil {
		t.Fatal(err)
	}
	inTx, release, txDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		txDone <- db.Tx(ctx, func(tx core.Store) error {
			if err := tx.Meta().Set(ctx, "uncommitted", "yes"); err != nil {
				return err
			}
			close(inTx)
			<-release
			return nil
		})
	}()
	<-inTx
	bctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := db.Backup(bctx, filepath.Join(dir, "backups"))
	close(release)
	if err != nil {
		t.Fatalf("Backup while the writer is busy = %v", err)
	}
	if err := <-txDone; err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	snap := filepath.Join(t.TempDir(), "snap.db")
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			t.Fatal("backup has no bothy.db")
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name != "bothy.db" {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(snap, b, 0o600); err != nil {
			t.Fatal(err)
		}
		break
	}
	rdb, err := Open(ctx, Options{Path: snap})
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	if v, err := rdb.Meta().Get(ctx, "committed"); err != nil || v != "yes" {
		t.Errorf("snapshot lost committed data: %q, %v", v, err)
	}
	if _, err := rdb.Meta().Get(ctx, "uncommitted"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("snapshot holds an uncommitted write: %v", err)
	}
}

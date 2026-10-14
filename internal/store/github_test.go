package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/conformance"
)

func TestGitHubConformance(t *testing.T) {
	conformance.GitHubStore(t, func(t *testing.T) core.GitHubStore {
		db, err := Open(context.Background(), Options{Path: filepath.Join(t.TempDir(), "bothy.db")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db.GitHub()
	})
}

// A transaction's Store carries the GitHub repositories too, so a caller can
// combine them with other writes atomically.
func TestGitHubInsideTx(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "bothy.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id := core.NewID(core.PrefixGitHubConn)
	err = db.Tx(ctx, func(s core.Store) error {
		gs, ok := s.(interface{ GitHub() core.GitHubStore })
		if !ok {
			t.Fatal("transaction store has no GitHub()")
		}
		return gs.GitHub().PutConnection(ctx, &core.GitHubConnection{ID: id, Auth: core.GitHubPAT, SecretRef: "secret:x"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.GitHub().Connection(ctx, id); err != nil {
		t.Fatalf("committed connection: %v", err)
	}
}

package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/conformance"
)

func TestConformance(t *testing.T) {
	conformance.Store(t, func(t *testing.T) core.Store {
		db, err := Open(context.Background(), Options{Path: filepath.Join(t.TempDir(), "bothy.db")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	})
}

func TestKnowledgeConformance(t *testing.T) {
	conformance.Knowledge(t, func(t *testing.T) (core.Store, core.KnowledgeRepo) {
		db, err := Open(context.Background(), Options{Path: filepath.Join(t.TempDir(), "bothy.db")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db, db.Knowledge()
	})
}

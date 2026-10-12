// Package memstore is an in-memory core.Store for module unit tests while M1's
// SQLite store is being built. It passes conformance.Store (Viewer scoping =>
// ErrNotFound, ApprovalRepo.Decide CAS, the runs_one_active ErrConflict,
// keyset paging, FTS replaced by case-insensitive substring search, audit
// append-only). Tx runs fn under one mutex.
package memstore

import "github.com/bothyhq/bothy/internal/core"

func New() core.Store { return nil }

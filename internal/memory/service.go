package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	searchDefaultLimit = 10
	searchMaxLimit     = 50
)

// Snapshot reads MEMORY.md and USER.md together with the id of the newest
// memory event, so DeltasSince(LastID) reports exactly what changed after it.
func (s *Service) Snapshot(ctx context.Context, p core.PrincipalID) (core.MemorySnapshot, error) {
	if core.ValidID(core.PrefixSpace, string(p)) {
		return core.MemorySnapshot{}, nil
	}
	if err := checkPrincipal(p); err != nil {
		return core.MemorySnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mem, err := s.readFile(p, core.FileMemory)
	if err != nil {
		return core.MemorySnapshot{}, err
	}
	usr, err := s.readFile(p, core.FileUser)
	if err != nil {
		return core.MemorySnapshot{}, err
	}
	last, err := s.lastEventID(ctx, p)
	if err != nil {
		return core.MemorySnapshot{}, err
	}
	return core.MemorySnapshot{
		Memory:  mem,
		User:    usr,
		LastID:  last,
		Private: strings.TrimSpace(mem) != "" || strings.TrimSpace(usr) != "",
	}, nil
}

func (s *Service) lastEventID(ctx context.Context, p core.PrincipalID) (int64, error) {
	evs, err := s.store.MemoryEvents().List(ctx, core.SystemViewer(), p, "", core.Page{Limit: 1})
	if err != nil {
		return 0, fmt.Errorf("memory: last event: %w", err)
	}
	if len(evs) == 0 {
		return 0, nil
	}
	return evs[0].ID, nil
}

// Apply runs one agent op (add, replace or remove of a single-line entry).
func (s *Service) Apply(ctx context.Context, p core.PrincipalID, run core.RunID, op core.MemoryOp, prov core.Provenance) (*core.MemoryEvent, error) {
	if err := checkPrincipal(p); err != nil {
		return nil, err
	}
	op, err := normalizeOp(op)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	before, err := s.readFile(p, op.File)
	if err != nil {
		return nil, err
	}
	after, err := applyOp(before, op, false)
	if err != nil {
		return nil, err
	}
	if err := s.checkCap(op.File, before, after); err != nil {
		return nil, err
	}
	actor := "system"
	if run != "" {
		actor = "agent:" + string(run)
	}
	e := &core.MemoryEvent{
		Principal: p, File: op.File, Op: op.Op, Before: before, After: after,
		Run: run, Actor: actor, Provenance: prov, At: s.clock.Now(),
	}
	if err := s.commit(ctx, e); err != nil {
		return nil, err
	}
	s.record(ctx, "memory.write", actor, e)
	return e, nil
}

// commit writes the file, then its event row; if the row cannot be stored the
// file is put back so the files and memory_events never disagree.
func (s *Service) commit(ctx context.Context, e *core.MemoryEvent) error {
	if err := s.writeFile(e.Principal, e.File, e.After); err != nil {
		return err
	}
	id, err := s.store.MemoryEvents().Add(ctx, e)
	if err != nil {
		if rerr := s.restoreFile(e.Principal, e.File, e.Before); rerr != nil {
			s.log.Error("memory: rollback failed", "principal", e.Principal, "file", e.File, "err", rerr)
		}
		return fmt.Errorf("memory: record event: %w", err)
	}
	// The repository contract is the returned id; it need not fill e.ID.
	e.ID = id
	return nil
}

func (s *Service) record(ctx context.Context, action, actor string, e *core.MemoryEvent) {
	if s.bus != nil {
		s.bus.Publish(core.Event{Type: core.EvMemoryUpdated, Principal: e.Principal, Run: e.Run,
			Data: core.MemoryData{File: e.File, EventID: e.ID}})
	}
	if s.audit == nil {
		return
	}
	detail, _ := json.Marshal(map[string]any{
		"principal_id": e.Principal, "file": e.File, "op": e.Op, "event_id": e.ID,
		"bytes_before": len(e.Before), "bytes_after": len(e.After),
	})
	err := s.audit.Record(ctx, core.AuditEntry{
		At: e.At, Actor: actor, Action: action, Target: "memory:" + string(e.Principal) + "/" + string(e.File),
		Outcome: "ok", Run: e.Run, Detail: detail,
	})
	if err != nil {
		s.log.Error("memory: audit", "action", action, "err", err)
	}
}

func access(v core.Viewer, p core.PrincipalID, f core.MemoryFile) error {
	if !v.Can(p) {
		return fmt.Errorf("memory of %s: %w", p, core.ErrNotFound)
	}
	if err := checkPrincipal(p); err != nil {
		return err
	}
	if !validFile(f) {
		return invalidf("unknown memory file %q", f)
	}
	return nil
}

func (s *Service) Read(_ context.Context, v core.Viewer, p core.PrincipalID, f core.MemoryFile) (string, string, error) {
	if err := access(v, p, f); err != nil {
		return "", "", err
	}
	c, err := s.readFile(p, f)
	if err != nil {
		return "", "", err
	}
	return c, etag(c), nil
}

// Write replaces a whole file from the web editor. An empty tag skips the
// precondition; any other tag must equal the current ETag.
func (s *Service) Write(ctx context.Context, v core.Viewer, p core.PrincipalID, f core.MemoryFile, content, tag string, by core.UserID) (string, error) {
	if err := access(v, p, f); err != nil {
		return "", err
	}
	if err := checkText("content", content); err != nil {
		return "", err
	}
	if limit := s.capOf(f); len(content) > limit {
		return "", invalidf("%s is capped at %d bytes; this text is %d bytes", fileName(f), limit, len(content))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.readFile(p, f)
	if err != nil {
		return "", err
	}
	if tag != "" && tag != etag(cur) {
		return "", fmt.Errorf("memory %s changed since it was read: %w", fileName(f), core.ErrConflict)
	}
	if content == cur {
		return etag(cur), nil
	}
	actor := "user:" + string(by)
	e := &core.MemoryEvent{
		Principal: p, File: f, Op: "edit", Before: cur, After: content,
		Actor: actor, Provenance: core.ProvUser, At: s.clock.Now(),
	}
	if err := s.commit(ctx, e); err != nil {
		return "", err
	}
	s.record(ctx, "memory.edit", actor, e)
	return etag(content), nil
}

func (s *Service) History(ctx context.Context, v core.Viewer, p core.PrincipalID, f core.MemoryFile, page core.Page) ([]*core.MemoryEvent, error) {
	if err := access(v, p, f); err != nil {
		return nil, err
	}
	evs, err := s.store.MemoryEvents().List(ctx, v, p, f, page)
	if err != nil {
		return nil, fmt.Errorf("memory: history: %w", err)
	}
	return evs, nil
}

// Search is full-text search over the Viewer's sessions.
func (s *Service) Search(ctx context.Context, v core.Viewer, query string, limit int) ([]core.SearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, invalidf("search query is empty")
	}
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	limit = min(limit, searchMaxLimit)
	hits, err := s.store.Messages().Search(ctx, v, query, limit)
	if err != nil {
		return nil, fmt.Errorf("memory: search: %w", err)
	}
	return hits, nil
}

// Purge deletes every memory file of a principal (user purge). The
// memory_events rows go with the principal through ON DELETE CASCADE.
func (s *Service) Purge(_ context.Context, p core.PrincipalID) error {
	if !core.ValidID(core.PrefixUser, string(p)) && !core.ValidID(core.PrefixSpace, string(p)) {
		return fmt.Errorf("memory: principal %q: %w", p, core.ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(filepath.Join(s.dir, string(p))); err != nil {
		return fmt.Errorf("memory: purge %s: %w", p, err)
	}
	return nil
}

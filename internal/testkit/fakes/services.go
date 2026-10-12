package fakes

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

var (
	_ core.Memory    = (*Memory)(nil)
	_ core.Skills    = (*Skills)(nil)
	_ core.MCP       = (*MCP)(nil)
	_ core.Scheduler = (*Scheduler)(nil)
)

// Memory, Skills, MCP and Scheduler embed their interface and implement the
// read paths over in-memory maps (files, skill bodies, statuses, schedules).
//
// Memory also implements Apply, Write and DeltasSince over Files: entries are
// lines, add appends one, replace and remove act on the first exact match of
// Old. Space principals ("spc_…") get an empty Snapshot and core.ErrForbidden
// from the other file methods, as with the real service. ETags are sha256 hex
// prefixes. DeltasSince renders one "<FILE> <op>: <text>" line per event.
type Memory struct {
	core.Memory
	Files map[core.PrincipalID]map[core.MemoryFile]string

	mu     sync.Mutex
	events []core.MemoryEvent
}

func isSpace(p core.PrincipalID) bool { return strings.HasPrefix(string(p), core.PrefixSpace+"_") }

func etag(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:8])
}

func (m *Memory) Tools() []core.Tool { return nil }

func (m *Memory) Snapshot(_ context.Context, p core.PrincipalID) (core.MemorySnapshot, error) {
	if isSpace(p) {
		return core.MemorySnapshot{}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := core.MemorySnapshot{Memory: m.Files[p][core.FileMemory], User: m.Files[p][core.FileUser]}
	for _, e := range m.events {
		if e.Principal == p {
			s.LastID = e.ID
		}
	}
	s.Private = s.Memory != "" || s.User != ""
	return s, nil
}

func (m *Memory) setLocked(p core.PrincipalID, f core.MemoryFile, content string) {
	if m.Files == nil {
		m.Files = map[core.PrincipalID]map[core.MemoryFile]string{}
	}
	if m.Files[p] == nil {
		m.Files[p] = map[core.MemoryFile]string{}
	}
	m.Files[p][f] = content
}

func (m *Memory) addEventLocked(e core.MemoryEvent) core.MemoryEvent {
	e.ID = int64(len(m.events)) + 1
	e.At = time.Now()
	m.events = append(m.events, e)
	return e
}

func (m *Memory) Apply(_ context.Context, p core.PrincipalID, run core.RunID, op core.MemoryOp, prov core.Provenance) (*core.MemoryEvent, error) {
	if isSpace(p) {
		return nil, fmt.Errorf("memory of %s: %w", p, core.ErrForbidden)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.Files[p][op.File]
	var after string
	switch op.Op {
	case "add":
		after = before
		if after != "" && !strings.HasSuffix(after, "\n") {
			after += "\n"
		}
		after += op.New
	case "replace", "remove":
		if op.Old == "" || !strings.Contains(before, op.Old) {
			return nil, fmt.Errorf("%w: entry not found in %s", core.ErrInvalid, op.File)
		}
		if op.Op == "replace" {
			after = strings.Replace(before, op.Old, op.New, 1)
		} else {
			after = strings.Replace(before, op.Old+"\n", "", 1)
			if after == before {
				after = strings.TrimSuffix(strings.Replace(before, op.Old, "", 1), "\n")
			}
		}
	default:
		return nil, fmt.Errorf("%w: unknown memory op %q", core.ErrInvalid, op.Op)
	}
	m.setLocked(p, op.File, after)
	e := m.addEventLocked(core.MemoryEvent{
		Principal: p, File: op.File, Op: op.Op, Before: before, After: after,
		Run: run, Actor: "agent:" + string(run), Provenance: prov,
	})
	return &e, nil
}

func (m *Memory) Read(_ context.Context, v core.Viewer, p core.PrincipalID, f core.MemoryFile) (string, string, error) {
	if err := memoryAccess(v, p); err != nil {
		return "", "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.Files[p][f]
	return c, etag(c), nil
}

func memoryAccess(v core.Viewer, p core.PrincipalID) error {
	if !v.Can(p) {
		return fmt.Errorf("memory of %s: %w", p, core.ErrNotFound)
	}
	if isSpace(p) {
		return fmt.Errorf("memory of %s: %w", p, core.ErrForbidden)
	}
	return nil
}

// Write replaces the file when tag matches its current ETag (an empty tag
// always matches) and records an "edit" event.
func (m *Memory) Write(_ context.Context, v core.Viewer, p core.PrincipalID, f core.MemoryFile, content, tag string, by core.UserID) (string, error) {
	if err := memoryAccess(v, p); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.Files[p][f]
	if tag != "" && tag != etag(before) {
		return "", fmt.Errorf("memory %s: %w", f, core.ErrConflict)
	}
	m.setLocked(p, f, content)
	m.addEventLocked(core.MemoryEvent{Principal: p, File: f, Op: "edit", Before: before, After: content, Actor: "user:" + string(by), Provenance: core.ProvUser})
	return etag(content), nil
}

func (m *Memory) DeltasSince(_ context.Context, p core.PrincipalID, afterID int64) (string, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	last := afterID
	for _, e := range m.events {
		if e.Principal != p || e.ID <= afterID {
			continue
		}
		last = e.ID
		text := e.After
		switch e.Op {
		case "add":
			text = strings.TrimPrefix(e.After, e.Before)
			text = strings.TrimPrefix(text, "\n")
		case "remove":
			text = e.Before
		}
		fmt.Fprintf(&b, "%s %s: %s\n", e.File, e.Op, text)
	}
	return b.String(), last, nil
}

// History returns the file's events newest first; p.Before bounds the id.
func (m *Memory) History(_ context.Context, v core.Viewer, p core.PrincipalID, f core.MemoryFile, page core.Page) ([]*core.MemoryEvent, error) {
	if err := memoryAccess(v, p); err != nil {
		return nil, err
	}
	limit := page.Limit
	if limit <= 0 {
		limit = 50
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*core.MemoryEvent
	for i := len(m.events) - 1; i >= 0 && len(out) < limit; i-- {
		e := m.events[i]
		if e.Principal == p && e.File == f && (page.Before <= 0 || e.ID < page.Before) {
			out = append(out, &e)
		}
	}
	return out, nil
}

func (m *Memory) Search(context.Context, core.Viewer, string, int) ([]core.SearchHit, error) {
	return nil, nil
}

// Skills serves Metas and Bodies; Files holds each skill's extra files by
// relative path. Index lists enabled skills whose Hash equals ApprovedHash.
// SetEnabled approves the current hash.
type Skills struct {
	core.Skills
	Metas  []core.SkillMeta
	Bodies map[string]string

	mu    sync.Mutex
	Files map[string]map[string][]byte
}

func (s *Skills) Tools() []core.Tool { return nil }

func (s *Skills) Index(context.Context) []core.SkillMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.SkillMeta
	for _, m := range s.Metas {
		if m.Status == core.SkillEnabled && m.Hash == m.ApprovedHash {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b core.SkillMeta) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

func (s *Skills) Load(_ context.Context, name string) (string, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.Bodies[name]
	if !ok {
		return "", nil, fmt.Errorf("skill %q: %w", name, core.ErrNotFound)
	}
	var files []string
	for rel := range s.Files[name] {
		files = append(files, rel)
	}
	slices.Sort(files)
	return body, files, nil
}

func (s *Skills) ReadFile(_ context.Context, name, rel string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.Files[name][rel]
	if !ok {
		return nil, fmt.Errorf("skill file %s/%s: %w", name, rel, core.ErrNotFound)
	}
	return slices.Clone(b), nil
}

func (s *Skills) List(context.Context) ([]*core.SkillMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*core.SkillMeta, len(s.Metas))
	for i := range s.Metas {
		m := s.Metas[i]
		out[i] = &m
	}
	slices.SortFunc(out, func(a, b *core.SkillMeta) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

func (s *Skills) SetEnabled(_ context.Context, name string, on bool, by core.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Metas {
		m := &s.Metas[i]
		if m.Name != name {
			continue
		}
		m.Status = core.SkillDisabled
		if on {
			m.Status, m.ApprovedHash, m.ApprovedBy = core.SkillEnabled, m.Hash, by
		}
		m.UpdatedAt = time.Now()
		return nil
	}
	return fmt.Errorf("skill %q: %w", name, core.ErrNotFound)
}

func (s *Skills) Remove(_ context.Context, name string, _ core.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.Metas, func(m core.SkillMeta) bool { return m.Name == name })
	if i < 0 {
		return fmt.Errorf("skill %q: %w", name, core.ErrNotFound)
	}
	s.Metas = slices.Delete(s.Metas, i, i+1)
	delete(s.Bodies, name)
	delete(s.Files, name)
	return nil
}

func (s *Skills) Rescan(context.Context) error { return nil }

// MCP reports StatusV; Start, Reload and Close succeed without doing anything.
type MCP struct {
	core.MCP
	StatusV []core.MCPStatus

	mu sync.Mutex
}

func (m *MCP) Start(context.Context) error { return nil }

func (m *MCP) Status() []core.MCPStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.StatusV)
}

func (m *MCP) Pins(context.Context, string) ([]*core.MCPPin, error) { return nil, nil }
func (m *MCP) Reload(context.Context, string) error                 { return nil }
func (m *MCP) Close() error                                         { return nil }

// Scheduler keeps Schedules, Viewer-scoped like the real service (a schedule
// of a principal outside the Viewer is core.ErrNotFound). Upsert assigns ids;
// RunNow records the id in RunNows and returns a fresh run id. Preview
// supports once and heartbeat specs only.
type Scheduler struct {
	core.Scheduler
	Schedules map[core.ScheduleID]*core.Schedule

	mu      sync.Mutex
	RunNows []core.ScheduleID
	PausedV bool
}

func (s *Scheduler) Tools() []core.Tool { return nil }

func (s *Scheduler) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (s *Scheduler) Upsert(_ context.Context, v core.Viewer, sc *core.Schedule, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !v.Can(sc.Principal) {
		return fmt.Errorf("schedule %s: %w", sc.ID, core.ErrNotFound)
	}
	if old, ok := s.Schedules[sc.ID]; ok && !v.Can(old.Principal) {
		return fmt.Errorf("schedule %s: %w", sc.ID, core.ErrNotFound)
	}
	now := time.Now()
	if sc.ID == "" {
		sc.ID = core.ScheduleID(core.NewID(core.PrefixSchedule))
		sc.CreatedAt = now
	}
	if sc.CreatedBy == "" {
		sc.CreatedBy = by
	}
	sc.UpdatedAt = now
	if s.Schedules == nil {
		s.Schedules = map[core.ScheduleID]*core.Schedule{}
	}
	cp := *sc
	s.Schedules[sc.ID] = &cp
	return nil
}

func (s *Scheduler) get(v core.Viewer, id core.ScheduleID) (*core.Schedule, error) {
	sc, ok := s.Schedules[id]
	if !ok || !v.Can(sc.Principal) {
		return nil, fmt.Errorf("schedule %s: %w", id, core.ErrNotFound)
	}
	return sc, nil
}

func (s *Scheduler) Delete(_ context.Context, v core.Viewer, id core.ScheduleID, _ core.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.get(v, id); err != nil {
		return err
	}
	delete(s.Schedules, id)
	return nil
}

func (s *Scheduler) RunNow(_ context.Context, v core.Viewer, id core.ScheduleID, _ core.UserID) (core.RunID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.get(v, id); err != nil {
		return "", err
	}
	s.RunNows = append(s.RunNows, id)
	return core.RunID(core.NewID(core.PrefixRun)), nil
}

func (s *Scheduler) Preview(kind core.ScheduleKind, spec, tz string, n int) ([]time.Time, error) {
	loc := time.UTC
	if tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("%w: timezone %q", core.ErrInvalid, tz)
		}
		loc = l
	}
	switch kind {
	case core.SchedOnce:
		t, err := time.ParseInLocation(time.RFC3339, spec, loc)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", core.ErrInvalid, err)
		}
		return []time.Time{t}, nil
	case core.SchedHeartbeat:
		d, err := time.ParseDuration(spec)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("%w: interval %q", core.ErrInvalid, spec)
		}
		now := time.Now().In(loc)
		out := make([]time.Time, n)
		for i := range out {
			out[i] = now.Add(time.Duration(i+1) * d)
		}
		return out, nil
	}
	return nil, fmt.Errorf("fakes.Scheduler: preview of %s: %w", kind, core.ErrNotImplemented)
}

func (s *Scheduler) SetPaused(paused bool) {
	s.mu.Lock()
	s.PausedV = paused
	s.mu.Unlock()
}

package memory

import (
	"context"
	"fmt"

	"github.com/rielicat/bothy/internal/core"
)

// errTeamNeedsApproval: Apply is the agent's direct write path, and TEAM.md
// reaches every chat, so only an approved memory tool call may change it.
var errTeamNeedsApproval = &userError{
	msg:  "TEAM.md is remembered for everyone and changes only with an admin's approval.",
	kind: core.ErrForbidden,
}

// ReadTeam returns TEAM.md, what Bothy remembers for everyone, and its ETag.
func (s *Service) ReadTeam(context.Context) (string, string, error) {
	c, err := s.readFile(teamDir, core.FileTeam)
	if err != nil {
		return "", "", err
	}
	return c, etag(c), nil
}

// WriteTeam replaces TEAM.md from the web editor. by must be an enabled
// admin, checked here as well as by the route, because the text reaches
// every chat. An empty tag skips the precondition, as in Write.
func (s *Service) WriteTeam(ctx context.Context, content, tag string, by core.UserID) (string, error) {
	if err := s.checkAdmin(ctx, by); err != nil {
		return "", err
	}
	if err := checkText("content", content); err != nil {
		return "", err
	}
	if limit := s.capOf(core.FileTeam); len(content) > limit {
		return "", invalidf("TEAM.md is capped at %d bytes; this text is %d bytes", limit, len(content))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.readFile(teamDir, core.FileTeam)
	if err != nil {
		return "", err
	}
	if tag != "" && tag != etag(cur) {
		return "", fmt.Errorf("memory TEAM.md changed since it was read: %w", core.ErrConflict)
	}
	if content == cur {
		return etag(cur), nil
	}
	actor := "user:" + string(by)
	e := &core.MemoryEvent{
		File: core.FileTeam, Op: "edit", Before: cur, After: content,
		Actor: actor, Provenance: core.ProvUser, At: s.clock.Now(),
	}
	if err := s.commit(ctx, e); err != nil {
		return "", err
	}
	s.record(ctx, "memory.edit", actor, e, "")
	return etag(content), nil
}

func (s *Service) checkAdmin(ctx context.Context, by core.UserID) error {
	if by == "" {
		return fmt.Errorf("memory TEAM.md: no user: %w", core.ErrForbidden)
	}
	u, err := s.store.Users().Get(ctx, by)
	if err != nil || u.Disabled || !u.Role.IsAdmin() {
		return fmt.Errorf("memory TEAM.md: only an admin can change it: %w", core.ErrForbidden)
	}
	return nil
}

// TeamHistory lists TEAM.md's changes, newest first.
func (s *Service) TeamHistory(ctx context.Context, page core.Page) ([]*core.MemoryEvent, error) {
	evs, err := s.store.TeamMemoryEvents().List(ctx, page)
	if err != nil {
		return nil, fmt.Errorf("memory: team history: %w", err)
	}
	return evs, nil
}

// teamChange is one agent op on TEAM.md that an admin approved.
type teamChange struct {
	run        core.RunID
	approval   core.ApprovalID
	approvedBy core.UserID
	op         core.MemoryOp
	prov       core.Provenance
}

// applyTeam runs an approved op on TEAM.md. Only the memory tool's Exec
// calls it, after checking the approval; it is not part of core.Memory.
func (s *Service) applyTeam(ctx context.Context, c teamChange) (*core.MemoryEvent, error) {
	op, err := normalizeOp(c.op)
	if err != nil {
		return nil, err
	}
	if op.File != core.FileTeam || c.run == "" || c.approval == "" {
		return nil, fmt.Errorf("memory: team change without its file, run or approval: %w", core.ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	before, err := s.readFile(teamDir, core.FileTeam)
	if err != nil {
		return nil, err
	}
	// Revealing near misses is safe: TEAM.md is in every prompt already.
	after, err := applyOp(before, op, true)
	if err != nil {
		return nil, err
	}
	if err := s.checkCap(core.FileTeam, before, after); err != nil {
		return nil, err
	}
	actor := "agent:" + string(c.run)
	e := &core.MemoryEvent{
		File: core.FileTeam, Op: op.Op, Before: before, After: after,
		Run: c.run, Approval: c.approval, Actor: actor, Provenance: c.prov, At: s.clock.Now(),
	}
	if err := s.commit(ctx, e); err != nil {
		return nil, err
	}
	s.record(ctx, "memory.write", actor, e, c.approvedBy)
	return e, nil
}

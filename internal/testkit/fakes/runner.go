package fakes

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

var _ core.Runner = (*Runner)(nil)

// Runner records Submit/Stop/StopAll/Resume calls and returns Result (or Err)
// from Submit; Paused/SetPaused toggle a flag; ResolveSession/NewSession/Fork
// create sessions in Store (a memstore) when set.
//
// Submit records the request first, then returns core.ErrPaused while paused,
// else Err, else Result; a zero Result gets a fresh run id per call. Without a
// Store, sessions live in the fake. Stop always reports a stopped run.
type Runner struct {
	core.Runner
	Store   core.Store
	Result  core.SubmitResult
	Err     error
	Submits []core.RunRequest
	Stops   []core.SessionID
	Resumed []core.ApprovalID
	PausedV bool
	// PausedByV and PausedAtV are who paused and when; SetPaused fills them
	// with the user id, or the audit origin's chat actor when by is "".
	PausedByV string
	PausedAtV time.Time
	// StopPrincipals records StopPrincipal calls; StopPrincipalN is the
	// count each returns.
	StopPrincipals []core.PrincipalID
	StopPrincipalN int

	mu       sync.Mutex
	StopAlls int
	sessions map[core.SessionID]*core.Session
}

func (r *Runner) StopPrincipal(_ context.Context, p core.PrincipalID) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.StopPrincipals = append(r.StopPrincipals, p)
	return r.StopPrincipalN, nil
}

// StoppedPrincipals returns a copy of StopPrincipals.
func (r *Runner) StoppedPrincipals() []core.PrincipalID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.PrincipalID(nil), r.StopPrincipals...)
}

func (r *Runner) Submit(_ context.Context, req core.RunRequest) (core.SubmitResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Submits = append(r.Submits, req)
	return r.resultLocked()
}

func (r *Runner) resultLocked() (core.SubmitResult, error) {
	if r.PausedV {
		return core.SubmitResult{}, core.ErrPaused
	}
	if r.Err != nil {
		return core.SubmitResult{}, r.Err
	}
	res := r.Result
	if res == (core.SubmitResult{}) {
		res.Run = core.RunID(core.NewID(core.PrefixRun))
	}
	return res, nil
}

func (r *Runner) Stop(_ context.Context, _ core.Viewer, s core.SessionID, _ core.UserID) (bool, error) {
	r.mu.Lock()
	r.Stops = append(r.Stops, s)
	r.mu.Unlock()
	return true, nil
}

func (r *Runner) StopAll(context.Context, core.UserID) (int, error) {
	r.mu.Lock()
	r.StopAlls++
	r.mu.Unlock()
	return 0, nil
}

func (r *Runner) SetPaused(ctx context.Context, paused bool, by core.UserID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	who := string(by)
	if o, ok := core.AuditOriginFrom(ctx); ok && who == "" {
		who = o.Actor
	}
	switch {
	case !paused:
		r.PausedByV, r.PausedAtV = "", time.Time{}
	case !r.PausedV:
		r.PausedByV, r.PausedAtV = who, time.Now()
	}
	r.PausedV = paused
	return nil
}

func (r *Runner) Paused() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.PausedV }

func (r *Runner) PausedBy() (string, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.PausedByV, r.PausedAtV
}

func (r *Runner) Resume(_ context.Context, a *core.Approval) error {
	r.mu.Lock()
	r.Resumed = append(r.Resumed, a.ID)
	r.mu.Unlock()
	return nil
}

func (r *Runner) Active() []core.RunInfo { return nil }

func (r *Runner) ResolveSession(ctx context.Context, p core.PrincipalID, chat core.ChatRef) (*core.Session, error) {
	key := chat.Key()
	if r.Store != nil {
		s, err := r.Store.Sessions().ByChannelKey(ctx, p, key)
		if err == nil || !errors.Is(err, core.ErrNotFound) {
			return s, err
		}
	} else {
		r.mu.Lock()
		for _, s := range r.sessions {
			if s.Principal == p && s.ChannelKey == key && !s.Archived {
				cp := *s
				r.mu.Unlock()
				return &cp, nil
			}
		}
		r.mu.Unlock()
	}
	return r.NewSession(ctx, p, chat.Channel, &chat)
}

// NewSession sets ChannelKey from chat when chat is not nil.
func (r *Runner) NewSession(ctx context.Context, p core.PrincipalID, origin string, chat *core.ChatRef) (*core.Session, error) {
	now := time.Now()
	s := &core.Session{
		ID: core.SessionID(core.NewID(core.PrefixSession)), Principal: p, Origin: origin,
		ModelSlot: core.SlotMain, CreatedAt: now, UpdatedAt: now,
	}
	if chat != nil {
		c := *chat
		s.Chat, s.ChannelKey = &c, c.Key()
	}
	return s, r.save(ctx, s)
}

func (r *Runner) save(ctx context.Context, s *core.Session) error {
	if r.Store != nil {
		return r.Store.Sessions().Create(ctx, s)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions = map[core.SessionID]*core.Session{}
	}
	cp := *s
	r.sessions[s.ID] = &cp
	return nil
}

// Fork archives a channel session and moves its channel key to the fork,
// which hangs from the old session (parent, and its root or itself as root).
func (r *Runner) Fork(ctx context.Context, v core.Viewer, id core.SessionID, _ core.UserID) (*core.Session, error) {
	var old *core.Session
	if r.Store != nil {
		s, err := r.Store.Sessions().Get(ctx, v, id)
		if err != nil {
			return nil, err
		}
		old = s
	} else {
		r.mu.Lock()
		s, ok := r.sessions[id]
		if ok && v.Can(s.Principal) {
			cp := *s
			old = &cp
		}
		r.mu.Unlock()
		if old == nil {
			return nil, fmt.Errorf("session %s: %w", id, core.ErrNotFound)
		}
	}
	if old.ChannelKey != "" {
		if r.Store != nil {
			if err := r.Store.Sessions().Archive(ctx, v, old.ID); err != nil {
				return nil, err
			}
		} else {
			r.mu.Lock()
			r.sessions[old.ID].Archived = true
			r.mu.Unlock()
		}
	}
	now := time.Now()
	fork := &core.Session{
		ID: core.SessionID(core.NewID(core.PrefixSession)), Principal: old.Principal, Origin: old.Origin,
		ChannelKey: old.ChannelKey, Chat: old.Chat, ModelSlot: old.ModelSlot, CreatedAt: now, UpdatedAt: now, LastMessageAt: now,
		ParentID: old.ID, RootID: cmp.Or(old.RootID, old.ID), TitleAuto: true,
	}
	if old.Title != "" {
		fork.Title = old.Title + " (clean)"
	}
	return fork, r.save(ctx, fork)
}

func (r *Runner) Compact(context.Context, core.Viewer, core.SessionID) error { return nil }

func (r *Runner) ContextPreview(context.Context, core.Viewer, core.SessionID) (*core.Breakdown, error) {
	return &core.Breakdown{}, nil
}

// Continue returns what Submit would, without recording a request.
func (r *Runner) Continue(context.Context, core.Viewer, core.RunID, core.UserID) (core.SubmitResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resultLocked()
}

func (r *Runner) Constitution() (string, string) { return "fake-1", "Fake constitution." }

// Submitted returns a copy of Submits.
func (r *Runner) Submitted() []core.RunRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.RunRequest(nil), r.Submits...)
}

// Stopped returns a copy of Stops.
func (r *Runner) Stopped() []core.SessionID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.SessionID(nil), r.Stops...)
}

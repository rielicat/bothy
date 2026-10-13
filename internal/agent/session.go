package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

var channelOrigins = map[string]bool{"telegram": true, "discord": true, "slack": true}

// ResolveSession returns the open session of a channel chat for a principal,
// keyed by ChatRef.Key(), creating it on first contact. With
// agent.session_idle_reset set, a session idle for longer is archived and a
// fresh one started. Web and API sessions have no channel key.
func (r *Runner) ResolveSession(ctx context.Context, p core.PrincipalID, chat core.ChatRef) (*core.Session, error) {
	if !channelOrigins[chat.Channel] {
		return nil, fmt.Errorf("agent: channel %q: %w", chat.Channel, core.ErrInvalid)
	}
	key := chat.Key()
	for range 3 {
		s, err := r.d.Store.Sessions().ByChannelKey(ctx, p, key)
		switch {
		case err == nil:
			reset := r.cfg().Agent.SessionIdleReset.D()
			if reset <= 0 || s.LastMessageAt.IsZero() || r.now().Sub(s.LastMessageAt) < reset {
				return s, nil
			}
			if err := r.d.Store.Sessions().Archive(ctx, core.SystemViewer(), s.ID); err != nil {
				return nil, err
			}
		case !errors.Is(err, core.ErrNotFound):
			return nil, err
		}
		c := chat
		s, err = r.NewSession(ctx, p, chat.Channel, &c)
		if errors.Is(err, core.ErrConflict) {
			continue // another message created it first
		}
		return s, err
	}
	return nil, fmt.Errorf("agent: resolve session for %s: %w", key, core.ErrConflict)
}

func (r *Runner) NewSession(ctx context.Context, p core.PrincipalID, origin string, chat *core.ChatRef) (*core.Session, error) {
	switch origin {
	case "web", "telegram", "discord", "slack", "api", "schedule":
	default:
		return nil, fmt.Errorf("agent: session origin %q: %w", origin, core.ErrInvalid)
	}
	if _, err := r.d.Store.Principals().Get(ctx, core.SystemViewer(), p); err != nil {
		return nil, err
	}
	now := r.now()
	s := &core.Session{
		ID:        core.SessionID(core.NewID(core.PrefixSession)),
		Principal: p,
		Origin:    origin,
		ModelSlot: core.SlotMain,
		Hidden:    origin == "api",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if chat != nil {
		c := *chat
		s.Chat = &c
		if channelOrigins[origin] {
			s.ChannelKey = c.Key()
		}
	}
	if err := r.d.Store.Sessions().Create(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

// Fork creates the clean fork of §5.3: no history, taint 0, a system note
// linking the old session. For a channel session it archives the old one and
// moves the channel key to the fork, so the chat continues in the fork.
func (r *Runner) Fork(ctx context.Context, v core.Viewer, id core.SessionID, by core.UserID) (*core.Session, error) {
	old, err := r.d.Store.Sessions().Get(ctx, v, id)
	if err != nil {
		return nil, err
	}
	now := r.now()
	title := strings.TrimSpace(old.Title)
	fork := &core.Session{
		ID:            core.SessionID(core.NewID(core.PrefixSession)),
		Principal:     old.Principal,
		Origin:        old.Origin,
		ChannelKey:    old.ChannelKey,
		Chat:          old.Chat,
		ModelSlot:     old.ModelSlot,
		ModelOverride: old.ModelOverride,
		Hidden:        old.Hidden,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if title != "" {
		fork.Title = truncateRunes(title, 70) + " (clean)"
	}
	err = r.d.Store.Tx(ctx, func(tx core.Store) error {
		if old.ChannelKey != "" {
			if err := tx.Sessions().Archive(ctx, core.SystemViewer(), old.ID); err != nil {
				return err
			}
		}
		return tx.Sessions().Create(ctx, fork)
	})
	if err != nil {
		return nil, fmt.Errorf("agent: fork %s: %w", id, err)
	}
	name := title
	if name == "" {
		name = string(old.ID)
	}
	r.notice(ctx, fork.ID, fork.Principal, "", fmt.Sprintf("Clean fork of “%s” (%s). History and taint were not carried over; "+
		"the workspace files are shared, so reading ones that came from untrusted sources taints this session too.", name, old.ID), nil)
	r.d.Bus.Publish(core.Event{Type: core.EvSessionUpdated, Principal: fork.Principal, Session: fork.ID,
		Data: core.SessionData{Title: fork.Title}})
	return fork, nil
}

// Compact runs a compaction now (/compact). It waits behind the session's
// active run, if any. Summarizing is an LLM call, so the kill switch refuses
// it like a run.
func (r *Runner) Compact(ctx context.Context, v core.Viewer, id core.SessionID) error {
	sess, err := r.d.Store.Sessions().Get(ctx, v, id)
	if err != nil {
		return err
	}
	if err := r.ensureInit(ctx); err != nil {
		return err
	}
	if r.paused.Load() {
		return core.ErrPaused
	}
	p, err := r.d.Store.Principals().Get(ctx, core.SystemViewer(), sess.Principal)
	if err != nil {
		return err
	}
	j := &compactJob{user: p.User, done: make(chan error, 1)}
	for {
		w, err := r.worker(id)
		if err != nil {
			return err
		}
		w.mu.Lock()
		if w.dead {
			w.mu.Unlock()
			continue
		}
		w.queue = append(w.queue, j)
		w.signal()
		w.mu.Unlock()
		break
	}
	select {
	case err := <-j.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ContextPreview estimates the next request for the session's head model.
func (r *Runner) ContextPreview(ctx context.Context, v core.Viewer, id core.SessionID) (*core.Breakdown, error) {
	if _, err := r.d.Store.Sessions().Get(ctx, v, id); err != nil {
		return nil, err
	}
	if err := r.ensureInit(ctx); err != nil {
		return nil, err
	}
	rs := &runState{r: r, ctx: ctx, cancel: func(error) {}, done: make(chan struct{}), req: core.RunRequest{Session: id}}
	if err := r.loadRunContext(ctx, rs); err != nil {
		return nil, err
	}
	rs.req.Principal, rs.req.User, rs.req.Trigger, rs.req.Chat = rs.sess.Principal, rs.principal.User, core.TrigMessage, rs.sess.Chat
	if end := r.resolveModel(ctx, rs); end != nil {
		return nil, fmt.Errorf("agent: %s: %w", end.err, core.ErrInvalid)
	}
	if err := r.ensureSnapshot(ctx, rs); err != nil {
		return nil, err
	}
	hist, err := r.loadHistory(ctx, rs)
	if err != nil {
		return nil, err
	}
	bd := r.newAssembler(rs, hist).assemble(rs.chain[0], false).bd
	return &bd, nil
}

// Continue starts a new run in the session of a run stopped by its steps
// budget, with 10 more steps. The caller is a signed-in user who can answer
// cards (the HTTP route is cookie-only), so the run is message-triggered, but
// it keeps the provenance of the run it continues: pressing Continue on a run
// an untrusted participant started does not make their instructions the
// user's (floor F6).
func (r *Runner) Continue(ctx context.Context, v core.Viewer, id core.RunID, by core.UserID) (core.SubmitResult, error) {
	run, err := r.d.Store.Runs().Get(ctx, v, id)
	if err != nil {
		return core.SubmitResult{}, err
	}
	if run.Status != core.RunBudgetExceeded || !strings.Contains(run.Error, string(core.BudgetSteps)) {
		return core.SubmitResult{}, fmt.Errorf("agent: run %s did not stop on its steps budget: %w", id, core.ErrInvalid)
	}
	sess, err := r.d.Store.Sessions().Get(ctx, v, run.Session)
	if err != nil {
		return core.SubmitResult{}, err
	}
	limits := r.d.Budget.DefaultLimits(core.TrigMessage)
	limits.MaxSteps = 10
	req := core.RunRequest{
		Session: sess.ID, Principal: sess.Principal, User: by, Trigger: core.TrigMessage,
		Input: []core.Part{{Type: core.PartText, Text: "Continue."}}, Provenance: r.runProvenance(ctx, run.Session, run.ID),
		Sender: "web:" + string(by), Chat: sess.Chat, Limits: &limits,
	}
	if sess.Chat != nil {
		if h := r.hubOrNil(); h != nil {
			req.Sink = h.StreamTo(*sess.Chat, "stream")
		}
	}
	return r.Submit(ctx, req)
}

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// refineScanPages bounds how far back GET /api/runs/{id} looks for a run's
// refined answer (pages of 200 messages, newest first).
const refineScanPages = 10

// refineInfo is GET /api/refine: the refine mode defaults, for the composer
// toggle and its cost note.
func (s *Server) refineInfo(w http.ResponseWriter, _ *http.Request, _ core.Actor) error {
	c := s.cfg().Agent.Refine
	slot := core.SlotMain
	if c.CriticSlot == string(core.SlotFast) {
		slot = core.SlotFast
	}
	return ok(w, dto.RefineInfo{Enabled: c.Enabled, Drafts: c.Drafts, Depth: c.Depth, EarlyStop: c.EarlyStop, CriticSlot: slot,
		MaxCalls: core.RefineMaxCalls(c.Drafts, c.Depth)})
}

// sessionRefine is the session's own refine setting, "" when it has none.
func (s *Server) sessionRefine(ctx context.Context, id core.SessionID) (string, error) {
	v, err := s.d.Store.Meta().Get(ctx, core.RefineMetaKey(id))
	switch {
	case errors.Is(err, core.ErrNotFound):
		return "", nil
	case err != nil:
		return "", err
	case v == core.RefineOn || v == core.RefineOff:
		return v, nil
	}
	return "", nil
}

// refineHeader reads X-Bothy-Refine on /v1/chat/completions: "on" or "off"
// for this answer; absent follows the session and agent.refine.enabled.
func refineHeader(v string) (*bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return nil, nil
	case core.RefineOn:
		on := true
		return &on, nil
	case core.RefineOff:
		off := false
		return &off, nil
	}
	return nil, &v1Err{status: http.StatusBadRequest, code: "invalid_request", param: "X-Bothy-Refine", msg: `X-Bothy-Refine must be "on" or "off"`}
}

func validRefine(v string) bool { return v == "" || v == core.RefineOn || v == core.RefineOff }

func (s *Server) setSessionRefine(ctx context.Context, id core.SessionID, v string) error {
	key := core.RefineMetaKey(id)
	if v != "" {
		return s.d.Store.Meta().Set(ctx, key, v)
	}
	if err := s.d.Store.Meta().Delete(ctx, key); err != nil && !errors.Is(err, core.ErrNotFound) {
		return err
	}
	return nil
}

// runRefineTrace finds the trace of the run's last refined answer. A refined
// answer makes more agent calls than the run has steps, so other runs cost no
// lookup; the search stops at messages older than the run.
func (s *Server) runRefineTrace(ctx context.Context, v core.Viewer, run *core.Run, calls []core.CallInfo) (*core.RefineTrace, error) {
	agent := 0
	for _, c := range calls {
		if c.Purpose == "agent" {
			agent++
		}
	}
	if agent <= run.Steps {
		return nil, nil
	}
	p := core.Page{Limit: 200}
	for range refineScanPages {
		msgs, err := s.d.Store.Messages().Page(ctx, v, run.Session, p)
		if err != nil {
			return nil, err
		}
		for _, m := range msgs {
			if m.CreatedAt.Before(run.CreatedAt) {
				return nil, nil
			}
			if m.Run != run.ID || m.Role != core.MsgAssistant {
				continue
			}
			for _, part := range m.Parts {
				if part.Type == core.PartRefine && part.Refine != nil {
					return part.Refine, nil
				}
			}
		}
		if len(msgs) < p.Limit {
			return nil, nil
		}
		p.Before = msgs[len(msgs)-1].Seq
	}
	return nil, nil
}

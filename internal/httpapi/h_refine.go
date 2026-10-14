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

const (
	// refineEstimateRuns and refineEstimateSteps bound how far back the
	// estimate looks for the chat's answer steps.
	refineEstimateRuns  = 10
	refineEstimateSteps = 3
)

// refineEstimate is GET /api/sessions/{id}/refine-estimate: what refining the
// chat's next reply would cost, from its recent answer steps.
func (s *Server) refineEstimate(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	sess, err := s.session(ctx, a, sessionID(r))
	if err != nil {
		return err
	}
	c := s.cfg().Agent.Refine
	drafts, depth := max(c.Drafts, 1), max(c.Depth, 0)
	out := dto.RefineEstimate{Multiplier: refineMultiplier(drafts, depth, c.EarlyStop), Drafts: drafts}
	base, found, err := s.answerStepCost(ctx, a.Viewer, sess.ID)
	if err != nil {
		return err
	}
	if found {
		usd := float64(base) * out.Multiplier / 1e6
		out.USD = &usd
	}
	return ok(w, out)
}

// refineMultiplier is how many answer steps a refined reply costs: every
// draft once, and continue, critique and correct for each draft in every
// round. Early stop is counted as ending halfway through the rounds, since
// drafts that agree stop deepening.
func refineMultiplier(drafts, depth int, earlyStop bool) float64 {
	rounds := depth
	if earlyStop {
		rounds = (depth + 1) / 2
	}
	return float64(drafts * (1 + 3*rounds))
}

// answerStepCost averages the cost of the chat's latest priced answer steps:
// the first model call of each run's last step, which refine mode repeats.
func (s *Server) answerStepCost(ctx context.Context, v core.Viewer, id core.SessionID) (core.Micros, bool, error) {
	runs, err := s.d.Store.Runs().List(ctx, v, core.RunFilter{Session: id, Page: core.Page{Limit: refineEstimateRuns}})
	if err != nil {
		return 0, false, err
	}
	var sum core.Micros
	n := 0
	for _, run := range runs {
		if n == refineEstimateSteps {
			break
		}
		if run.Status != core.RunDone {
			continue // its last step may not be an answer
		}
		calls, err := s.d.Store.Runs().LLMCalls(ctx, v, run.ID)
		if err != nil {
			return 0, false, err
		}
		var answer *core.CallInfo
		for i := range calls {
			c := &calls[i]
			if c.Purpose == "agent" && c.Error == "" && (answer == nil || c.Step > answer.Step) {
				answer = c
			}
		}
		if answer != nil && answer.PriceKnown {
			sum += answer.Cost
			n++
		}
	}
	if n == 0 {
		return 0, false, nil
	}
	return sum / core.Micros(n), true, nil
}

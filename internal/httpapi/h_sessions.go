package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/httpapi/dto"
)

const (
	maxPinChars  = 2000
	maxTitle     = 200
	compactLimit = 5 * time.Minute
)

func sessionID(r *http.Request) core.SessionID { return core.SessionID(r.PathValue("id")) }

func (s *Server) session(ctx context.Context, a core.Actor, id core.SessionID) (*core.Session, error) {
	return s.d.Store.Sessions().Get(ctx, a.Viewer, id)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	p, err := page(r)
	if err != nil {
		return err
	}
	archived, err := boolParam(r, "archived")
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := core.SessionFilter{Principal: core.PrincipalID(q.Get("principal")), Archived: archived, Query: q.Get("q"), Page: p}
	ss, err := s.d.Store.Sessions().List(r.Context(), a.Viewer, f)
	if err != nil {
		return err
	}
	return ok(w, listOf(derefs(ss), p, func(s core.Session) int64 { return ms(s.LastMessageAt) }))
}

func derefs[T any](xs []*T) []T {
	out := make([]T, 0, len(xs))
	for _, x := range xs {
		out = append(out, *x)
	}
	return out
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.SessionCreateReq
	if err := decode(r, &req); err != nil {
		return err
	}
	p := req.Principal
	if p == "" {
		p = core.UserPrincipal(a.User)
	}
	if !a.Viewer.Can(p) {
		return errNotFound
	}
	title := strings.TrimSpace(req.Title)
	if utf8.RuneCountInString(title) > maxTitle {
		return badRequest("title must be at most %d characters", maxTitle)
	}
	ctx := r.Context()
	sess, err := s.d.Runner.NewSession(ctx, p, "web", nil)
	if err != nil {
		return err
	}
	if title != "" {
		sess.Title = title
		sess.UpdatedAt = s.now()
		if err := s.d.Store.Sessions().Update(ctx, sess); err != nil {
			return err
		}
	}
	return ok(w, sess)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	sess, err := s.session(ctx, a, sessionID(r))
	if err != nil {
		return err
	}
	pins, err := s.d.Store.Pins().List(ctx, a.Viewer, sess.ID)
	if err != nil {
		return err
	}
	d := dto.SessionDetail{Session: *sess, Pins: nonNil(pins), Pending: []*core.Approval{}}
	for _, ri := range s.d.Runner.Active() {
		if ri.Session == sess.ID {
			d.Active = &ri
			break
		}
	}
	pending, err := s.d.Approvals.Pending(ctx, a.Viewer)
	if err != nil {
		return err
	}
	for _, ap := range pending {
		if ap.Session == sess.ID {
			d.Pending = append(d.Pending, ap)
		}
	}
	return ok(w, d)
}

func (s *Server) patchSession(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.SessionPatchReq
	if err := decode(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	sess, err := s.session(ctx, a, sessionID(r))
	if err != nil {
		return err
	}
	if req.Title != nil {
		t := strings.TrimSpace(*req.Title)
		if utf8.RuneCountInString(t) > maxTitle || strings.ContainsFunc(t, isControl) {
			return badRequest("title must be at most %d printable characters", maxTitle)
		}
		sess.Title = t
	}
	if req.ModelOverride != nil {
		if m := strings.TrimSpace(*req.ModelOverride); m != "" {
			if _, err := core.ParseModelRef(m); err != nil {
				return badRequest("model_override: %v", err)
			}
			sess.ModelOverride = m
		} else {
			sess.ModelOverride = ""
		}
	}
	sess.UpdatedAt = s.now()
	if err := s.d.Store.Sessions().Update(ctx, sess); err != nil {
		return err
	}
	if s.d.Bus != nil {
		s.d.Bus.Publish(core.Event{Type: core.EvSessionUpdated, Principal: sess.Principal, Session: sess.ID,
			Data: core.SessionData{Title: sess.Title, Taint: sess.Taint, Epoch: sess.Epoch}})
	}
	return ok(w, sess)
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

func (s *Server) archiveSession(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if err := s.d.Store.Sessions().Archive(r.Context(), a.Viewer, sessionID(r)); err != nil {
		return err
	}
	return noContent(w)
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	p, err := page(r)
	if err != nil {
		return err
	}
	msgs, err := s.d.Store.Messages().Page(r.Context(), a.Viewer, sessionID(r), p)
	if err != nil {
		return err
	}
	slices.Reverse(msgs)
	return ok(w, dto.MessagePage{Messages: nonNil(msgs), HasMore: len(msgs) >= pageLimit(p)})
}

// runChat is the web ChatRef of a session (message tool target, card routing).
func runChat(id core.SessionID) *core.ChatRef {
	return &core.ChatRef{Channel: "web", ChatID: string(id), Kind: core.ChatWeb}
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.SendReq
	if err := decode(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	sess, err := s.session(ctx, a, sessionID(r))
	if err != nil {
		return err
	}
	if sess.Archived {
		return newError(http.StatusConflict, "conflict", "this session is archived")
	}
	text := strings.TrimSpace(req.Text)
	if text == "" && len(req.Uploads) == 0 {
		return badRequest("text or uploads required")
	}
	var parts []core.Part
	if text != "" {
		parts = append(parts, core.Part{Type: core.PartText, Text: req.Text})
	}
	for _, id := range req.Uploads {
		up, err := s.d.Store.Uploads().Get(ctx, a.Viewer, id)
		if err != nil {
			return err
		}
		if up.Principal != sess.Principal {
			return errNotFound
		}
		pt := core.PartFile
		if strings.HasPrefix(up.MIME, "image/") {
			pt = core.PartImage
		}
		parts = append(parts, core.Part{Type: pt, Blob: &core.Blob{MIME: up.MIME, Name: up.Name, Ref: up.Ref, Size: up.Size}})
	}
	rr := core.RunRequest{Session: sess.ID, Principal: sess.Principal, User: a.User, Trigger: core.TrigMessage, Input: parts,
		Provenance: core.ProvUser, Sender: "web:" + string(a.User), Chat: runChat(sess.ID), OnApproval: "deny"}
	// Bearer clients cannot answer approval cards, so their runs follow the
	// token's on_approval like /v1 (trigger api: admin-class actions denied).
	if a.Via == "token" {
		rr.Trigger, rr.Sender, rr.OnApproval = core.TrigAPI, "api:"+a.TokenID, a.OnApproval
		rr.ApprovalWait = s.cfg().Policy.Approval.APIWait.D()
	}
	res, err := s.d.Runner.Submit(ctx, rr)
	if err != nil {
		return err
	}
	return accepted(w, res)
}

// Handlers that delegate to the runner check visibility through the store
// first, so isolation never depends on the runner alone.
func (s *Server) stopSession(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if _, err := s.session(r.Context(), a, sessionID(r)); err != nil {
		return err
	}
	stopped, err := s.d.Runner.Stop(r.Context(), a.Viewer, sessionID(r), a.User)
	if err != nil {
		return err
	}
	return ok(w, dto.StopResp{Stopped: stopped})
}

// compactSession runs the compaction in the background: it may take a model
// call, and progress arrives on the event stream. A request while one is
// already running for the session joins it instead of starting another.
func (s *Server) compactSession(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	sess, err := s.session(r.Context(), a, sessionID(r))
	if err != nil {
		return err
	}
	v := a.Viewer
	s.jobs.start(r.Context(), "compact:"+string(sess.ID), compactLimit, func(ctx context.Context) {
		if err := s.d.Runner.Compact(ctx, v, sess.ID); err != nil {
			s.log.Warn("compaction failed", "session", sess.ID, "err", err)
		}
	})
	return accepted(w, nil)
}

func (s *Server) sessionContext(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if _, err := s.session(r.Context(), a, sessionID(r)); err != nil {
		return err
	}
	b, err := s.d.Runner.ContextPreview(r.Context(), a.Viewer, sessionID(r))
	if err != nil {
		return err
	}
	return ok(w, b)
}

func (s *Server) forkSession(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if _, err := s.session(r.Context(), a, sessionID(r)); err != nil {
		return err
	}
	sess, err := s.d.Runner.Fork(r.Context(), a.Viewer, sessionID(r), a.User)
	if err != nil {
		return err
	}
	return ok(w, sess)
}

func (s *Server) listPins(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if _, err := s.session(r.Context(), a, sessionID(r)); err != nil {
		return err
	}
	pins, err := s.d.Store.Pins().List(r.Context(), a.Viewer, sessionID(r))
	if err != nil {
		return err
	}
	return ok(w, nonNil(pins))
}

func (s *Server) addPin(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.PinReq
	if err := decode(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	sess, err := s.session(ctx, a, sessionID(r))
	if err != nil {
		return err
	}
	text := strings.TrimSpace(req.Text)
	if text == "" && req.MsgSeq > 0 {
		msgs, err := s.d.Store.Messages().Range(ctx, a.Viewer, sess.ID, req.MsgSeq, 1)
		if err != nil {
			return err
		}
		if len(msgs) == 0 || msgs[0].Seq != req.MsgSeq {
			return errNotFound
		}
		text = strings.TrimSpace(msgs[0].PlainText())
	}
	if text == "" {
		return badRequest("text or msg_seq required")
	}
	if utf8.RuneCountInString(text) > maxPinChars {
		return badRequest("a pin holds at most %d characters", maxPinChars)
	}
	p := &core.Pin{Session: sess.ID, Text: text, Source: "user", MsgSeq: req.MsgSeq, CreatedAt: s.now()}
	if err := s.d.Store.Pins().Add(ctx, p); err != nil {
		return err
	}
	return ok(w, p)
}

func (s *Server) deletePin(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	id, err := strconv.ParseInt(r.PathValue("pin"), 10, 64)
	if err != nil {
		return errNotFound
	}
	if err := s.d.Store.Pins().Delete(r.Context(), a.Viewer, sessionID(r), id); err != nil {
		return err
	}
	return noContent(w)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	p, err := page(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := core.RunFilter{Session: core.SessionID(q.Get("session")), Schedule: core.ScheduleID(q.Get("schedule")),
		Status: core.RunStatus(q.Get("status")), Page: p}
	runs, err := s.d.Store.Runs().List(r.Context(), a.Viewer, f)
	if err != nil {
		return err
	}
	return ok(w, listOf(derefs(runs), p, func(r core.Run) int64 { return ms(r.CreatedAt) }))
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	id := core.RunID(r.PathValue("id"))
	run, err := s.d.Store.Runs().Get(ctx, a.Viewer, id)
	if err != nil {
		return err
	}
	calls, err := s.d.Store.Runs().LLMCalls(ctx, a.Viewer, id)
	if err != nil {
		return err
	}
	tools, err := s.d.Store.Runs().ToolCalls(ctx, a.Viewer, id)
	if err != nil {
		return err
	}
	aps, err := s.d.Store.Approvals().ByRun(ctx, id)
	if err != nil {
		return err
	}
	aps = slices.DeleteFunc(aps, func(ap *core.Approval) bool { return !a.Viewer.Can(ap.Principal) })
	return ok(w, dto.RunDetail{Run: run, LLMCalls: nonNil(calls), ToolCalls: nonNil(tools), Approvals: nonNil(aps)})
}

func (s *Server) continueRun(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	id := core.RunID(r.PathValue("id"))
	if _, err := s.d.Store.Runs().Get(r.Context(), a.Viewer, id); err != nil {
		return err
	}
	res, err := s.d.Runner.Continue(r.Context(), a.Viewer, id, a.User)
	if err != nil {
		return err
	}
	return accepted(w, res)
}

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	p, err := page(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	switch status := r.URL.Query().Get("status"); status {
	case "recent":
		aps, err := s.d.Approvals.Recent(ctx, a.Viewer, p)
		if err != nil {
			return err
		}
		return ok(w, listOf(aps, p, func(ap *core.Approval) int64 { return ms(ap.CreatedAt) }))
	case "", "pending", "parked":
		aps, err := s.d.Approvals.Pending(ctx, a.Viewer)
		if err != nil {
			return err
		}
		if status != "" {
			aps = slices.DeleteFunc(aps, func(ap *core.Approval) bool { return string(ap.Status) != status })
		}
		return ok(w, dto.List[*core.Approval]{Items: nonNil(aps)})
	default:
		return badRequest("status must be pending, parked or recent")
	}
}

func (s *Server) getApproval(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ap, err := s.d.Approvals.Get(r.Context(), a.Viewer, core.ApprovalID(r.PathValue("id")))
	if err != nil {
		return err
	}
	return ok(w, ap)
}

func (s *Server) resolveApproval(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.ApprovalResolveReq
	if err := decode(r, &req); err != nil {
		return err
	}
	switch req.Choice {
	case core.AllowOnce, core.AllowSession, core.AllowAlways, core.Deny:
	default:
		return badRequest("choice must be allow_once, allow_session, allow_always or deny")
	}
	if utf8.RuneCountInString(req.Comment) > 1000 {
		return badRequest("comment must be at most 1000 characters")
	}
	ctx := r.Context()
	id := core.ApprovalID(r.PathValue("id"))
	if _, err := s.d.Approvals.Get(ctx, a.Viewer, id); err != nil {
		return err
	}
	ap, err := s.d.Approvals.Resolve(ctx, core.Resolution{ID: id, Choice: req.Choice, By: a.User, ByRole: a.Role, Via: "web", Comment: req.Comment})
	if err != nil {
		return err
	}
	return ok(w, ap)
}

func (s *Server) listGrants(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	q := r.URL.Query()
	gs, err := s.d.Store.Grants().List(r.Context(), a.Viewer, core.GrantScope(q.Get("scope")), q.Get("scope_id"))
	if err != nil {
		return err
	}
	return ok(w, nonNil(gs))
}

// validGrant rejects grants that are not narrow: whole-tool grants, admin
// or financial classes, and wildcard domains outside principal scope.
func validGrant(g core.Grant) error {
	if !g.MaxSE.Valid() {
		return badRequest("max_se must be a side-effect class")
	}
	if g.MaxSE == core.SEAdmin || g.MaxSE == core.SEFinancial {
		return badRequest("admin and financial actions can never be granted")
	}
	switch g.Kind {
	case core.GrantTool:
		if g.Tool == "" || len(g.Pattern) == 0 {
			return badRequest("a tool grant needs a tool and a non-empty pattern; whole-tool grants are not allowed")
		}
		for ptr, glob := range g.Pattern {
			if strings.TrimSpace(glob) == "" || strings.Trim(glob, "*") == "" {
				return badRequest("pattern %q matches everything; whole-tool grants are not allowed", ptr)
			}
		}
	case core.GrantDomain:
		host, port, ok := strings.Cut(g.Value, ":")
		if !ok || host == "" || port == "" {
			return badRequest("a domain grant value is host:port")
		}
		if strings.Contains(host, "*") && (g.Scope != core.GrantPrincipal || !strings.HasPrefix(host, "*.") || strings.Count(host, "*") != 1) {
			return badRequest("wildcard domains (*.suffix) are allowed only in principal grants")
		}
	case core.GrantRecipient:
		if g.Value == "" || strings.Contains(g.Value, "*") {
			return badRequest("a recipient grant needs an exact value")
		}
	default:
		return badRequest("kind must be tool, domain or recipient")
	}
	if g.AllowWhenTainted && g.Kind != core.GrantRecipient {
		return badRequest("allow_when_tainted is only valid for recipient grants")
	}
	return nil
}

func (s *Server) createGrant(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.GrantCreateReq
	if err := decode(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	av, err := s.adminViewer(ctx, a)
	if err != nil {
		return err
	}
	if _, err := s.d.Store.Principals().Get(ctx, av, req.Principal); err != nil {
		return err
	}
	g := core.Grant{Principal: req.Principal, Scope: core.GrantPrincipal, Kind: req.Kind, Tool: req.Tool, Pattern: req.Pattern,
		Value: req.Value, MaxSE: req.MaxSE, CreatedBy: a.User, CreatedAt: s.now(), ExpiresAt: req.ExpiresAt}
	if err := validGrant(g); err != nil {
		return err
	}
	if !g.ExpiresAt.IsZero() && !g.ExpiresAt.After(s.now()) {
		return badRequest("expires_at is in the past")
	}
	id, err := s.d.Policy.Grant(ctx, g)
	if err != nil {
		return err
	}
	g.ID = id
	return ok(w, g)
}

func (s *Server) revokeGrant(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	v := a.Viewer
	if isAdmin(a) {
		av, err := s.adminViewer(ctx, a)
		if err != nil {
			return err
		}
		v = av
	}
	if err := s.d.Policy.Revoke(ctx, v, core.GrantID(r.PathValue("id")), a.User); err != nil {
		return err
	}
	return noContent(w)
}

// policyPreview plans a sample call and runs the policy on it without any
// side effect: Plan may read but never writes, sends, execs or dials.
func (s *Server) policyPreview(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.PolicyPreviewReq
	if err := decode(r, &req); err != nil {
		return err
	}
	if s.tools == nil {
		return newError(http.StatusServiceUnavailable, "unavailable", "the tool registry is not wired into the HTTP API in this build")
	}
	tool, found := s.tools.Get(req.Tool)
	if !found {
		return errNotFound
	}
	ctx := r.Context()
	trigger := req.Trigger
	if trigger == "" {
		trigger = core.TrigMessage
	}
	pid := core.UserPrincipal(a.User)
	var taint core.Taint
	if req.Session != "" {
		sess, err := s.session(ctx, a, req.Session)
		if err != nil {
			return err
		}
		pid, taint = sess.Principal, sess.Taint
	}
	pr, err := s.d.Store.Principals().Get(ctx, a.Viewer, pid)
	if err != nil {
		return err
	}
	env := &core.ToolEnv{User: a.User, Role: a.Role, Principal: *pr, Session: req.Session, Trigger: trigger, Taint: taint,
		Interactive: trigger.Interactive(), Workspace: s.workspaceDir(pr.Workspace)}
	args := req.Args
	if len(args) == 0 {
		args = []byte("{}")
	}
	act, err := tool.Plan(ctx, env, args)
	if err != nil {
		return badRequest("plan: %v", err)
	}
	spec := tool.Spec()
	in := core.PolicyInput{User: a.User, Role: a.Role, Principal: *pr, Session: req.Session, Trigger: trigger, Action: act,
		Spec: spec, Taint: taint, Provenance: core.ProvUser}
	dec := s.d.Policy.Decide(ctx, in)
	resp := dto.PolicyPreviewResp{Action: act, Decision: dec}
	if dec.Verdict == core.VerdictAsk {
		card := s.d.Approvals.RenderCard(core.ApprovalRequest{Session: req.Session, Principal: pid, Action: act, Spec: spec, Decision: dec,
			Context: "Policy preview"})
		resp.Card = &card
	}
	return ok(w, resp)
}

func (s *Server) workspaceDir(ws core.WorkspaceID) string {
	if s.d.WorkDir == "" || !core.ValidID(core.PrefixWorkspace, string(ws)) {
		return ""
	}
	return filepath.Join(s.d.WorkDir, "ws", string(ws))
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return badRequest("limit must be between 1 and 100")
		}
		limit = n
	}
	if q == "" {
		return ok(w, []core.SearchHit{})
	}
	if utf8.RuneCountInString(q) > 500 {
		return badRequest("query too long")
	}
	hits, err := s.d.Store.Messages().Search(r.Context(), a.Viewer, q, limit)
	if err != nil {
		return err
	}
	return ok(w, nonNil(hits))
}

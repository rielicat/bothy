package httpapi

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/auth"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// uiStateRoutes serve what the redesigned screens read instead of working
// it out on the client (docs/design/ui.md §17, after S1–S20).
func (s *Server) uiStateRoutes() []route {
	return []route{
		{pattern: "GET /api/me", access: accessUser, h: s.getProfile},
		// Cookie-only: where approval requests go is part of answering them,
		// which tokens cannot do either.
		{pattern: "PATCH /api/me", access: accessUserCookie, h: s.patchProfile},
		{pattern: "POST /api/search/test", access: accessAdmin, h: s.testSearch},
		{pattern: "GET /api/skills/{name}/files/{path...}", access: accessUser, h: s.skillFile},
		{pattern: "GET /api/knowledge/repos/{id}/notes/history", access: accessUser, h: s.notesHistory},
		{pattern: "GET /api/knowledge/repos/{id}/notes/history/{version}", access: accessUser, h: s.notesVersion},
		{pattern: "GET /api/github/repos/{owner}/{repo}/workflows", access: accessAdmin, h: s.githubWorkflows},
	}
}

// ---- profile ----

type profileEditor interface {
	UpdateProfile(ctx context.Context, by core.Actor, p auth.ProfileUpdate) (*core.User, error)
}

type heartbeatSyncer interface{ ResyncHeartbeat() }

const approvalsWeb = "web"

// profile describes u's own settings: the chat apps they linked, and where
// approval requests reach them as things stand now.
func (s *Server) profile(ctx context.Context, u *core.User) (*dto.Profile, error) {
	ids, err := s.d.Store.Identities().ForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	p := &dto.Profile{User: u.ID, Username: u.Username, DisplayName: cmpOr(u.DisplayName, u.Username), Timezone: u.Timezone,
		PreferredChat: u.PreferredChat, ApprovalsIn: approvalsWeb, Chats: []string{}}
	var last *core.ChannelIdentity
	for _, id := range ids {
		if id.Status != core.IdentityLinked || id.User != u.ID || id.DMChat == "" {
			continue
		}
		if !slices.Contains(p.Chats, id.Channel) {
			p.Chats = append(p.Chats, id.Channel)
		}
		if last == nil || id.LastSeen.After(last.LastSeen) {
			last = id
		}
	}
	slices.Sort(p.Chats)
	switch {
	case u.ApprovalsWebOnly:
	case slices.Contains(p.Chats, u.PreferredChat):
		p.ApprovalsIn = u.PreferredChat
	case last != nil:
		p.ApprovalsIn = last.Channel
	}
	return p, nil
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	u, err := s.d.Store.Users().Get(r.Context(), a.User)
	if err != nil {
		return err
	}
	p, err := s.profile(r.Context(), u)
	if err != nil {
		return err
	}
	return ok(w, p)
}

func (s *Server) patchProfile(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ed, has := s.d.Auth.(profileEditor)
	if !has {
		return core.ErrNotImplemented
	}
	var req dto.ProfilePatch
	if err := decode(r, &req); err != nil {
		return err
	}
	up := auth.ProfileUpdate{DisplayName: req.DisplayName, Timezone: req.Timezone, PreferredChat: req.PreferredChat}
	if req.ApprovalsIn != nil {
		webOnly := *req.ApprovalsIn == approvalsWeb
		up.ApprovalsWebOnly = &webOnly
		if !webOnly {
			if req.PreferredChat != nil && *req.PreferredChat != *req.ApprovalsIn {
				return badRequest("approvals_in and preferred_chat name different chat apps")
			}
			up.PreferredChat = req.ApprovalsIn
		}
	}
	u, err := ed.UpdateProfile(r.Context(), a, up)
	if err != nil {
		return err
	}
	// The owner's check-in runs in their time zone, which the scheduler
	// otherwise reads again only when the configuration changes.
	if hb, has := s.d.Scheduler.(heartbeatSyncer); has && req.Timezone != nil && u.Role == core.RoleOwner {
		hb.ResyncHeartbeat()
	}
	p, err := s.profile(r.Context(), u)
	if err != nil {
		return err
	}
	return ok(w, p)
}

// ---- providers ----

// credentialSources fills the plain credential source of providers whose
// key is a reference in the configuration (bedrock rows already say theirs).
func (s *Server) credentialSources(ctx context.Context, rows []core.ProviderHealth) {
	var metas []core.SecretMeta
	loaded := false
	for i := range rows {
		if rows[i].Source != "" {
			continue
		}
		ref := ""
		for _, p := range s.cfg().Providers {
			if p.ID == rows[i].ID {
				ref = p.APIKey
			}
		}
		switch {
		case ref == "":
		case strings.HasPrefix(ref, "aws-sm:"):
			rows[i].Source = "AWS Secrets Manager"
		case strings.HasPrefix(ref, "secret:"):
			if !loaded && s.d.Vault != nil {
				metas, _ = s.d.Vault.List(ctx, core.GlobalScope)
				loaded = true
			}
			rows[i].Source = "Saved key"
			name := strings.TrimPrefix(ref, "secret:")
			for _, m := range metas {
				if m.Name == name && m.Source == "env" {
					rows[i].Source = "Environment variable"
				}
			}
		}
	}
}

// ---- web search check ----

type searchTester interface {
	TestSearch(ctx context.Context, provider, searxngURL string, allowPrivate bool, key core.Secret) error
}

func (s *Server) testSearch(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	st, has := s.d.Egress.(searchTester)
	if !has {
		return core.ErrNotImplemented
	}
	var req dto.SearchTestReq
	if err := decode(r, &req); err != nil {
		return err
	}
	switch req.Provider {
	case "searxng":
		u, err := url.Parse(req.SearxngURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(req.SearxngURL) > 2048 {
			return badRequest("searxng_url must be an http or https address")
		}
		if req.APIKey != "" {
			return badRequest("searxng takes no api_key")
		}
	case "brave", "tavily":
		if req.SearxngURL != "" {
			return badRequest("searxng_url applies to searxng only")
		}
		if len(req.APIKey) > maxSecretValue {
			return badRequest("api_key must be at most 64 KB")
		}
	default:
		return badRequest("provider must be searxng, brave or tavily")
	}
	if req.APIKey != "" && s.d.Vault != nil {
		// The key is write-only: keep it out of logs and the error.
		id := "searchtest:" + requestID()
		s.d.Vault.Redactor().Add(id, []byte(req.APIKey))
		defer s.d.Vault.Redactor().Remove(id)
	}
	err := st.TestSearch(r.Context(), req.Provider, req.SearxngURL, s.cfg().Search.SearxngAllowPrivate, core.NewSecret([]byte(req.APIKey)))
	switch {
	case err == nil:
		return ok(w, dto.SearchTestResp{OK: true})
	case errors.Is(err, core.ErrInvalid):
		return err
	}
	return ok(w, dto.SearchTestResp{Code: cmp.Or(core.CodeOf(err), core.FailUnknown), Error: s.redact(err.Error())})
}

// ---- skills ----

// skillFile is one file of a skill as plain text: any skill for admins, an
// enabled one for others, read through the skill library's os.Root and
// size cap. It is served inert (text/plain, sandboxed) since anyone who can
// write the skills directory chose the bytes.
func (s *Server) skillFile(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	m, err := s.skillMeta(ctx, a, r.PathValue("name"))
	if err != nil {
		return err
	}
	data, err := s.d.Skills.ReadFile(ctx, m.Name, r.PathValue("path"))
	if err != nil {
		if errors.Is(err, core.ErrForbidden) {
			return errNotFound
		}
		return err
	}
	if !isText(data) {
		return newError(http.StatusUnsupportedMediaType, "bad_request", "this file is not text")
	}
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data) //nolint:gosec // text/plain, nosniff and a sandbox CSP: the bytes are never run as a page
	return nil
}

func isText(b []byte) bool { return utf8.Valid(b) && !slices.Contains(b, 0) }

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

const (
	skillDiffFiles = 50
	skillDiffBytes = 512 << 10
	skillFileName  = "SKILL.md"
)

// skillChanges compares a skill's files with the ones an admin approved:
// what a review needs to see. Text within the caps is sent whole on both
// sides; the client renders the diff.
func (s *Server) skillChanges(ctx context.Context, m *core.SkillMeta) *dto.SkillChanges {
	approved, err := s.d.Store.Skills().Approved(ctx, m.Name)
	if err != nil {
		s.log.Warn("approved skill files", "skill", m.Name, "err", err)
		return nil
	}
	out := &dto.SkillChanges{Files: []dto.SkillFileChange{}}
	if m.Status == core.SkillChanged && len(approved) == 0 {
		out.NoBaseline = true
		return out
	}
	_, files, err := s.d.Skills.Load(ctx, m.Name)
	if err != nil {
		return nil
	}
	before := make(map[string]core.SkillFile, len(approved))
	for _, f := range approved {
		before[f.Path] = f
	}
	budget := skillDiffBytes
	add := func(c dto.SkillFileChange, text ...string) {
		if len(out.Files) >= skillDiffFiles {
			out.Truncated = true
			return
		}
		n := 0
		for _, t := range text {
			n += len(t)
		}
		if !c.Opaque && n > budget {
			c.Before, c.After, c.Opaque, out.Truncated = "", "", true, true
		}
		if !c.Opaque {
			budget -= n
		}
		out.Files = append(out.Files, c)
	}
	for _, p := range append([]string{skillFileName}, slices.DeleteFunc(files, func(f string) bool { return f == skillFileName })...) {
		data, rerr := s.d.Skills.ReadFile(ctx, m.Name, p)
		text := rerr == nil && isText(data)
		old, had := before[p]
		delete(before, p)
		switch {
		case !had:
			c := dto.SkillFileChange{Path: p, Change: "added", Opaque: !text}
			if text {
				c.After = string(data)
			}
			add(c, c.After)
		case rerr == nil && old.SHA256 == sha256Hex(data), rerr != nil && old.SHA256 == "":
		default:
			c := dto.SkillFileChange{Path: p, Change: "changed", Opaque: !text || !old.Text}
			if !c.Opaque {
				c.Before, c.After = old.Content, string(data)
			}
			add(c, c.Before, c.After)
		}
	}
	gone := make([]string, 0, len(before))
	for p := range before {
		gone = append(gone, p)
	}
	slices.Sort(gone)
	for _, p := range gone {
		old := before[p]
		c := dto.SkillFileChange{Path: p, Change: "removed", Opaque: !old.Text, Before: old.Content}
		add(c, c.Before)
	}
	return out
}

// ---- knowledge ----

func (s *Server) notesChanges(ctx context.Context, v core.Viewer, list []core.NotesVersion) []dto.NotesChange {
	n := s.namer(ctx, v)
	out := make([]dto.NotesChange, 0, len(list))
	for _, x := range list {
		c := dto.NotesChange{NotesVersion: x}
		if x.Actor == core.NotesByUser {
			c.Name = n.user(x.User)
		}
		// A chat the caller cannot open names nothing, not even its id.
		if title := n.chat(x.Session); title != "" {
			c.Chat = &dto.ChatLink{ID: x.Session, Title: title}
		} else {
			c.Session, c.Run = "", ""
		}
		out = append(out, c)
	}
	return out
}

func (s *Server) notesHistory(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	k, err := s.knowledge()
	if err != nil {
		return err
	}
	id, err := knowledgeID(r)
	if err != nil {
		return err
	}
	list, err := k.NotesHistory(r.Context(), a.Viewer, id)
	if err != nil {
		return err
	}
	return ok(w, dto.List[dto.NotesChange]{Items: s.notesChanges(r.Context(), a.Viewer, list)})
}

func (s *Server) notesVersion(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	k, err := s.knowledge()
	if err != nil {
		return err
	}
	id, err := knowledgeID(r)
	if err != nil {
		return err
	}
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || version <= 0 {
		return errNotFound
	}
	v, err := k.NotesVersion(r.Context(), a.Viewer, id, version)
	if err != nil {
		return err
	}
	return ok(w, s.notesChanges(r.Context(), a.Viewer, []core.NotesVersion{*v})[0])
}

const (
	headTTL     = 10 * time.Minute
	headFailTTL = time.Minute
	headBudget  = 4 * time.Second
	headMax     = 20
)

// branchHeads caches the commit each repository's default branch points
// at, so listing projects asks GitHub at most once per repository in a
// while.
type branchHeads struct {
	mu sync.Mutex
	m  map[string]branchHead
}

type branchHead struct {
	sha string
	at  time.Time
	ok  bool
}

// markStale sets Stale on the caller's GitHub repos whose default branch
// has moved past the commit their index read. A repository GitHub cannot
// answer for is left as it was.
func (s *Server) markStale(ctx context.Context, repos []*core.IndexedRepo) {
	if s.d.GitHub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, headBudget)
	defer cancel()
	asked := 0
	for _, r := range repos {
		if r.Source != core.KnowledgeGitHub || r.Status != core.IndexReady || r.IndexedSHA == "" {
			continue
		}
		head, fresh := s.heads.get(r.Repo, s.now())
		if !fresh {
			if asked >= headMax || ctx.Err() != nil {
				continue
			}
			asked++
			sha, err := s.d.GitHub.BranchHead(ctx, r.Repo)
			s.heads.put(r.Repo, sha, err == nil, s.now())
			head = sha
		}
		r.Stale = head != "" && head != r.IndexedSHA
	}
}

func (b *branchHeads) get(repo string, now time.Time) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h, ok := b.m[repo]
	if !ok {
		return "", false
	}
	ttl := headTTL
	if !h.ok {
		ttl = headFailTTL
	}
	return h.sha, now.Sub(h.at) < ttl
}

func (b *branchHeads) put(repo, sha string, ok bool, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string]branchHead{}
	}
	b.m[repo] = branchHead{sha: sha, at: now, ok: ok}
}

// ---- GitHub ----

func (s *Server) githubWorkflows(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	gh, err := s.gh()
	if err != nil {
		return err
	}
	list, err := gh.Workflows(r.Context(), r.PathValue("owner")+"/"+r.PathValue("repo"))
	if err != nil {
		return err
	}
	return ok(w, nonNil(list))
}

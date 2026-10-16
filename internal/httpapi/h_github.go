package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// githubRoutes are the GitHub connection endpoints (v0.2 F4). Managing
// connections is admin-only; any user may list the allowlisted repositories
// their runs can use, with their own index state.
func (s *Server) githubRoutes() []route {
	return []route{
		{pattern: "GET /api/github/repos", access: accessUser, h: s.githubRepos},
		{pattern: "POST /api/github/repos/read", access: accessUser, h: s.githubRead},
		{pattern: "GET /api/github/connections", access: accessAdmin, h: s.githubConnections},
		{pattern: "POST /api/github/connections", access: accessAdmin, h: s.githubConnect},
		{pattern: "POST /api/github/connections/test", access: accessAdmin, h: s.githubTest},
		{pattern: "GET /api/github/connections/{id}", access: accessAdmin, h: s.githubConnection},
		{pattern: "POST /api/github/connections/{id}/test", access: accessAdmin, h: s.githubProbe},
		{pattern: "PUT /api/github/connections/{id}/repos", access: accessAdmin, h: s.githubSetRepos},
		{pattern: "DELETE /api/github/connections/{id}", access: accessAdmin, h: s.githubDisconnect},
	}
}

var errNoGitHub = newError(http.StatusServiceUnavailable, "unavailable", "GitHub connections are not available in this build")

func (s *Server) gh() (core.GitHub, error) {
	if s.d.GitHub == nil {
		return nil, errNoGitHub
	}
	return s.d.GitHub, nil
}

func githubCreds(req dto.GitHubConnectReq) core.GitHubCredentials {
	c := core.GitHubCredentials{Auth: req.Auth, SecretRef: strings.TrimSpace(req.SecretRef), AppID: strings.TrimSpace(req.AppID),
		InstallationID: req.InstallationID}
	if req.Token != "" {
		c.Token = core.NewSecret([]byte(req.Token))
	}
	if req.PrivateKey != "" {
		c.PrivateKey = core.NewSecret([]byte(req.PrivateKey))
	}
	return c
}

func (s *Server) githubRepos(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	gh, err := s.gh()
	if err != nil {
		return err
	}
	repos, err := gh.Repos(r.Context())
	if err != nil {
		return err
	}
	if err := s.callerIndexState(r.Context(), a, repos); err != nil {
		return err
	}
	return ok(w, nonNil(repos))
}

// callerIndexState sets each repository's LastIndexedSHA to the commit the
// caller's own index of its checkout last read. Every principal indexes on
// its own, so no other user's index is ever shown.
func (s *Server) callerIndexState(ctx context.Context, a core.Actor, repos []*core.GitHubRepo) error {
	for _, g := range repos {
		g.LastIndexedSHA = ""
	}
	if s.d.Knowledge == nil || len(repos) == 0 {
		return nil
	}
	indexed, err := s.d.Knowledge.Repos(ctx, a.Viewer, core.UserPrincipal(a.User))
	if err != nil {
		return err
	}
	sha := make(map[string]string, len(indexed))
	for _, x := range indexed {
		if x.Source == core.KnowledgeGitHub {
			sha[x.Repo] = x.IndexedSHA
		}
	}
	for _, g := range repos {
		g.LastIndexedSHA = sha[strings.ToLower(g.FullName())]
	}
	return nil
}

func (s *Server) githubConnections(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	gh, err := s.gh()
	if err != nil {
		return err
	}
	conns, err := gh.Connections(r.Context())
	if err != nil {
		return err
	}
	return ok(w, nonNil(conns))
}

func (s *Server) githubTest(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	gh, err := s.gh()
	if err != nil {
		return err
	}
	var req dto.GitHubConnectReq
	if err := decode(r, &req); err != nil {
		return err
	}
	p, err := gh.Test(r.Context(), githubCreds(req))
	if err != nil {
		return err
	}
	return ok(w, p)
}

func (s *Server) githubConnect(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	gh, err := s.gh()
	if err != nil {
		return err
	}
	var req dto.GitHubConnectReq
	if err := decode(r, &req); err != nil {
		return err
	}
	c, err := gh.Connect(r.Context(), githubCreds(req), req.Repos, a.User)
	if err != nil {
		return err
	}
	s.prefetchRepos(r.Context(), gh, a, req.Repos)
	return ok(w, c)
}

func (s *Server) githubConnection(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	gh, err := s.gh()
	if err != nil {
		return err
	}
	c, repos, err := gh.Connection(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	detail := dto.GitHubConnectionDetail{Connection: c, Repos: nonNil(repos)}
	if mk, isMaker := gh.(core.GitHubAppMaker); isMaker {
		app, err := mk.ConnectionApp(r.Context(), c.ID)
		switch {
		case err == nil:
			detail.App = app
		case !errors.Is(err, core.ErrNotFound):
			return err
		}
	}
	return ok(w, detail)
}

func (s *Server) githubProbe(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	gh, err := s.gh()
	if err != nil {
		return err
	}
	p, err := gh.Probe(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return ok(w, p)
}

func (s *Server) githubSetRepos(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	gh, err := s.gh()
	if err != nil {
		return err
	}
	var req dto.GitHubReposReq
	if err := decode(r, &req); err != nil {
		return err
	}
	repos, err := gh.SetRepos(r.Context(), r.PathValue("id"), req.Repos, a.User)
	if err != nil {
		return err
	}
	s.prefetchRepos(r.Context(), gh, a, req.Repos)
	return ok(w, nonNil(repos))
}

func (s *Server) githubDisconnect(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	gh, err := s.gh()
	if err != nil {
		return err
	}
	if err := gh.Disconnect(r.Context(), r.PathValue("id"), a.User); err != nil {
		return err
	}
	return noContent(w)
}

// githubRead reads allowed repositories into the caller's own workspace,
// so Knowledge has them for that person too (indexes are per principal).
// Any user may: the agent could check the same repositories out for them.
func (s *Server) githubRead(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	gh, err := s.gh()
	if err != nil {
		return err
	}
	var req dto.GitHubReposReq
	if err := decode(r, &req); err != nil {
		return err
	}
	allowed, err := gh.Repos(r.Context())
	if err != nil {
		return err
	}
	var list []string
	for _, g := range allowed {
		if len(req.Repos) == 0 || slices.ContainsFunc(req.Repos, func(n string) bool { return strings.EqualFold(n, g.FullName()) }) {
			list = append(list, g.FullName())
		}
	}
	if len(list) == 0 {
		return badRequest("none of those repositories is connected")
	}
	s.prefetchRepos(r.Context(), gh, a, list)
	return accepted(w, map[string]any{"repos": list})
}

// prefetchLimit bounds one background read of newly allowed repositories.
const prefetchLimit = 15 * time.Minute

// prefetchRepos checks newly allowed repositories out into the admin's own
// workspace in the background, so Knowledge starts reading them now, as the
// GitHub page says, instead of when a chat first needs one.
func (s *Server) prefetchRepos(ctx context.Context, gh core.GitHub, a core.Actor, repos []string) {
	if len(repos) == 0 || s.d.Knowledge == nil {
		return
	}
	pr, err := s.d.Store.Principals().Get(ctx, a.Viewer, core.UserPrincipal(a.User))
	if err != nil {
		s.log.Warn("github: no workspace to read repositories into", "user", a.User, "err", err)
		return
	}
	root, err := s.openWorkspace(pr.Workspace, true)
	if err != nil {
		s.log.Warn("github: no workspace to read repositories into", "user", a.User, "err", err)
		return
	}
	_ = root.Close()
	env := &core.ToolEnv{User: a.User, Role: a.Role, Principal: *pr, Workspace: s.workspaceDir(pr.Workspace), Sandbox: s.d.Sandbox}
	list := slices.Clone(repos)
	s.jobs.start(ctx, "github-prefetch:"+string(a.User), prefetchLimit, func(ctx context.Context) { gh.Prefetch(ctx, env, list) })
}

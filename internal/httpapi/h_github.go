package httpapi

import (
	"net/http"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// githubRoutes are the GitHub connection endpoints (v0.2 F4). Managing
// connections is admin-only; any user may list the allowlisted repositories
// their runs can use.
func (s *Server) githubRoutes() []route {
	return []route{
		{pattern: "GET /api/github/repos", access: accessUser, h: s.githubRepos},
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

func (s *Server) githubRepos(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	gh, err := s.gh()
	if err != nil {
		return err
	}
	repos, err := gh.Repos(r.Context())
	if err != nil {
		return err
	}
	return ok(w, nonNil(repos))
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
	return ok(w, dto.GitHubConnectionDetail{Connection: c, Repos: nonNil(repos)})
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

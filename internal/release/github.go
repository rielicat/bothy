package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

const (
	defaultGitHubHost = "api.github.com"
	apiVersion        = "2022-11-28"
	maxResponse       = 1 << 20
)

var (
	enumRe = regexp.MustCompile(`^[a-z_]{1,32}$`)
	shaRe  = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

// SecretResolver resolves "secret:<name>" references (core.Vault does).
type SecretResolver interface {
	Resolve(ctx context.Context, ref string, u core.UserID, purpose string) (core.Secret, error)
}

// GitHub implements core.WorkflowDispatcher against the GitHub REST API
// (github.com or GitHub Enterprise Server at releases.github_api). The token
// comes from the vault on every call and is sent only to that host, through
// the system egress client.
type GitHub struct {
	cfg    config.View
	secret SecretResolver
	http   func(core.SystemDest) *http.Client
	clock  core.Clock

	// findFor and findEvery bound the search for the run a dispatch started
	// when GitHub does not return its id.
	findFor, findEvery time.Duration
}

var _ core.WorkflowDispatcher = (*GitHub)(nil)

func NewGitHub(cfg config.View, secret SecretResolver, client func(core.SystemDest) *http.Client, clock core.Clock) *GitHub {
	if clock == nil {
		clock = core.SystemClock{}
	}
	return &GitHub{cfg: cfg, secret: secret, http: client, clock: clock, findFor: 30 * time.Second, findEvery: 2 * time.Second}
}

// apiError is a non-2xx answer from GitHub.
type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("GitHub answered %d", e.status)
	}
	return fmt.Sprintf("GitHub answered %d: %s", e.status, e.message)
}

type conn struct {
	hc    *http.Client
	base  string // API base without trailing slash
	web   string // web base for run links
	token string
}

func (g *GitHub) connect(ctx context.Context) (*conn, error) {
	c := g.cfg.Current().Releases
	base := strings.TrimRight(c.GitHubAPI, "/")
	if base == "" {
		base = "https://" + defaultGitHubHost
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return nil, fmt.Errorf("release: invalid releases.github_api %q: %w", base, core.ErrInvalid)
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	if strings.TrimSpace(c.GitHubToken) == "" {
		return nil, errors.New("release: releases.github_token is not set")
	}
	if g.secret == nil || g.http == nil {
		return nil, errors.New("release: no vault or HTTP client configured")
	}
	sec, err := g.secret.Resolve(ctx, c.GitHubToken, "", "release:github")
	if err != nil {
		return nil, fmt.Errorf("release: resolving the GitHub token: %w", err)
	}
	tok := strings.TrimSpace(sec.Reveal())
	if tok == "" || strings.ContainsAny(tok, "\r\n ") {
		return nil, errors.New("release: the stored GitHub token is empty or malformed")
	}
	hc := g.http(core.SystemDest{
		Name:  "release:github",
		Hosts: []string{net.JoinHostPort(u.Hostname(), port)},
		// Enterprise Server often lives on a private network; the public
		// API never does.
		AllowPrivate: !strings.EqualFold(u.Hostname(), defaultGitHubHost),
	})
	if hc == nil {
		return nil, errors.New("release: no HTTP client available")
	}
	return &conn{hc: hc, base: base, web: webBase(u), token: tok}, nil
}

// webBase is where run pages live: github.com for the public API, the
// Enterprise host (without /api/v3) otherwise.
func webBase(api *url.URL) string {
	if strings.EqualFold(api.Hostname(), defaultGitHubHost) {
		return "https://github.com"
	}
	return api.Scheme + "://" + api.Host
}

func (c *conn) do(ctx context.Context, method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "bothy")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("release: GitHub request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("release: reading GitHub's answer: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		return resp.StatusCode, &apiError{status: resp.StatusCode, message: clip(e.Message, 200)}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("release: decoding GitHub's answer: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func repoPath(repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("%w: repository %q is not owner/name", core.ErrInvalid, repo)
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

// Dispatch implements core.WorkflowDispatcher. It asks GitHub for the run's
// id (return_run_details); an older server answers 204 instead, and the run
// is then looked up among the workflow's recent workflow_dispatch runs.
func (g *GitHub) Dispatch(ctx context.Context, d core.WorkflowDispatch) (*core.WorkflowRun, error) {
	c, err := g.connect(ctx)
	if err != nil {
		return nil, err
	}
	rp, err := repoPath(d.Repo)
	if err != nil {
		return nil, err
	}
	inputs := d.Inputs
	if inputs == nil {
		inputs = map[string]string{}
	}
	since := g.clock.Now().Add(-10 * time.Second)
	var res struct {
		RunID int64 `json:"workflow_run_id"`
	}
	status, err := c.do(ctx, http.MethodPost, rp+"/actions/workflows/"+url.PathEscape(d.Workflow)+"/dispatches",
		map[string]any{"ref": d.Ref, "inputs": inputs, "return_run_details": true}, &res)
	if err != nil {
		return nil, err
	}
	if status == http.StatusOK && res.RunID > 0 {
		if run, err := g.runStatus(ctx, c, rp, d.Repo, res.RunID); err == nil {
			return run, nil
		}
		// The dispatch went through; a failed read only loses the details.
		return &core.WorkflowRun{ID: res.RunID, Repo: d.Repo, Status: "queued", URL: runURL(c.web, d.Repo, res.RunID)}, nil
	}
	return g.findRun(ctx, c, rp, d, since), nil
}

// findRun looks for the run a dispatch started; a zero ID means none showed
// up in time. Lookup errors only end the search: the dispatch itself
// succeeded.
func (g *GitHub) findRun(ctx context.Context, c *conn, rp string, d core.WorkflowDispatch, since time.Time) *core.WorkflowRun {
	q := url.Values{"event": {"workflow_dispatch"}, "branch": {d.Ref}, "per_page": {"20"},
		"created": {">=" + since.UTC().Format(time.RFC3339)}}
	path := rp + "/actions/workflows/" + url.PathEscape(d.Workflow) + "/runs?" + q.Encode()
	deadline := g.clock.Now().Add(g.findFor)
	for {
		var list struct {
			Runs []ghRun `json:"workflow_runs"`
		}
		if _, err := c.do(ctx, http.MethodGet, path, nil, &list); err == nil {
			var best *ghRun
			for i := range list.Runs {
				r := &list.Runs[i]
				if r.CreatedAt.Before(since) {
					continue
				}
				if best == nil || r.CreatedAt.Before(best.CreatedAt) || r.CreatedAt.Equal(best.CreatedAt) && r.ID < best.ID {
					best = r
				}
			}
			if best != nil {
				return best.core(c.web, d.Repo)
			}
		}
		if !g.clock.Now().Before(deadline) {
			return &core.WorkflowRun{Repo: d.Repo, Status: "requested"}
		}
		t := time.NewTimer(g.findEvery)
		select {
		case <-ctx.Done():
			t.Stop()
			return &core.WorkflowRun{Repo: d.Repo, Status: "requested"}
		case <-t.C:
		}
	}
}

// RunStatus implements core.WorkflowDispatcher.
func (g *GitHub) RunStatus(ctx context.Context, repo string, runID int64) (*core.WorkflowRun, error) {
	c, err := g.connect(ctx)
	if err != nil {
		return nil, err
	}
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	return g.runStatus(ctx, c, rp, repo, runID)
}

func (g *GitHub) runStatus(ctx context.Context, c *conn, rp, repo string, runID int64) (*core.WorkflowRun, error) {
	var r ghRun
	if _, err := c.do(ctx, http.MethodGet, rp+"/actions/runs/"+strconv.FormatInt(runID, 10), nil, &r); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.status == http.StatusNotFound {
			return nil, fmt.Errorf("release: workflow run %d: %w", runID, core.ErrNotFound)
		}
		return nil, err
	}
	if r.ID != runID {
		return nil, fmt.Errorf("release: GitHub returned run %d for run %d", r.ID, runID)
	}
	return r.core(c.web, repo), nil
}

// ghRun is the part of GitHub's workflow run object Bothy reads.
type ghRun struct {
	ID         int64     `json:"id"`
	RunNumber  int       `json:"run_number"`
	Status     string    `json:"status"`
	Conclusion *string   `json:"conclusion"`
	HeadSHA    string    `json:"head_sha"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// core keeps only fields whose shape is checked, and builds the run link
// itself rather than trusting html_url.
func (r *ghRun) core(web, repo string) *core.WorkflowRun {
	out := &core.WorkflowRun{ID: r.ID, Repo: repo, Number: max(r.RunNumber, 0), Status: "unknown",
		URL: runURL(web, repo, r.ID), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if enumRe.MatchString(r.Status) {
		out.Status = r.Status
	}
	if r.Conclusion != nil {
		out.Conclusion = "unknown"
		if enumRe.MatchString(*r.Conclusion) {
			out.Conclusion = *r.Conclusion
		}
	}
	if shaRe.MatchString(r.HeadSHA) {
		out.HeadSHA = r.HeadSHA
	}
	return out
}

func runURL(web, repo string, id int64) string {
	owner, name, _ := strings.Cut(repo, "/")
	return web + "/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/actions/runs/" + strconv.FormatInt(id, 10)
}

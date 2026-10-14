package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

var _ core.WorkflowDispatcher = (*Service)(nil)

var (
	// As releases.environments[].workflow allows.
	dispatchWorkflowRE = regexp.MustCompile(`^(?:[A-Za-z0-9._-]{1,200}\.ya?ml|[0-9]{1,19})$`)
	runEnumRE          = regexp.MustCompile(`^[a-z_]{1,32}$`)
	runSHARE           = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

// Serves reports whether repo is allowlisted on a GitHub connection. The
// release module dispatches deploys of such repositories through the
// connection instead of releases.github_token.
func (s *Service) Serves(ctx context.Context, repo string) bool {
	_, err := s.allowedRepo(ctx, repo)
	return err == nil
}

// Dispatch implements core.WorkflowDispatcher through the connection of an
// allowlisted repository. It asks for the run's id (return_run_details); a
// server that answers 204 instead has its run looked up among the
// workflow's recent workflow_dispatch runs.
func (s *Service) Dispatch(ctx context.Context, d core.WorkflowDispatch) (*core.WorkflowRun, error) {
	if !dispatchWorkflowRE.MatchString(d.Workflow) {
		return nil, fmt.Errorf("%w: workflow %q is not a file name such as deploy.yml or a numeric id", core.ErrInvalid, d.Workflow)
	}
	if err := checkRef(d.Ref); err != nil {
		return nil, err
	}
	r, cl, err := s.repoClient(ctx, d.Repo)
	if err != nil {
		return nil, err
	}
	ep, err := s.endpoint()
	if err != nil {
		return nil, err
	}
	inputs := d.Inputs
	if inputs == nil {
		inputs = map[string]string{}
	}
	since := s.now().Add(-10 * time.Second)
	path := repoPath(r) + "/actions/workflows/" + url.PathEscape(d.Workflow) + "/dispatches"
	body := map[string]any{"ref": d.Ref, "inputs": inputs, "return_run_details": true}
	runID, err := dispatchOnce(ctx, cl, path, body)
	// Enterprise Server releases that predate return_run_details refuse the
	// unknown key with 422 and start nothing, so asking again without it
	// cannot dispatch twice.
	if statusOf(err) == http.StatusUnprocessableEntity && strings.Contains(err.Error(), "return_run_details") {
		delete(body, "return_run_details")
		runID, err = dispatchOnce(ctx, cl, path, body)
	}
	switch {
	case err != nil:
		return nil, err
	case runID > 0:
		if run, err := s.runStatus(ctx, cl, ep, r, runID); err == nil {
			return run, nil
		}
		// The dispatch went through; a failed read only loses the details.
		return &core.WorkflowRun{ID: runID, Repo: r.FullName(), Status: "queued", URL: runURL(ep, r, runID)}, nil
	}
	return s.findRun(ctx, cl, ep, r, d, since), nil
}

// dispatchOnce posts one workflow_dispatch and returns the run id GitHub
// reported (0 when it answered without one).
func dispatchOnce(ctx context.Context, cl *apiClient, path string, body map[string]any) (int64, error) {
	resp, err := cl.request(ctx, http.MethodPost, path, nil, body, "")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, nil
	}
	var res struct {
		RunID int64 `json:"workflow_run_id"`
	}
	// The dispatch happened whatever the body says; an unreadable answer
	// only loses the run id, which findRun then looks up.
	if decodeJSON(resp, &res) != nil {
		return 0, nil
	}
	return res.RunID, nil
}

// findRun looks for the run a dispatch started; a zero ID means none showed
// up in time. Lookup errors only end the search: the dispatch itself
// succeeded.
func (s *Service) findRun(ctx context.Context, cl *apiClient, ep endpoint, r *core.GitHubRepo, d core.WorkflowDispatch, since time.Time) *core.WorkflowRun {
	q := url.Values{"event": {"workflow_dispatch"}, "branch": {d.Ref}, "per_page": {"20"},
		"created": {">=" + since.UTC().Format(time.RFC3339)}}
	path := repoPath(r) + "/actions/workflows/" + url.PathEscape(d.Workflow) + "/runs"
	deadline := s.now().Add(s.findFor)
	for {
		var list struct {
			Runs []ghRun `json:"workflow_runs"`
		}
		if _, err := cl.getJSON(ctx, path, q, &list); err == nil {
			var best *ghRun
			for i := range list.Runs {
				run := &list.Runs[i]
				if run.CreatedAt.Before(since) {
					continue
				}
				if best == nil || run.CreatedAt.Before(best.CreatedAt) || run.CreatedAt.Equal(best.CreatedAt) && run.ID < best.ID {
					best = run
				}
			}
			if best != nil {
				return workflowRun(ep, r, best)
			}
		}
		if !s.now().Before(deadline) {
			return &core.WorkflowRun{Repo: r.FullName(), Status: "requested"}
		}
		t := time.NewTimer(s.findEvery)
		select {
		case <-ctx.Done():
			t.Stop()
			return &core.WorkflowRun{Repo: r.FullName(), Status: "requested"}
		case <-t.C:
		}
	}
}

// RunStatus implements core.WorkflowDispatcher.
func (s *Service) RunStatus(ctx context.Context, repo string, runID int64) (*core.WorkflowRun, error) {
	r, cl, err := s.repoClient(ctx, repo)
	if err != nil {
		return nil, err
	}
	ep, err := s.endpoint()
	if err != nil {
		return nil, err
	}
	return s.runStatus(ctx, cl, ep, r, runID)
}

func (s *Service) runStatus(ctx context.Context, cl *apiClient, ep endpoint, r *core.GitHubRepo, runID int64) (*core.WorkflowRun, error) {
	var run ghRun
	if _, err := cl.getJSON(ctx, repoPath(r)+"/actions/runs/"+strconv.FormatInt(runID, 10), nil, &run); err != nil {
		if statusOf(err) == http.StatusNotFound {
			return nil, fmt.Errorf("github: workflow run %d: %w", runID, core.ErrNotFound)
		}
		return nil, err
	}
	if run.ID != runID {
		return nil, errors.New("github: GitHub returned another workflow run than the one asked for")
	}
	return workflowRun(ep, r, &run), nil
}

// workflowRun keeps only fields whose shape is checked: names and titles
// are authored by whoever can push, and the link is built here rather than
// taken from html_url.
func workflowRun(ep endpoint, r *core.GitHubRepo, run *ghRun) *core.WorkflowRun {
	out := &core.WorkflowRun{ID: run.ID, Repo: r.FullName(), Number: max(run.RunNumber, 0), Status: "unknown",
		URL: runURL(ep, r, run.ID), CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt}
	if runEnumRE.MatchString(run.Status) {
		out.Status = run.Status
	}
	if run.Conclusion != "" {
		out.Conclusion = "unknown"
		if runEnumRE.MatchString(run.Conclusion) {
			out.Conclusion = run.Conclusion
		}
	}
	if runSHARE.MatchString(run.HeadSHA) {
		out.HeadSHA = run.HeadSHA
	}
	return out
}

func runURL(ep endpoint, r *core.GitHubRepo, id int64) string {
	return ep.web() + "/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(r.Name) + "/actions/runs/" + strconv.FormatInt(id, 10)
}

// web is where repository pages live: github.com for the public API, the
// Enterprise host (without /api/v3) otherwise.
func (e endpoint) web() string {
	if strings.EqualFold(e.base.Hostname(), "api.github.com") {
		return "https://github.com"
	}
	return e.base.Scheme + "://" + e.base.Host
}

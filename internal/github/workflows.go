package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/rielicat/bothy/internal/core"
)

const (
	maxWorkflows    = 200
	maxWorkflowName = 200
	maxSubject      = 300
)

// allowedClient is repoClient for the HTTP API: a repository outside the
// allowlist is ErrNotFound, like any id the caller cannot see.
func (s *Service) allowedClient(ctx context.Context, repo string) (*core.GitHubRepo, *apiClient, error) {
	r, cl, err := s.repoClient(ctx, repo)
	if errors.Is(err, core.ErrInvalid) {
		return nil, nil, fmt.Errorf("github: %s: %w", repo, core.ErrNotFound)
	}
	return r, cl, err
}

// Workflows lists the repository's GitHub Actions workflows by path.
func (s *Service) Workflows(ctx context.Context, repo string) ([]core.GitHubWorkflow, error) {
	r, cl, err := s.allowedClient(ctx, repo)
	if err != nil {
		return nil, err
	}
	type ghWorkflow struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Path  string `json:"path"`
		State string `json:"state"`
	}
	list, _, err := paged(ctx, cl, repoPath(r)+"/actions/workflows", nil, maxWorkflows, func(b []byte) ([]ghWorkflow, error) {
		var page struct {
			Workflows []ghWorkflow `json:"workflows"`
		}
		err := json.Unmarshal(b, &page)
		return page.Workflows, err
	})
	if err != nil {
		return nil, err
	}
	out := make([]core.GitHubWorkflow, 0, len(list))
	for _, w := range list {
		// Only files under .github/workflows can be dispatched; dynamic
		// workflows (Dependabot, Pages) have other paths.
		if !strings.HasPrefix(w.Path, ".github/workflows/") || strings.ContainsAny(w.Path, "\x00\n\r") {
			continue
		}
		out = append(out, core.GitHubWorkflow{ID: w.ID, Name: plainLine(w.Name, maxWorkflowName), Path: w.Path, State: w.State})
	}
	return out, nil
}

// BranchHead is the commit the repository's default branch points at.
func (s *Service) BranchHead(ctx context.Context, repo string) (string, error) {
	r, cl, err := s.allowedClient(ctx, repo)
	if err != nil {
		return "", err
	}
	if r.DefaultBranch == "" {
		return "", nil
	}
	sha, _, err := cl.getText(ctx, repoPath(r)+"/commits/"+url.PathEscape(r.DefaultBranch), nil, "application/vnd.github.sha", 64)
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return "", nil
		}
		return "", err
	}
	sha = strings.TrimSpace(sha)
	if !shaRE.MatchString(sha) {
		return "", fmt.Errorf("github: unexpected commit id for %s", r.FullName())
	}
	return sha, nil
}

// CommitSubject is the first line of a commit's message, for the deploy
// history. Like RunStatus it also reads a repository its connection no
// longer allows. The text is written by whoever can push: the release
// module cleans it and it is shown as quoted text only.
func (s *Service) CommitSubject(ctx context.Context, repo, sha string) (string, error) {
	if !shaRE.MatchString(sha) {
		return "", fmt.Errorf("%w: not a commit id", core.ErrInvalid)
	}
	r, cl, err := s.repoClientFor(ctx, repo, false)
	if err != nil {
		return "", err
	}
	var c struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	if _, err := cl.getJSON(ctx, repoPath(r)+"/commits/"+sha, nil, &c); err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(c.Commit.Message, "\n")
	return plainLine(first, maxSubject), nil
}

// plainLine is oneLine without control or format characters (bidi
// overrides would reorder what people read next to it).
func plainLine(s string, n int) string {
	return oneLine(strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) || unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s), n)
}

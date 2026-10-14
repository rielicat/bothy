package release

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/rielicat/bothy/internal/core"
)

const maxStepName = 100

// StepReader is a dispatcher that can name the step a failed run stopped
// at, for release.finished. The GitHub module's connections implement it
// too; a dispatcher without it leaves the step out.
type StepReader interface {
	FailedStep(ctx context.Context, repo string, runID int64) (string, error)
}

var _ StepReader = (*GitHub)(nil)

// CommitReader is a dispatcher that can read a commit's subject line, for
// the deploy history. The GitHub module's connections implement it too; a
// dispatcher without it leaves the subject out.
type CommitReader interface {
	CommitSubject(ctx context.Context, repo, sha string) (string, error)
}

var _ CommitReader = (*GitHub)(nil)

const maxSubject = 120

// noteCommit records the commit a deploy's run built and its subject, once
// GitHub names it; it reports whether d changed. The subject is written by
// whoever can push to the repository, so it is cleaned to one plain line
// and kept out of notices, events and the model's view.
func (s *Service) noteCommit(ctx context.Context, d *core.Deploy, run *core.WorkflowRun) bool {
	changed := false
	if d.SHA == "" && run != nil && run.HeadSHA != "" {
		d.SHA, changed = run.HeadSHA, true
	}
	if d.SHA == "" || d.Subject != "" {
		return changed
	}
	cr, ok := s.follower(ctx, d).(CommitReader)
	if !ok {
		return changed
	}
	subject, err := cr.CommitSubject(ctx, d.Repo, d.SHA)
	if err != nil {
		s.d.Log.WarnContext(ctx, "release: reading the deployed commit failed", "deploy", d.ID, "err", err)
		return changed
	}
	if subject = plainLine(subject, maxSubject); subject != "" {
		d.Subject, changed = subject, true
	}
	return changed
}

// CommitSubject implements CommitReader with the commit's message.
func (g *GitHub) CommitSubject(ctx context.Context, repo, sha string) (string, error) {
	if !shaRe.MatchString(sha) {
		return "", fmt.Errorf("release: %q is not a commit id: %w", sha, core.ErrInvalid)
	}
	c, err := g.connect(ctx)
	if err != nil {
		return "", err
	}
	rp, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	var out struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	if _, err := c.do(ctx, http.MethodGet, rp+"/commits/"+sha, nil, &out); err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(out.Commit.Message, "\n")
	return first, nil
}

// publishDispatched tells the requester's session (and admins' release
// pages) that GitHub took the deploy.
func (s *Service) publishDispatched(d *core.Deploy) {
	s.publish(core.EvReleaseDispatched, d, core.ReleaseDispatchedData{Deploy: d.ID, Environment: d.Environment, Repo: d.Repo,
		Ref: d.Ref, URL: d.RunURL})
}

func (s *Service) publishFinished(d *core.Deploy, took time.Duration, step string) {
	s.publish(core.EvReleaseFinished, d, core.ReleaseFinishedData{Deploy: d.ID, Environment: d.Environment, Repo: d.Repo, Ref: d.Ref,
		Status: d.Status, OK: d.Status == core.DeploySucceeded, URL: d.RunURL, Duration: took, FailedStep: step})
}

// publish sends a deploy event to its principal and, as for the deploy
// records themselves, to admins (an admin broadcast).
func (s *Service) publish(typ core.EventType, d *core.Deploy, data core.EventData) {
	if s.d.Bus == nil {
		return
	}
	for _, p := range []core.PrincipalID{d.Principal, ""} {
		s.d.Bus.Publish(core.Event{Type: typ, Principal: p, Session: d.Session, Run: d.Run, Data: data})
	}
}

// failedStep asks the dispatcher that started the deploy where a failed run
// stopped; any error leaves the step out.
func (s *Service) failedStep(ctx context.Context, d *core.Deploy) string {
	if d.Status != core.DeployFailed || d.WorkflowRun == 0 {
		return ""
	}
	sr, ok := s.follower(ctx, d).(StepReader)
	if !ok {
		return ""
	}
	step, err := sr.FailedStep(ctx, d.Repo, d.WorkflowRun)
	if err != nil {
		s.d.Log.WarnContext(ctx, "release: reading the failed step failed", "deploy", d.ID, "run", d.WorkflowRun, "err", err)
		return ""
	}
	return stepName(step)
}

// FailedStep implements StepReader with the run's latest jobs.
func (g *GitHub) FailedStep(ctx context.Context, repo string, runID int64) (string, error) {
	c, err := g.connect(ctx)
	if err != nil {
		return "", err
	}
	rp, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	var jobs ghJobs
	if _, err := c.do(ctx, http.MethodGet, rp+"/actions/runs/"+strconv.FormatInt(runID, 10)+"/jobs?filter=latest&per_page=100", nil, &jobs); err != nil {
		return "", err
	}
	return jobs.failedStep(), nil
}

// ghJobs is the part of GitHub's list of a run's jobs Bothy reads.
type ghJobs struct {
	Jobs []struct {
		Conclusion *string `json:"conclusion"`
		Steps      []struct {
			Name       string  `json:"name"`
			Conclusion *string `json:"conclusion"`
		} `json:"steps"`
	} `json:"jobs"`
}

func (j ghJobs) failedStep() string {
	for _, job := range j.Jobs {
		for _, st := range job.Steps {
			if st.Conclusion != nil && (*st.Conclusion == "failure" || *st.Conclusion == "timed_out") {
				return stepName(st.Name)
			}
		}
	}
	return ""
}

// stepName cleans a workflow step's name for display: one line without
// control or invisible characters, at most 100 characters. The name stays
// untrusted text; renderers show it verbatim.
func stepName(s string) string { return plainLine(s, maxStepName) }

// plainLine is s on one line without control or invisible characters, cut
// to n characters.
func plainLine(s string, n int) string {
	var b strings.Builder
	gap := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			gap = b.Len() > 0
			continue
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			continue
		}
		if gap {
			b.WriteByte(' ')
			gap = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if r := []rune(out); len(r) > n {
		out = string(r[:n-1]) + "…"
	}
	return out
}

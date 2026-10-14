// Package release implements release_deploy and release_status: named
// environments (releases.environments) whose deploys dispatch a GitHub
// Actions workflow once the environment's approvers agree, and a follower
// that reports each run's outcome to the chat and session it came from.
package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

const (
	defaultPollInterval = 15 * time.Second
	defaultFollowFor    = 6 * time.Hour
	maxErrorText        = 300
)

type Deps struct {
	Config  config.View
	Store   core.Store
	Deploys core.ReleaseRepo
	Vault   SecretResolver
	HTTP    func(core.SystemDest) *http.Client // Egress.SystemClient
	// Dispatcher defaults to the built-in GitHub client over Vault and HTTP
	// (releases.github_token). It serves the repositories Connections does not.
	Dispatcher core.WorkflowDispatcher
	// Connections dispatches for the repositories allowlisted on a GitHub
	// connection (the GitHub module); nil leaves every repository to
	// Dispatcher.
	Connections RepoDispatcher
	Messenger   core.Messenger // posts outcomes to chats; nil = sessions only
	Bus         core.Bus
	Audit       core.Auditor
	Redactor    core.Redactor
	Clock       core.Clock
	Log         *slog.Logger
}

// RepoDispatcher is a WorkflowDispatcher for the repositories it serves.
type RepoDispatcher interface {
	core.WorkflowDispatcher
	Serves(ctx context.Context, repo string) bool
}

// dispatcher picks who dispatches a new deploy of repo and names the choice
// (Deploy.Via). It is made per deploy, so a repository that joins or leaves
// a connection's allowlist moves with it for the next one.
func (s *Service) dispatcher(ctx context.Context, repo string) (core.WorkflowDispatcher, string) {
	if s.d.Connections != nil && s.d.Connections.Serves(ctx, repo) {
		return s.d.Connections, core.DeployViaConnection
	}
	return s.d.Dispatcher, core.DeployViaToken
}

// follower reads d's run through the dispatcher that started it, even after
// the repository joined or left an allowlist: the other one may hold
// credentials that cannot see the run, or none at all. A deploy recorded
// without Via is followed through today's choice.
func (s *Service) follower(ctx context.Context, d *core.Deploy) core.WorkflowDispatcher {
	switch {
	case d.Via == core.DeployViaConnection && s.d.Connections != nil:
		return s.d.Connections
	case d.Via == core.DeployViaToken:
		return s.d.Dispatcher
	}
	disp, _ := s.dispatcher(ctx, d.Repo)
	return disp
}

// Service provides the release tools and follows dispatched runs.
type Service struct {
	d    Deps
	wake chan struct{}
}

var (
	_ core.ToolProvider  = (*Service)(nil)
	_ core.CheckProvider = (*Service)(nil)
)

func New(d Deps) (*Service, error) {
	if d.Config == nil || d.Store == nil || d.Deploys == nil {
		return nil, errors.New("release: Config, Store and Deploys are required")
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if d.Dispatcher == nil {
		d.Dispatcher = NewGitHub(d.Config, d.Vault, d.HTTP, d.Clock)
	}
	return &Service{d: d, wake: make(chan struct{}, 1)}, nil
}

// Tools implements core.ToolProvider.
func (s *Service) Tools() []core.Tool { return []core.Tool{&deployTool{s: s}, &statusTool{s: s}} }

// Run follows open deploys until ctx ends: every releases.poll_interval, and
// at once after a dispatch. Deploys left open by a restart are picked up on
// the first pass.
func (s *Service) Run(ctx context.Context) error {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		case <-s.wake:
			t.Stop()
		}
		s.poll(ctx)
		t.Reset(s.interval())
	}
}

func (s *Service) interval() time.Duration {
	if d := s.d.Config.Current().Releases.PollInterval.D(); d > 0 {
		return d
	}
	return defaultPollInterval
}

func (s *Service) followFor() time.Duration {
	if d := s.d.Config.Current().Releases.FollowFor.D(); d > 0 {
		return d
	}
	return defaultFollowFor
}

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// poll checks every open deploy once.
func (s *Service) poll(ctx context.Context) {
	open, err := s.d.Deploys.List(ctx, core.SystemViewer(), core.DeployFilter{Open: true, Page: core.Page{Limit: 500}})
	if err != nil {
		s.d.Log.ErrorContext(ctx, "release: listing open deploys failed", "err", err)
		return
	}
	for _, d := range open {
		if ctx.Err() != nil {
			return
		}
		s.check(ctx, d)
	}
}

func (s *Service) check(ctx context.Context, d *core.Deploy) {
	now := s.d.Clock.Now()
	switch {
	case d.WorkflowRun == 0:
		s.finish(ctx, d, core.DeployUnknown, "", "GitHub accepted the deploy but its workflow run could not be identified", 0)
		return
	case now.Sub(d.CreatedAt) > s.followFor():
		s.finish(ctx, d, core.DeployUnknown, "", "stopped following the run after "+s.followFor().String(), 0)
		return
	}
	run, err := s.follower(ctx, d).RunStatus(ctx, d.Repo, d.WorkflowRun)
	switch {
	case errors.Is(err, core.ErrNotFound):
		s.finish(ctx, d, core.DeployUnknown, "", "GitHub no longer knows the workflow run", 0)
		return
	case err != nil:
		s.d.Log.WarnContext(ctx, "release: checking a workflow run failed; retrying later", "deploy", d.ID, "run", d.WorkflowRun, "err", err)
		return
	}
	if run.Done() {
		st := core.DeployFailed
		if run.Conclusion == "success" {
			st = core.DeploySucceeded
		}
		var took time.Duration
		if !run.CreatedAt.IsZero() && run.UpdatedAt.After(run.CreatedAt) {
			took = run.UpdatedAt.Sub(run.CreatedAt)
		}
		s.finish(ctx, d, st, run.Conclusion, "", took)
		return
	}
	if d.Status == core.DeployDispatched && run.Status == "in_progress" {
		d.Status, d.UpdatedAt = core.DeployRunning, now
		if err := s.d.Deploys.Update(ctx, d); err != nil {
			s.d.Log.ErrorContext(ctx, "release: saving deploy progress failed", "deploy", d.ID, "err", err)
		}
	}
}

// finish records a deploy's end and reports it. took is the run's own
// start to finish when GitHub said so; otherwise the time Bothy followed it.
func (s *Service) finish(ctx context.Context, d *core.Deploy, st core.DeployStatus, conclusion, why string, took time.Duration) {
	now := s.d.Clock.Now()
	d.Status, d.Conclusion, d.Error, d.UpdatedAt, d.FinishedAt = st, conclusion, clip(why, maxErrorText), now, now
	if err := s.d.Deploys.Update(ctx, d); err != nil {
		s.d.Log.ErrorContext(ctx, "release: saving a finished deploy failed", "deploy", d.ID, "err", err)
		return
	}
	s.audit(ctx, core.AuditEntry{Actor: "system", Action: "release.finish", Target: d.ID, Outcome: "ok", Session: d.Session, Run: d.Run,
		Detail: detail(map[string]any{"environment": d.Environment, "status": st, "conclusion": conclusion, "workflow_run_id": d.WorkflowRun})})
	if took <= 0 {
		took = max(now.Sub(d.CreatedAt), 0)
	}
	s.report(ctx, d, outcomeText(d, s.followFor(), took))
	s.publishFinished(d, took, s.failedStep(ctx, d))
}

// outcomeText is the message posted when Bothy stops following a deploy
// (docs/design/ui.md §8.7). It never names a workflow step: step names are
// written by whoever can push to the repository.
func outcomeText(d *core.Deploy, followed, took time.Duration) string {
	what := d.Repo + " to " + d.Environment
	link := ""
	if d.WorkflowRun != 0 && d.RunURL != "" {
		link = " " + d.RunURL
	}
	switch {
	case d.Status == core.DeploySucceeded:
		return fmt.Sprintf("Deployed %s in %s. View it on GitHub:%s", what, humanDuration(took), link)
	case d.Status == core.DeployFailed && d.Conclusion == "cancelled":
		return fmt.Sprintf("The deploy of %s was cancelled on GitHub. View it on GitHub:%s", what, link)
	case d.Status == core.DeployFailed && d.Conclusion != "":
		return fmt.Sprintf("The deploy of %s failed (GitHub says “%s”). View the logs on GitHub:%s", what, d.Conclusion, link)
	case d.Status == core.DeployFailed:
		return fmt.Sprintf("GitHub didn't start the deploy of %s. Check the workflow and try again. Details: %s", what, d.Error)
	case d.WorkflowRun == 0:
		return fmt.Sprintf("GitHub accepted the deploy of %s, but Bothy couldn't find its run. Check the Actions tab of %s on GitHub.", what, d.Repo)
	case strings.HasPrefix(d.Error, "stopped following"):
		return fmt.Sprintf("The deploy of %s is still running after %s, so Bothy stopped following it. Check it on GitHub:%s",
			what, humanDuration(followed), link)
	}
	return fmt.Sprintf("Bothy lost track of the deploy of %s: %s. Check it on GitHub:%s", what, d.Error, link)
}

// humanDuration reads like "3 min", "2 min 10 s" or "1 h 5 min".
func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", max(int(d/time.Second), 1))
	case d < time.Hour:
		m, sec := int(d/time.Minute), int(d%time.Minute/time.Second)
		if sec == 0 {
			return fmt.Sprintf("%d min", m)
		}
		return fmt.Sprintf("%d min %d s", m, sec)
	}
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	if m == 0 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}

// report posts a Bothy notice to the deploy's session (the web UI) and, when
// it came from a chat, to that chat.
func (s *Service) report(ctx context.Context, d *core.Deploy, text string) {
	text = s.redact(text)
	if d.Session != "" {
		m := &core.Message{Session: d.Session, Role: core.MsgAssistant, Kind: core.KindNotice, Provenance: core.ProvSystem,
			Parts: []core.Part{{Type: core.PartText, Text: text}}, CreatedAt: s.d.Clock.Now()}
		if err := s.d.Store.Messages().Append(ctx, m); err != nil {
			s.d.Log.WarnContext(ctx, "release: posting the outcome to the session failed", "deploy", d.ID, "err", err)
		} else if s.d.Bus != nil {
			s.d.Bus.Publish(core.Event{Type: core.EvMessageCreated, Principal: d.Principal, Session: d.Session,
				Data: core.MessageData{Message: m}})
		}
	}
	if d.Chat != nil && s.d.Messenger != nil {
		if _, err := s.d.Messenger.Send(ctx, *d.Chat, text); err != nil {
			s.d.Log.WarnContext(ctx, "release: posting the outcome to the chat failed", "deploy", d.ID, "chat", d.Chat.Key(), "err", err)
		}
	}
}

func (s *Service) redact(t string) string {
	if s.d.Redactor == nil {
		return t
	}
	return s.d.Redactor.Redact(t)
}

func (s *Service) audit(ctx context.Context, e core.AuditEntry) {
	if s.d.Audit == nil {
		return
	}
	if err := s.d.Audit.Record(ctx, e); err != nil {
		s.d.Log.ErrorContext(ctx, "release: audit write failed", "action", e.Action, "err", err)
	}
}

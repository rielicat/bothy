package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

const (
	deployName = "release_deploy"
	statusName = "release_status"

	statusDeploysPerEnv = 3
	statusLiveChecks    = 3
)

// deployPayload is release_deploy's Action payload: what the approvers saw on
// the card and exactly what Exec dispatches.
type deployPayload struct {
	Environment string            `json:"environment"`
	Repo        string            `json:"repo"`
	Workflow    string            `json:"workflow"`
	Ref         string            `json:"ref"`
	Inputs      map[string]string `json:"inputs"`
}

// deployTool dispatches an environment's workflow. Policy decides it with the
// release floor; Exec checks again that the call's own approval was granted
// by enough approvers, so no rule, grant or policy bug can run it unasked.
type deployTool struct{ s *Service }

func (t *deployTool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Def: core.ToolDef{
			Name: deployName,
			Description: "Deploy a branch or tag to a release environment (for example staging) by running its GitHub Actions workflow. " +
				"The environment's approvers must agree first; the outcome is posted here when the run finishes. " +
				"Use release_status to list environments and recent deploys.",
			Schema: json.RawMessage(`{"type":"object","properties":{` +
				`"environment":{"type":"string","description":"Release environment name"},` +
				`"ref":{"type":"string","description":"Branch or tag to deploy; may be omitted when the environment allows exactly one"}},` +
				`"required":["environment"],"additionalProperties":false}`),
		},
		Source: "builtin",
		Manifest: core.Manifest{
			Title: "Deploy a release", SideEffect: core.SEIrreversible,
			Triggers: []core.Trigger{core.TrigMessage}, Core: true, Timeout: 90 * time.Second, MaxOutputKB: 4,
		},
	}
}

// Enabled implements core.Gated: the tools exist only once an environment is
// configured.
func (t *deployTool) Enabled(*core.ToolEnv) bool {
	return len(t.s.d.Config.Current().Releases.Environments) > 0
}

func (t *deployTool) Plan(_ context.Context, _ *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var in struct {
		Environment string `json:"environment"`
		Ref         string `json:"ref"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	c := t.s.d.Config.Current().Releases
	env, ok := c.Env(in.Environment)
	if !ok {
		return nil, fmt.Errorf("%w: there is no release environment %q; configured: %s", core.ErrInvalid, in.Environment, envNames(c))
	}
	ref := strings.TrimSpace(in.Ref)
	if ref == "" {
		if ref, ok = env.DefaultRef(); !ok {
			return nil, fmt.Errorf("%w: say which branch or tag to deploy to %s (allowed: %s)", core.ErrInvalid, env.Name, env.RefPattern)
		}
	}
	if !env.AllowsRef(ref) {
		return nil, fmt.Errorf("%w: %q cannot be deployed to %s; allowed: %s", core.ErrInvalid, ref, env.Name, env.RefPattern)
	}
	p := deployPayload{Environment: env.Name, Repo: env.Repo, Workflow: env.Workflow, Ref: ref, Inputs: inputsOf(env)}
	b, err := core.CanonicalJSON(p)
	if err != nil {
		return nil, fmt.Errorf("encode %s action: %w", deployName, err)
	}
	return &core.Action{Tool: deployName, Kind: core.ActRelease, SideEffect: core.SEIrreversible, Payload: b,
		Fingerprint: core.ActionFingerprint(deployName, b)}, nil
}

func (t *deployTool) Exec(ctx context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	s := t.s
	var p deployPayload
	if err := json.Unmarshal(a.Payload, &p); err != nil {
		return nil, fmt.Errorf("%w: %s payload: %w", core.ErrInvalid, deployName, err)
	}
	cur, ok := s.d.Config.Current().Releases.Env(p.Environment)
	if !ok || cur.Repo != p.Repo || cur.Workflow != p.Workflow || !maps.Equal(inputsOf(cur), nonNil(p.Inputs)) || !cur.AllowsRef(p.Ref) {
		return failed("The " + p.Environment + " environment changed after this deploy was approved, so it did not run. Ask again to deploy with the current settings."), nil
	}
	appr, voters, err := s.approvalFor(ctx, env, a, cur)
	if err != nil {
		s.audit(ctx, core.AuditEntry{Actor: "agent:" + string(env.Run), Action: "release.dispatch", Target: p.Environment, Outcome: "denied",
			Session: env.Session, Run: env.Run, Detail: detail(map[string]any{"environment": p.Environment, "reason": err.Error()})})
		return nil, fmt.Errorf("this deploy did not run: %w", err)
	}
	now := s.d.Clock.Now()
	disp, via := s.dispatcher(ctx, p.Repo)
	d := &core.Deploy{Environment: p.Environment, Repo: p.Repo, Workflow: p.Workflow, Ref: p.Ref, Principal: env.Principal.ID,
		Session: env.Session, Run: env.Run, Approval: appr.ID, RequestedBy: env.User, ApprovedBy: voters, Chat: chatOf(env),
		Status: core.DeployDispatched, CreatedAt: now, UpdatedAt: now, Via: via}
	run, derr := disp.Dispatch(ctx, core.WorkflowDispatch{Repo: p.Repo, Workflow: p.Workflow, Ref: p.Ref, Inputs: p.Inputs})
	// Whatever happens to the tool call now, the dispatch is done or refused
	// for good: record it even if the call was cancelled meanwhile.
	bg := context.WithoutCancel(ctx)
	if derr != nil {
		d.Status, d.Error, d.FinishedAt = core.DeployFailed, clip(derr.Error(), maxErrorText), now
	} else {
		d.WorkflowRun, d.RunURL, d.SHA = run.ID, run.URL, run.HeadSHA
		if run.Status == "in_progress" {
			d.Status = core.DeployRunning
		}
	}
	recErr := s.d.Deploys.Create(bg, d)
	if recErr != nil {
		s.d.Log.ErrorContext(bg, "release: recording a deploy failed", "environment", d.Environment, "err", recErr)
	}
	outcome := "ok"
	if derr != nil {
		outcome = "error"
	}
	s.audit(bg, core.AuditEntry{Actor: "agent:" + string(env.Run), Action: "release.dispatch", Target: d.ID, Outcome: outcome,
		Session: env.Session, Run: env.Run, Detail: detail(map[string]any{"environment": d.Environment, "repo": d.Repo,
			"workflow": d.Workflow, "ref": d.Ref, "workflow_run_id": d.WorkflowRun, "approval": appr.ID, "approved_by": voters,
			"error": d.Error})})
	if derr != nil {
		if recErr == nil {
			s.publishFinished(d, 0, "")
		}
		return nil, fmt.Errorf("GitHub did not start the deploy: %w", derr)
	}
	if recErr == nil {
		s.publishDispatched(d)
	}
	s.poke()
	var b strings.Builder
	fmt.Fprintf(&b, "Started %s on %s at %s for %s", p.Workflow, p.Repo, p.Ref, p.Environment)
	if names := s.names(bg, voters); names != "" {
		fmt.Fprintf(&b, ", approved by %s", names)
	}
	if run.ID != 0 {
		fmt.Fprintf(&b, ". Run %d: %s", run.ID, run.URL)
	}
	if recErr != nil {
		b.WriteString(". Bothy could not record the deploy, so it will not report the outcome; follow the run on GitHub.")
	} else {
		b.WriteString(". The outcome will be posted here when the run finishes.")
	}
	return &core.ToolResult{Content: b.String()}, nil
}

// approvalFor finds the allowed multi-approver approval of this very call
// and returns it with the users whose allow votes count. The votes must also
// satisfy the environment as configured now: an approval may wait (parked)
// while an admin tightens required or replaces the approvers.
func (s *Service) approvalFor(ctx context.Context, env *core.ToolEnv, a *core.Action, cur config.ReleaseEnv) (*core.Approval, []core.UserID, error) {
	list, err := s.d.Store.Approvals().ByRun(ctx, env.Run)
	if err != nil {
		return nil, nil, fmt.Errorf("could not read its approval")
	}
	for _, ap := range list {
		if ap.ToolCallID != env.CallID || ap.Action == nil || ap.Action.Fingerprint != a.Fingerprint {
			continue
		}
		if ap.Status != core.ApprovalAllowed || !ap.Approver.Multi() || ap.Approver.Requester != env.User {
			return nil, nil, errors.New("it was not approved by the environment's approvers")
		}
		votes, err := s.d.Store.Approvals().Votes(ctx, ap.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("could not read its approvals")
		}
		var voters []core.UserID
		for _, v := range votes {
			if v.Choice == core.AllowOnce && v.User != ap.Approver.Requester && !slices.Contains(voters, v.User) {
				voters = append(voters, v.User)
			}
		}
		if len(voters) < ap.Approver.Needed() {
			return nil, nil, fmt.Errorf("only %d of %d approvals were recorded", len(voters), ap.Approver.Needed())
		}
		users, roles := cur.ApproverSet()
		now := core.ApproverReq{Users: users, Roles: roles, Required: cur.Needed(), Requester: env.User}
		n := 0
		for _, id := range voters {
			if u, err := s.d.Store.Users().Get(ctx, id); err == nil && !u.Disabled && now.Eligible(u.ID, u.Role) {
				n++
			}
		}
		if n < now.Needed() {
			return nil, nil, fmt.Errorf("the %s environment's approvers changed since it was approved: %d of the %d approvals it needs now still count",
				cur.Name, n, now.Needed())
		}
		return ap, voters, nil
	}
	return nil, nil, errors.New("no approval was recorded for it, and deploys always need the environment's approvers")
}

func (s *Service) names(ctx context.Context, ids []core.UserID) string {
	var out []string
	for _, id := range ids {
		u, err := s.d.Store.Users().Get(ctx, id)
		switch {
		case err != nil:
			out = append(out, string(id))
		case u.DisplayName != "":
			out = append(out, u.DisplayName)
		default:
			out = append(out, u.Username)
		}
	}
	switch len(out) {
	case 0:
		return ""
	case 1:
		return out[0]
	}
	return strings.Join(out[:len(out)-1], ", ") + " and " + out[len(out)-1]
}

// statusTool lists the environments and their recent deploys, with the live
// state of runs still being followed.
type statusTool struct{ s *Service }

func (t *statusTool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Def: core.ToolDef{
			Name:        statusName,
			Description: "List release environments (repository, workflow, allowed refs, who approves) and their recent deploys with the state of running ones.",
			Schema: json.RawMessage(`{"type":"object","properties":{` +
				`"environment":{"type":"string","description":"Only this environment; omit for all"}},"additionalProperties":false}`),
		},
		Source:   "builtin",
		Manifest: core.Manifest{Title: "Check releases", SideEffect: core.SERead, Core: true, Timeout: 30 * time.Second, MaxOutputKB: 8},
	}
}

func (t *statusTool) Enabled(*core.ToolEnv) bool {
	return len(t.s.d.Config.Current().Releases.Environments) > 0
}

func (t *statusTool) Plan(_ context.Context, _ *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var in struct {
		Environment string `json:"environment"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	c := t.s.d.Config.Current().Releases
	if _, ok := c.Env(in.Environment); in.Environment != "" && !ok {
		return nil, fmt.Errorf("%w: there is no release environment %q; configured: %s", core.ErrInvalid, in.Environment, envNames(c))
	}
	b, err := core.CanonicalJSON(in)
	if err != nil {
		return nil, fmt.Errorf("encode %s action: %w", statusName, err)
	}
	return &core.Action{Tool: statusName, Kind: core.ActGeneric, SideEffect: core.SERead, Payload: b,
		Fingerprint: core.ActionFingerprint(statusName, b)}, nil
}

func (t *statusTool) Exec(ctx context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	s := t.s
	var in struct {
		Environment string `json:"environment"`
	}
	if err := json.Unmarshal(a.Payload, &in); err != nil {
		return nil, fmt.Errorf("%w: %s payload: %w", core.ErrInvalid, statusName, err)
	}
	v := core.Viewer{User: env.User, Principals: []core.PrincipalID{env.Principal.ID}, Admin: env.Role.IsAdmin()}
	live := statusLiveChecks
	var b strings.Builder
	for _, e := range s.d.Config.Current().Releases.Environments {
		if in.Environment != "" && e.Name != in.Environment {
			continue
		}
		fmt.Fprintf(&b, "%s: %s, workflow %s, refs %s, needs %d approval(s) from %s\n",
			e.Name, e.Repo, e.Workflow, e.RefPattern, e.Needed(), approversText(e))
		ds, err := s.d.Deploys.List(ctx, v, core.DeployFilter{Environment: e.Name, Page: core.Page{Limit: statusDeploysPerEnv}})
		if err != nil {
			return nil, fmt.Errorf("release: listing deploys: %w", err)
		}
		if len(ds) == 0 {
			b.WriteString("  no deploys yet\n")
		}
		for _, d := range ds {
			state := string(d.Status)
			if !d.Status.Terminal() && d.WorkflowRun != 0 && live > 0 {
				live--
				if run, err := s.follower(ctx, d).RunStatus(ctx, d.Repo, d.WorkflowRun); err == nil {
					state += " (GitHub: " + run.Status + ")"
				}
			}
			if d.Conclusion != "" {
				state += ", " + d.Conclusion
			}
			fmt.Fprintf(&b, "  %s %s at %s: %s", d.CreatedAt.UTC().Format(time.RFC3339), d.Repo, d.Ref, state)
			if d.WorkflowRun != 0 {
				fmt.Fprintf(&b, ", run %d %s", d.WorkflowRun, d.RunURL)
			}
			b.WriteString("\n")
		}
	}
	if b.Len() == 0 {
		b.WriteString("No release environments are configured.")
	}
	return &core.ToolResult{Content: strings.TrimRight(b.String(), "\n")}, nil
}

func approversText(e config.ReleaseEnv) string {
	users, roles := e.ApproverSet()
	var parts []string
	for _, r := range roles {
		parts = append(parts, "any "+string(r))
	}
	if len(users) == 1 {
		parts = append(parts, "1 named user")
	} else if len(users) > 1 {
		parts = append(parts, fmt.Sprintf("%d named users", len(users)))
	}
	if len(parts) == 0 {
		return "nobody"
	}
	return strings.Join(parts, " or ") + ", never the requester"
}

func envNames(c config.ReleasesCfg) string {
	if len(c.Environments) == 0 {
		return "none"
	}
	names := make([]string, len(c.Environments))
	for i, e := range c.Environments {
		names[i] = e.Name
	}
	return strings.Join(names, ", ")
}

func inputsOf(e config.ReleaseEnv) map[string]string { return nonNil(maps.Clone(e.Inputs)) }

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// chatOf is the run's chat when it is a messaging channel: web and API runs
// see the outcome in their session.
func chatOf(env *core.ToolEnv) *core.ChatRef {
	if env == nil || env.Chat == nil || env.Chat.ChatID == "" {
		return nil
	}
	switch env.Chat.Channel {
	case "", "web", "api":
		return nil
	}
	c := *env.Chat
	return &c
}

func failed(text string) *core.ToolResult { return &core.ToolResult{Content: text, IsError: true} }

func decodeArgs(args json.RawMessage, v any) error {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: arguments: %w", core.ErrInvalid, err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: arguments: trailing data", core.ErrInvalid)
	}
	return nil
}

func detail(m map[string]any) json.RawMessage {
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

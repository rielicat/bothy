package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// auditNamer phrases audit rows. Approval and tool rows name the action
// they were about, read through the admin view from the approval or the
// run's tool calls (cached per page); a pruned run leaves the tool's generic
// phrase. Chats and schedules are named through the reader's own view: their
// titles stay private to their owner even in the log.
type auditNamer struct {
	*namer
	av        core.Viewer
	approvals map[core.ApprovalID]*core.Approval
	calls     map[core.RunID][]*core.ToolCallRecord
}

func (s *Server) auditRows(ctx context.Context, own, av core.Viewer, es []*core.AuditEntry) []dto.AuditRow {
	an := &auditNamer{namer: s.namer(ctx, own), av: av, approvals: map[core.ApprovalID]*core.Approval{},
		calls: map[core.RunID][]*core.ToolCallRecord{}}
	out := make([]dto.AuditRow, 0, len(es))
	for _, e := range es {
		out = append(out, dto.AuditRow{AuditEntry: *e, ActorName: an.actor(e.Actor), Summary: an.summary(e)})
	}
	return out
}

func (an *auditNamer) approval(id string) *core.Approval {
	aid := core.ApprovalID(id)
	if ap, ok := an.approvals[aid]; ok {
		return ap
	}
	ap, err := an.s.d.Store.Approvals().Get(an.ctx, an.av, aid)
	if err != nil {
		ap = nil
	}
	an.approvals[aid] = ap
	return ap
}

// toolAction finds the Action a tool row was about by its fingerprint.
func (an *auditNamer) toolAction(run core.RunID, fingerprint string) *core.Action {
	if run == "" || fingerprint == "" {
		return nil
	}
	calls, ok := an.calls[run]
	if !ok {
		calls, _ = an.s.d.Store.Runs().ToolCalls(an.ctx, an.av, run)
		an.calls[run] = calls
	}
	for _, c := range calls {
		if c.Action != nil && c.Action.Fingerprint == fingerprint {
			return c.Action
		}
	}
	return nil
}

func (an *auditNamer) approvalPhrase(e *core.AuditEntry, d auditDetail) phrase {
	if ap := an.approval(e.Target); ap != nil && ap.Action != nil {
		return actionPhrase(ap.Action.Tool, ap.Action)
	}
	return actionPhrase(d.str("tool"), nil)
}

// auditDetail reads the redacted detail object of a row.
type auditDetail map[string]any

func (d auditDetail) str(k string) string {
	switch v := d[k].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprint(v)
	}
	return ""
}

var viaNames = map[string]string{"telegram": "Telegram", "slack": "Slack", "discord": "Discord"}

// summary is the row's one plain line (docs/design/ui.md §10.6, §14).
func (an *auditNamer) summary(e *core.AuditEntry) string {
	var d auditDetail
	_ = json.Unmarshal(e.Detail, &d)
	failed := e.Outcome == "error"
	denied := e.Outcome == "denied"
	targetUser := func() string {
		id, ok := strings.CutPrefix(e.Target, "user:")
		if !ok {
			return e.Target
		}
		return cmpOr(an.user(core.UserID(id)), d.str("username"), "someone")
	}
	switch e.Action {
	case "approval.request":
		return "Asked to " + an.approvalPhrase(e, d).ask
	case "approval.decide":
		p := an.approvalPhrase(e, d)
		if d.str("status") == string(core.ApprovalCancelled) {
			return "Stopped asking to " + p.ask
		}
		if d["withdrawn"] == true {
			return "Cancelled the request to " + p.ask
		}
		line := "Approved " + p.ing
		switch core.Choice(d.str("choice")) {
		case core.AllowSession:
			line += " for the rest of the chat"
		case core.AllowAlways:
			line += " from now on"
		case core.Deny:
			line = "Denied " + p.ing
		}
		if via := viaNames[d.str("via")]; via != "" {
			line += " in " + via
		}
		return line
	case "approval.vote":
		p := an.approvalPhrase(e, d)
		switch {
		case denied:
			return "Couldn't vote on " + p.ing
		case core.Choice(d.str("choice")) == core.Deny:
			return "Voted to deny " + p.ing
		}
		return "Voted to approve " + p.ing
	case "approval.expire":
		p := an.approvalPhrase(e, d)
		if d.str("status") == string(core.ApprovalParked) {
			return "Kept waiting for approval to " + p.ask
		}
		return "Nobody approved " + p.ing + " in time"
	case "tool.exec":
		p := actionPhrase(cmpOr(d.str("tool"), e.Target), an.toolAction(e.Run, d.str("fingerprint")))
		if failed {
			return p.failed
		}
		return p.done
	case "tool.denied":
		return "Blocked from " + actionPhrase(cmpOr(d.str("tool"), e.Target), an.toolAction(e.Run, d.str("fingerprint"))).ing
	case "egress.deny":
		return "Blocked from reaching " + siteOf(e.Target)
	case "egress.allow":
		return "Allowed access to " + siteOf(e.Target)
	case "auth.login":
		return "Signed in"
	case "auth.login_failed":
		return "Someone couldn't sign in as " + targetUser()
	case "auth.lockout":
		return "Sign-in for " + targetUser() + " paused after too many tries"
	case "auth.logout":
		return "Signed out"
	case "auth.password_changed":
		switch {
		case e.Actor == e.Target:
			return "Changed their password"
		case d["one_time"] == true:
			return "Gave " + targetUser() + " a one-time password"
		}
		return "Changed the password of " + targetUser()
	case "auth.setup_claim":
		if denied {
			return "Someone tried a setup code that didn't work"
		}
		return "Opened setup"
	case "auth.owner_created":
		return "Created the owner account"
	case "token.create":
		if name := d.str("name"); name != "" {
			return "Created the access token " + quoted(name, "")
		}
		return "Created an access token"
	case "token.revoke":
		if d.str("reason") == "expired" {
			return "An access token expired"
		}
		return "Revoked an access token"
	case "user.create":
		return "Added " + cmpOr(an.user(core.UserID(strings.TrimPrefix(e.Target, "user:"))), d.str("username"), "a person")
	case "user.update":
		if _, purge := d["purge"]; purge {
			return "Removed " + targetUser() + " and their data"
		}
		return "Changed the account of " + targetUser()
	case "pairing.create":
		return "Started linking a chat app account"
	case "pairing.redeem":
		if denied {
			return "A chat app account used a link code that didn't work"
		}
		return "Linked a chat app account"
	case "identity.link", "identity.guest", "identity.block":
		ch, ext, _ := strings.Cut(strings.TrimPrefix(e.Target, "channel:"), ":")
		who := cmpOr(an.identity(ch, ext), "an account")
		if app := viaNames[ch]; app != "" {
			who += " on " + app
		}
		switch e.Action {
		case "identity.guest":
			return "Let " + who + " chat as a guest"
		case "identity.block":
			return "Blocked " + who
		}
		return "Linked " + who
	case "space.update":
		if sp, err := an.s.d.Store.Spaces().Get(an.ctx, an.av, core.PrincipalID(strings.TrimPrefix(e.Target, "space:"))); err == nil && sp.Title != "" {
			return "Changed the group chat " + sp.Title
		}
		return "Changed a group chat"
	case "persona.update":
		return "Changed Bothy's personality"
	case "vault.put":
		return "Saved a key"
	case "vault.delete":
		return "Removed a saved key"
	case "vault.rotate":
		return "Replaced the key that protects saved keys"
	case "vault.unseal":
		if denied {
			return "Couldn't unlock the saved keys"
		}
		return "Unlocked the saved keys"
	case "secret.use":
		return "Used a saved key"
	case "grant.create":
		return "Allowed something without asking"
	case "grant.revoke":
		return "Removed a permission"
	case "memory.write":
		switch d.str("op") {
		case "replace":
			return "Updated a saved note"
		case "remove":
			return "Forgot something"
		}
		return "Remembered something"
	case "memory.edit":
		return "Edited what Bothy remembers"
	case "skill.install":
		return "Installed the skill " + quoted(e.Target, "")
	case "skill.enable":
		return "Turned on the skill " + quoted(e.Target, "")
	case "skill.disable":
		return "Turned off the skill " + quoted(e.Target, "")
	case "skill.changed":
		return "The skill " + quoted(e.Target, "") + " changed"
	case "mcp.install":
		return "Installed the connected tool " + e.Target
	case "mcp.approve":
		return "Approved the tools of " + e.Target
	case "mcp.changed":
		return e.Target + " changed its tools"
	case "schedule.create", "schedule.update", "schedule.delete", "schedule.run":
		name := "a schedule"
		if n := cmpOr(d.str("name"), an.schedule(core.ScheduleID(strings.TrimPrefix(e.Target, "schedule:")))); n != "" {
			name = "the schedule " + quoted(n, "")
		}
		switch {
		case e.Action == "schedule.create":
			return "Created " + name
		case e.Action == "schedule.delete":
			return "Deleted " + name
		case e.Action == "schedule.run":
			return "Started " + name + " by hand"
		case d.str("reason") == "consecutive_failures":
			return "Turned off " + name + " after it failed several times"
		}
		return "Changed " + name
	case "run.stop":
		if t := an.chat(core.SessionID(e.Target)); t != "" {
			return "Stopped the task in “" + t + "”"
		}
		return "Stopped a task"
	case "budget.exceeded":
		switch core.BudgetKind(d.str("kind")) {
		case core.BudgetDailyCost:
			return "Reached today's spending limit"
		case core.BudgetRunCost:
			return "A task reached its spending limit"
		case core.BudgetSteps:
			return "A task reached its step limit"
		case core.BudgetTime:
			return "A task ran out of time"
		}
		return "A task reached a limit"
	case "admin.pause":
		return "Paused Bothy"
	case "admin.resume":
		return "Resumed Bothy"
	case "admin.restart":
		return "Restarted Bothy"
	case "backup.create":
		if failed {
			return "Couldn't make a backup"
		}
		return "Made a backup"
	case "sandbox.selftest_failed":
		return "Running code failed its safety check"
	case "api.passthrough":
		return "Used an AI model through the API"
	case "audit.archived":
		return "Archived old log entries"
	case "provider.connect":
		return "Connected an AI model"
	case "github.connect":
		return "Connected GitHub"
	case "github.repos":
		return "Changed the repositories Bothy can use"
	case "github.disconnect":
		return "Disconnected GitHub"
	case "github.checkout":
		return "Got a copy of " + e.Target
	case "github.pull_request":
		if failed {
			return "Couldn't open a pull request in " + e.Target
		}
		return "Opened a pull request in " + e.Target
	case "github.comment":
		return "Commented in " + e.Target
	case "knowledge.index", "knowledge.delete", "knowledge.notes_draft", "knowledge.notes_edit", "knowledge.notes_write":
		return knowledgeSummary(e, d)
	case "release.dispatch":
		env := cmpOr(d.str("environment"), e.Target)
		what := env
		if repo := d.str("repo"); repo != "" {
			what = repo + " to " + env
		}
		switch {
		case denied:
			return "Didn't deploy " + what
		case failed:
			return "Couldn't start deploying " + what
		}
		return "Started deploying " + what
	case "release.finish":
		env := cmpOr(d.str("environment"), "an environment")
		switch {
		case d.str("conclusion") == "success":
			return "The deploy to " + env + " finished"
		case d.str("status") == string(core.DeployUnknown):
			return "Bothy stopped following the deploy to " + env
		}
		return "The deploy to " + env + " didn't finish"
	}
	if strings.HasPrefix(e.Action, "config.") {
		if e.Action == "config.reset" {
			return "Reset a setting to its default"
		}
		return "Changed settings"
	}
	return humanize(strings.ReplaceAll(e.Action, ".", " "))
}

// siteOf names a host:port target by its host, keeping unusual ports.
func siteOf(target string) string {
	if h, ok := strings.CutSuffix(target, ":443"); ok {
		return h
	}
	if h, ok := strings.CutSuffix(target, ":80"); ok {
		return h
	}
	return cmpOr(target, "a website")
}

// knowledgeSummary phrases knowledge rows; their target is
// knowledge:<principal>/<owner>/<repo>.
func knowledgeSummary(e *core.AuditEntry, d auditDetail) string {
	repo := "a repository"
	if _, rest, ok := strings.Cut(strings.TrimPrefix(e.Target, "knowledge:"), "/"); ok && rest != "" {
		repo = rest
	}
	failed := e.Outcome == "error"
	switch e.Action {
	case "knowledge.delete":
		return "Removed " + repo + " from Knowledge"
	case "knowledge.notes_draft":
		if failed {
			return "Couldn't draft the notes for " + repo
		}
		return "Drafted the notes for " + repo
	case "knowledge.notes_edit":
		return "Edited the notes for " + repo
	case "knowledge.notes_write":
		return "Updated the notes for " + repo
	}
	switch {
	case failed:
		return "Couldn't read " + repo
	case d.str("state") == "queued":
		return "Started reading " + repo
	}
	return "Read " + repo
}

package policy

import (
	"context"
	"fmt"
	"maps"
	"strconv"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

const (
	toolReleaseDeploy = "release_deploy"
	floorRelease      = "floor:release"
)

// isRelease reports whether an action dispatches a deploy. Either mark is
// enough: the floor must not depend on a tool getting both right.
func isRelease(a *core.Action) bool { return a.Kind == core.ActRelease || a.Tool == toolReleaseDeploy }

// releaseFloor decides a deploy: never auto and never answered by a rule or
// grant (a deny rule may still refuse it), only in interactive runs of an
// account's own principal or a teammate's, never a channel's, and approved
// by the environment's approvers, of whom enough besides the requester must
// exist.
func (e *Engine) releaseFloor(ctx context.Context, x *input, rule *config.Rule) core.Decision {
	switch {
	case x.Trigger != core.TrigMessage:
		return denied(floorRelease, "deploys can only start from a chat, not from a schedule, check-in or API request", true)
	case x.Principal.Kind == core.PrincipalSpace:
		return denied(floorRelease, "deploys can't be requested from a channel, where who asked isn't clear", true)
	case x.Principal.Kind != core.PrincipalUser && x.Principal.Kind != core.PrincipalPerson:
		return denied(floorRelease, "deploys can only be requested from your own chats", true)
	case rule != nil && core.Verdict(rule.Verdict) == core.VerdictDeny:
		return ruleDenied(rule)
	}
	name, _ := mustPointer(x.payload, "/environment").(string)
	env, ok := x.cfg.Releases.Env(name)
	if !ok {
		return denied(floorRelease, "there's no environment called "+quoted(name), true)
	}
	if why := releaseMismatch(x.payload, env); why != "" {
		return denied(floorRelease, why, true)
	}
	users, roles := env.ApproverSet()
	need := env.Needed()
	fromChat := x.Principal.Kind == core.PrincipalPerson
	if fromChat {
		// The person who asked may also hold a web account Bothy can't tell
		// apart, so one more allow keeps env.Needed() people besides them.
		need++
	}
	approver := core.ApproverReq{Users: users, Roles: roles, Required: need, Requester: x.User}
	if !approver.Multi() {
		return denied(floorRelease, "the "+env.Name+" environment has no approvers", true)
	}
	n, err := e.eligibleApprovers(ctx, approver)
	if err != nil {
		e.log.WarnContext(ctx, "policy: listing release approvers failed", "environment", env.Name, "err", err)
		return denied(floorRelease, "Bothy couldn't check who can approve this deploy", true)
	}
	if n < approver.Needed() {
		if fromChat {
			return denied(floorRelease, fmt.Sprintf("a deploy to %s asked from a chat app needs %s, one more than usual because Bothy can't tell whether one of them asked, and %s",
				env.Name, plural(need, "approver"), canApproveText(n)), true)
		}
		return denied(floorRelease, fmt.Sprintf("a deploy to %s needs %s other than you, and %s", env.Name,
			plural(approver.Needed(), "approver"), canApproveText(n)), true)
	}
	// Every approver reads this reason, not only the requester.
	reason := fmt.Sprintf("deploy to %s: %s other than the person who asked must approve", env.Name, plural(env.Needed(), "approver"))
	if fromChat {
		reason = fmt.Sprintf("deploy to %s asked from a chat app: %s must approve, one more than usual because Bothy can't tell whether one of them asked",
			env.Name, plural(need, "approver"))
	}
	d := e.ask(x, askSpec{rule: floorRelease, floor: true, reason: reason})
	d.Approver = approver
	return d
}

// releaseMismatch compares a release action's payload with the environment it
// names, so the card can show the payload and still be what gets configured.
// The reason never quotes the ref pattern: that is configuration, not words.
func releaseMismatch(doc any, env config.ReleaseEnv) string {
	text := func(ptr string) string { s, _ := mustPointer(doc, ptr).(string); return s }
	the := "the " + inline(env.Name) + " environment"
	switch {
	case text("/repo") != env.Repo:
		return the + " doesn't deploy the repository " + inline(text("/repo"))
	case text("/workflow") != env.Workflow:
		return the + " doesn't use the workflow " + inline(text("/workflow"))
	case !env.AllowsRef(text("/ref")):
		return the + " doesn't deploy " + inline(text("/ref"))
	}
	inputs := "the workflow inputs don't match " + the
	got := map[string]string{}
	if m, ok := mustPointer(doc, "/inputs").(map[string]any); ok {
		for k, v := range m {
			s, ok := v.(string)
			if !ok {
				return inputs
			}
			got[k] = s
		}
	}
	want := env.Inputs
	if want == nil {
		want = map[string]string{}
	}
	if !maps.Equal(got, want) {
		return inputs
	}
	return ""
}

// eligibleApprovers counts enabled users who may vote allow.
func (e *Engine) eligibleApprovers(ctx context.Context, r core.ApproverReq) (int, error) {
	us, err := e.store.Users().List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range us {
		if !u.Disabled && r.Eligible(u.ID, u.Role) {
			n++
		}
	}
	return n, nil
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func canApproveText(n int) string {
	switch n {
	case 0:
		return "nobody else can approve it"
	case 1:
		return "only 1 other person can approve it"
	}
	return "only " + strconv.Itoa(n) + " other people can approve it"
}

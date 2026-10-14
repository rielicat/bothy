package policy

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

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
// grant (a deny rule may still refuse it), only in interactive runs of a
// user's own principal, and approved by the environment's approvers, of whom
// enough besides the requester must exist.
func (e *Engine) releaseFloor(ctx context.Context, x *input, rule *config.Rule) core.Decision {
	switch {
	case x.Trigger != core.TrigMessage:
		return denied(floorRelease, "deploys are not allowed in scheduled, heartbeat or API runs", true)
	case x.Principal.Kind != core.PrincipalUser:
		return denied(floorRelease, "deploys can only be requested from your own chats, not from a group", true)
	case rule != nil && core.Verdict(rule.Verdict) == core.VerdictDeny:
		return ruleDenied(rule)
	}
	name, _ := mustPointer(x.payload, "/environment").(string)
	env, ok := x.cfg.Releases.Env(name)
	if !ok {
		return denied(floorRelease, "there is no release environment named "+strconv.Quote(name), true)
	}
	if why := releaseMismatch(x.payload, env); why != "" {
		return denied(floorRelease, "this deploy does not match the "+env.Name+" environment: "+why, true)
	}
	users, roles := env.ApproverSet()
	approver := core.ApproverReq{Users: users, Roles: roles, Required: env.Needed(), Requester: x.User}
	if !approver.Multi() {
		return denied(floorRelease, "the "+env.Name+" environment has no approvers", true)
	}
	n, err := e.eligibleApprovers(ctx, approver)
	if err != nil {
		e.log.WarnContext(ctx, "policy: listing release approvers failed", "environment", env.Name, "err", err)
		return denied(floorRelease, "could not check who may approve this deploy", true)
	}
	if n < approver.Needed() {
		return denied(floorRelease, fmt.Sprintf("a deploy to %s needs %s other than you, and %s", env.Name,
			plural(approver.Needed(), "approver"), canApproveText(n)), true)
	}
	d := e.ask(x, askSpec{rule: floorRelease, floor: true,
		reason: fmt.Sprintf("deploy to %s: %s other than you must approve", env.Name, plural(approver.Needed(), "approver"))})
	d.Approver = approver
	return d
}

// releaseMismatch compares a release action's payload with the environment it
// names, so the card can show the payload and still be what gets configured.
func releaseMismatch(doc any, env config.ReleaseEnv) string {
	text := func(ptr string) string { s, _ := mustPointer(doc, ptr).(string); return s }
	switch {
	case text("/repo") != env.Repo:
		return "the repository differs"
	case text("/workflow") != env.Workflow:
		return "the workflow differs"
	case !env.AllowsRef(text("/ref")):
		return "ref " + strconv.Quote(text("/ref")) + " is not allowed (" + env.RefPattern + ")"
	}
	got := map[string]string{}
	if m, ok := mustPointer(doc, "/inputs").(map[string]any); ok {
		for k, v := range m {
			s, ok := v.(string)
			if !ok {
				return "input " + k + " is not text"
			}
			got[k] = s
		}
	}
	want := env.Inputs
	if want == nil {
		want = map[string]string{}
	}
	if !maps.Equal(got, want) {
		return "the workflow inputs differ"
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

// approverSummary describes who may approve, for cards: named users first,
// then roles ("Alice, Bob or any admin").
func approverSummary(r core.ApproverReq, name func(core.UserID) string) string {
	var parts []string
	for _, u := range r.Users {
		parts = append(parts, name(u))
	}
	roles := slices.Clone(r.Roles)
	slices.SortFunc(roles, func(a, b core.Role) int { return strings.Compare(string(a), string(b)) })
	for _, ro := range roles {
		switch ro {
		case core.RoleMember:
			parts = append(parts, "anyone")
		case core.RoleOwner:
			parts = append(parts, "the owner")
		default:
			parts = append(parts, "any "+string(ro))
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}

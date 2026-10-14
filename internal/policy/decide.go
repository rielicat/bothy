package policy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

const (
	toolWebFetch = "web_fetch"
	toolNetAllow = "net_allow"
	metaPaused   = "paused"

	floorTrifecta  = "floor:trifecta"
	trifectaReason = "this session has seen private data and untrusted content, so anything leaving it needs approval"
)

// Defaults used when the configured approval timeouts are unset (§5.9).
const (
	defaultInteractiveTimeout = 15 * time.Minute
	defaultScheduledTimeout   = 2 * time.Minute
	defaultParkTTL            = 24 * time.Hour
)

// presets maps policy.preset to the class defaults of SPEC §5.8. Financial
// and admin never reach the class defaults: floors F2 and F3 decide them.
var presets = map[string]map[core.SideEffect]core.Verdict{
	"careful": {
		core.SERead: core.VerdictAuto, core.SELocalWrite: core.VerdictAuto,
		core.SEExternalSend: core.VerdictAsk, core.SEIrreversible: core.VerdictAsk,
	},
	"balanced": {
		core.SERead: core.VerdictAuto, core.SELocalWrite: core.VerdictAuto,
		core.SEExternalSend: core.VerdictAuto, core.SEIrreversible: core.VerdictAsk,
	},
	"hands_off": {
		core.SERead: core.VerdictAuto, core.SELocalWrite: core.VerdictAuto,
		core.SEExternalSend: core.VerdictAuto, core.SEIrreversible: core.VerdictAsk,
	},
}

// input is one Decide call with the derived facts every step reads.
type input struct {
	core.PolicyInput
	cfg     *config.Config
	a       *core.Action
	se      core.SideEffect // max(Action, Manifest): an Action may escalate, never lower
	payload any
	argv    []string   // what an exec action runs: the payload's argv when present
	hosts   []hostPort // every destination the action names
	grants  []*core.Grant
	loaded  bool
	// only is set when the trifecta floor was lifted by an AllowWhenTainted
	// recipient grant: then no other grant may allow the action.
	only *core.Grant
}

func newInput(in core.PolicyInput, c *config.Config) (*input, error) {
	a := in.Action
	if a == nil {
		return nil, errors.New("no action")
	}
	if in.Spec.Def.Name == "" || a.Tool != in.Spec.Def.Name {
		return nil, fmt.Errorf("action tool %q does not match tool %q", a.Tool, in.Spec.Def.Name)
	}
	se := max(validOrZero(a.SideEffect), validOrZero(in.Spec.Manifest.SideEffect))
	if !se.Valid() {
		return nil, errors.New("no valid side-effect class")
	}
	x := &input{PolicyInput: in, cfg: c, a: a, se: se}
	var ok bool
	// An unreadable payload would silently skip every match rule, deny ones
	// included.
	if x.payload, ok = decodePayload(a.Payload); !ok && len(bytes.TrimSpace(a.Payload)) > 0 {
		return nil, errors.New("the action payload is not JSON")
	}
	x.argv = a.Argv
	if v, ok := pointer(x.payload, "/argv"); ok {
		if list, ok := stringList(v); ok {
			x.argv = list
		}
	}
	x.hosts = actionHosts(a)
	return x, nil
}

func validOrZero(s core.SideEffect) core.SideEffect {
	if s.Valid() {
		return s
	}
	return 0
}

func stringList(v any) ([]string, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, len(items))
	for i, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}

// actionHosts lists the destinations of an action: Host (port from Port or
// the URL scheme), else the URL's host, then every entry of Domains.
func actionHosts(a *core.Action) []hostPort {
	var out []hostPort
	switch {
	case a.Host != "":
		hp := hostPort{host: normalizeHost(a.Host), port: a.Port}
		if hp.port == 0 && a.URL != "" {
			if u, ok := urlHostPort(a.URL); ok && u.host == hp.host {
				hp.port = u.port
			}
		}
		out = append(out, hp)
	case a.URL != "":
		if u, ok := urlHostPort(a.URL); ok {
			out = append(out, u)
		}
	}
	for _, d := range a.Domains {
		hp, ok := splitHostPort(d)
		if !ok || slices.Contains(out, hp) {
			continue
		}
		// A portless entry naming a host already listed adds nothing.
		if hp.port == 0 && slices.ContainsFunc(out, func(o hostPort) bool { return o.host == hp.host }) {
			continue
		}
		out = append(out, hp)
	}
	return out
}

func (x *input) primary() hostPort {
	if len(x.hosts) == 0 {
		return hostPort{}
	}
	return x.hosts[0]
}

func (x *input) exposed() bool { return x.Taint.Exposed() }

// activeGrants loads the principal's applicable grants once per decision. A
// store failure yields none: grants only ever relax a decision.
func (e *Engine) activeGrants(ctx context.Context, x *input) []*core.Grant {
	if x.loaded {
		return x.grants
	}
	x.loaded = true
	gs, err := e.store.Grants().Active(ctx, x.Principal.ID, x.Session, x.Schedule, e.clock.Now())
	if err != nil {
		e.log.WarnContext(ctx, "policy: loading grants failed; deciding without them", "principal", x.Principal.ID, "err", err)
		return nil
	}
	x.grants = gs
	return gs
}

// Decide implements core.Policy (SPEC §5.8): floors, rules, grants, first
// contact, class defaults, stopping at the first verdict.
func (e *Engine) Decide(ctx context.Context, in core.PolicyInput) core.Decision {
	x, err := newInput(in, e.cfg.Current())
	if err != nil {
		return denied("floor:invalid", "the tool call could not be checked: "+err.Error(), true)
	}
	rule := firstRule(x)
	if d, done := e.floors(ctx, x, rule); done {
		return d
	}
	if x.only != nil {
		return e.taintedException(ctx, x, rule)
	}
	var pending *config.Rule
	if rule != nil {
		switch core.Verdict(rule.Verdict) {
		case core.VerdictDeny:
			return ruleDenied(rule)
		case core.VerdictAuto:
			return allowed("rule:"+rule.ID, ruleReason(rule))
		case core.VerdictAsk:
			pending = rule
		}
	}
	// Rule asks and the class defaults are what a narrow grant exists to
	// answer, so grants are consulted only when they could change the result.
	var d core.Decision
	if pending != nil {
		d = e.ask(x, askSpec{rule: "rule:" + pending.ID, reason: ruleReason(pending), grants: true})
	} else {
		d = e.contactOrDefault(ctx, x)
		if d.Verdict == core.VerdictAuto {
			return d
		}
	}
	if g := e.useGrant(ctx, x); g != nil {
		return allowed("grant:"+string(g.ID), "allowed by an earlier approval")
	}
	return d
}

// floors evaluates F1–F9. Ask floors still yield to a deny rule: rules may
// tighten a floor, never relax it.
func (e *Engine) floors(ctx context.Context, x *input, rule *config.Rule) (core.Decision, bool) {
	m := x.Spec.Manifest
	switch {
	case len(m.Triggers) > 0 && !slices.Contains(m.Triggers, x.Trigger):
		return denied("floor:unavailable", fmt.Sprintf("%s is not available in %s runs", x.a.Tool, triggerName(x.Trigger)), true), true
	case m.MinRole != "" && roleRank(x.Role) < roleRank(m.MinRole):
		return denied("floor:unavailable", fmt.Sprintf("%s needs the %s role", x.a.Tool, m.MinRole), true), true
	case x.Principal.Restricted() && !m.SpaceOK:
		return denied("floor:unavailable", x.a.Tool+" is not available in restricted spaces", true), true
	case x.se != core.SERead && e.paused(ctx):
		return denied("floor:paused", "Bothy is paused by an admin; only read-only tools run", true), true
	}

	askFloor := func(name, reason string, admin bool) (core.Decision, bool) {
		if rule != nil && core.Verdict(rule.Verdict) == core.VerdictDeny {
			return ruleDenied(rule), true
		}
		return e.ask(x, askSpec{rule: name, reason: reason, floor: true, admin: admin}), true
	}

	// F4 is applied before the F2/F3 asks: an admin's approval must not open
	// a non-read action to a restricted space, which the tool profile forbids.
	restricted := x.Principal.Restricted() && x.se > core.SERead
	switch x.se {
	case core.SEFinancial:
		if rule != nil && core.Verdict(rule.Verdict) == core.VerdictDeny {
			return ruleDenied(rule), true
		}
		// Only a rule aimed at the financial class opts in: a broad ask rule
		// written to tighten other tools must not turn this deny into an ask.
		explicit := rule != nil && rule.SideEffect == core.SEFinancial.String()
		optIn := explicit && core.Verdict(rule.Verdict) == core.VerdictAsk ||
			!explicit && core.Verdict(x.cfg.Policy.Defaults[core.SEFinancial.String()]) == core.VerdictAsk
		if !optIn || restricted {
			return denied("floor:financial", "financial actions are never allowed", true), true
		}
		return e.ask(x, askSpec{rule: "floor:financial", reason: "financial action: an admin must approve it", floor: true, admin: true}), true
	case core.SEAdmin:
		if x.Trigger.Scheduled() || x.Trigger == core.TrigAPI {
			return denied("floor:admin", "self-modification is not allowed in scheduled, heartbeat or API runs", true), true
		}
		if !restricted {
			return askFloor("floor:admin", "self-modification: an admin must approve it", true)
		}
	}
	if restricted {
		return denied("floor:restricted_space", "restricted spaces can only use read-only tools", true), true
	}
	if isRelease(x.a) {
		return e.releaseFloor(ctx, x, rule), true
	}
	if x.exposed() && (x.se == core.SEExternalSend || x.se == core.SEIrreversible) {
		x.only = e.taintedRecipientGrant(ctx, x)
		if x.only == nil {
			return askFloor(floorTrifecta, trifectaReason, false)
		}
	}
	if x.Provenance == core.ProvUntrusted && x.se >= core.SELocalWrite {
		return askFloor("floor:untrusted_origin", "this run was started by an untrusted message", false)
	}
	if x.a.Kind == core.ActMemory && x.Taint.Has(core.TaintUntrusted) {
		if x.cfg.Memory.TaintedWrites == "deny" {
			return denied("floor:tainted_memory", "memory writes are off after untrusted content entered the session", true), true
		}
		return askFloor("floor:tainted_memory", "untrusted content entered this session, so memory writes need approval", false)
	}
	if x.a.Tool == toolWebFetch && x.exposed() && !e.knownURL(ctx, x) {
		return askFloor("floor:url_provenance", "this URL did not come from search results or your messages", false)
	}
	if x.a.Tool == toolNetAllow || x.a.Kind == core.ActNet {
		if rule != nil && core.Verdict(rule.Verdict) == core.VerdictDeny {
			return ruleDenied(rule), true
		}
		return e.ask(x, askSpec{rule: "floor:net_allow", reason: "network access for the sandbox always needs approval", floor: true, grants: true, noOnce: true}), true
	}
	return core.Decision{}, false
}

func (e *Engine) paused(ctx context.Context) bool {
	v, err := e.store.Meta().Get(ctx, metaPaused)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return false
	case err != nil:
		e.log.WarnContext(ctx, "policy: reading the kill switch failed; treating as paused", "err", err)
		return true
	}
	v = strings.TrimSpace(v)
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

func (e *Engine) knownURL(ctx context.Context, x *input) bool {
	if x.a.URL == "" || x.Session == "" {
		return false
	}
	ok, err := e.store.Sessions().HasURL(ctx, x.Session, x.a.URL)
	if err != nil {
		e.log.WarnContext(ctx, "policy: session URL lookup failed", "session", x.Session, "err", err)
		return false
	}
	return ok
}

// taintedException decides an action whose trifecta floor an
// AllowWhenTainted grant lifted. Only that grant may allow it, and its use is
// what lifts the floor: a rule auto must not skip Use (MaxUses), and a grant
// spent or revoked since it was loaded puts the floor back.
func (e *Engine) taintedException(ctx context.Context, x *input, rule *config.Rule) core.Decision {
	if rule != nil && core.Verdict(rule.Verdict) == core.VerdictDeny {
		return ruleDenied(rule)
	}
	if g := e.useGrant(ctx, x); g != nil {
		return allowed("grant:"+string(g.ID), "allowed by an earlier approval")
	}
	return e.ask(x, askSpec{rule: floorTrifecta, reason: trifectaReason, floor: true})
}

// taintedRecipientGrant finds the only kind of grant that lifts the
// trifecta floor: an AllowWhenTainted grant for exactly this recipient.
func (e *Engine) taintedRecipientGrant(ctx context.Context, x *input) *core.Grant {
	r := x.a.Recipient
	if r == "" {
		return nil
	}
	for _, g := range e.activeGrants(ctx, x) {
		if g.Kind == core.GrantRecipient && g.AllowWhenTainted && !hasGlobMeta(g.Value) && g.Value == r && g.MaxSE >= x.se {
			return g
		}
	}
	return nil
}

// useGrant returns the first applicable grant after recording its use.
func (e *Engine) useGrant(ctx context.Context, x *input) *core.Grant {
	cands := e.activeGrants(ctx, x)
	if x.only != nil {
		cands = []*core.Grant{x.only}
	}
	for _, g := range cands {
		if g.MaxSE < x.se || !grantMatches(g, x) {
			continue
		}
		if err := e.store.Grants().Use(ctx, g.ID); err != nil {
			e.log.DebugContext(ctx, "policy: grant not usable", "grant", g.ID, "err", err)
			continue
		}
		return g
	}
	return nil
}

// contactOrDefault applies first contact (C) and the class default (D). First
// contact only ever turns an auto into an ask; it never softens a deny.
func (e *Engine) contactOrDefault(ctx context.Context, x *input) core.Decision {
	class := x.se.String()
	v := defaultVerdict(x.cfg, x.se)
	if v == core.VerdictDeny {
		return denied("default:"+class, class+" actions are denied by the policy defaults", false)
	}
	if kind, target, first := e.firstContact(ctx, x); first {
		return e.ask(x, askSpec{
			rule: "first_contact:" + kind, reason: "first contact with " + target, grants: true,
			warnings: []string{"First contact with " + target + "."},
		})
	}
	if v == core.VerdictAsk {
		return e.ask(x, askSpec{rule: "default:" + class, reason: class + " actions need approval under the " + presetName(x.cfg) + " preset", grants: true})
	}
	return allowed("default:"+class, class+" actions are allowed under the "+presetName(x.cfg)+" preset")
}

func defaultVerdict(c *config.Config, se core.SideEffect) core.Verdict {
	if v := core.Verdict(c.Policy.Defaults[se.String()]); v == core.VerdictAuto || v == core.VerdictAsk || v == core.VerdictDeny {
		return v
	}
	if v, ok := presets[presetName(c)][se]; ok {
		return v
	}
	return core.VerdictAsk
}

func presetName(c *config.Config) string {
	if _, ok := presets[c.Policy.Preset]; ok {
		return c.Policy.Preset
	}
	return "careful"
}

// firstContact reports whether the action reaches a recipient or, for
// non-GET HTTP, a domain this principal has not dealt with before.
func (e *Engine) firstContact(ctx context.Context, x *input) (kind, target string, first bool) {
	if presetName(x.cfg) == "hands_off" {
		return "", "", false
	}
	a := x.a
	switch {
	case a.Kind == core.ActSend || a.Kind == core.ActMCP && a.Recipient != "":
		r := a.Recipient
		if r == "" {
			return "recipient", "an unknown recipient", true
		}
		if x.Chat != nil && r == x.Chat.Key() {
			return "", "", false
		}
		for _, g := range e.activeGrants(ctx, x) {
			if g.Kind == core.GrantRecipient && g.Value == r {
				return "", "", false
			}
		}
		if e.seen(ctx, x, r, "") {
			return "", "", false
		}
		return "recipient", r, true
	case a.Kind == core.ActHTTP && a.Method != "" && !strings.EqualFold(a.Method, "GET"):
		h := x.primary()
		if h.host == "" {
			return "domain", "an unknown host", true
		}
		for _, s := range x.cfg.Egress.Allow {
			if p, ok := parseHostPattern(s); ok && p.match(h) {
				return "", "", false
			}
		}
		for _, g := range e.activeGrants(ctx, x) {
			if p, ok := parseHostPattern(g.Value); g.Kind == core.GrantDomain && ok && p.match(hostPort{host: h.host, port: p.port}) {
				return "", "", false
			}
		}
		if e.seen(ctx, x, "", h.host) {
			return "", "", false
		}
		return "domain", h.host, true
	}
	return "", "", false
}

func (e *Engine) seen(ctx context.Context, x *input, recipient, host string) bool {
	ok, err := e.store.Approvals().Seen(ctx, x.Principal.ID, recipient, host)
	if err != nil {
		e.log.WarnContext(ctx, "policy: first-contact lookup failed; treating as first contact", "err", err)
		return false
	}
	return ok
}

type askSpec struct {
	rule     string
	reason   string
	floor    bool
	admin    bool // AdminOnly approver
	grants   bool // offer allow_session/allow_always where a narrow grant exists
	noOnce   bool // net_allow: the only useful answer is a grant
	warnings []string
}

func (e *Engine) ask(x *input, s askSpec) core.Decision {
	d := core.Decision{Verdict: core.VerdictAsk, Rule: s.rule, Reason: s.reason, Floor: s.floor}
	if s.admin {
		d.Approver = core.ApproverReq{AdminOnly: true}
	} else {
		d.Approver = core.ApproverReq{User: x.User}
	}
	d.Timeout, d.OnTimeout = timeouts(x.cfg, x.Trigger)
	if !s.noOnce {
		d.Choices = append(d.Choices, core.AllowOnce)
	}
	if s.grants && !s.admin && e.sessionGrantOK(x) {
		d.Choices = append(d.Choices, core.AllowSession)
		if e.alwaysGrantOK(x) {
			d.Choices = append(d.Choices, core.AllowAlways)
		}
	}
	if len(d.Choices) == 0 {
		d.Choices = append(d.Choices, core.AllowOnce)
	}
	d.Choices = append(d.Choices, core.Deny)
	if x.exposed() {
		d.Warnings = append(d.Warnings, "This session has seen private data and untrusted content; anything sent out could leak it.")
	}
	if x.Provenance == core.ProvUntrusted {
		d.Warnings = append(d.Warnings, "Started by a message from an untrusted sender.")
	}
	d.Warnings = append(d.Warnings, s.warnings...)
	return d
}

// sessionGrantOK: allow_session needs a session, a narrow grant, a class a
// grant may cover and a session that is not Exposed (§5.8).
func (e *Engine) sessionGrantOK(x *input) bool {
	if x.exposed() || x.Session == "" || x.se >= core.SEFinancial {
		return false
	}
	_, ok := narrowGrant(x.cfg, x.a, x.Principal.ID, core.GrantSession, string(x.Session), x.User, time.Time{})
	return ok
}

// alwaysGrantOK: allow_always is offered exactly when allow_session is,
// except for irreversible actions and non-interactive triggers, whose
// persistent grants would be created without a human looking at a card in
// context. narrowGrant refuses shared-tenant hosts at principal scope.
func (e *Engine) alwaysGrantOK(x *input) bool {
	if x.se == core.SEIrreversible || x.Trigger.Scheduled() || x.Trigger == core.TrigAPI {
		return false
	}
	_, ok := narrowGrant(x.cfg, x.a, x.Principal.ID, core.GrantPrincipal, "", x.User, time.Time{})
	return ok
}

func timeouts(c *config.Config, t core.Trigger) (time.Duration, core.TimeoutAction) {
	ap := c.Policy.Approval
	switch {
	case t.Scheduled():
		return orDefault(ap.ScheduledTimeout.D(), defaultScheduledTimeout), timeoutAction(ap.ScheduledOnTimeout)
	case t == core.TrigAPI:
		// API runs never park (D23); the runner bounds the wait by the token.
		return ap.APIWait.D(), core.TimeoutDeny
	}
	return orDefault(ap.InteractiveTimeout.D(), defaultInteractiveTimeout), timeoutAction(ap.InteractiveOnTimeout)
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func timeoutAction(s string) core.TimeoutAction {
	if s == string(core.TimeoutPark) {
		return core.TimeoutPark
	}
	return core.TimeoutDeny
}

func denied(rule, reason string, floor bool) core.Decision {
	return core.Decision{Verdict: core.VerdictDeny, Rule: rule, Reason: reason, Floor: floor}
}

func allowed(rule, reason string) core.Decision {
	return core.Decision{Verdict: core.VerdictAuto, Rule: rule, Reason: reason}
}

func ruleDenied(r *config.Rule) core.Decision {
	return denied("rule:"+r.ID, ruleReason(r), false)
}

func ruleReason(r *config.Rule) string {
	if r.Reason != "" {
		return r.Reason
	}
	return "matched policy rule " + r.ID
}

func roleRank(r core.Role) int {
	switch r {
	case core.RoleOwner:
		return 3
	case core.RoleAdmin:
		return 2
	case core.RoleMember:
		return 1
	}
	return 0
}

func triggerName(t core.Trigger) string {
	if t == "" {
		return "these"
	}
	return string(t)
}

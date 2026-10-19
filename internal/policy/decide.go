package policy

import (
	"bytes"
	"cmp"
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

	floorTrifecta        = "floor:trifecta"
	trifectaReason       = "this session has seen private data and untrusted content, so anything leaving it needs approval"
	trifectaUndoneReason = "this session has seen private data and untrusted content, so anything that can't be undone needs approval"

	unknownRecipient = "an unknown recipient"
	unknownHost      = "an unknown host"

	warnExposed   = "This chat has seen private information and content from outside. Check carefully before letting anything leave."
	warnUntrusted = "This request came from content Bothy read (a web page, repository or message), not from a person."
	// warnTeamOutside: what the team remembers reaches every chat, so outside
	// content that could have asked for it is worth a line even when the
	// chat holds nothing private.
	warnTeamOutside = "This chat has seen content from outside your team. Check that a person asked for this."

	floorTeammate  = "floor:teammate"
	teammateReason = "Bothy checks with an approver before it changes or sends anything for people in chat apps"
)

// contactWarning is the card line for a first contact (§8.3 of the UI plan).
func contactWarning(a *core.Action, p core.Principal, kind, target string) string {
	switch {
	case kind == "domain" && target == unknownHost:
		return "Bothy can't tell which website this sends data to."
	case kind == "domain":
		return "Bothy hasn't sent data to " + inline(target) + " before."
	case target == unknownRecipient:
		return "Bothy can't tell who this goes to."
	case a.Tool == toolRepoOpenPR && strings.HasPrefix(target, "github:"):
		// First contact is per principal: another person's pull requests in
		// the repository don't count.
		who := "you"
		switch p.Kind {
		case core.PrincipalSpace:
			who = "this group chat"
		case core.PrincipalPerson:
			who = inline(p.DisplayName)
		}
		return "This is the first pull request Bothy opens in " + inline(strings.TrimPrefix(target, "github:")) + " for " + who + "."
	}
	return "Bothy hasn't sent anything to " + inline(recipientText(target)) + " before."
}

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
	se      core.SideEffect // actionClass: an Action may escalate, never lower
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
	se := actionClass(a, in.Spec.Manifest)
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
	x.argv = execArgv(a, x.payload)
	x.hosts = actionHosts(a)
	return x, nil
}

// actionClass is the side-effect class an action is decided and shown as:
// the higher of the Action's and the Manifest's, and irreversible for a
// command that deletes files, as fs_delete is, whatever its tool declares.
func actionClass(a *core.Action, m core.Manifest) core.SideEffect {
	if isTeamMemory(a) {
		return core.SEAdmin
	}
	se := max(validOrZero(a.SideEffect), validOrZero(m.SideEffect))
	if a.Kind == core.ActExec && se.Valid() && se < core.SEIrreversible {
		doc, _ := decodePayload(a.Payload)
		if deletesFiles(execArgv(a, doc)) {
			return core.SEIrreversible
		}
	}
	return se
}

// isTeamMemory reports a change to TEAM.md, which every chat reads: it is
// admin-class (F3) whatever the Action declares.
func isTeamMemory(a *core.Action) bool {
	if a.Tool != toolMemory {
		return false
	}
	doc, _ := decodePayload(a.Payload)
	f, _ := mustPointer(doc, "/file").(string)
	return f == string(core.FileTeam)
}

// execArgv is what an exec action runs: the payload's argv when present.
func execArgv(a *core.Action, doc any) []string {
	if v, ok := pointer(doc, "/argv"); ok {
		if list, ok := stringList(v); ok {
			return list
		}
	}
	return a.Argv
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

// staysLocal reports whether an action only touches Bothy's own files and
// memory, so nothing it does can carry data out.
func (x *input) staysLocal() bool {
	switch x.a.Kind {
	case core.ActFSRead, core.ActFSWrite, core.ActFSDelete, core.ActMemory:
		return x.se != core.SEExternalSend && x.se < core.SEFinancial
	}
	return false
}

func (x *input) trifectaReason() string {
	if x.se == core.SEIrreversible {
		return trifectaUndoneReason
	}
	return trifectaReason
}

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
		e.log.WarnContext(ctx, "policy: the tool call could not be checked", "tool", in.Spec.Def.Name, "err", err)
		return denied("floor:invalid", "Bothy couldn't check this step, so it didn't run it", true)
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
		return denied("floor:unavailable", "Bothy can't "+x.askPhrase()+" "+triggerPlace(x.Trigger), true), true
	case m.MinRole != "" && roleRank(x.Role) < roleRank(m.MinRole):
		return denied("floor:unavailable", roleOnly(m.MinRole)+" can "+x.askPhrase(), true), true
	case x.Principal.Restricted() && !m.SpaceOK:
		return denied("floor:unavailable", "Bothy can't "+x.askPhrase()+" in this group chat", true), true
	case x.se != core.SERead && e.paused(ctx):
		return denied("floor:paused", "Bothy is paused, so it can only read until an admin resumes it", true), true
	}

	askFloor := func(name, reason string, admin bool) (core.Decision, bool) {
		if rule != nil && core.Verdict(rule.Verdict) == core.VerdictDeny {
			return ruleDenied(rule), true
		}
		d := e.ask(x, askSpec{rule: name, reason: reason, floor: true, admin: admin})
		// An always-ask tool never reaches step C: whichever floor asks
		// first carries its first-contact warning.
		if m.AlwaysAsk {
			if kind, target, first := e.firstContact(ctx, x); first {
				d.Warnings = append(d.Warnings, contactWarning(x.a, x.Principal, kind, target))
			}
		}
		return d, true
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
		team := isTeamMemory(x.a)
		if x.Trigger.Scheduled() || x.Trigger == core.TrigAPI {
			what := "change its settings "
			if team {
				what = "change what it remembers for everyone "
			}
			return denied("floor:admin", "Bothy can't "+what+triggerPlace(x.Trigger), true), true
		}
		if team && x.Taint.Has(core.TaintUntrusted) && x.cfg.Memory.TaintedWrites == "deny" {
			return denied("floor:tainted_memory", taintedMemoryDenied, true), true
		}
		if !restricted {
			return askFloor("floor:admin", adminReason(x.a), true)
		}
	}
	if restricted {
		return denied("floor:restricted_space", "in this group chat Bothy can only read", true), true
	}
	if isRelease(x.a) {
		return e.releaseFloor(ctx, x, rule), true
	}
	if x.exposed() && (x.se == core.SEExternalSend || x.se == core.SEIrreversible) {
		x.only = e.taintedRecipientGrant(ctx, x)
		if x.only == nil {
			return askFloor(floorTrifecta, x.trifectaReason(), false)
		}
	}
	if x.Provenance == core.ProvUntrusted && x.se >= core.SELocalWrite {
		return askFloor("floor:untrusted_origin", "this run was started by an untrusted message", false)
	}
	if x.a.Kind == core.ActMemory && x.Taint.Has(core.TaintUntrusted) {
		if x.cfg.Memory.TaintedWrites == "deny" {
			return denied("floor:tainted_memory", taintedMemoryDenied, true), true
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
	if m.AlwaysAsk {
		return askFloor("floor:always_ask", cmp.Or(m.Title, x.a.Tool)+" always needs approval", false)
	}
	if x.Principal.Kind == core.PrincipalPerson && x.se > core.SERead && !fpExempt(x) {
		return e.teammateFloor(ctx, x, rule), true
	}
	return core.Decision{}, false
}

// fpExempt: the teammate's own memory, and checking out a repository Bothy
// may read without discarding anything (local_write; a discarding checkout
// is irreversible), copy nothing out and lose nothing.
func fpExempt(x *input) bool {
	return x.a.Tool == toolMemory || x.a.Tool == toolRepoCheckout && x.se == core.SELocalWrite
}

// teammateFloor is FP: everything above read that Bothy does for a person
// in a chat app waits for an approver, unless an earlier approval's grant
// covers it. A deny rule still denies; a rule auto never lifts it.
func (e *Engine) teammateFloor(ctx context.Context, x *input, rule *config.Rule) core.Decision {
	if rule != nil && core.Verdict(rule.Verdict) == core.VerdictDeny {
		return ruleDenied(rule)
	}
	if g := e.useGrant(ctx, x); g != nil {
		return allowed("grant:"+string(g.ID), "allowed by an earlier approval")
	}
	d := e.ask(x, askSpec{rule: floorTeammate, reason: teammateReason, floor: true, grants: true})
	if kind, target, first := e.firstContact(ctx, x); first {
		d.Warnings = append(d.Warnings, contactWarning(x.a, x.Principal, kind, target))
	}
	return d
}

const taintedMemoryDenied = "Bothy can't remember new things in this chat because it has seen content from outside your team"

// adminReason is F3's reason as the approval card's "Why Bothy asked" shows it.
func adminReason(a *core.Action) string {
	if isTeamMemory(a) {
		return "changes to what Bothy remembers for everyone need an admin's approval"
	}
	switch a.Tool {
	case toolScheduleNew:
		return "creating a schedule needs an admin's approval"
	case toolScheduleDel:
		return "deleting a schedule needs an admin's approval"
	}
	return "changes to how Bothy works need an admin's approval"
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
	return e.ask(x, askSpec{rule: floorTrifecta, reason: x.trifectaReason(), floor: true})
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
		return denied("default:"+class, "Bothy isn't allowed to "+classPhrase(x.se)+" on this server", false)
	}
	if kind, target, first := e.firstContact(ctx, x); first {
		return e.ask(x, askSpec{
			rule: "first_contact:" + kind, reason: "first contact with " + target, grants: true,
			warnings: []string{contactWarning(x.a, x.Principal, kind, target)},
		})
	}
	// The reason reaches people (the card's "Why Bothy asked", the task),
	// so it names the class and the preset as Settings → Security does.
	if v == core.VerdictAsk {
		return e.ask(x, askSpec{rule: "default:" + class, reason: "the " + presetTitle(x.cfg) + " safety setting asks before Bothy can " + classPhrase(x.se), grants: true})
	}
	return allowed("default:"+class, "the "+presetTitle(x.cfg)+" safety setting lets Bothy "+classPhrase(x.se)+" without asking")
}

// presetTitle is a preset's name as Settings → Security shows it.
func presetTitle(c *config.Config) string {
	switch p := presetName(c); p {
	case "hands_off":
		return "Hands-off"
	default:
		return strings.ToUpper(p[:1]) + p[1:]
	}
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
			return "recipient", unknownRecipient, true
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
			return "domain", unknownHost, true
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
	switch {
	case s.admin:
		d.Approver = core.ApproverReq{AdminOnly: true}
	case x.Principal.TeamOwned():
		d.Approver = core.ApproverReq{Team: true}
	default:
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
	if x.exposed() && !x.staysLocal() {
		d.Warnings = append(d.Warnings, warnExposed)
	}
	if x.Provenance == core.ProvUntrusted {
		d.Warnings = append(d.Warnings, warnUntrusted)
	}
	if isTeamMemory(x.a) && x.Taint.Has(core.TaintUntrusted) && !x.exposed() {
		d.Warnings = append(d.Warnings, warnTeamOutside)
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
	scope, id := grantScopeOf(x.Trigger, x.Session, x.Schedule)
	_, ok := narrowGrant(x.cfg, x.a, x.Principal.ID, scope, id, core.Decider{User: x.User}, time.Time{})
	return ok
}

// grantScopeOf is what allow_session covers: the chat, or for a scheduled
// run the schedule, whose next run starts in a new chat.
func grantScopeOf(t core.Trigger, s core.SessionID, sc core.ScheduleID) (core.GrantScope, string) {
	if t.Scheduled() && sc != "" {
		return core.GrantSchedule, string(sc)
	}
	return core.GrantSession, string(s)
}

// alwaysGrantOK: allow_always is offered exactly when allow_session is,
// except for irreversible actions and non-interactive triggers, whose
// persistent grants would be created without a human looking at a card in
// context. narrowGrant refuses shared-tenant hosts at principal scope.
func (e *Engine) alwaysGrantOK(x *input) bool {
	if x.se == core.SEIrreversible || x.Trigger.Scheduled() || x.Trigger == core.TrigAPI {
		return false
	}
	_, ok := narrowGrant(x.cfg, x.a, x.Principal.ID, core.GrantPrincipal, "", core.Decider{User: x.User}, time.Time{})
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

// ruleDenied keeps the rule's id in Decision.Rule only: the reason is shown
// under the blocked step, where ids don't belong.
func ruleDenied(r *config.Rule) core.Decision {
	return denied("rule:"+r.ID, cmp.Or(r.Reason, "a rule an admin set doesn't allow this"), false)
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

// triggerPlace finishes "Bothy can't … " with where the task came from.
func triggerPlace(t core.Trigger) string {
	switch t {
	case core.TrigMessage:
		return "from a chat"
	case core.TrigSchedule:
		return "from a schedule"
	case core.TrigHeartbeat:
		return "during a check-in"
	case core.TrigAPI:
		return "from an API request"
	}
	return "in this task"
}

// roleOnly names who may use a tool that needs role r.
func roleOnly(r core.Role) string {
	switch r {
	case core.RoleOwner:
		return "only the owner"
	case core.RoleAdmin:
		return "only admins"
	}
	return "only people on the team"
}

// classPhrase finishes "Bothy isn't allowed to … " for a side-effect class.
func classPhrase(se core.SideEffect) string {
	switch se {
	case core.SERead:
		return "read or look things up"
	case core.SELocalWrite:
		return "change files"
	case core.SEExternalSend:
		return "send data out"
	case core.SEIrreversible:
		return "do things that can't be undone"
	}
	return "do this"
}

func (x *input) askPhrase() string { return askPhrase(x.Spec, x.a, x.payload, x.se) }

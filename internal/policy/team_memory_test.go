package policy

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

const teamReason = "changes to what Bothy remembers for everyone need an admin's approval"

func teamMemoryAction(se core.SideEffect) *core.Action {
	return action("memory", core.ActMemory, se, map[string]any{"file": "TEAM", "op": "add", "new": "Deploy day is Thursday"})
}

// memorySpec is the memory tool's manifest: not offered to restricted channels.
func memorySpec() core.ToolSpec {
	return spec("memory", core.SELocalWrite, func(m *core.Manifest) { m.Title, m.SpaceOK = "Update memory", false })
}

func wantTeamAsk(t *testing.T, who string, d core.Decision) {
	t.Helper()
	if d.Verdict != core.VerdictAsk || d.Rule != "floor:admin" || !d.Floor || d.Reason != teamReason ||
		!reflect.DeepEqual(d.Approver, core.ApproverReq{AdminOnly: true}) || !slices.Equal(d.Choices, []core.Choice{core.AllowOnce, core.Deny}) {
		t.Errorf("%s: %+v, want an admin's approval, once or deny", who, d)
	}
}

// TestTeamMemoryIsAdminClass: what everyone's chats read is changed only
// with an admin's approval, whoever asks and from wherever a person asks.
func TestTeamMemoryIsAdminClass(t *testing.T) {
	h := newHarness(t, nil)
	a, s := teamMemoryAction(core.SEAdmin), memorySpec()

	wantTeamAsk(t, "the owner", h.decide(h.in(a, s)))
	member := h.in(a, s)
	member.User, member.Role, member.Principal, member.Session = h.member.ID, core.RoleMember, h.memberP, h.memberSession
	wantTeamAsk(t, "a member", h.decide(member))
	maya, sess := h.teammate("slack", "U1", "Maya Chen", true)
	wantTeamAsk(t, "a teammate", h.decide(personIn(maya, sess, a, s)))
	standard := h.space
	standard.ToolProfile = "standard"
	ch := core.PolicyInput{Principal: standard, Session: h.spaceSession, Trigger: core.TrigMessage, Action: a, Spec: s, Provenance: core.ProvUser}
	wantTeamAsk(t, "a standard channel", h.decide(ch))

	ch.Principal = h.space
	if d := h.decide(ch); d.Verdict != core.VerdictDeny || d.Rule != "floor:unavailable" {
		t.Errorf("a restricted channel: %+v", d)
	}
	// An Action that understates its class is still decided as admin.
	wantTeamAsk(t, "a local_write action", h.decide(h.in(teamMemoryAction(core.SELocalWrite), s)))

	for trig, place := range map[core.Trigger]string{core.TrigSchedule: "from a schedule", core.TrigHeartbeat: "during a check-in", core.TrigAPI: "from an API request"} {
		in := h.in(a, s)
		in.Trigger = trig
		if d := h.decide(in); d.Verdict != core.VerdictDeny || d.Rule != "floor:admin" ||
			d.Reason != "Bothy can't change what it remembers for everyone "+place {
			t.Errorf("%s: %+v", trig, d)
		}
	}
	// Personal memory stays the owner's own local write.
	personal := action("memory", core.ActMemory, core.SELocalWrite, map[string]any{"file": "MEMORY", "op": "add", "new": "x"})
	if d := h.decide(h.in(personal, s)); d.Verdict != core.VerdictAuto {
		t.Errorf("personal memory: %+v", d)
	}
}

func TestTeamMemoryRulesAndGrants(t *testing.T) {
	a, s := teamMemoryAction(core.SEAdmin), memorySpec()
	h := newHarness(t, func(c *config.Config) {
		c.Policy.Rules = []config.Rule{{ID: "mem-ok", Tool: "memory", Verdict: "auto"}}
	})
	h.grant(core.Grant{Principal: h.ownerP.ID, Scope: core.GrantPrincipal, Kind: core.GrantTool, Tool: "memory",
		Pattern: map[string]string{"/op": "add"}, MaxSE: core.SELocalWrite})
	wantTeamAsk(t, "an auto rule and a grant", h.decide(h.in(a, s)))

	h = newHarness(t, func(c *config.Config) {
		c.Policy.Rules = []config.Rule{{ID: "no-mem", Tool: "memory", Verdict: "deny"}}
	})
	if d := h.decide(h.in(a, s)); d.Verdict != core.VerdictDeny || d.Rule != "rule:no-mem" {
		t.Errorf("a deny rule: %+v", d)
	}
}

func TestTeamMemoryWarnings(t *testing.T) {
	a, s := teamMemoryAction(core.SEAdmin), memorySpec()
	h := newHarness(t, nil)
	in := h.in(a, s)
	in.Taint = core.TaintUntrusted
	d := h.decide(in)
	wantTeamAsk(t, "outside content", d)
	if !slices.Equal(d.Warnings, []string{warnTeamOutside}) {
		t.Errorf("outside content warnings = %q", d.Warnings)
	}
	in.Taint = core.TaintUntrusted | core.TaintPrivate
	if d := h.decide(in); !slices.Equal(d.Warnings, []string{warnExposed}) {
		t.Errorf("exposed warnings = %q", d.Warnings)
	}
	in.Taint, in.Provenance = 0, core.ProvUntrusted
	if d := h.decide(in); !slices.Equal(d.Warnings, []string{warnUntrusted}) {
		t.Errorf("untrusted origin warnings = %q", d.Warnings)
	}

	h = newHarness(t, func(c *config.Config) { c.Memory.TaintedWrites = "deny" })
	in = h.in(a, s)
	in.Taint = core.TaintUntrusted
	if d := h.decide(in); d.Verdict != core.VerdictDeny || d.Rule != "floor:tainted_memory" {
		t.Errorf("tainted_writes deny: %+v", d)
	}
}

// TestTeamMemoryApproval: the card says what everyone's chats will read,
// and only an admin on the web may approve it, once.
func TestTeamMemoryApproval(t *testing.T) {
	h := newHarness(t, nil)
	maya, sess := h.teammate("slack", "U1", "Maya Chen", false)
	_, _ = h.teammate("slack", "U9", "Sam", true)
	a, s := teamMemoryAction(core.SEAdmin), memorySpec()
	d := h.decide(personIn(maya, sess, a, s))
	run := h.newRun(sess, maya.ID, "")
	chat := core.ChatRef{Channel: "slack", ChatID: "U1", Kind: core.ChatDM}
	ap, err := h.ap.Request(h.ctx, core.ApprovalRequest{Run: run, Session: sess, Principal: maya.ID, ToolCallID: core.NewID(core.PrefixToolCall),
		Action: a, Spec: s, Decision: d, Chat: &chat, Sender: "slack:U1"})
	if err != nil {
		t.Fatal(err)
	}
	c := ap.Card
	if c.Question != "Remember this for everyone?" || c.Consequence != "Bothy will use this in every chat, for everyone." ||
		c.PrimaryLabel != "Remember for everyone" || c.Risk != core.SEAdmin || c.Code != "" || c.Tone != core.ToneDefault ||
		len(c.Fields) != 1 || c.Fields[0] != (core.CardField{Label: "Note", Value: "Deploy day is Thursday"}) {
		t.Errorf("card = %+v", c)
	}
	if !slices.Equal(ap.Decision.Choices, []core.Choice{core.AllowOnce, core.Deny}) {
		t.Errorf("choices = %v", ap.Decision.Choices)
	}
	press := core.Resolution{ID: ap.ID, Choice: core.AllowOnce, ByIdentity: "slack:U9", Via: "slack", ViaMsg: h.cardRef(ap, "slack", "U9")}
	if _, err := h.ap.Resolve(h.ctx, press); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("a chat approver approving: %v", err)
	}
	if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.AllowOnce, By: h.member.ID, Via: "web"}); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("a member approving: %v", err)
	}
	if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.AllowAlways, By: h.owner.ID, Via: "web"}); err == nil {
		t.Errorf("an admin always-allowing: %v", err)
	}
	got, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.AllowOnce, By: h.owner.ID, Via: "web"})
	if err != nil || got.Status != core.ApprovalAllowed || got.DecidedBy != h.owner.ID {
		t.Fatalf("the owner approving: %+v, %v", got, err)
	}
}

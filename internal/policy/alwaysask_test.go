package policy

import (
	"slices"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func TestAlwaysAskFloor(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Policy.Preset = "hands_off"
		c.Policy.Rules = []config.Rule{
			{ID: "no-evil", Tool: "repo_open_pr", Match: map[string]string{"/to": "github:evil/*"}, Verdict: "deny"},
			{ID: "auto-pr", Tool: "repo_open_pr", Verdict: "auto"},
		}
	})
	s := spec("repo_open_pr", core.SEExternalSend, func(m *core.Manifest) { m.AlwaysAsk, m.Title = true, "Open pull request" })
	a := sendAction("repo_open_pr", "github:octocat/hello", core.SEExternalSend)

	d := h.decide(h.in(a, s))
	if d.Verdict != core.VerdictAsk || d.Rule != "floor:always_ask" || !d.Floor {
		t.Fatalf("hands_off and an auto rule must still ask: %+v", d)
	}
	if !slices.Equal(d.Choices, []core.Choice{core.AllowOnce, core.Deny}) {
		t.Errorf("choices %v, want allow_once and deny only", d.Choices)
	}
	if !strings.Contains(d.Reason, "Open pull request") {
		t.Errorf("reason %q", d.Reason)
	}
	if d := h.decide(h.in(sendAction("repo_open_pr", "github:evil/x", core.SEExternalSend), s)); d.Verdict != core.VerdictDeny || d.Rule != "rule:no-evil" {
		t.Errorf("a deny rule still denies: %+v", d)
	}

	// No grant answers it, an AllowWhenTainted recipient grant included.
	h.grant(core.Grant{Principal: h.ownerP.ID, Scope: core.GrantSession, ScopeID: string(h.session), Kind: core.GrantRecipient,
		Value: "github:octocat/hello", MaxSE: core.SEExternalSend})
	h.grant(core.Grant{Principal: h.ownerP.ID, Scope: core.GrantPrincipal, Kind: core.GrantRecipient,
		Value: "github:octocat/hello", MaxSE: core.SEExternalSend, AllowWhenTainted: true})
	in := h.in(a, s)
	if d := h.decide(in); d.Verdict != core.VerdictAsk {
		t.Errorf("grant answered an always-ask tool: %+v", d)
	}
	in.Taint = core.TaintPrivate | core.TaintUntrusted
	if d := h.decide(in); d.Verdict != core.VerdictAsk || hasChoice(d, core.AllowSession) {
		t.Errorf("exposed: %+v", d)
	}
}

func TestAlwaysAskFirstContactWarning(t *testing.T) {
	h := newHarness(t, nil)
	s := spec("repo_open_pr", core.SEExternalSend, func(m *core.Manifest) { m.AlwaysAsk = true })
	a := sendAction("repo_open_pr", "github:octocat/hello", core.SEExternalSend)
	d := h.decide(h.in(a, s))
	if d.Rule != "floor:always_ask" || !slices.Contains(d.Warnings, "This is the first pull request Bothy opens in octocat/hello for you.") {
		t.Fatalf("first contact: %+v", d)
	}
	run := h.newRun(h.session, h.ownerP.ID, h.owner.ID)
	ap, err := h.ap.Request(h.ctx, core.ApprovalRequest{Run: run, Session: h.session, Principal: h.ownerP.ID, Action: a, Spec: s, Decision: d})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.AllowOnce, By: h.owner.ID, Via: "web"}); err != nil {
		t.Fatal(err)
	}
	d = h.decide(h.in(a, s))
	if d.Verdict != core.VerdictAsk || slices.ContainsFunc(d.Warnings, func(w string) bool {
		return strings.Contains(w, "first pull request") || strings.Contains(w, " before.")
	}) {
		t.Errorf("seen repo: %+v, want a card without the first-contact warning", d)
	}
}

// TestAlwaysAskFirstContactUnderEarlierFloor: an exposed session asks at the
// trifecta floor before the always-ask floor; the card still says it is
// the first contact with the repository.
func TestAlwaysAskFirstContactUnderEarlierFloor(t *testing.T) {
	h := newHarness(t, nil)
	s := spec("repo_open_pr", core.SEExternalSend, func(m *core.Manifest) { m.AlwaysAsk = true })
	in := h.in(sendAction("repo_open_pr", "github:octocat/hello", core.SEExternalSend), s)
	in.Taint = core.TaintPrivate | core.TaintUntrusted
	d := h.decide(in)
	if d.Verdict != core.VerdictAsk || d.Rule != floorTrifecta || !slices.Contains(d.Warnings, "This is the first pull request Bothy opens in octocat/hello for you.") {
		t.Fatalf("exposed always-ask: %+v", d)
	}
	other := h.in(sendAction("send", "github:octocat/hello", core.SEExternalSend), spec("send", core.SEExternalSend))
	other.Taint = in.Taint
	if d := h.decide(other); d.Rule != floorTrifecta || slices.ContainsFunc(d.Warnings, func(w string) bool {
		return strings.Contains(w, "first pull request") || strings.Contains(w, " before.")
	}) {
		t.Errorf("other tools keep their trifecta card: %+v", d)
	}
}

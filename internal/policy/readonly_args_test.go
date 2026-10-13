package policy

import (
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// find and rg are in the default read-only shell rule, but some of their
// flags delete or write files or run other programs; such a command must
// fall through to the defaults instead of riding on the auto rule.
func TestReadonlyShellRefusesWritingAndExecutingFlags(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Policy.Defaults = map[string]string{"local_write": "ask"}
	})
	s := spec("shell", core.SELocalWrite, func(m *core.Manifest) { m.Exec = true })
	for _, cmd := range []string{
		"find . -delete",
		"find . -name x -exec rm {} +",
		"find . -execdir rm x",
		"find . -ok rm",
		"find . -okdir rm",
		"find / -fprint out",
		"find / -fprint0 out",
		"find / -fprintf out %p",
		"find / -fls out",
		"find . -de'le'te",
		`find . -del\ete`,
		`find . "-delete"`,
		"rg --pre=sh x payload.sh",
		"rg --pre sh x",
		"rg --pre-glob '*.sh' --pre sh x",
		"rg -z x",
		"rg -iz x",
		"rg --search-zip x",
		"rg --hostname-bin=sh x",
		"/usr/bin/find . -delete",
	} {
		if d := h.decide(h.in(shellAction(cmd), s)); d.Verdict == core.VerdictAuto {
			t.Errorf("%q: auto via %s", cmd, d.Rule)
		}
	}
	argv := []string{"find", ".", "-delete"}
	raw := action("shell", core.ActExec, core.SELocalWrite, map[string]any{"argv": argv}, func(a *core.Action) { a.Argv = argv })
	if d := h.decide(h.in(raw, s)); d.Verdict == core.VerdictAuto {
		t.Errorf("argv %v: auto via %s", argv, d.Rule)
	}
	for _, cmd := range []string{"find . -type f -newer go.mod", "find src -name x.go", "rg -n TODO", "rg --hidden -g x foo", "grep -rn delete ."} {
		if d := h.decide(h.in(shellAction(cmd), s)); d.Verdict != core.VerdictAuto || d.Rule != "rule:readonly-shell" {
			t.Errorf("%q: got %s %s, want auto by readonly-shell", cmd, d.Verdict, d.Rule)
		}
	}
	// A tightening rule naming find still catches the dangerous form.
	h2 := newHarness(t, func(c *config.Config) {
		c.Policy.Rules = []config.Rule{{ID: "find-ask", Tool: "shell", Argv0: []string{"find"}, Verdict: "deny"}}
	})
	if d := h2.decide(h2.in(shellAction("find . -delete"), s)); d.Rule != "rule:find-ask" {
		t.Errorf("tightening rule: %+v", d)
	}
}

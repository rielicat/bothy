package policy

import (
	"slices"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func TestDeletesFiles(t *testing.T) {
	deleting := []string{
		"rm telegram-test.txt && ls -la",
		"rm x",
		"ls; rm -rf x",
		"ls\nrm -rf x",
		"rmdir old",
		"unlink a.txt",
		"shred -u secret.txt",
		"truncate -s 0 log.txt",
		"/bin/rm x",
		"'rm' x",
		`r\m x`,
		`r''m x`,
		`$'rm' x`,
		"FOO=1 rm x",
		"2>/dev/null rm x",
		"ls &>/dev/null && rm x",
		"{ rm x; }",
		"(cd build && rm -f a.o)",
		"if [ -f x ]; then rm x; fi",
		`for f in a b; do rm "$f"; done`,
		"function tidy { rm x; }",
		"# clean up\nrm x",
		"find . -name '*.tmp' -delete",
		"find . -name x -exec rm {} +",
		`find . -exec sh -c 'rm "$1"' _ {} \;`,
		"timeout 5 find . -delete",
		"ls | xargs rm",
		"ls | xargs -I {} sh -c 'rm {}'",
		"sudo rm -rf /tmp/x",
		`echo y | sudo -u bob find /tmp -exec rm {} \;`,
		"nohup sh -c 'rm x' &",
		"bash -c 'rm x'",
		`sh -lc "rm -f x"`,
		"bash -o pipefail -c 'rm x'",
		"eval 'rm x'",
		`echo "$(rm x)"`,
		"echo `rm x`",
		"echo $(rm x)",
		"bash <<'EOF'\nrm -rf x\nEOF",
		"bash <<< 'rm x'",
		// The quote in the document's body does not hide the command after it.
		"cat <<'EOF' > a.txt\n'\nEOF\nrm -rf x",
		"cat <<-EOF > a.txt\n\tdon't\n\tEOF\nrm x",
		// Here-documents whose substitutions run, and scripts piped into a shell.
		"cat <<EOF > out.txt\n$(rm -rf build)\nEOF",
		"cat <<EOF | bash\nrm x\nEOF",
		"echo 'rm -rf x' | sh",
		"printf 'rm x' | bash -s",
		"ls $(rm x) /tmp",
		"xargs -n 1 rm < list.txt",
		"sudo -u bob rm x",
		"git clean -fdx",
		"git -C repo rm notes.md",
		"env -i git clean -f",
		"rsync -a --delete src/ dst/",
	}
	for _, cmd := range deleting {
		if !deletesFiles([]string{"bash", "-lc", cmd}) {
			t.Errorf("%q: not seen as deleting", cmd)
		}
	}
	keeping := []string{
		"ls -la",
		"grep -rn rm .",
		"echo 'rm -rf /'",
		`echo "rm x"`,
		`git commit -m "rm old files"`,
		"cat notes.txt > rm",
		"echo hi > a.txt",
		"mv a.txt b.txt",
		"cp a.txt b.txt",
		"git reset --hard",
		"git clean -n",
		"git clean -fdn",
		"git rm --cached x",
		// A delete program's name as a runner's argument is not its command.
		"find . -name '*.go' | xargs grep -n unlink",
		"timeout 10 rg -n truncate src/",
		"time grep -c rmdir log",
		"command -v rm",
		"env GIT_DIR=x git rm --cached foo",
		"nice -n 19 grep -r shred .",
		"ls -la $(pwd)/rm",
		"ls $(pwd) /tmp/rm",
		"cat <<'EOF' > out.txt\n$(rm -rf build)\nEOF",
		"ls | sh -c 'wc -l'",
		"cat script.sh | bash",
		"git status",
		"find . -name '*.go'",
		"find . -exec grep -l x {} +",
		"rsync -a src/ dst/",
		"cat <<'EOF' > tidy.sh\nrm -rf build\nEOF",
		"# rm x",
		"python3 -c 'print(1)'",
		"bash tidy.sh",
		"",
	}
	for _, cmd := range keeping {
		if deletesFiles([]string{"bash", "-lc", cmd}) {
			t.Errorf("%q: seen as deleting", cmd)
		}
	}
	for _, argv := range [][]string{{"rm", "x"}, {"/usr/bin/find", ".", "-delete"}, {"env", "rm", "x"}, {"sh", "-c", "rm x"}} {
		if !deletesFiles(argv) {
			t.Errorf("argv %q: not seen as deleting", argv)
		}
	}
	if deletesFiles([]string{"git", "status"}) || deletesFiles(nil) {
		t.Error("git status or an empty argv seen as deleting")
	}
}

// TestDeletesFilesNesting: a script nested too deep to check counts as
// deleting, a shallow one is read through.
func TestDeletesFilesNesting(t *testing.T) {
	nest := func(cmd string, n int) string {
		for range n {
			cmd = "bash -c " + shellQuote(cmd)
		}
		return cmd
	}
	if deletesFiles([]string{"bash", "-lc", nest("ls", 3)}) || !deletesFiles([]string{"bash", "-lc", nest("rm x", 3)}) {
		t.Error("three levels are read through")
	}
	if !deletesFiles([]string{"bash", "-lc", nest("ls", maxScriptDepth+2)}) {
		t.Error("a script nested too deep must count as deleting")
	}
	long := "find . " + strings.Repeat("-exec find ", 20000)
	if !deletesFiles([]string{"bash", "-lc", long}) {
		t.Error("deeply nested find -exec must count as deleting")
	}
}

// TestShellDeleteIsIrreversible: a shell command that deletes files meets
// the same class default as fs_delete in every preset, offers no lasting
// grant, and its card says it can't be undone.
func TestShellDeleteIsIrreversible(t *testing.T) {
	s := spec("shell", core.SELocalWrite, func(m *core.Manifest) { m.Exec = true })
	rm := shellAction("rm telegram-test.txt && ls -la")
	for _, preset := range allPresets {
		h := newHarness(t, func(c *config.Config) { c.Policy.Preset = preset })
		d := h.decide(h.in(rm, s))
		if d.Verdict != core.VerdictAsk || d.Rule != "default:irreversible" || hasChoice(d, core.AllowAlways) || !hasChoice(d, core.AllowSession) {
			t.Errorf("%s: %+v", preset, d)
		}
		if d := h.decide(h.in(shellAction("ls -la && cat a.txt"), s)); d.Verdict != core.VerdictAuto {
			t.Errorf("%s: a command that deletes nothing asks: %+v", preset, d)
		}
	}

	h := newHarness(t, func(c *config.Config) {
		c.Policy.Rules = []config.Rule{{ID: "shell-ok", Tool: "shell", SideEffect: "local_write", Verdict: "auto"}}
	})
	if d := h.decide(h.in(rm, s)); d.Rule != "default:irreversible" {
		t.Errorf("a local_write rule matched a deleting command: %+v", d)
	}
	// A grant made for a local_write class does not cover it.
	h.grant(core.Grant{Principal: h.ownerP.ID, Scope: core.GrantSession, ScopeID: string(h.session), Kind: core.GrantTool, Tool: "shell",
		Pattern: map[string]string{"/argv/0": "bash", "/argv": globEscape(`["bash","-lc","rm telegram-test.txt && ls -la"]`)}, MaxSE: core.SELocalWrite})
	if d := h.decide(h.in(rm, s)); d.Verdict != core.VerdictAsk {
		t.Errorf("a local_write grant answered a deleting command: %+v", d)
	}
	g := narrowOf(t, h, rm, core.GrantSession, string(h.session))
	if g.MaxSE != core.SEIrreversible {
		t.Errorf("session grant ceiling %s, want irreversible", g.MaxSE)
	}
	if _, ok := h.engine.NarrowGrant(rm, h.ownerP.ID, core.GrantPrincipal, "", h.owner.ID); ok {
		t.Error("an always grant was built for a deleting command")
	}
	choices, err := h.ap.offered(h.ctx, core.ApprovalRequest{Action: rm, Spec: s,
		Decision: core.Decision{Choices: []core.Choice{core.AllowOnce, core.AllowSession, core.AllowAlways, core.Deny}}})
	if err != nil || slices.Contains(choices, core.AllowAlways) {
		t.Errorf("offered %v %v, want no allow_always", choices, err)
	}

	c := cardFor(rm, s)
	if c.Risk != core.SEIrreversible || c.Consequence != cantUndo || c.Tone != core.ToneDestructive || field(c, "Command").Value != "rm telegram-test.txt && ls -la" {
		t.Errorf("card: %+v", c)
	}

	in := h.in(rm, s)
	in.Taint = core.TaintPrivate | core.TaintUntrusted
	if d := h.decide(in); d.Rule != floorTrifecta || d.Reason != trifectaUndoneReason {
		t.Errorf("exposed: %+v", d)
	}
}

// TestExposedWarningOnlyWhereDataCanLeave: the "before letting anything
// leave" line is for actions that can send data out, not for deleting or
// replacing Bothy's own files.
func TestExposedWarningOnlyWhereDataCanLeave(t *testing.T) {
	h := newHarness(t, nil)
	exposed := core.TaintPrivate | core.TaintUntrusted
	discard := action("repo_checkout", core.ActFSWrite, core.SEIrreversible, map[string]any{"repo": "acme/web", "ref": "main", "discard": "abc"},
		func(a *core.Action) { a.Paths = []string{"repos/acme/web"} })
	del := action("fs_delete", core.ActFSDelete, core.SEIrreversible, map[string]any{"path": "a.txt"}, func(a *core.Action) { a.Paths = []string{"a.txt"} })
	gone := action("mcp__linear__delete_issue", core.ActMCP, core.SEIrreversible, map[string]any{"id": "ENG-1"})
	for _, tc := range []struct {
		a      *core.Action
		s      core.ToolSpec
		reason string
		warned bool
	}{
		{discard, spec("repo_checkout", core.SELocalWrite), trifectaUndoneReason, false},
		{del, spec("fs_delete", core.SEIrreversible), trifectaUndoneReason, false},
		{gone, spec("mcp__linear__delete_issue", core.SEIrreversible), trifectaUndoneReason, true},
		{sendAction("send_mail", "bob@example.com", core.SEExternalSend), spec("send_mail", core.SEExternalSend), trifectaReason, true},
		// The box can reach allowed websites from a command.
		{shellAction("rm a.txt"), spec("shell", core.SELocalWrite), trifectaUndoneReason, true},
	} {
		in := h.in(tc.a, tc.s)
		in.Taint = exposed
		d := h.decide(in)
		if d.Verdict != core.VerdictAsk || d.Rule != floorTrifecta || d.Reason != tc.reason || slices.Contains(d.Warnings, warnExposed) != tc.warned {
			t.Errorf("%s: %+v, want reason %q and the warning %v", tc.a.Tool, d, tc.reason, tc.warned)
		}
	}
}

// TestFirstPullRequestWarningScope: first contact is per principal, and the
// warning says whose first pull request it is.
func TestFirstPullRequestWarningScope(t *testing.T) {
	h := newHarness(t, nil)
	s := spec("repo_open_pr", core.SEExternalSend, func(m *core.Manifest) { m.AlwaysAsk = true })
	a := sendAction("repo_open_pr", "github:acme/web", core.SEExternalSend)
	if d := h.decide(h.in(a, s)); !slices.Contains(d.Warnings, "This is the first pull request Bothy opens in acme/web for you.") {
		t.Errorf("user: %v", d.Warnings)
	}
	space := h.space
	space.ToolProfile = "standard"
	in := h.in(a, s)
	in.Principal, in.Session = space, h.spaceSession
	if d := h.decide(in); !slices.Contains(d.Warnings, "This is the first pull request Bothy opens in acme/web for this group chat.") {
		t.Errorf("group: %+v", d)
	}
}

package knowledge

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/conformance"
)

func (h *harness) tool(name string) core.Tool {
	h.t.Helper()
	for _, t := range h.svc.Tools() {
		if t.Spec().Def.Name == name {
			return t
		}
	}
	h.t.Fatalf("no tool %s", name)
	return nil
}

func (h *harness) planExec(tool core.Tool, env *core.ToolEnv, args string) (*core.Action, *core.ToolResult) {
	h.t.Helper()
	a, err := tool.Plan(h.ctx, env, json.RawMessage(args))
	if err != nil {
		h.t.Fatalf("Plan(%s): %v", args, err)
	}
	res, err := tool.Exec(h.ctx, env, a)
	if err != nil {
		h.t.Fatalf("Exec(%s): %v", args, err)
	}
	return a, res
}

func TestToolConformance(t *testing.T) {
	h := newHarness(t)
	h.writeRepo()
	h.index("repos/acme/api", "acme/api", "0123456789abcdef")
	env := h.env(h.session(h.p.ID))
	conformance.Tool(t, h.tool(searchToolName), []conformance.ToolCase{
		{Name: "query", Env: env, Args: json.RawMessage(`{"query":"rate limiter"}`)},
		{Name: "repo", Env: env, Args: json.RawMessage(`{"query":"Allow","repo":"Acme/API","limit":3}`)},
		{Name: "empty", Env: env, Args: json.RawMessage(`{"query":"  "}`), WantErr: true},
		{Name: "only punctuation", Env: env, Args: json.RawMessage(`{"query":"\"()*"}`), WantErr: true},
		{Name: "unknown repo", Env: env, Args: json.RawMessage(`{"query":"x","repo":"acme/none"}`), WantErr: true},
		{Name: "unknown field", Env: env, Args: json.RawMessage(`{"query":"x","path":"y"}`), WantErr: true},
	})
	conformance.Tool(t, h.tool(notesToolName), []conformance.ToolCase{
		{Name: "read", Env: env, Args: json.RawMessage(`{"repo":"acme/api","op":"read"}`)},
		{Name: "append", Env: env, Args: json.RawMessage(`{"repo":"acme/api","op":"append","section":"Conventions","text":"- gofmt"}`)},
		{Name: "replace missing", Env: env, Args: json.RawMessage(`{"repo":"acme/api","op":"replace","old":"nope","new":"x"}`), WantErr: true},
		{Name: "read with text", Env: env, Args: json.RawMessage(`{"repo":"acme/api","op":"read","text":"x"}`), WantErr: true},
		{Name: "bad op", Env: env, Args: json.RawMessage(`{"repo":"acme/api","op":"delete"}`), WantErr: true},
		{Name: "unknown repo", Env: env, Args: json.RawMessage(`{"repo":"evil/x","op":"read"}`), WantErr: true},
	})
}

func TestSearchToolResultsAndCitations(t *testing.T) {
	h := newHarness(t)
	h.writeRepo()
	r := h.index("repos/acme/api", "acme/api", "0123456789abcdef")
	sess := h.session(h.p.ID)
	env := h.env(sess)
	a, res := h.planExec(h.tool(searchToolName), env, `{"query":"rate limiter"}`)
	if a.Kind != core.ActSearch || a.SideEffect != core.SERead {
		t.Fatalf("action = %+v", a)
	}
	if !res.Untrusted || res.IsError {
		t.Fatalf("result flags = %+v", res)
	}
	for _, want := range []string{
		`1 results for "rate limiter"`,
		"[1] acme/api@0123456789ab:limit.go#L3-L6 (go func · func Allow() bool)",
		"3  // Allow takes a token from the rate limiter bucket.",
		"6  }",
	} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("result lacks %q:\n%s", want, res.Content)
		}
	}
	touches, _ := h.repo.Touched(h.ctx, h.viewer(), sess.ID)
	if len(touches) != 1 || touches[0].Repo != r.ID {
		t.Fatalf("search did not touch the repo: %+v", touches)
	}
	_, res = h.planExec(h.tool(searchToolName), env, `{"query":"quixotic"}`)
	if !strings.Contains(res.Content, "No indexed code or docs match") {
		t.Fatalf("empty result = %q", res.Content)
	}
}

func TestNotesToolEdits(t *testing.T) {
	h := newHarness(t)
	h.writeRepo()
	r := h.index("repos/acme/api", "acme/api", "")
	notes := h.tool(notesToolName)
	env := h.env(h.session(h.p.ID))

	_, res := h.planExec(notes, env, `{"repo":"acme/api","op":"read"}`)
	if !strings.Contains(res.Content, "no notes for acme/api yet") || res.IsError {
		t.Fatalf("read empty = %+v", res)
	}
	a, res := h.planExec(notes, env, `{"repo":"acme/api","op":"append","section":"## Conventions","text":"- Run gofmt."}`)
	if a.Kind != core.ActMemory || a.SideEffect != core.SELocalWrite || !strings.Contains(a.Diff, "+- Run gofmt.") ||
		!strings.Contains(a.Diff, "+## Conventions") {
		t.Fatalf("append action = %+v", a)
	}
	if res.IsError || !strings.Contains(res.Content, "Added to the notes for acme/api") {
		t.Fatalf("append result = %+v", res)
	}
	h.planExec(notes, env, `{"repo":"acme/api","op":"append","section":"conventions","text":"- Tabs."}`)
	h.planExec(notes, env, `{"repo":"acme/api","op":"append","section":"Decisions","text":"- SQLite only."}`)
	n, _ := h.svc.Notes(h.ctx, h.viewer(), r.ID)
	want := "# acme/api\n\n## Conventions\n- Run gofmt.\n- Tabs.\n\n## Decisions\n- SQLite only.\n"
	if n.Content != want || n.Untrusted {
		t.Fatalf("notes = %q (untrusted %v), want %q", n.Content, n.Untrusted, want)
	}

	tainted := *env
	tainted.Taint = core.TaintUntrusted
	a, res = h.planExec(notes, &tainted, `{"repo":"acme/api","op":"replace","old":"- Tabs.","new":"- Tabs, width 4."}`)
	if !strings.Contains(a.Diff, "-- Tabs.\n+- Tabs, width 4.\n") || res.IsError {
		t.Fatalf("replace = %+v %+v", a, res)
	}
	n, _ = h.svc.Notes(h.ctx, h.viewer(), r.ID)
	if !n.Untrusted || !strings.Contains(n.Content, "- Tabs, width 4.") {
		t.Fatalf("an edit under untrusted taint must mark the notes untrusted: %+v", n)
	}
	_, res = h.planExec(notes, env, `{"repo":"acme/api","op":"read"}`)
	if !res.Untrusted || !strings.Contains(res.Content, "SQLite only") {
		t.Fatalf("read of untrusted notes = %+v", res)
	}
	if _, err := h.svc.WriteNotes(h.ctx, h.viewer(), r.ID, n.Content, n.ETag, h.user.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ = h.svc.Notes(h.ctx, h.viewer(), r.ID); n.Untrusted {
		t.Fatal("a user save must make the notes trusted")
	}

	// Exec re-checks against the current file: a planned replace whose text
	// a user removed meanwhile fails cleanly.
	a, err := notes.Plan(h.ctx, env, json.RawMessage(`{"repo":"acme/api","op":"replace","old":"- SQLite only.","new":""}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.WriteNotes(h.ctx, h.viewer(), r.ID, "# acme/api\n", n.ETag, h.user.ID); err != nil {
		t.Fatal(err)
	}
	res, err = notes.Exec(h.ctx, env, a)
	if err != nil || !res.IsError || !strings.Contains(res.Content, "not found") {
		t.Fatalf("stale replace = %+v %v", res, err)
	}
	if strings.Count(strings.Join(h.audit.Actions(), ","), "knowledge.notes_write") != 4 {
		t.Fatalf("audit = %v", h.audit.Actions())
	}
}

func TestNotesToolCap(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Knowledge.NotesMaxBytes = 1024 })
	h.writeRepo()
	h.index("repos/acme/api", "acme/api", "")
	env := h.env(h.session(h.p.ID))
	args, _ := json.Marshal(map[string]string{"repo": "acme/api", "op": "append", "section": "Log", "text": strings.Repeat("y", 1100)})
	_, err := h.tool(notesToolName).Plan(h.ctx, env, args)
	if !errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), "exceed 1024 bytes") {
		t.Fatalf("over cap = %v", err)
	}
}

func TestToolGating(t *testing.T) {
	h := newHarness(t)
	env := h.env(h.session(h.p.ID))
	search := h.tool(searchToolName).(core.Gated)
	if search.Enabled(env) {
		t.Fatal("offered before anything was indexed")
	}
	h.write("proj/a.go", "package a\n")
	h.index("proj", "", "")
	if !search.Enabled(env) || !h.tool(notesToolName).(core.Gated).Enabled(env) {
		t.Fatal("not offered after an index")
	}
	bob := h.newUser("bob", core.RoleMember)
	other := *env
	other.Principal = *h.principal(bob)
	if search.Enabled(&other) {
		t.Fatal("offered to a principal without repos")
	}
	if _, err := h.tool(searchToolName).Plan(h.ctx, &other, json.RawMessage(`{"query":"x","repo":"local/proj"}`)); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("another principal resolved the repo: %v", err)
	}
}

//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// kWaitRepo polls a knowledge repo until cond holds.
func kWaitRepo(t *testing.T, u *pltUser, id int64, cond func(core.IndexedRepo) bool) core.IndexedRepo {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var r core.IndexedRepo
		code := u.JSON(t, http.MethodGet, "/api/knowledge/repos/"+strconv.FormatInt(id, 10), nil, &r)
		if code == http.StatusOK && cond(r) {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("knowledge repo %d: condition not met (last %d %+v)\nlogs:\n%s", id, code, r, u.g.Logs())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// kLastRequest is the newest raw mockllm request containing marker.
func kLastRequest(t *testing.T, llm *mockllm.Server, marker string) []byte {
	t.Helper()
	reqs := llm.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if bytes.Contains(reqs[i], []byte(marker)) {
			return reqs[i]
		}
	}
	t.Fatalf("mockllm saw no request containing %q", marker)
	return nil
}

// TestKnowledge drives F6 end to end: index a workspace directory over HTTP,
// the drafted notes (fast slot, untrusted), search with citations, the
// knowledge_search tool, the notes summary in the next turn-context, a notes
// edit under untrusted taint that needs approval, a user save that makes the
// notes trusted, and another user's 404s.
func TestKnowledge(t *testing.T) {
	llm := pltLLM(t,
		mockllm.Rule{MatchSystem: `^You keep short project notes`, Text: []string{"## Summary\nAcme API serves widgets.\n\n## Build and test\nmake test\n"}},
		mockllm.Rule{MatchUser: `K-SEARCH`, MatchToolResult: `.`, Text: []string{"Found it in limit.go."}},
		mockllm.Rule{MatchUser: `K-SEARCH`, ToolCalls: []core.ToolCall{pltCall("call_ks", "knowledge_search", `{"query":"rate limiter","repo":"acme/api"}`)}},
		mockllm.Rule{MatchUser: `K-NOTE`, MatchToolResult: `.`, Text: []string{"Noted."}},
		mockllm.Rule{MatchUser: `K-NOTE`, ToolCalls: []core.ToolCall{pltCall("call_kn", "notes",
			`{"repo":"acme/api","op":"append","section":"Conventions","text":"- Run make lint before pushing."}`)}},
		mockllm.Rule{MatchUser: `K-`, Text: []string{"Okay."}},
	)
	g := Start(t, Options{Config: pltYAML(llm.URL(), nil, "mode: multi\n")})
	owner := pltOwner(t, g)
	member := owner.CreateUser(t, "kmember")
	owner.Relogin(t)

	repo := filepath.Join(g.DataDir, "work", "ws", string(owner.Principal(t).Workspace), "repos", "acme", "api")
	for name, body := range map[string]string{
		"README.md":       "# Acme API\n\nServes widgets.\n",
		"Makefile":        "test:\n\tgo test ./...\n",
		"limit.go":        "package api\n\n// Allow takes a token from the rate limiter bucket.\nfunc Allow() bool {\n\treturn true\n}\n",
		".gitignore":      "secret.txt\n",
		"secret.txt":      "hunter2 rate limiter\n",
		"vendor/x/lib.go": "package x // rate limiter vendored\n",
	} {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o770); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o660); err != nil {
			t.Fatal(err)
		}
	}

	var queued core.IndexedRepo
	if code := owner.JSON(t, http.MethodPost, "/api/knowledge/repos",
		dto.KnowledgeIndexReq{Dir: "repos/acme/api", Repo: "acme/api", SHA: "0123456789abcdef"}, &queued); code != http.StatusAccepted {
		t.Fatalf("index = %d", code)
	}
	r := kWaitRepo(t, owner, queued.ID, func(r core.IndexedRepo) bool {
		return r.Status == core.IndexReady && r.Notes.Status == core.NotesReady
	})
	if r.Files != 4 || r.Repo != "acme/api" || !r.Notes.Untrusted {
		t.Fatalf("indexed repo = %+v", r)
	}
	id := strconv.FormatInt(r.ID, 10)
	var files dto.List[core.KnowledgeFile]
	if code := owner.JSON(t, http.MethodGet, "/api/knowledge/repos/"+id+"/files", nil, &files); code != http.StatusOK || len(files.Items) != 4 {
		t.Fatalf("files = %d %+v", code, files.Items)
	}

	var res dto.KnowledgeSearchResp
	if code := owner.JSON(t, http.MethodGet, "/api/knowledge/search?q="+url.QueryEscape("rate limiter"), nil, &res); code != http.StatusOK {
		t.Fatalf("search = %d", code)
	}
	if len(res.Items) != 1 || res.Items[0].Citation != "acme/api@0123456789ab:limit.go#L3-L6" {
		t.Fatalf("search = %+v", res.Items)
	}
	var notes core.KnowledgeNotes
	if code := owner.JSON(t, http.MethodGet, "/api/knowledge/repos/"+id+"/notes", nil, &notes); code != http.StatusOK {
		t.Fatalf("notes = %d", code)
	}
	if !strings.HasPrefix(notes.Content, "# acme/api\n") || !strings.Contains(notes.Content, "Acme API serves widgets.") || !notes.Untrusted {
		t.Fatalf("drafted notes = %+v", notes)
	}

	sess := owner.NewSession(t)
	d := owner.Finished(t, owner.Send(t, sess.ID, "K-SEARCH where is the limiter?"))
	if d.Run.Status != core.RunDone || len(d.ToolCalls) != 1 || d.ToolCalls[0].Tool != "knowledge_search" ||
		d.ToolCalls[0].Status != core.TCOK || !d.ToolCalls[0].Untrusted {
		t.Fatalf("search run = %+v %+v", d.Run, d.ToolCalls)
	}
	if req := string(kLastRequest(t, llm, "call_ks")); !strings.Contains(req, "acme/api@0123456789ab:limit.go#L3-L6") {
		t.Fatal("the tool result did not reach the model with its citation")
	}
	// Workspace content anyone may have authored: one search arms the
	// trifecta floor, as an fs_read of the same file plus a web fetch would.
	var sd dto.SessionDetail
	if code := owner.JSON(t, http.MethodGet, "/api/sessions/"+string(sess.ID), nil, &sd); code != http.StatusOK || !sd.Session.Taint.Exposed() {
		t.Fatalf("session taint after knowledge_search = %d %v, want Exposed", code, sd.Session.Taint)
	}
	owner.Finished(t, owner.Send(t, sess.ID, "K-NEXT anything else?"))
	next := string(kLastRequest(t, llm, "K-NEXT"))
	if !strings.Contains(next, "project notes for acme/api") || !strings.Contains(next, "Acme API serves widgets.") ||
		!strings.Contains(next, `source=\"project_notes\"`) {
		t.Fatalf("the next turn-context lacks the spotlighted notes:\n%s", next)
	}

	run := owner.Send(t, sess.ID, "K-NOTE remember the lint rule")
	ap := owner.WaitApproval(t, run, core.ApprovalPending)
	if ap.Decision.Rule != "floor:tainted_memory" || ap.Action == nil || !strings.Contains(ap.Action.Diff, "+- Run make lint") {
		t.Fatalf("approval = %+v", ap.Decision)
	}
	owner.Resolve(t, ap.ID, core.AllowOnce)
	if d := owner.Finished(t, run); d.Run.Status != core.RunDone {
		t.Fatalf("notes run = %+v", d.Run)
	}
	resp := owner.Do(t, http.MethodGet, "/api/knowledge/repos/"+id+"/notes", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("notes after edit = %d", resp.StatusCode)
	}
	if err := json.Unmarshal(resp.Body, &notes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notes.Content, "## Conventions\n- Run make lint before pushing.") || !notes.Untrusted {
		t.Fatalf("agent-edited notes = %+v", notes)
	}

	saved := owner.Do(t, http.MethodPut, "/api/knowledge/repos/"+id+"/notes", dto.KnowledgeNotesPutReq{Content: notes.Content + "\nReviewed.\n"},
		http.Header{"If-Match": {resp.Header.Get("ETag")}})
	if saved.StatusCode != http.StatusOK {
		t.Fatalf("save = %s %s", saved.Status, saved.Body)
	}
	if err := json.Unmarshal(saved.Body, &notes); err != nil || notes.Untrusted {
		t.Fatalf("saved notes = %+v %v", notes, err)
	}

	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, "/api/knowledge/repos/" + id},
		{http.MethodGet, "/api/knowledge/repos/" + id + "/files"},
		{http.MethodGet, "/api/knowledge/repos/" + id + "/notes"},
		{http.MethodPost, "/api/knowledge/repos/" + id + "/reindex"},
		{http.MethodPost, "/api/knowledge/repos/" + id + "/notes/draft"},
		{http.MethodDelete, "/api/knowledge/repos/" + id},
		{http.MethodGet, "/api/knowledge/search?q=rate&repo_id=" + id},
	} {
		if resp := member.Do(t, probe.method, probe.path, nil, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("member %s %s = %s", probe.method, probe.path, resp.Status)
		}
	}
	if code := member.JSON(t, http.MethodGet, "/api/knowledge/search?q=rate", nil, &res); code != http.StatusOK || len(res.Items) != 0 {
		t.Fatalf("member search = %d %+v", code, res.Items)
	}
	for _, action := range []string{"knowledge.index", "knowledge.notes_draft", "knowledge.notes_write", "knowledge.notes_edit"} {
		owner.WaitAudit(t, action, nil)
	}
}

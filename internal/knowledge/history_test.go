package knowledge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func TestNotesHistory(t *testing.T) {
	h := newHarness(t)
	h.writeRepo()
	r := h.index("repos/acme/api", "acme/api", "")
	if list, err := h.svc.NotesHistory(h.ctx, h.viewer(), r.ID); err != nil || len(list) != 0 {
		t.Fatalf("history before any notes = %+v %v", list, err)
	}

	if _, err := h.svc.WriteNotes(h.ctx, h.viewer(), r.ID, "# acme/api\n\nBuild with make.\n", "", h.user.ID); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Minute)
	env := h.env(h.session(h.p.ID))
	h.planExec(h.tool(notesToolName), env, `{"repo":"acme/api","op":"append","section":"Conventions","text":"- Run gofmt."}`)
	// Saving the same text again changes nothing, so it adds no entry.
	n, _ := h.svc.Notes(h.ctx, h.viewer(), r.ID)
	if _, err := h.svc.WriteNotes(h.ctx, h.viewer(), r.ID, n.Content, n.ETag, h.user.ID); err != nil {
		t.Fatal(err)
	}
	h.llm.Steps = []fakes.LLMStep{draftStep("## Summary\nThe acme API.\n")}
	if _, err := h.svc.DraftNotes(h.ctx, h.viewer(), r.ID, h.user.ID); err != nil {
		t.Fatal(err)
	}
	h.svc.bgWG.Wait()

	list, err := h.svc.NotesHistory(h.ctx, h.viewer(), r.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("history = %+v %v", list, err)
	}
	draft, edit, save := list[0], list[1], list[2]
	if save.ID != 1 || save.Actor != core.NotesByUser || save.User != h.user.ID || save.Before != "" || save.After != "# acme/api\n\nBuild with make.\n" {
		t.Fatalf("user save = %+v", save)
	}
	if edit.ID != 2 || edit.Actor != core.NotesByBothy || edit.Session != env.Session || edit.Run != env.Run || edit.Before != save.After ||
		!strings.Contains(edit.After, "- Run gofmt.") || !edit.At.After(save.At) {
		t.Fatalf("agent edit = %+v", edit)
	}
	if draft.ID != 3 || draft.Actor != core.NotesByBothy || draft.Session != "" || draft.Before != edit.After || !strings.Contains(draft.After, "The acme API.") {
		t.Fatalf("draft = %+v", draft)
	}
	got, err := h.svc.NotesVersion(h.ctx, h.viewer(), r.ID, 2)
	if err != nil || got.After != edit.After {
		t.Fatalf("version 2 = %+v %v", got, err)
	}
	if _, err := h.svc.NotesVersion(h.ctx, h.viewer(), r.ID, 9); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing version = %v", err)
	}

	// Another principal's viewer sees none of it.
	other := h.newUser("other", core.RoleMember)
	ov := core.Viewer{User: other.ID, Principals: []core.PrincipalID{core.UserPrincipal(other.ID)}}
	if _, err := h.svc.NotesHistory(h.ctx, ov, r.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("foreign history = %v", err)
	}
	if _, err := h.svc.NotesVersion(h.ctx, ov, r.ID, 1); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("foreign version = %v", err)
	}

	p := filepath.Join(h.data, "knowledge", string(h.p.ID), "acme", "api", historyFile)
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("history file = %v %v", fi, err)
	}
	if err := h.svc.Delete(h.ctx, h.viewer(), r.ID, h.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("history survived the repo: %v", err)
	}
}

func TestNotesHistoryIsBounded(t *testing.T) {
	h := newHarness(t)
	h.writeRepo()
	r := h.index("repos/acme/api", "acme/api", "")
	etag := ""
	for i := range historyMax + 5 {
		n, err := h.svc.WriteNotes(h.ctx, h.viewer(), r.ID, strings.Repeat("x", i+1), etag, h.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		etag = n.ETag
	}
	list, err := h.svc.NotesHistory(h.ctx, h.viewer(), r.ID)
	if err != nil || len(list) != historyMax || list[0].ID != historyMax+5 || list[len(list)-1].ID != 6 {
		t.Fatalf("history kept %d entries, newest %d, oldest %d (%v)", len(list), list[0].ID, list[len(list)-1].ID, err)
	}
	// A damaged line is skipped, not fatal.
	p := filepath.Join(h.data, "knowledge", string(h.p.ID), "acme", "api", historyFile)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("{not json\n")
	f.Close()
	if list, err := h.svc.NotesHistory(h.ctx, h.viewer(), r.ID); err != nil || len(list) != historyMax {
		t.Fatalf("after a damaged line: %d %v", len(list), err)
	}
}

func TestSummaryOf(t *testing.T) {
	for in, want := range map[string]string{
		"# acme/web\n\n## What this is\n\nCustomer-facing web app, deployed from main. Built with Next.js.": "Customer-facing web app, deployed from main.",
		"---\ntitle: x\n---\n# Title\n\n- Uses **Go** and [SQLite](https://sqlite.org).\n- Two":             "Uses Go and SQLite.",
		"```sh\nmake\n```\n\nRun `make test` first":                                                         "Run make test first",
		"# Only a heading\n":                       "",
		"> a quote\n\n| a | b |\n---\nPlain text.": "Plain text.",
		"": "",
	} {
		if got := summaryOf(in); got != want {
			t.Errorf("summaryOf(%q) = %q, want %q", in, got, want)
		}
	}
	long := summaryOf(strings.Repeat("word ", 100))
	if n := len([]rune(long)); n != summaryMax || !strings.HasSuffix(long, "…") {
		t.Fatalf("long summary (%d runes) %q", n, long)
	}
}

func TestReposCarrySummaryAndProgress(t *testing.T) {
	h := newHarness(t)
	h.writeRepo()
	r := h.index("repos/acme/api", "acme/api", "")
	if _, err := h.svc.WriteNotes(h.ctx, h.viewer(), r.ID, "# acme/api\n\nThe acme API service. It serves JSON.\n", "", h.user.ID); err != nil {
		t.Fatal(err)
	}
	got, err := h.svc.Repo(h.ctx, h.viewer(), r.ID)
	if err != nil || got.Summary != "The acme API service." || got.Progress != nil {
		t.Fatalf("repo = %+v %v", got, err)
	}
	list, _ := h.svc.Repos(h.ctx, h.viewer(), "")
	if len(list) != 1 || list[0].Summary != "The acme API service." {
		t.Fatalf("repos = %+v", list)
	}

	sub := h.bus.Subscribe(core.Filter{Principals: []core.PrincipalID{h.p.ID}, Types: []core.EventType{core.EvKnowledgeUpdated}}, 64)
	defer sub.Close()
	for i := range 30 {
		h.write("repos/acme/api/pkg/f"+string(rune('a'+i%26))+string(rune('a'+i/26))+".go", "package pkg\n\nfunc F() {}\n")
	}
	if got := estimateFiles(h.ws, "repos/acme/api", 1000); got < 30 {
		t.Fatalf("estimateFiles = %d", got)
	}
	if got := estimateFiles(h.ws, "repos/acme/api", 5); got != 5 {
		t.Fatalf("estimateFiles capped = %d", got)
	}
	h.runJob(r.ID)
	var running []core.IndexedRepo
	for {
		select {
		case e := <-sub.C():
			if d := e.Data.(core.KnowledgeUpdatedData); d.Repo.Status == core.IndexRunning {
				running = append(running, d.Repo)
			}
			continue
		default:
		}
		break
	}
	if len(running) == 0 || running[0].Progress == nil || running[0].Progress.Total == 0 || running[0].Summary != "The acme API service." {
		t.Fatalf("running events = %+v", running)
	}
	if got, _ := h.svc.Repo(h.ctx, h.viewer(), r.ID); got.Progress != nil || got.Status != core.IndexReady {
		t.Fatalf("finished repo still shows progress: %+v", got)
	}
}

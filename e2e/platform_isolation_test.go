//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/channel/telegram/telegramtest"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// pltProbe is one by-ID request another principal's client must not reach.
type pltProbe struct {
	method, path string
	body         any
	hdr          http.Header
	bearerStatus int // 0 = 404; set where the route refuses bearer tokens outright
}

// pltSQLite runs a read-only query with the sqlite3 CLI; ok is false when
// the CLI is not installed (the e2e module may not link a SQLite driver).
func pltSQLite(t *testing.T, db, query string) (string, bool) {
	t.Helper()
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		return "", false
	}
	// immutable: a cleanly closed WAL database has no -shm file, which a
	// plain read-only open would need to create.
	uri := (&url.URL{Scheme: "file", Path: db, RawQuery: "immutable=1"}).String()
	out, err := exec.CommandContext(context.Background(), bin, "-readonly", uri, query).CombinedOutput()
	if err != nil {
		t.Fatalf("sqlite3 %q: %v\n%s", query, err, out)
	}
	return strings.TrimSpace(string(out)), true
}

func pltMultipartUpload(t *testing.T, sid core.SessionID) ([]byte, http.Header) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("session_id", string(sid)); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", "probe.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte("probe"))
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), http.Header{"Content-Type": {mw.FormDataContentType()}}
}

// TestPrincipalIsolation is E19, in multi mode with users A (alice) and B
// (bob) and an owner's group space: B's cookie and token get 404 from every
// by-ID route for A's sessions, runs, approvals, memory, grants, vault
// entries, schedules and files, see none of A's events, and B's fs tools
// cannot see A's workspace; a space run sees no owner memory and resolves
// vault references in the global scope only; purging A leaves no vault row,
// grant, memory file or workspace directory behind. Sandbox boxes are not
// part of this run (no sandbox backend); purge's box step is covered only
// as a no-op here.
func TestPrincipalIsolation(t *testing.T) {
	ownerMem := "OWNER-MEMORY-" + pltRand(6)
	ownerVault := "OWNER-VAULT-" + pltRand(8)
	globalVault := "GLOBAL-VAULT-" + pltRand(8)
	aSecret := "A-PRIVATE-" + pltRand(6)
	aMem := "A-MEMORY-" + pltRand(6)
	aWord := "zephyrine" + pltRand(3)
	const (
		ownerTG = 7001
		group   = -1007001
	)
	llm := pltLLM(t,
		mockllm.Rule{MatchUser: `E19-`, MatchToolResult: `.`, Text: []string{"Done."}},
		mockllm.Rule{MatchUser: `E19-A-WRITE`, ToolCalls: []core.ToolCall{pltCall("call_aw", "fs_write", `{"path":"ask/secret-a.txt","content":"`+aSecret+`\n"}`)}},
		mockllm.Rule{MatchUser: `E19-A-PENDING`, ToolCalls: []core.ToolCall{pltCall("call_ap", "fs_write", `{"path":"later/pending.txt","content":"later\n"}`)}},
		mockllm.Rule{MatchUser: `E19-B-READ`, ToolCalls: []core.ToolCall{pltCall("call_br", "fs_read", `{"path":"ask/secret-a.txt"}`)}},
		mockllm.Rule{MatchUser: `E19-B-LIST`, ToolCalls: []core.ToolCall{pltCall("call_bl", "fs_list", `{"path":"."}`)}},
		mockllm.Rule{MatchUser: `E19-B-UP`, ToolCalls: []core.ToolCall{pltCall("call_bu", "fs_list", `{"path":".."}`)}},
		mockllm.Rule{MatchUser: `E19-MCP`, ToolCalls: []core.ToolCall{pltCall("call_m", "use_tool", `{"name":"mcp__e19__lookup","args":{"query":"who"}}`)}},
		mockllm.Rule{MatchUser: `E19-`, Text: []string{"Hello."}},
	)
	mcp := pltNewMCP(t, pltLookupTool("Look up a term.", "LOOKUP-RESULT: ok"))
	tg := pltNewTelegram()
	g := Start(t, Options{Platforms: []Platform{tg}, Config: pltYAML(llm.URL(), nil, "mode: multi\n"+
		pltMCPYAML("e19", mcp, "      headers: {X-Vault: \"secret:e19_token\"}\n")+`
policy:
  rules:
    - {id: e19-write, tool: fs_write, verdict: ask}
`)})
	owner := pltOwner(t, g)
	put := func(t *testing.T, u *pltUser, scope, name, value string) {
		t.Helper()
		if code := u.JSON(t, http.MethodPut, "/api/vault/"+scope+"/"+name, dto.VaultPutReq{Value: value}, nil); code != http.StatusOK {
			t.Fatalf("vault put %s/%s = %d", scope, name, code)
		}
	}
	writeMemory := func(t *testing.T, u *pltUser, content string) {
		t.Helper()
		var doc dto.MemoryDoc
		if code := u.JSON(t, http.MethodGet, "/api/memory/MEMORY.md", nil, &doc); code != http.StatusOK {
			t.Fatalf("get memory = %d", code)
		}
		resp := u.Do(t, http.MethodPut, "/api/memory/MEMORY.md", dto.MemoryPutReq{Content: content}, http.Header{"If-Match": {`"` + doc.ETag + `"`}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("put memory = %s %s", resp.Status, resp.Body)
		}
	}

	// The MCP header resolves in the caller's scope, then global; discovery
	// uses the global scope, so the global entry must exist first.
	put(t, owner, "global", "e19_token", globalVault)
	if code := owner.JSON(t, http.MethodPost, "/api/mcp/e19/reload", nil, nil); code != http.StatusAccepted {
		t.Fatalf("mcp reload = %d", code)
	}
	pltMCPStatus(t, owner, "e19", func(st core.MCPStatus) bool { return st.Pending == 1 })
	pltApproveMCP(t, owner, "e19", core.SERead)
	put(t, owner, "user:"+string(owner.ID()), "e19_token", ownerVault)
	writeMemory(t, owner, "- "+ownerMem+"\n")

	a := owner.CreateUser(t, "alice")
	b := owner.CreateUser(t, "bob")
	bTok := b.Token(t, "", "chat", "read")
	aTok := a.Token(t, "", "chat", "read")
	bEvents := pltRecord(b.Events(t))
	aPrincipal := a.Principal(t)
	aID := a.ID()

	// A's data: a session with a run, a decided approval and its grant, a
	// pin, memory, a vault entry, a schedule, and a second run left waiting
	// on a pending approval.
	sA := a.NewSession(t)
	runA := a.Send(t, sA.ID, "E19-A-WRITE keep this "+aWord)
	apA := a.WaitApproval(t, runA, core.ApprovalPending)
	a.Resolve(t, apA.ID, core.AllowAlways)
	if d := a.Finished(t, runA); d.Run.Status != core.RunDone {
		t.Fatalf("A's run = %+v", d.Run)
	}
	var aGrants []core.Grant
	if code := a.JSON(t, http.MethodGet, "/api/grants", nil, &aGrants); code != http.StatusOK || len(aGrants) != 1 {
		t.Fatalf("A's grants = %d %+v", code, aGrants)
	}
	gA := aGrants[0]
	var pinA core.Pin
	if code := a.JSON(t, http.MethodPost, "/api/sessions/"+string(sA.ID)+"/pins", dto.PinReq{Text: "Answer briefly."}, &pinA); code != http.StatusOK {
		t.Fatalf("A's pin = %d", code)
	}
	writeMemory(t, a, "- "+aMem+"\n")
	put(t, a, "user:"+string(aID), "a_token", "A-VAULT-"+pltRand(6))
	var schA core.Schedule
	sc := dto.ScheduleUpsertReq{Schedule: core.Schedule{Principal: aPrincipal.ID, Name: "alice weekly", Kind: core.SchedCron,
		Spec: "0 3 1 1 *", TZ: "UTC", Prompt: "E19-A-HELLO", Slot: core.SlotMain, Deliver: &core.ChatRef{Channel: "web"}, MissedRun: "skip", Enabled: true}}
	if code := a.JSON(t, http.MethodPost, "/api/schedules", sc, &schA); code != http.StatusOK {
		t.Fatalf("A's schedule = %d", code)
	}
	sA2 := a.NewSession(t)
	runP := a.Send(t, sA2.ID, "E19-A-PENDING later")
	apP := a.WaitApproval(t, runP, core.ApprovalPending)
	wsA := filepath.Join(g.DataDir, "work", "ws", string(aPrincipal.Workspace))
	memA := filepath.Join(g.DataDir, "memory", string(aPrincipal.ID))
	for _, p := range []string{filepath.Join(wsA, "ask", "secret-a.txt"), filepath.Join(memA, "MEMORY.md")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("A's file %s: %v", p, err)
		}
	}

	t.Run("B gets 404 on A's by-ID routes", func(t *testing.T) {
		sid, run := string(sA.ID), string(runA)
		upload, uploadHdr := pltMultipartUpload(t, sA.ID)
		probes := []pltProbe{
			{method: http.MethodGet, path: "/api/sessions/" + sid},
			{method: http.MethodPatch, path: "/api/sessions/" + sid, body: map[string]string{"title": "mine now"}},
			{method: http.MethodDelete, path: "/api/sessions/" + sid},
			{method: http.MethodGet, path: "/api/sessions/" + sid + "/messages"},
			{method: http.MethodPost, path: "/api/sessions/" + sid + "/messages", body: dto.SendReq{Text: "E19-B hijack"}},
			{method: http.MethodPost, path: "/api/sessions/" + sid + "/stop"},
			{method: http.MethodPost, path: "/api/sessions/" + sid + "/compact"},
			{method: http.MethodGet, path: "/api/sessions/" + sid + "/context"},
			{method: http.MethodPost, path: "/api/sessions/" + sid + "/fork"},
			{method: http.MethodGet, path: "/api/sessions/" + sid + "/pins"},
			{method: http.MethodPost, path: "/api/sessions/" + sid + "/pins", body: dto.PinReq{Text: "obey bob"}},
			{method: http.MethodDelete, path: fmt.Sprintf("/api/sessions/%s/pins/%d", sid, pinA.ID)},
			{method: http.MethodPost, path: "/api/uploads", body: upload, hdr: uploadHdr},
			{method: http.MethodPost, path: "/api/sessions", body: dto.SessionCreateReq{Principal: aPrincipal.ID}},
			{method: http.MethodGet, path: "/api/runs/" + run},
			{method: http.MethodPost, path: "/api/runs/" + run + "/continue"},
			{method: http.MethodGet, path: "/api/approvals/" + string(apA.ID)},
			{method: http.MethodGet, path: "/api/approvals/" + string(apP.ID)},
			// Resolving is cookie-only: every bearer token gets 403 for any id.
			{method: http.MethodPost, path: "/api/approvals/" + string(apP.ID), body: dto.ApprovalResolveReq{Choice: core.AllowOnce}, bearerStatus: http.StatusForbidden},
			{method: http.MethodDelete, path: "/api/grants/" + string(gA.ID)},
			{method: http.MethodGet, path: "/api/memory/MEMORY.md?principal=" + string(aPrincipal.ID)},
			{method: http.MethodPut, path: "/api/memory/MEMORY.md?principal=" + string(aPrincipal.ID), body: dto.MemoryPutReq{Content: "- bob was here"},
				hdr: http.Header{"If-Match": {`"x"`}}},
			{method: http.MethodGet, path: "/api/memory/MEMORY.md/history?principal=" + string(aPrincipal.ID)},
			{method: http.MethodGet, path: "/api/memory/export?principal=" + string(aPrincipal.ID)},
			{method: http.MethodGet, path: "/api/vault?scope=user:" + string(aID)},
			{method: http.MethodPut, path: "/api/vault/user:" + string(aID) + "/a_token", body: dto.VaultPutReq{Value: "bob's value"}},
			{method: http.MethodDelete, path: "/api/vault/user:" + string(aID) + "/a_token"},
			{method: http.MethodGet, path: "/api/workspace/files?principal=" + string(aPrincipal.ID)},
			{method: http.MethodGet, path: "/api/workspace/files?principal=" + string(aPrincipal.ID) + "&path=ask/secret-a.txt"},
			{method: http.MethodGet, path: "/api/schedules/" + string(schA.ID)},
			{method: http.MethodPatch, path: "/api/schedules/" + string(schA.ID), body: dto.ScheduleUpsertReq{Schedule: schA}},
			{method: http.MethodDelete, path: "/api/schedules/" + string(schA.ID)},
			{method: http.MethodPost, path: "/api/schedules/" + string(schA.ID) + "/run"},
			{method: http.MethodGet, path: "/api/schedules/" + string(schA.ID) + "/runs"},
		}
		// The GET probes answer A, so B's 404s come from isolation.
		for _, p := range probes {
			if p.method == http.MethodGet {
				if resp := a.Do(t, p.method, p.path, nil, p.hdr); resp.StatusCode != http.StatusOK {
					t.Errorf("A's own GET %s = %s %s", p.path, resp.Status, resp.Body)
				}
			}
		}
		for _, c := range []struct {
			name   string
			client *pltUser
		}{{"cookie", b}, {"bearer", bTok}} {
			for _, p := range probes {
				want := http.StatusNotFound
				if c.name == "bearer" && p.bearerStatus != 0 {
					want = p.bearerStatus
				}
				resp := c.client.Do(t, p.method, p.path, p.body, p.hdr)
				if resp.StatusCode != want {
					t.Errorf("%s %s %s = %s %s, want %d", c.name, p.method, p.path, resp.Status, resp.Body, want)
				}
				if bytes.Contains(resp.Body, []byte(aSecret)) || bytes.Contains(resp.Body, []byte(aMem)) || bytes.Contains(resp.Body, []byte(aWord)) {
					t.Errorf("%s %s %s leaked A's data: %s", c.name, p.method, p.path, resp.Body)
				}
			}
			// Lists and searches show nothing of A's.
			for _, path := range []string{"/api/sessions?archived=false", "/api/sessions?principal=" + string(aPrincipal.ID),
				"/api/runs", "/api/runs?session=" + sid, "/api/approvals?status=pending", "/api/approvals?status=recent",
				"/api/grants", "/api/schedules", "/api/search?q=" + aWord, "/api/spaces", "/api/identities"} {
				resp := c.client.Do(t, http.MethodGet, path, nil, nil)
				for _, id := range []string{sid, string(sA2.ID), run, string(runP), string(apA.ID), string(apP.ID), string(gA.ID), string(schA.ID), aWord} {
					if bytes.Contains(resp.Body, []byte(id)) {
						t.Errorf("%s GET %s = %s lists A's %s", c.name, path, resp.Status, id)
					}
				}
			}
		}
		resp := bTok.Do(t, http.MethodPost, "/v1/chat/completions", map[string]any{"model": "bothy",
			"messages": []map[string]string{{"role": "user", "content": "E19-B via api"}}}, http.Header{"X-Bothy-Session": {sid}})
		if status, _, code, _ := pltOAError(t, resp); status != http.StatusNotFound || code != "session_not_found" {
			t.Errorf("/v1 with A's X-Bothy-Session = %d %s", status, code)
		}
		// Nothing B tried changed A's data.
		var d dto.SessionDetail
		if code := a.JSON(t, http.MethodGet, "/api/sessions/"+sid, nil, &d); code != http.StatusOK || d.Session.Archived || d.Session.Title == "mine now" || len(d.Pins) != 1 {
			t.Errorf("A's session after B's attempts = %d %+v", code, d)
		}
		if st := a.WaitApproval(t, runP, core.ApprovalPending); st.ID != apP.ID {
			t.Errorf("A's pending approval changed: %+v", st)
		}
	})

	t.Run("B's fs tools cannot see A's workspace", func(t *testing.T) {
		sB := b.NewSession(t)
		// refused: the call fails at planning (error) or as invalid arguments.
		check := func(text string, refused bool) *core.ToolCallRecord {
			t.Helper()
			d := b.Finished(t, b.Send(t, sB.ID, text))
			if d.Run.Status != core.RunDone || len(d.ToolCalls) != 1 {
				t.Fatalf("%s: run %+v tool calls %+v, want one call", text, d.Run, d.ToolCalls)
			}
			tc := d.ToolCalls[0]
			if failed := tc.Status == core.TCError || tc.Status == core.TCInvalid; failed != refused || (!refused && tc.Status != core.TCOK) {
				t.Fatalf("%s: tool call %+v, want refused=%v", text, tc, refused)
			}
			if strings.Contains(tc.ResultPreview, aSecret) || strings.Contains(tc.ResultPreview, string(aPrincipal.Workspace)) {
				t.Fatalf("%s: B saw A's workspace: %q", text, tc.ResultPreview)
			}
			return tc
		}
		check("E19-B-READ ask/secret-a.txt", true)
		if tc := check("E19-B-LIST my files", false); strings.Contains(tc.ResultPreview, "ask") {
			t.Fatalf("B's listing shows A's folder: %q", tc.ResultPreview)
		}
		if tc := check("E19-B-UP the parent folder", true); !strings.Contains(tc.ResultPreview, "outside the workspace") {
			t.Fatalf("listing .. = %q, want it refused as outside the workspace", tc.ResultPreview)
		}
		if resp := b.Do(t, http.MethodGet, "/api/workspace/files?path=ask/secret-a.txt", nil, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("B's own workspace has A's file: %s", resp.Status)
		}
		for _, body := range llm.Requests() {
			if bytes.Contains(body, []byte("E19-B")) && bytes.Contains(body, []byte(aSecret)) {
				t.Fatalf("A's file content reached B's model request")
			}
		}
	})

	t.Run("B's event stream carries none of A's events", func(t *testing.T) {
		b.Finished(t, b.Send(t, b.NewSession(t).ID, "E19-B-HELLO")) // B's own run proves the stream works
		bEvents.WaitFor(t, core.EvRunFinished, 5*time.Second, func(e pltEvent) bool { return e.Principal == core.UserPrincipal(b.ID()) })
		for _, f := range bEvents.Frames() {
			var ev pltEvent
			if json.Unmarshal(f.Data, &ev) == nil && (ev.Principal == aPrincipal.ID || ev.Session == sA.ID || ev.Session == sA2.ID || ev.Run == runA || ev.Run == runP) {
				t.Fatalf("B received A's %s event: %s", f.Event, f.Data)
			}
			if bytes.Contains(f.Data, []byte(aSecret)) || bytes.Contains(f.Data, []byte(aWord)) {
				t.Fatalf("B's stream leaked A's content: %s", f.Data)
			}
		}
	})

	t.Run("space run sees no owner memory or vault", func(t *testing.T) {
		// The owner's own run resolves the header in the owner's scope first.
		if d := owner.Finished(t, owner.Send(t, owner.NewSession(t).ID, "E19-MCP as the owner")); d.Run.Status != core.RunDone {
			t.Fatalf("owner MCP run = %+v", d.Run)
		}
		calls := mcp.Calls()
		if len(calls) != 1 || calls[0].Header.Get("X-Vault") != ownerVault {
			t.Fatalf("owner's MCP call header = %v, want the owner's user-scope value", calls)
		}

		tg.Pair(t, owner, ownerTG)
		tg.InjectBotAdded(group, "E19 Group", ownerTG)
		var space core.Space
		deadline := time.Now().Add(10 * time.Second)
		for space.Principal == "" {
			var spaces []core.Space
			if code := owner.JSON(t, http.MethodGet, "/api/spaces", nil, &spaces); code != http.StatusOK {
				t.Fatalf("spaces = %d", code)
			}
			for _, sp := range spaces {
				if sp.Chat.ChatID == fmt.Sprint(group) {
					space = sp
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("no space for the group\nlogs:\n%s", g.Logs())
			}
			time.Sleep(25 * time.Millisecond)
		}
		on := true
		if code := owner.JSON(t, http.MethodPatch, "/api/spaces/"+string(space.Principal), dto.SpacePatchReq{Enabled: &on}, nil); code != http.StatusOK {
			t.Fatalf("enable space = %d", code)
		}
		if resp := b.Do(t, http.MethodPatch, "/api/spaces/"+string(space.Principal), dto.SpacePatchReq{Enabled: &on}, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("B patching the owner's space = %s, want 404", resp.Status)
		}

		say := func(text, reply string) map[string]json.RawMessage {
			t.Helper()
			tg.Inject(telegramtest.Inbound{ChatID: group, ChatType: "supergroup", ChatTitle: "E19 Group", SenderID: ownerTG, Text: text, Mention: true})
			tg.WaitText(t, group, reply, 15*time.Second)
			return pltLastRequest(t, llm, text)
		}
		req := say("E19-SPACE-R hello group", "Hello.")
		body, _ := json.Marshal(req)
		for _, secret := range []string{ownerMem, ownerVault, globalVault, aMem} {
			if bytes.Contains(body, []byte(secret)) {
				t.Fatalf("the space run's model request contains %s", secret)
			}
		}
		names := pltToolNames(t, req)
		for _, banned := range []string{"memory", "session_search", "fs_write", "fs_delete", "shell", "python"} {
			if slices.Contains(names, banned) {
				t.Fatalf("restricted space tools %v include %s", names, banned)
			}
		}

		standard := "standard"
		if code := owner.JSON(t, http.MethodPatch, "/api/spaces/"+string(space.Principal), dto.SpacePatchReq{ToolProfile: &standard}, nil); code != http.StatusOK {
			t.Fatalf("space tool profile = %d", code)
		}
		req = say("E19-MCP from the group", "Done.")
		if body, _ := json.Marshal(req); bytes.Contains(body, []byte(ownerMem)) {
			t.Fatalf("the standard space run's model request contains the owner's memory")
		}
		calls = mcp.Calls()
		if len(calls) != 2 {
			t.Fatalf("MCP calls = %d, want the space's call too", len(calls))
		}
		if got := calls[1].Header.Get("X-Vault"); got != globalVault {
			t.Fatalf("space MCP call header = %q, want the global value (never the owner's)", got)
		}
	})

	t.Run("purging A leaves nothing behind", func(t *testing.T) {
		if code := owner.JSON(t, http.MethodDelete, "/api/users/"+string(aID)+"?purge=true", nil, nil); code != http.StatusNoContent {
			t.Fatalf("purge = %d", code)
		}
		var users []core.User
		if code := owner.JSON(t, http.MethodGet, "/api/users", nil, &users); code != http.StatusOK ||
			slices.ContainsFunc(users, func(u core.User) bool { return u.ID == aID }) {
			t.Fatalf("users after purge = %d %+v", code, users)
		}
		var secrets []core.SecretMeta
		if code := owner.JSON(t, http.MethodGet, "/api/vault?scope=user:"+string(aID), nil, &secrets); code != http.StatusOK || len(secrets) != 0 {
			t.Fatalf("A's vault after purge = %d %+v", code, secrets)
		}
		for _, p := range []string{wsA, memA} {
			if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s after purge: %v, want it gone", p, err)
			}
		}
		for _, c := range []*pltUser{a, aTok} {
			if resp := c.Do(t, http.MethodGet, "/api/auth/me", nil, nil); resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("A's credentials after purge: /api/auth/me = %s", resp.Status)
			}
		}
		e := owner.WaitAudit(t, "user.update", func(e core.AuditEntry) bool {
			return e.Target == "user:"+string(aID) && strings.Contains(string(e.Detail), `"purge":true`)
		})
		if e.Outcome != "ok" {
			t.Fatalf("purge audit = %+v", e)
		}
		// B and the owner are untouched.
		if code := b.JSON(t, http.MethodGet, "/api/auth/me", nil, nil); code != http.StatusOK {
			t.Fatalf("B after A's purge: me = %d", code)
		}
		if code := owner.JSON(t, http.MethodGet, "/api/memory/MEMORY.md", nil, nil); code != http.StatusOK {
			t.Fatalf("owner memory after A's purge = %d", code)
		}

		if err := g.Stop(); err != nil {
			t.Fatalf("stop: %v", err)
		}
		db := filepath.Join(g.DataDir, "bothy.db")
		ps := "('" + string(aPrincipal.ID) + "')"
		for _, q := range []string{
			"SELECT count(*) FROM vault_secrets WHERE scope = 'user:" + string(aID) + "'",
			"SELECT count(*) FROM grants WHERE principal_id IN " + ps,
			"SELECT count(*) FROM principals WHERE user_id = '" + string(aID) + "'",
			"SELECT count(*) FROM sessions WHERE principal_id IN " + ps,
			"SELECT count(*) FROM runs WHERE principal_id IN " + ps,
			"SELECT count(*) FROM approvals WHERE principal_id IN " + ps,
			"SELECT count(*) FROM memory_events WHERE principal_id IN " + ps,
			"SELECT count(*) FROM schedules WHERE principal_id IN " + ps,
			"SELECT count(*) FROM api_tokens WHERE user_id = '" + string(aID) + "'",
			"SELECT count(*) FROM auth_sessions WHERE user_id = '" + string(aID) + "'",
		} {
			out, ok := pltSQLite(t, db, q)
			if !ok {
				t.Log("sqlite3 not installed: row-level purge checks skipped")
				break
			}
			if out != "0" {
				t.Errorf("%s = %s after purge, want 0", q, out)
			}
		}
		// The same queries find B's and the owner's rows.
		for q, want := range map[string]string{
			"SELECT count(*) FROM principals WHERE user_id = '" + string(b.ID()) + "'":           "1",
			"SELECT count(*) > 0 FROM sessions WHERE principal_id = '" + string(b.ID()) + "'":    "1",
			"SELECT count(*) FROM vault_secrets WHERE scope = 'user:" + string(owner.ID()) + "'": "1",
			"SELECT count(*) > 0 FROM audit_log WHERE target = 'user:" + string(aID) + "'":       "1",
		} {
			if out, ok := pltSQLite(t, db, q); ok && out != want {
				t.Errorf("%s = %s, want %s", q, out, want)
			}
		}
	})
}

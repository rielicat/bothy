//go:build e2e

package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/channel/telegram/telegramtest"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// pltProviderProxy stands between the gateway and mockllm as the provider:
// it records every request, and answers a request containing
// "E15-ECHO-ERROR" with an error that echoes the X-Canary header back, as
// real APIs echo a bad key.
type pltProviderProxy struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []mockllm.Recorded
}

func pltNewProviderProxy(t testing.TB, llm *mockllm.Server) *pltProviderProxy {
	target, err := url.Parse(llm.URL())
	if err != nil {
		t.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: target.Scheme, Host: target.Host})
	p := &pltProviderProxy{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.got = append(p.got, mockllm.Recorded{Method: r.Method, Path: r.URL.RequestURI(), Header: r.Header.Clone(), Body: body})
		p.mu.Unlock()
		if bytes.Contains(body, []byte("E15-ECHO-ERROR")) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
				"message": "Invalid header X-Canary: " + r.Header.Get("X-Canary"), "type": "invalid_request_error", "code": "invalid_header"}})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *pltProviderProxy) URL() string { return p.srv.URL + "/v1" }

func (p *pltProviderProxy) Requests() []mockllm.Recorded {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]mockllm.Recorded(nil), p.got...)
}

// TestVaultCanary is E15: a vault secret used as a provider header reaches
// the provider in that header only. Even when the model echoes it (whole
// or split across stream chunks), writes and reads it through tools, and
// when the provider echoes it in an error, it never appears in request
// bodies, Telegram messages, logs, audit rows, SSE frames or /v1 replies.
func TestVaultCanary(t *testing.T) {
	canary := "CANARY-" + pltRand(12)
	tail := canary[len("CANARY-"):]
	llm := pltLLM(t,
		mockllm.Rule{MatchUser: `E15-HELLO`, Text: []string{"Hello there."}},
		mockllm.Rule{MatchUser: `E15-ECHO`, Text: []string{"The key is ", canary, " so keep it safe."}},
		mockllm.Rule{MatchUser: `E15-SPLIT`, Text: []string{"Split key: CANARY-", tail[:10], tail[10:] + " end of key."}, Delay: 150 * time.Millisecond},
		mockllm.Rule{MatchUser: `E15-TOOL`, MatchToolResult: `.`, Times: 1, ToolCalls: []core.ToolCall{pltCall("call_r", "fs_read", `{"path":"leak.txt"}`)}},
		mockllm.Rule{MatchUser: `E15-TOOL`, MatchToolResult: `.`, Text: []string{"Stored and read back."}},
		mockllm.Rule{MatchUser: `E15-TOOL`, ToolCalls: []core.ToolCall{pltCall("call_w", "fs_write", `{"path":"leak.txt","content":"token=`+canary+`\n"}`)}},
		mockllm.Rule{MatchUser: `E15-CARD`, MatchToolResult: `.`, Text: []string{"Not sent."}},
		mockllm.Rule{MatchUser: `E15-CARD`, ToolCalls: []core.ToolCall{pltCall("call_c", "fs_write", `{"path":"outbox/key.txt","content":"token=`+canary+`\n"}`)}},
	)
	proxy := pltNewProviderProxy(t, llm)
	tg := pltNewTelegram()
	const tgUser = 6001
	g := Start(t, Options{Config: pltYAML(proxy.URL(), map[string]string{"X-Canary": "secret:e15_canary"}, `
policy:
  rules:
    - {id: e15-outbox, tool: fs_write, paths: ["outbox/**"], verdict: ask}
`), Platforms: []Platform{tg}})
	owner := pltOwner(t, g)
	if code := owner.JSON(t, http.MethodPut, "/api/vault/global/e15_canary", dto.VaultPutReq{Value: canary}, nil); code != http.StatusOK {
		t.Fatalf("vault put = %d", code)
	}
	sse := pltRecord(owner.Events(t))
	tg.Pair(t, owner, tgUser)
	s := owner.NewSession(t)

	for _, text := range []string{"E15-HELLO", "E15-ECHO the key", "E15-SPLIT the key", "E15-TOOL store the key"} {
		if d := owner.Finished(t, owner.Send(t, s.ID, text)); d.Run.Status != core.RunDone {
			t.Fatalf("%s: run = %+v\nlogs:\n%s", text, d.Run, g.Logs())
		}
	}
	// An approval card built from an action carrying the secret goes to the
	// web inbox (SSE) and to the owner's Telegram DM.
	cardRun := owner.Send(t, s.ID, "E15-CARD send the key out")
	card := owner.WaitApproval(t, cardRun, core.ApprovalPending)
	tg.WaitText(t, tgUser, "Create outbox/key.txt?", 10*time.Second)
	owner.Resolve(t, card.ID, core.Deny)
	owner.Finished(t, cardRun)
	// The provider got the secret in its header, so it was really in use.
	used := 0
	for _, r := range proxy.Requests() {
		if r.Path == "/v1/chat/completions" {
			if got := r.Header.Get("X-Canary"); got != canary {
				t.Fatalf("provider request header X-Canary = %q, want the vault value", got)
			}
			used++
		}
	}
	if used == 0 {
		t.Fatal("no provider request was made")
	}

	for text, reply := range map[string]string{"E15-ECHO the key on Telegram": "keep it safe.", "E15-SPLIT the key on Telegram": "end of key."} {
		tg.Inject(telegramtest.Inbound{ChatID: tgUser, SenderID: tgUser, Text: text})
		tg.WaitText(t, tgUser, reply, 15*time.Second)
	}

	api := owner.Token(t, "", "chat")
	var replies []string
	for _, c := range []struct {
		text, reply string
		stream      bool
	}{{"E15-ECHO via the API", "keep it safe", false}, {"E15-ECHO via the API", "keep it safe", true}, {"E15-SPLIT via the API", "end of key.", true}} {
		resp := api.Do(t, http.MethodPost, "/v1/chat/completions", map[string]any{"model": "bothy", "stream": c.stream,
			"messages": []map[string]string{{"role": "user", "content": c.text}}}, nil)
		content := string(resp.Body)
		if c.stream {
			content = pltParseStream(t, resp.Body).content
		}
		if resp.StatusCode != http.StatusOK || !strings.Contains(content, c.reply) {
			t.Fatalf("/v1 %q stream=%v = %s %s", c.text, c.stream, resp.Status, resp.Body)
		}
		replies = append(replies, content, string(resp.Body))
	}

	failed := owner.Finished(t, owner.Send(t, s.ID, "E15-ECHO-ERROR trigger a provider error"))
	if failed.Run.Status != core.RunFailed || failed.Run.Error == "" {
		t.Fatalf("run against the echoing provider = %+v, want failed with an error", failed.Run)
	}
	owner.WaitAudit(t, "secret.use", func(e core.AuditEntry) bool { return strings.Contains(e.Target+string(e.Detail), "e15_canary") })
	time.Sleep(600 * time.Millisecond) // let the audit writer flush everything above

	// Every form the redactor masks counts as a leak; so does the random
	// part alone, which is what survives a split across chunks.
	forms := []string{canary, tail, hex.EncodeToString([]byte(canary)), base64.StdEncoding.EncodeToString([]byte(canary)),
		base64.URLEncoding.EncodeToString([]byte(canary))}
	leak := func(where, text string) {
		t.Helper()
		for _, f := range forms {
			if strings.Contains(text, f) {
				i := strings.Index(text, f)
				t.Errorf("canary leaked in %s: …%s…", where, text[max(0, i-80):min(len(text), i+len(f)+40)])
				return
			}
		}
	}

	for i, r := range proxy.Requests() {
		leak("provider request body "+strconv.Itoa(i), string(r.Body))
		for k, vs := range r.Header {
			if k != "X-Canary" {
				leak("provider request header "+k, strings.Join(vs, " "))
			}
		}
	}
	for _, chat := range []int64{tgUser} {
		leak("telegram messages", strings.Join(tg.AllTexts(chat), "\n"))
	}
	leak("gateway logs", g.Logs())
	audit, _ := json.Marshal(owner.Audit(t, ""))
	leak("audit rows", string(audit))
	var deltas strings.Builder
	for _, f := range sse.Frames() {
		leak("SSE frame "+f.Event, string(f.Data))
		var ev pltEvent
		if f.Event == string(core.EvRunDelta) && json.Unmarshal(f.Data, &ev) == nil {
			var d core.DeltaData
			_ = json.Unmarshal(ev.Data, &d)
			deltas.WriteString(d.Text)
		}
	}
	leak("SSE run.delta text joined", deltas.String())
	if !strings.Contains(deltas.String(), "keep it safe") || !strings.Contains(deltas.String(), "end of key.") {
		t.Errorf("SSE deltas %q lack the echo replies: the stream was not checked", deltas.String())
	}
	for _, r := range replies {
		leak("/v1 reply", r)
	}
	var d dto.RunDetail
	if code := owner.JSON(t, http.MethodGet, "/api/runs/"+string(failed.Run.ID), nil, &d); code == http.StatusOK {
		b, _ := json.Marshal(d)
		leak("run detail of the failed run", string(b))
	}
}

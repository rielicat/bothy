//go:build e2e

package e2e

// Helpers of the channel and context scenarios (E4–E6, E9, E10, E16). Names
// carry a "ch" prefix so they cannot collide with other workstreams' helpers.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/channel/telegram/telegramtest"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

const (
	chModel    = "mock-model"
	chPassword = "Granite-River-42-Lantern"
)

// chTitleRule answers the session-title call so it never consumes a
// scenario rule; it must come first.
var chTitleRule = mockllm.Rule{MatchSystem: `^Write a title`, Text: []string{"Test chat"}}

// chTelegram is the fake Bot API, reached through the locked
// channels.telegram.api_base key and the env-supplied bot token.
type chTelegram struct{ *telegramtest.Server }

func (p *chTelegram) Env() map[string]string {
	return map[string]string{
		"BOTHY_CHANNELS__TELEGRAM__ENABLED":  "true",
		"BOTHY_CHANNELS__TELEGRAM__API_BASE": p.URL,
		"BOTHY_SECRET_TELEGRAM_BOT_TOKEN":    p.Token,
	}
}

type chOpts struct {
	rules    []mockllm.Rule
	window   int    // model context; default 32768
	yaml     string // appended to the mock provider fragment of bothy.yaml
	telegram bool
	sandboxd bool // a fake sandboxd, so exec tools are offered without Docker
}

// chEnv is a gateway with mockllm as its only provider and an owner whose
// cookie the gateway's client carries.
type chEnv struct {
	*Gateway
	llm *mockllm.Server
	tg  *chTelegram
	sbx *chSandboxd
}

func chStart(t *testing.T, o chOpts) *chEnv {
	t.Helper()
	window := o.window
	if window == 0 {
		window = 32768
	}
	e := &chEnv{llm: mockllm.New(o.rules, []core.ModelInfo{{Ref: core.ModelRef{Provider: "mock", Model: chModel},
		Context: window, MaxOutput: 2048, Tools: true}})}
	t.Cleanup(e.llm.Close)
	env := map[string]string{}
	var platforms []Platform
	if o.telegram {
		e.tg = &chTelegram{telegramtest.New(telegramtest.Config{})}
		platforms = append(platforms, e.tg)
	}
	if o.sandboxd {
		e.sbx = chStartSandboxd(t)
		env["BOTHY_SANDBOX__BACKEND"] = "sandboxd"
		env["BOTHY_SANDBOX__SOCKET"] = e.sbx.socket
	}
	e.Gateway = Start(t, Options{Config: MockProviderYAML(e.llm.URL(), chModel, window) + o.yaml, Env: env, Platforms: platforms})
	e.LLM = e.llm
	chSetupOwner(t, e.Gateway)
	if e.tg != nil {
		e.waitChannel(t, "telegram")
	}
	return e
}

func chSetupOwner(t *testing.T, g *Gateway) dto.Me {
	t.Helper()
	var claim dto.SetupClaimResp
	if code := g.JSON(t, http.MethodPost, "/api/setup/claim", dto.SetupClaimReq{Token: g.SetupToken(t)}, &claim); code != http.StatusOK {
		t.Fatalf("setup claim = %d", code)
	}
	g.SetCSRF(claim.CSRF)
	var me dto.Me
	owner := dto.SetupOwnerReq{Username: "owner", Password: chPassword, Timezone: "UTC"}
	if code := g.JSON(t, http.MethodPost, "/api/setup/owner", owner, &me); code != http.StatusOK {
		t.Fatalf("setup owner = %d", code)
	}
	g.SetCSRF(me.CSRF)
	if code := g.JSON(t, http.MethodPost, "/api/setup/complete", nil, nil); code != http.StatusNoContent {
		t.Fatalf("setup complete = %d", code)
	}
	return me
}

func chEventually(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *chEnv) waitChannel(t *testing.T, name string) {
	t.Helper()
	chEventually(t, 10*time.Second, name+" to connect", func() bool {
		var sts []core.ChannelStatus
		if e.JSON(t, http.MethodGet, "/api/channels", nil, &sts) != http.StatusOK {
			return false
		}
		return slices.ContainsFunc(sts, func(s core.ChannelStatus) bool { return s.Name == name && s.State == "connected" })
	})
}

// pairTelegram links Telegram user id to the owner the way a person does:
// create a code in the web UI, open its t.me deep link, which makes the
// Telegram client send "/start <code>" to the bot.
func (e *chEnv) pairTelegram(t *testing.T, id int64, firstName string) {
	t.Helper()
	var pc core.PairingCode
	if code := e.JSON(t, http.MethodPost, "/api/pairing", nil, &pc); code != http.StatusOK || pc.Code == "" {
		t.Fatalf("POST /api/pairing = %d %+v", code, pc)
	}
	link, err := url.Parse(pc.DeepLinks["telegram"])
	if err != nil || link.Scheme != "https" || link.Host != "t.me" || link.Path != "/"+e.tg.Bot || link.Query().Get("start") == "" {
		t.Fatalf("telegram deep link = %q (err %v)", pc.DeepLinks["telegram"], err)
	}
	e.tg.Inject(telegramtest.Inbound{ChatID: id, SenderID: id, FirstName: firstName, Text: "/start " + link.Query().Get("start")})
	chEventually(t, 10*time.Second, "the pairing reply", func() bool {
		return slices.ContainsFunc(e.tg.SentTexts(fmt.Sprint(id)), func(s string) bool { return strings.HasPrefix(s, "Linked to ") })
	})
}

// waitRuns waits until session has want runs and all of them have ended.
func (e *chEnv) waitRuns(t *testing.T, session core.SessionID, want int) []core.Run {
	t.Helper()
	var runs dto.List[core.Run]
	chEventually(t, 30*time.Second, fmt.Sprintf("%d finished runs in %s", want, session), func() bool {
		runs = dto.List[core.Run]{}
		if e.JSON(t, http.MethodGet, "/api/runs?limit=100&session="+string(session), nil, &runs) != http.StatusOK || len(runs.Items) != want {
			return false
		}
		for _, r := range runs.Items {
			if !r.Status.Terminal() {
				return false
			}
		}
		return true
	})
	return runs.Items
}

func (e *chEnv) send(t *testing.T, session core.SessionID, text string) {
	t.Helper()
	if code := e.JSON(t, http.MethodPost, "/api/sessions/"+string(session)+"/messages", dto.SendReq{Text: text}, nil); code != http.StatusAccepted {
		t.Fatalf("send message = %d", code)
	}
}

func (e *chEnv) newSession(t *testing.T) core.SessionID {
	t.Helper()
	var s core.Session
	if code := e.JSON(t, http.MethodPost, "/api/sessions", dto.SessionCreateReq{}, &s); code/100 != 2 || s.ID == "" {
		t.Fatalf("create session = %d %+v", code, s)
	}
	return s.ID
}

// pending returns the pending approvals visible to the owner.
func (e *chEnv) pending(t *testing.T) []core.Approval {
	t.Helper()
	var l dto.List[core.Approval]
	if code := e.JSON(t, http.MethodGet, "/api/approvals?status=pending", nil, &l); code != http.StatusOK {
		t.Fatalf("list approvals = %d", code)
	}
	return l.Items
}

func (e *chEnv) approval(t *testing.T, id core.ApprovalID) core.Approval {
	t.Helper()
	var ap core.Approval
	if code := e.JSON(t, http.MethodGet, "/api/approvals/"+string(id), nil, &ap); code != http.StatusOK {
		t.Fatalf("get approval = %d", code)
	}
	return ap
}

// tgMessage waits for a bot message in chat that satisfies match.
func (e *chEnv) tgMessage(t *testing.T, chat int64, what string, match func(telegramtest.Message) bool) telegramtest.Message {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		msgs := e.tg.Messages(chat)
		for _, m := range msgs {
			if !m.Deleted && match(m) {
				return m
			}
		}
		if time.Now().After(deadline) {
			var b strings.Builder
			for _, m := range msgs {
				fmt.Fprintf(&b, "\n  #%d deleted=%v edits=%d buttons=%v: %q", m.ID, m.Deleted, m.Edits, m.Buttons, m.Text)
			}
			t.Fatalf("timed out waiting for %s; chat %d shows:%s", what, chat, b.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tgAnswer waits for the toast of the n-th callback answer (1-based).
func (e *chEnv) tgAnswer(t *testing.T, n int) string {
	t.Helper()
	chEventually(t, 10*time.Second, fmt.Sprintf("callback answer %d", n), func() bool { return len(e.tg.Answers()) >= n })
	return e.tg.Answers()[n-1].Text
}

func chButton(m telegramtest.Message, label string) (telegramtest.Button, bool) {
	for _, row := range m.Buttons {
		for _, b := range row {
			if b.Text == label {
				return b, true
			}
		}
	}
	return telegramtest.Button{}, false
}

// chField is the card field with this label.
func chField(c core.Card, label string) core.CardField {
	for _, f := range c.Fields {
		if f.Label == label {
			return f
		}
	}
	return core.CardField{}
}

func chButtonCount(m telegramtest.Message) int {
	n := 0
	for _, row := range m.Buttons {
		n += len(row)
	}
	return n
}

// chReq is one chat completion request mockllm received.
type chReq struct {
	Messages []chMsg     `json:"messages"`
	Tools    []chToolDef `json:"tools"`
	rawMsgs  []json.RawMessage
	rawTools json.RawMessage
	size     int // body bytes
}

type chMsg struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

type chToolDef struct {
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

func chRequests(t *testing.T, llm *mockllm.Server) []chReq {
	t.Helper()
	var out []chReq
	for _, b := range llm.Requests() {
		var r chReq
		var raw struct {
			Messages []json.RawMessage `json:"messages"`
			Tools    json.RawMessage   `json:"tools"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatalf("mockllm request: %v\n%s", err, b)
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		r.rawMsgs, r.rawTools, r.size = raw.Messages, raw.Tools, len(b)
		out = append(out, r)
	}
	return out
}

func chText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

func (r chReq) system() string {
	var out []string
	for _, m := range r.Messages {
		if m.Role == "system" {
			out = append(out, chText(m.Content))
		}
	}
	return strings.Join(out, "\n")
}

func (r chReq) lastUser() string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "user" {
			return chText(r.Messages[i].Content)
		}
	}
	return ""
}

// toolResults returns the tool messages after the last user message.
func (r chReq) toolResults() []string {
	var out []string
	for i := len(r.Messages) - 1; i >= 0 && r.Messages[i].Role != "user"; i-- {
		if r.Messages[i].Role == "tool" {
			out = append([]string{chText(r.Messages[i].Content)}, out...)
		}
	}
	return out
}

func (r chReq) toolNames() []string {
	var out []string
	for _, d := range r.Tools {
		out = append(out, d.Function.Name)
	}
	return out
}

// agentCall reports whether the request is an agent step, not a session
// title or a compaction summary.
func (r chReq) agentCall() bool {
	s := r.system()
	return !strings.HasPrefix(s, "Write a title") && !strings.HasPrefix(s, "You compress a conversation")
}

// chMACKey reads the approval button HMAC key (meta approval_mac_key) out of
// the gateway's SQLite files, playing an attacker who knows it. The e2e
// package may not link a SQLite driver, but the key is a hex TEXT column
// stored right after its key in the meta table's record, so a byte scan of
// the database and its WAL finds it.
func chMACKey(t *testing.T, dataDir string) []byte {
	t.Helper()
	re := regexp.MustCompile(`approval_mac_key([0-9a-f]{64})`)
	for _, name := range []string{"bothy.db-wal", "bothy.db"} {
		b, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			continue
		}
		if m := re.FindSubmatch(b); m != nil {
			key, err := hex.DecodeString(string(m[1]))
			if err != nil {
				t.Fatal(err)
			}
			return key
		}
	}
	t.Fatal("approval_mac_key not found in the gateway database")
	return nil
}

// chButtonData builds card button data exactly as the gateway does:
// a:<id>:<letter>:<base32(HMAC-SHA256(key, id|choice)[:5])>.
func chButtonData(key []byte, id core.ApprovalID, c core.Choice) string {
	letter := map[core.Choice]string{core.AllowOnce: "o", core.AllowSession: "s", core.AllowAlways: "a", core.Deny: "d"}[c]
	h := hmac.New(sha256.New, key)
	h.Write([]byte(string(id) + "|" + string(c)))
	mac := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h.Sum(nil)[:5]))
	return "a:" + string(id) + ":" + letter + ":" + mac
}

// chSandboxd is a fake sandboxd speaking the §2.5 protocol on a unix socket.
// It reports a healthy, isolated sandbox so the gateway offers exec tools,
// records every box creation and exec, and never runs anything: an exec
// answers with a fixed line. Wire structs are local because the e2e package
// may not import internal/sandboxd/proto; the gateway decodes strictly, so
// only fields of that schema are sent.
type chSandboxd struct {
	socket string

	mu      sync.Mutex
	creates []string
	execs   [][]string
}

func chStartSandboxd(t *testing.T) *chSandboxd {
	t.Helper()
	// sun_path is 104 bytes on macOS; t.TempDir() paths can be longer.
	dir, err := os.MkdirTemp("", "b3chn-sbx-")
	if err == nil && len(dir) > 80 {
		_ = os.RemoveAll(dir)
		dir, err = os.MkdirTemp("/tmp", "b3chn-sbx-")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s := &chSandboxd{socket: filepath.Join(dir, "sandboxd.sock")}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", s.socket)
	if err != nil {
		t.Fatal(err)
	}
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	health := func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusOK, map[string]any{
			"version": "1", "api": "1.47", "engine": "fake sandboxd (e2e)", "runtime": "runc", "rootless": true,
			"subpath": true, "ok": true, "self_test": []any{}, "boxes": 0, "max_boxes": 8,
		})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", health)
	mux.HandleFunc("POST /v1/selftest", health)
	mux.HandleFunc("GET /v1/boxes", func(w http.ResponseWriter, _ *http.Request) { reply(w, http.StatusOK, []any{}) })
	mux.HandleFunc("POST /v1/boxes", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Key string `json:"key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		s.creates = append(s.creates, req.Key)
		s.mu.Unlock()
		reply(w, http.StatusOK, map[string]any{"id": "fake-" + req.Key, "created": true, "runtime": "runc"})
	})
	mux.HandleFunc("POST /v1/boxes/{key}/exec", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Argv []string `json:"argv"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		s.execs = append(s.execs, req.Argv)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		out := base64.StdEncoding.EncodeToString([]byte("fake sandboxd: nothing was executed\n"))
		fmt.Fprintf(w, "{\"s\":1,\"d\":%q}\n{\"exit\":0,\"ms\":1}\n", out)
	})
	mux.HandleFunc("POST /v1/boxes/{key}/kill", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusOK, map[string]any{"killed": 0, "recreated": false})
	})
	mux.HandleFunc("DELETE /v1/boxes/{key}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusNotFound, map[string]any{"code": "not_found", "message": r.Method + " " + r.URL.Path})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return s
}

func (s *chSandboxd) calls() (creates []string, execs [][]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.creates), slices.Clone(s.execs)
}

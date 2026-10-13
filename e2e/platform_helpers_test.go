//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/channel/telegram/telegramtest"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// Helpers of the platform scenarios (E8, E11, E13, E15, E17, E19, E20).

const (
	pltModel    = "mock-model"
	pltPassword = "Granite-River-42-Lantern"
)

// pltTitle answers the asynchronous session-title request, which would
// otherwise consume a scenario's scripted rules.
var pltTitle = mockllm.Rule{MatchSystem: `^Write a title of at most six words`, Text: []string{"Platform scenario"}}

func pltModels() []core.ModelInfo {
	return []core.ModelInfo{{Ref: core.ModelRef{Provider: "mock", Model: pltModel}, Context: 32768, MaxOutput: 2048, Tools: true}}
}

// pltLLM starts mockllm outside the gateway so its URL can go into
// bothy.yaml and so it outlives gateway restarts.
func pltLLM(t testing.TB, rules ...mockllm.Rule) *mockllm.Server {
	t.Helper()
	llm := mockllm.New(append([]mockllm.Rule{pltTitle}, rules...), pltModels())
	t.Cleanup(llm.Close)
	return llm
}

// pltYAML is a bothy.yaml with mockllm as the only provider plus extra
// top-level keys (which must not repeat providers or models). headers are
// provider header values ("secret:<name>" references).
func pltYAML(baseURL string, headers map[string]string, extra string) string {
	var h strings.Builder
	if len(headers) > 0 {
		h.WriteString("    headers:\n")
		for k, v := range headers {
			fmt.Fprintf(&h, "      %s: %q\n", k, v)
		}
	}
	return fmt.Sprintf(`providers:
  - id: mock
    type: openai_compat
    base_url: %q
    allow_private: true
%s    models:
      - {id: %q, context: 32768, max_output: 2048, tools: true}
models:
  main: [mock/%s]
  fast: [mock/%s]
`, baseURL, h.String(), pltModel, pltModel, pltModel) + extra
}

func pltRand(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func pltCall(id, name, args string) core.ToolCall {
	return core.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}
}

// pltUser is one client of the gateway: its own cookie jar and CSRF token,
// or a bearer token.
type pltUser struct {
	g      *Gateway
	client *http.Client
	csrf   string
	bearer string
	me     dto.Me
}

func pltNewClient(g *Gateway) *pltUser {
	jar, _ := cookiejar.New(nil)
	return &pltUser{g: g, client: &http.Client{Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// pltOwner claims the setup token, creates the owner and completes setup.
func pltOwner(t testing.TB, g *Gateway) *pltUser {
	t.Helper()
	return pltClaim(t, g, g.SetupToken(t))
}

func pltClaim(t testing.TB, g *Gateway, token string) *pltUser {
	t.Helper()
	u := pltNewClient(g)
	var claim dto.SetupClaimResp
	if code := u.JSON(t, http.MethodPost, "/api/setup/claim", dto.SetupClaimReq{Token: token}, &claim); code != http.StatusOK {
		t.Fatalf("setup claim = %d", code)
	}
	u.csrf = claim.CSRF
	req := dto.SetupOwnerReq{Username: "owner", Password: pltPassword, Timezone: "UTC"}
	if code := u.JSON(t, http.MethodPost, "/api/setup/owner", req, &u.me); code != http.StatusOK {
		t.Fatalf("setup owner = %d", code)
	}
	u.csrf = u.me.CSRF
	if code := u.JSON(t, http.MethodPost, "/api/setup/complete", nil, nil); code != http.StatusNoContent {
		t.Fatalf("setup complete = %d", code)
	}
	return u
}

// pltLogin signs a user in with a fresh cookie jar.
func pltLogin(t testing.TB, g *Gateway, username, password string) *pltUser {
	t.Helper()
	u := pltNewClient(g)
	if code := u.JSON(t, http.MethodPost, "/api/auth/login", dto.LoginReq{Username: username, Password: password}, &u.me); code != http.StatusOK {
		t.Fatalf("login %s = %d", username, code)
	}
	u.csrf = u.me.CSRF
	return u
}

// CreateUser (an admin) adds a member and signs them in.
func (u *pltUser) CreateUser(t testing.TB, username string) *pltUser {
	t.Helper()
	req := dto.UserCreateReq{Username: username, Password: pltPassword, DisplayName: strings.ToUpper(username[:1]) + username[1:], Role: core.RoleMember}
	if code := u.JSON(t, http.MethodPost, "/api/users", req, nil); code != http.StatusOK {
		t.Fatalf("create user %s = %d", username, code)
	}
	return pltLogin(t, u.g, username, pltPassword)
}

// Relogin refreshes Me (principals change when spaces are created).
func (u *pltUser) Relogin(t testing.TB) {
	t.Helper()
	if code := u.JSON(t, http.MethodGet, "/api/auth/me", nil, &u.me); code != http.StatusOK {
		t.Fatalf("me = %d", code)
	}
}

func (u *pltUser) ID() core.UserID { return u.me.User.ID }

// Principal is the user's own principal.
func (u *pltUser) Principal(t testing.TB) *core.Principal {
	t.Helper()
	for _, p := range u.me.Principals {
		if p.ID == core.UserPrincipal(u.ID()) {
			return p
		}
	}
	t.Fatalf("user %s has no own principal in %+v", u.ID(), u.me.Principals)
	return nil
}

// Token creates an API token (cookie only) and returns a client using it.
func (u *pltUser) Token(t testing.TB, onApproval string, scopes ...string) *pltUser {
	t.Helper()
	spec := core.TokenSpec{Name: "e2e-" + pltRand(3), Scopes: scopes, TTL: 24 * time.Hour, OnApproval: onApproval, RatePerMin: 600}
	var resp dto.TokenCreateResp
	if code := u.JSON(t, http.MethodPost, "/api/tokens", spec, &resp); code != http.StatusOK || resp.Token == "" {
		t.Fatalf("create token %v = %d", scopes, code)
	}
	return &pltUser{g: u.g, client: &http.Client{Timeout: 60 * time.Second}, bearer: resp.Token, me: u.me}
}

func (u *pltUser) request(t testing.TB, method, path string, body any, hdr http.Header) *http.Request {
	t.Helper()
	var r io.Reader
	ctype := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
	default:
		j, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		r, ctype = bytes.NewReader(j), "application/json"
	}
	req, err := http.NewRequestWithContext(context.Background(), method, u.g.BaseURL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if u.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+u.bearer)
	} else if u.csrf != "" && method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-CSRF-Token", u.csrf)
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return req
}

// Do sends a request and reads the whole response.
func (u *pltUser) Do(t testing.TB, method, path string, body any, hdr http.Header) *Response {
	t.Helper()
	resp, err := u.client.Do(u.request(t, method, path, body, hdr))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return &Response{StatusCode: resp.StatusCode, Status: resp.Status, Header: resp.Header, Body: data}
}

// JSON decodes a 2xx response into out and returns the status code.
func (u *pltUser) JSON(t testing.TB, method, path string, in, out any) int {
	t.Helper()
	resp := u.Do(t, method, path, in, nil)
	if resp.StatusCode/100 != 2 {
		t.Logf("%s %s: %s %s", method, path, resp.Status, resp.Body)
		return resp.StatusCode
	}
	if out != nil && len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			t.Fatalf("%s %s: decode %T: %v\n%s", method, path, out, err, resp.Body)
		}
	}
	return resp.StatusCode
}

func (u *pltUser) NewSession(t testing.TB) core.Session {
	t.Helper()
	var s core.Session
	if code := u.JSON(t, http.MethodPost, "/api/sessions", dto.SessionCreateReq{}, &s); code/100 != 2 || s.ID == "" {
		t.Fatalf("create session = %d", code)
	}
	return s
}

// Send posts a web message and returns the run it started.
func (u *pltUser) Send(t testing.TB, sid core.SessionID, text string) core.RunID {
	t.Helper()
	var res core.SubmitResult
	if code := u.JSON(t, http.MethodPost, "/api/sessions/"+string(sid)+"/messages", dto.SendReq{Text: text}, &res); code != http.StatusAccepted || res.Run == "" {
		t.Fatalf("send %q = %d %+v", text, code, res)
	}
	return res.Run
}

// WaitRun polls the run until cond holds.
func (u *pltUser) WaitRun(t testing.TB, id core.RunID, timeout time.Duration, cond func(*core.Run) bool) *dto.RunDetail {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		// The run row appears shortly after Submit returns its id.
		var d dto.RunDetail
		resp := u.Do(t, http.MethodGet, "/api/runs/"+string(id), nil, nil)
		if resp.StatusCode == http.StatusOK && json.Unmarshal(resp.Body, &d) == nil && d.Run != nil && cond(d.Run) {
			return &d
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s: condition not met within %s (last %s %s)\nlogs:\n%s", id, timeout, resp.Status, resp.Body, u.g.Logs())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// Finished waits until the run reaches a terminal status.
func (u *pltUser) Finished(t testing.TB, id core.RunID) *dto.RunDetail {
	t.Helper()
	return u.WaitRun(t, id, 30*time.Second, func(r *core.Run) bool { return r.Status.Terminal() })
}

func (u *pltUser) Messages(t testing.TB, sid core.SessionID) []*core.Message {
	t.Helper()
	var page dto.MessagePage
	if code := u.JSON(t, http.MethodGet, "/api/sessions/"+string(sid)+"/messages?limit=200", nil, &page); code != http.StatusOK {
		t.Fatalf("messages of %s = %d", sid, code)
	}
	return page.Messages
}

// Notices returns the text of the session's Bothy-authored notices.
func (u *pltUser) Notices(t testing.TB, sid core.SessionID) []string {
	t.Helper()
	var out []string
	for _, m := range u.Messages(t, sid) {
		if m.Kind == core.KindNotice {
			out = append(out, m.PlainText())
		}
	}
	return out
}

// WaitApproval polls the inbox for a pending or parked approval of run.
func (u *pltUser) WaitApproval(t testing.TB, run core.RunID, status core.ApprovalStatus) *core.Approval {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var list dto.List[core.Approval]
		if code := u.JSON(t, http.MethodGet, "/api/approvals?status="+string(status), nil, &list); code != http.StatusOK {
			t.Fatalf("approvals = %d", code)
		}
		for i := range list.Items {
			if a := &list.Items[i]; a.Run == run && a.Status == status {
				return a
			}
		}
		if time.Now().After(deadline) {
			resp := u.Do(t, http.MethodGet, "/api/runs/"+string(run), nil, nil)
			t.Fatalf("no %s approval for run %s (run: %s %s)\nlogs:\n%s", status, run, resp.Status, resp.Body, u.g.Logs())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// Resolve answers an approval from the web inbox.
func (u *pltUser) Resolve(t testing.TB, id core.ApprovalID, c core.Choice) *core.Approval {
	t.Helper()
	var a core.Approval
	if code := u.JSON(t, http.MethodPost, "/api/approvals/"+string(id), dto.ApprovalResolveReq{Choice: c}, &a); code != http.StatusOK {
		t.Fatalf("resolve %s with %s = %d", id, c, code)
	}
	return &a
}

// Audit returns every audit row whose action starts with prefix ("" = all),
// paging to the end.
func (u *pltUser) Audit(t testing.TB, prefix string) []core.AuditEntry {
	t.Helper()
	var out []core.AuditEntry
	before := int64(0)
	for {
		path := "/api/audit?limit=200&action=" + prefix
		if before != 0 {
			path += "&before=" + fmt.Sprint(before)
		}
		var page dto.List[core.AuditEntry]
		if code := u.JSON(t, http.MethodGet, path, nil, &page); code != http.StatusOK {
			t.Fatalf("GET /api/audit = %d", code)
		}
		out = append(out, page.Items...)
		if page.Next == 0 || len(page.Items) == 0 {
			return out
		}
		before = page.Next
	}
}

// WaitAudit waits for an audit row (the writer batches asynchronously).
func (u *pltUser) WaitAudit(t testing.TB, action string, match func(core.AuditEntry) bool) core.AuditEntry {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, e := range u.Audit(t, action) {
			if e.Action == action && (match == nil || match(e)) {
				return e
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no audit row %s within 10s", action)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Events opens GET /api/events with this client's credentials.
func (u *pltUser) Events(t testing.TB) *EventStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.g.BaseURL+"/api/events", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if u.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+u.bearer)
	}
	resp, err := (&http.Client{Jar: u.client.Jar}).Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET /api/events: %v", err)
	}
	s := &EventStream{Status: resp.StatusCode, frames: make(chan Frame, 256), errc: make(chan error, 1), cancel: cancel}
	t.Cleanup(s.Close)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("GET /api/events: %s %s", resp.Status, body)
	}
	go s.read(ctx, resp.Body)
	return s
}

// pltRecorder drains an event stream into memory so a scenario can both
// wait for events and inspect everything that was sent.
type pltRecorder struct {
	mu     sync.Mutex
	frames []Frame
	err    error
}

func pltRecord(s *EventStream) *pltRecorder {
	r := &pltRecorder{}
	go func() {
		for {
			f, err := s.Next(time.Hour)
			r.mu.Lock()
			if err != nil {
				r.err = err
				r.mu.Unlock()
				return
			}
			r.frames = append(r.frames, f)
			r.mu.Unlock()
		}
	}()
	return r
}

func (r *pltRecorder) Frames() []Frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Frame(nil), r.frames...)
}

// WaitForFrame returns the first recorded frame of SSE event type typ
// (core events and the hello/resync control frames alike).
func (r *pltRecorder) WaitForFrame(t testing.TB, typ string, timeout time.Duration) Frame {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, f := range r.Frames() {
			if f.Event == typ {
				return f
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("sse: no %s frame within %s", typ, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// pltEvent is the envelope of a core.Event frame.
type pltEvent struct {
	Type      core.EventType   `json:"type"`
	Principal core.PrincipalID `json:"principal_id"`
	Session   core.SessionID   `json:"session_id"`
	Run       core.RunID       `json:"run_id"`
	Data      json.RawMessage  `json:"data"`
}

// WaitFor returns the first recorded event of type typ (searching from the
// start) for which match returns true.
func (r *pltRecorder) WaitFor(t testing.TB, typ core.EventType, timeout time.Duration, match func(pltEvent) bool) pltEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, f := range r.Frames() {
			if f.Event != string(typ) {
				continue
			}
			var ev pltEvent
			if json.Unmarshal(f.Data, &ev) == nil && (match == nil || match(ev)) {
				return ev
			}
		}
		if time.Now().After(deadline) {
			r.mu.Lock()
			err := r.err
			r.mu.Unlock()
			t.Fatalf("sse: no matching %s within %s (stream error: %v)", typ, timeout, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// pltTelegram adapts the fake Bot API to Platform: the gateway reaches it
// through the locked channels.telegram.api_base key and the env secret.
type pltTelegram struct{ *telegramtest.Server }

func pltNewTelegram() pltTelegram { return pltTelegram{telegramtest.New(telegramtest.Config{})} }

func (p pltTelegram) Env() map[string]string {
	return map[string]string{
		"BOTHY_CHANNELS__TELEGRAM__ENABLED":  "true",
		"BOTHY_CHANNELS__TELEGRAM__API_BASE": p.URL,
		"BOTHY_SECRET_TELEGRAM_BOT_TOKEN":    p.Token,
	}
}

// AllTexts is every message the bot ever sent to chat, edits applied,
// deleted ones included, raw and visible text.
func (p pltTelegram) AllTexts(chat int64) []string {
	var out []string
	for _, m := range p.Messages(chat) {
		out = append(out, m.Raw, m.Text)
	}
	return out
}

// WaitText waits until a message in chat contains sub.
func (p pltTelegram) WaitText(t testing.TB, chat int64, sub string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, m := range p.Messages(chat) {
			if strings.Contains(m.Text, sub) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("telegram chat %d: no message containing %q within %s; got %q", chat, sub, timeout, p.AllTexts(chat))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Pair links the Telegram account id to u through a pairing code.
func (p pltTelegram) Pair(t testing.TB, u *pltUser, id int64) {
	t.Helper()
	var code core.PairingCode
	if c := u.JSON(t, http.MethodPost, "/api/pairing", nil, &code); c != http.StatusOK || code.Code == "" {
		t.Fatalf("pairing code = %d %+v", c, code)
	}
	p.Inject(telegramtest.Inbound{ChatID: id, SenderID: id, Text: "/start " + strings.ReplaceAll(code.Code, "-", "")})
	p.WaitText(t, id, "Linked to", 10*time.Second)
}

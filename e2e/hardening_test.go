//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/channel/telegram/telegramtest"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// Black-box checks of the hardening fixes against the real binary.

const hardModel = "mock-model"

// hardenedOwner boots a gateway (mockllm as the only provider when rules are
// given), claims setup and creates the owner, whose cookie the gateway's
// client then carries.
func hardenedOwner(t *testing.T, rules []mockllm.Rule, env map[string]string) *Gateway {
	t.Helper()
	opts := Options{Env: env}
	if rules != nil {
		opts.MockLLM = rules
		opts.Models = []core.ModelInfo{{Ref: core.ModelRef{Provider: "mock", Model: hardModel}, Context: 32768, MaxOutput: 2048, Tools: true}}
	}
	g := Start(t, opts)
	var claim dto.SetupClaimResp
	if code := g.JSON(t, http.MethodPost, "/api/setup/claim", dto.SetupClaimReq{Token: g.SetupToken(t)}, &claim); code != http.StatusOK {
		t.Fatalf("claim = %d", code)
	}
	g.SetCSRF(claim.CSRF)
	var me dto.Me
	owner := dto.SetupOwnerReq{Username: "owner", Password: "Granite-River-42-Lantern", Timezone: "UTC"}
	if code := g.JSON(t, http.MethodPost, "/api/setup/owner", owner, &me); code != http.StatusOK || me.CSRF == "" {
		t.Fatalf("owner = %d", code)
	}
	g.SetCSRF(me.CSRF)
	if rules != nil {
		// The provider goes in through the config API, as the wizard does:
		// mockllm's port is only known once it runs.
		provider, _ := json.Marshal(map[string]any{
			"id": "mock", "type": "openai_compat", "base_url": g.LLM.URL(), "allow_private": true,
			"models": []map[string]any{{"id": hardModel, "context": 32768, "max_output": 2048, "tools": true}},
		})
		set := dto.ConfigPatchReq{Set: map[string]json.RawMessage{
			"providers.mock": provider,
			"models.main":    json.RawMessage(`["mock/` + hardModel + `"]`),
			"models.fast":    json.RawMessage(`["mock/` + hardModel + `"]`),
		}}
		if code := g.JSON(t, http.MethodPatch, "/api/config", set, nil); code != http.StatusOK {
			t.Fatalf("config patch = %d", code)
		}
	}
	return g
}

func completeSetup(t *testing.T, g *Gateway) {
	t.Helper()
	if code := g.JSON(t, http.MethodPost, "/api/setup/complete", nil, nil); code != http.StatusNoContent {
		t.Fatalf("setup complete = %d", code)
	}
}

// rawSSE opens GET /api/events with the gateway's cookies and returns every
// line the server sends, so tests can see the retry: fields the frame
// reader skips. The channel closes when the server ends the stream.
func rawSSE(t *testing.T, g *Gateway) <-chan string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.BaseURL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Jar: g.Client.Jar}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/events = %d", resp.StatusCode)
	}
	lines := make(chan string, 1024)
	go func() {
		defer close(lines)
		defer resp.Body.Close()
		r := bufio.NewReader(resp.Body)
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			select {
			case lines <- strings.TrimRight(l, "\r\n"):
			case <-ctx.Done():
				return
			}
		}
	}()
	return lines
}

// retries collects the retry: values of a stream until it ends, wait
// passes or, with first, the first value arrives.
func retries(lines <-chan string, wait time.Duration, first bool) (vals []string, ended bool) {
	deadline := time.After(wait)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return vals, true
			}
			if v, found := strings.CutPrefix(l, "retry: "); found {
				vals = append(vals, v)
				if first {
					return vals, false
				}
			}
		case <-deadline:
			return vals, false
		}
	}
}

// A fifth tab used to start an eviction storm: every closed stream
// reconnected after ~3 s and closed the next oldest. The evicted stream is
// now told to wait a minute.
func TestHardeningEvictedStreamBacksOff(t *testing.T) {
	g := hardenedOwner(t, nil, nil)
	var streams []<-chan string
	for range 5 {
		s := rawSSE(t, g)
		streams = append(streams, s)
		if vals, _ := retries(s, 2*time.Second, true); len(vals) == 0 || vals[0] != "3000" {
			t.Fatalf("stream %d opened with retry fields %v, want 3000 first", len(streams), vals)
		}
	}
	vals, ended := retries(streams[0], 5*time.Second, false)
	if !ended || len(vals) != 1 || vals[0] != "60000" {
		t.Fatalf("evicted stream: retry fields %v, ended %v; want 60000 then the end", vals, ended)
	}
	for i, s := range streams[1:] {
		if vals, ended := retries(s, 300*time.Millisecond, false); ended || len(vals) != 0 {
			t.Fatalf("stream %d: retry fields %v, ended %v; want it left open", i+2, vals, ended)
		}
	}
}

// A secret split across two streamed deltas used to reach the web as two
// halves that the per-frame redaction could not recognise.
func TestHardeningStreamedSecretStaysMasked(t *testing.T) {
	const secret = "correct horse battery staple"
	g := hardenedOwner(t, []mockllm.Rule{
		{MatchUser: `passphrase`, Text: []string{"The passphrase is correct hor", "se battery staple, keep it safe."}, Delay: 300 * time.Millisecond},
		{Text: []string{"OK"}},
	}, nil)
	completeSetup(t, g)
	if code := g.JSON(t, http.MethodPut, "/api/vault/global/hardening_pass", dto.VaultPutReq{Value: secret}, nil); code != http.StatusOK {
		t.Fatalf("vault put = %d", code)
	}
	events := g.Events(t, "", nil)
	events.WaitFor(t, "hello", 5*time.Second)
	var sess core.Session
	if code := g.JSON(t, http.MethodPost, "/api/sessions", dto.SessionCreateReq{}, &sess); code/100 != 2 {
		t.Fatalf("create session = %d", code)
	}
	if code := g.JSON(t, http.MethodPost, "/api/sessions/"+string(sess.ID)+"/messages", dto.SendReq{Text: "what is the passphrase?"}, nil); code/100 != 2 {
		t.Fatalf("send = %d", code)
	}
	var deltas []string
	deadline := time.Now().Add(20 * time.Second)
	for {
		f, err := events.Next(time.Until(deadline))
		if err != nil {
			t.Fatalf("sse: %v (deltas %q)\n%s", err, deltas, g.Logs())
		}
		for _, w := range strings.Fields(secret) {
			if bytes.Contains(f.Data, []byte(w)) {
				t.Fatalf("a %s frame carries %q of the secret: %s", f.Event, w, f.Data)
			}
		}
		var ev struct {
			Session core.SessionID  `json:"session_id"`
			Data    json.RawMessage `json:"data"`
		}
		if json.Unmarshal(f.Data, &ev) != nil || ev.Session != sess.ID {
			continue
		}
		if f.Event == string(core.EvRunDelta) {
			var d core.DeltaData
			if err := json.Unmarshal(ev.Data, &d); err != nil {
				t.Fatal(err)
			}
			deltas = append(deltas, d.Text)
		}
		if f.Event == string(core.EvRunFinished) {
			break
		}
	}
	if got := strings.Join(deltas, ""); got != "The passphrase is [redacted], keep it safe." || len(deltas) < 2 {
		t.Fatalf("streamed text %q in %d deltas", got, len(deltas))
	}
}

// slowBody sends a few bytes, then blocks until release is closed, so the
// request keeps its large-body slot while its body is "uploading".
type slowBody struct {
	sent    bool
	release chan struct{}
}

func (b *slowBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, `{"model":"bothy","messages":[`), nil
	}
	<-b.release
	return 0, io.ErrUnexpectedEOF
}

// Several /v1 requests with images used to be parsed at once, each holding
// a multi-megabyte body several times over; large bodies now take turns.
func TestHardeningV1LargeBodiesTakeTurns(t *testing.T) {
	g := hardenedOwner(t, []mockllm.Rule{{Text: []string{"Seen."}}}, nil)
	completeSetup(t, g)
	var tok dto.TokenCreateResp
	spec := core.TokenSpec{Name: "hardening", Scopes: []string{"chat"}, TTL: time.Hour}
	if code := g.JSON(t, http.MethodPost, "/api/tokens", spec, &tok); code != http.StatusOK || tok.Token == "" {
		t.Fatalf("create token = %d", code)
	}
	auth := http.Header{"Authorization": {"Bearer " + tok.Token}}
	big, _ := json.Marshal(map[string]any{"model": "bothy", "messages": []map[string]any{{"role": "user", "content": strings.Repeat("x", 2<<20)}}})

	slow := &slowBody{release: make(chan struct{})}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, g.BaseURL+"/v1/chat/completions", slow)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = 3 << 20
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := (&http.Client{}).Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	time.Sleep(500 * time.Millisecond) // the slow request holds the slot

	start := time.Now()
	resp := g.Do(t, http.MethodPost, "/v1/chat/completions", big, auth)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" || !bytes.Contains(resp.Body, []byte(`"rate_limited"`)) {
		t.Fatalf("large request while another uploads = %s %s (after %s)", resp.Status, resp.Body, time.Since(start))
	}
	close(slow.release)
	<-done

	resp = g.Do(t, http.MethodPost, "/v1/chat/completions", big, auth)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(resp.Body, []byte("Seen.")) {
		t.Fatalf("large request once the slot is free = %s %s", resp.Status, resp.Body)
	}
}

// Each person waiting to be let in gets one line a day, but every new
// account also meant an approver DM, without bound; approvers get at most
// three an hour. Link codes are gone.
func TestHardeningUnknownSenderAdminDMsBounded(t *testing.T) {
	tg := telegramtest.New(telegramtest.Config{})
	t.Cleanup(tg.Close)
	g := hardenedOwner(t, nil, map[string]string{
		"BOTHY_CHANNELS__TELEGRAM__ENABLED":  "true",
		"BOTHY_CHANNELS__TELEGRAM__API_BASE": tg.URL,
		"BOTHY_SECRET_TELEGRAM_BOT_TOKEN":    tg.Token,
	})
	completeSetup(t, g)
	const ownerChat = "1001"
	if resp := g.Do(t, http.MethodPost, "/api/pairing", map[string]any{}, nil); resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/pairing = %s, want gone", resp.Status)
	}
	tg.InjectText(ownerChat, ownerChat, "hello", true)
	waitTexts(t, tg, ownerChat, func(ts []string) bool { return containsText(ts, "once an admin lets you in") }, "the owner's waiting line")
	teammate, yes := core.IdentityTeammate, true
	if c := g.JSON(t, http.MethodPatch, "/api/identities/telegram/"+ownerChat, dto.IdentityPatchReq{Status: &teammate, CanApprove: &yes}, nil); c != http.StatusOK {
		t.Fatalf("let in the owner's Telegram = %d", c)
	}
	waitTexts(t, tg, ownerChat, func(ts []string) bool { return containsText(ts, "I'm Bothy.") }, "the hello")

	strangers := []string{"2001", "2002", "2003", "2004", "2005", "2006", "2007", "2008"}
	for _, s := range strangers {
		tg.InjectText(s, s, "hi, let me in", true)
	}
	for _, s := range strangers {
		waitTexts(t, tg, s, func(ts []string) bool { return containsText(ts, "once an admin lets you in") }, "stranger "+s+"'s line")
	}
	time.Sleep(time.Second) // approver DMs go out on the approver's chat queue
	n := 0
	for _, txt := range tg.SentTexts(ownerChat) {
		if strings.Contains(txt, "is waiting to be let in") {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("the owner got %d DMs about %d strangers, want 3:\n%q", n, len(strangers), tg.SentTexts(ownerChat))
	}
}

func containsText(ts []string, sub string) bool {
	for _, s := range ts {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func waitTexts(t *testing.T, tg *telegramtest.Server, chat string, ok func([]string) bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok(tg.SentTexts(chat)) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no %s in chat %s: %q", what, chat, tg.SentTexts(chat))
}

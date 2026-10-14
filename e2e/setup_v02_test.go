//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// TestSetupOneCall is the v0.2 wizard against the real gateway: the owner's
// name, a probe that names and suggests models, one connect call (key saved,
// provider written, both slots set through the real config validation),
// setup completed as Step 3 opens, a first chat, and a teammate who signs in
// with a one-time password and must choose their own.
func TestSetupOneCall(t *testing.T) {
	const model = "mock-model"
	g := Start(t, Options{
		MockLLM: []mockllm.Rule{{MatchUser: `(?i)hello`, Text: []string{"Hi from the mock."}}, {Text: []string{"OK"}}},
		Models:  []core.ModelInfo{{Ref: core.ModelRef{Provider: "mock", Model: model}, Context: 32768, MaxOutput: 2048, Tools: true}},
	})
	var claim dto.SetupClaimResp
	if code := g.JSON(t, http.MethodPost, "/api/setup/claim", dto.SetupClaimReq{Token: g.SetupToken(t)}, &claim); code != http.StatusOK {
		t.Fatalf("claim = %d", code)
	}
	g.SetCSRF(claim.CSRF)
	var st dto.SetupState
	if code := g.JSON(t, http.MethodGet, "/api/setup/state", nil, &st); code != http.StatusOK || st.Install != "binary" {
		t.Fatalf("setup state = %d %+v", code, st)
	}
	var me dto.Me
	owner := dto.SetupOwnerReq{DisplayName: "Maya Chen", Username: "maya", Password: "Granite-River-42-Lantern", Timezone: "Europe/London"}
	if code := g.JSON(t, http.MethodPost, "/api/setup/owner", owner, &me); code != http.StatusOK || me.User.DisplayName != "Maya Chen" {
		t.Fatalf("owner = %d %+v", code, me.User)
	}
	g.SetCSRF(me.CSRF)
	if code := g.JSON(t, http.MethodPatch, "/api/config", dto.ConfigPatchReq{Set: map[string]json.RawMessage{"mode": json.RawMessage(`"multi"`)}}, nil); code != http.StatusOK {
		t.Fatalf("team mode = %d", code)
	}

	const key = "sk-e2e-setup-key-0000000000"
	var probe core.ProbeResult
	test := dto.ProviderTestReq{Kind: core.KindOpenAICompat, BaseURL: g.LLM.URL(), APIKey: key, AllowPrivate: true}
	if code := g.JSON(t, http.MethodPost, "/api/providers/test", test, &probe); code != http.StatusOK || !probe.OK || probe.Model != model {
		t.Fatalf("probe = %d %+v", code, probe)
	}
	if len(probe.Choices) != 1 || !probe.Choices[0].Recommended || !probe.Choices[0].QuickTasksDefault || probe.Choices[0].DisplayName != "Mock Model" {
		t.Fatalf("choices = %+v", probe.Choices)
	}

	var conn dto.ProviderConnectResp
	req := dto.ProviderConnectReq{Kind: core.KindOpenAICompat, BaseURL: g.LLM.URL(), APIKey: key, AllowPrivate: true, Model: model}
	if code := g.JSON(t, http.MethodPost, "/api/providers/connect", req, &conn); code != http.StatusOK ||
		conn.Provider != "custom" || conn.Main != "custom/"+model || conn.Secret == nil || conn.Secret.Name != "custom_api_key" {
		t.Fatalf("connect = %d %+v", code, conn)
	}
	var models []core.ModelInfo
	if code := g.JSON(t, http.MethodGet, "/api/models", nil, &models); code != http.StatusOK {
		t.Fatalf("models = %d", code)
	}
	found := false
	for _, m := range models {
		if m.Ref.String() == "custom/"+model && m.DisplayName == "Mock Model" && m.Recommended && m.ProviderName == "Model server" {
			found = true
		}
	}
	if !found {
		t.Fatalf("models = %+v", models)
	}
	if code := g.JSON(t, http.MethodPost, "/api/setup/complete", nil, nil); code != http.StatusNoContent {
		t.Fatalf("complete = %d", code)
	}

	var sess core.Session
	if code := g.JSON(t, http.MethodPost, "/api/sessions", dto.SessionCreateReq{}, &sess); code/100 != 2 {
		t.Fatalf("session = %d", code)
	}
	if code := g.JSON(t, http.MethodPost, "/api/sessions/"+string(sess.ID)+"/messages", dto.SendReq{Text: "hello"}, nil); code/100 != 2 {
		t.Fatalf("send = %d", code)
	}
	waitForReply(t, g, sess.ID, "Hi from the mock.")
	for _, r := range g.LLM.History() {
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Fatalf("mockllm request without the saved key: %s %v", r.Path, r.Header)
		}
	}

	var created dto.UserCreateResp
	if code := g.JSON(t, http.MethodPost, "/api/users", dto.UserCreateReq{Username: "jordan", DisplayName: "Jordan Kim", Role: core.RoleMember}, &created); code != http.StatusOK ||
		created.OneTimePassword == "" || !created.MustChangePassword {
		t.Fatalf("add person = %d %+v", code, created)
	}
	teammate := newBrowser(t, g.BaseURL)
	var tm dto.Me
	if code := teammate.json(t, http.MethodPost, "/api/auth/login", dto.LoginReq{Username: "jordan", Password: created.OneTimePassword}, &tm); code != http.StatusOK ||
		!tm.User.MustChangePassword {
		t.Fatalf("one-time sign-in = %d %+v", code, tm.User)
	}
	teammate.csrf = tm.CSRF
	if code := teammate.json(t, http.MethodGet, "/api/sessions", nil, nil); code != http.StatusForbidden {
		t.Fatalf("sessions before the new password = %d", code)
	}
	if code := teammate.json(t, http.MethodPost, "/api/auth/password", dto.PasswordReq{New: "Harbour-Lantern-Quietly-Folds"}, nil); code != http.StatusNoContent {
		t.Fatalf("new password = %d", code)
	}
	var after dto.Me
	if code := teammate.json(t, http.MethodGet, "/api/auth/me", nil, &after); code != http.StatusOK || after.User.MustChangePassword {
		t.Fatalf("me after the change = %d %+v", code, after.User)
	}
	if code := teammate.json(t, http.MethodGet, "/api/sessions", nil, nil); code != http.StatusOK {
		t.Fatalf("sessions after the new password = %d", code)
	}
}

func waitForReply(t *testing.T, g *Gateway, id core.SessionID, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var page dto.MessagePage
		if code := g.JSON(t, http.MethodGet, "/api/sessions/"+string(id)+"/messages", nil, &page); code == http.StatusOK {
			for _, m := range page.Messages {
				for _, p := range m.Parts {
					if m.Role == core.MsgAssistant && strings.Contains(p.Text, want) {
						return
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no reply %q\nlogs:\n%s", want, g.Logs())
}

// browser is a second signed-in person with their own cookies.
type browser struct {
	base string
	c    *http.Client
	csrf string
}

func newBrowser(t *testing.T, base string) *browser {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &browser{base: base, c: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

func (b *browser) json(t *testing.T, method, path string, in, out any) int {
	t.Helper()
	var body *bytes.Reader
	if in != nil {
		j, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(j)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, b.base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if b.csrf != "" && method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", b.csrf)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

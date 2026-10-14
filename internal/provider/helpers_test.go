package provider

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
	"github.com/rielicat/bothy/internal/testkit/sandboxtest"
)

// canary is the API key every test provider uses; no URL, log line or
// error may contain it.
const canary = "sk-ant-CANARY-91f7e2b4c3"

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

// view is a config.View whose snapshot tests can replace (hot reload).
type view struct {
	mu sync.Mutex
	c  *config.Config
}

func (v *view) Current() *config.Config {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.c
}

func (v *view) Watch(func(old, cur *config.Config)) func() { return func() {} }

func (v *view) set(c *config.Config) {
	v.mu.Lock()
	v.c = c
	v.mu.Unlock()
}

type harness struct {
	f      *Facade
	view   *view
	vault  *fakes.Vault
	egress *sandboxtest.Egress
	clock  *fakes.Clock
	logs   *syncBuffer
	sleeps []time.Duration
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// isolateAWS keeps the AWS SDK, which reads the process environment and
// ~/.aws itself, away from the developer's credentials.
func isolateAWS(t *testing.T) string {
	t.Helper()
	for _, v := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_AUTHORIZATION_TOKEN",
		"AWS_EC2_METADATA_SERVICE_ENDPOINT", "AWS_ENDPOINT_URL", "AWS_USE_FIPS_ENDPOINT", "AWS_USE_DUALSTACK_ENDPOINT", "AWS_CA_BUNDLE"} {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(home, ".aws", "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, ".aws", "credentials"))
	return home
}

func newHarness(t *testing.T, providers []config.Provider, main ...string) *harness {
	t.Helper()
	isolateAWS(t)
	cfg := config.Default()
	cfg.Providers = providers
	cfg.Models = config.Models{Main: main}
	h := &harness{
		view:  &view{c: cfg},
		vault: fakes.NewVault(map[string]string{"test_key": canary}),
		clock: fakes.NewClock(t0),
		logs:  &syncBuffer{},
		egress: &sandboxtest.Egress{Client: &http.Client{
			Transport: http.DefaultTransport.(*http.Transport).Clone(),
		}},
	}
	f, err := New(Deps{
		Config: h.view, Vault: h.vault, Egress: h.egress, Clock: h.clock,
		Log: slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.jitter = func() float64 { return 0.5 }
	f.sleep = func(_ context.Context, d time.Duration) error {
		h.sleeps = append(h.sleeps, d)
		return nil
	}
	h.f = f
	return h
}

// cfgProvider is an enabled provider pointed at a test server.
func cfgProvider(id string, kind core.ProviderKind, base string, models ...config.ProviderModel) config.Provider {
	return config.Provider{ID: id, Type: string(kind), BaseURL: base, APIKey: "secret:test_key", AllowPrivate: true, Models: models}
}

// reply is one canned HTTP response.
type reply struct {
	status int
	ctype  string
	header map[string]string
	body   string
}

func sse(body string) reply { return reply{status: 200, ctype: "text/event-stream", body: body} }

func jsonReply(status int, body string) reply {
	return reply{status: status, ctype: "application/json", body: body}
}

// server replies in order (the last reply repeats) and counts requests.
type server struct {
	*httptest.Server
	mu      sync.Mutex
	replies []reply
	bodies  []string
	urls    []string
	headers []http.Header
}

func newServer(t *testing.T, rs ...reply) *server {
	t.Helper()
	s := &server{replies: rs}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		n := len(s.bodies)
		s.bodies = append(s.bodies, string(b))
		s.urls = append(s.urls, r.URL.RequestURI())
		s.headers = append(s.headers, r.Header.Clone())
		rp := s.replies[min(n, len(s.replies)-1)]
		s.mu.Unlock()
		for k, v := range rp.header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", rp.ctype)
		w.WriteHeader(rp.status)
		_, _ = io.WriteString(w, rp.body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func (s *server) body(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[i]
}

// okStream is an OpenAI-format stream (the openai-python recording's
// frames) with text "Hi" and reported usage.
const okStream = `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"logprobs":null,"finish_reason":null}]}

data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"logprobs":null,"finish_reason":"stop"}]}

data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,"prompt_tokens_details":{"cached_tokens":400}}}

data: [DONE]

`

const noUsageStream = `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello there"},"finish_reason":null}]}

data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`

// midStream sends a text delta, then an error chunk.
const midStream = `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"Partial"},"finish_reason":null}]}

data: {"error":{"message":"The server had an error while processing your request.","type":"server_error","param":null,"code":null}}

`

func userReq(m core.ModelInfo) (*core.Request, error) {
	return &core.Request{Model: m.Ref.Model, Messages: []core.Message{{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: "hello"}}}}}, nil
}

func errBody(code, typ, msg string) string {
	return `{"error":{"message":"` + msg + `","type":"` + typ + `","param":null,"code":` + code + `}}`
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}

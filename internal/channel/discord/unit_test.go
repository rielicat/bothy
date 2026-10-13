package discord

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func TestRender(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"**bold** and *italic* and `co_de`", "**bold** and *italic* and `co_de`"},
		{"snake_case ~tilde~ a|b <@123> <t:1>", `snake_case ~tilde~ a|b \<@123> \<t:1>`},
		{"~~strike~~ ||spoiler|| __init__ a_b_ c _x_ y_", `\~\~strike\~\~ \|\|spoiler\|\| \_\_init\_\_ a_b\_ c \_x\_ y\_`},
		{`\||x|| café_x`, `\|\|x\|\| café\_x`},
		{"| a | b |\n|---|---|", "| a | b |\n|---|---|"},
		{"# Title_1\n## Sub_", "**Title_1**\n**Sub\\_**"},
		{"> not a quote\n-# not subtext\n- list item", "\\> not a quote\n\\-# not subtext\n- list item"},
		{"```go\nx := a_b || c\n```\nafter_", "```go\nx := a_b || c\n```\nafter\\_"},
		{"see [the_docs](https://example.com/a_b)", `see [the_docs](https://example.com/a_b)`},
		{"[evil](hxxps://evil[.]com)", "evil (hxxps://evil[.]com)"},
		{`escaped \_ stays \*`, `escaped \_ stays \*`},
		{"``a ` b`` and `unclosed", "``a ` b`` and `unclosed"},
		{"[not a link] (x)", "[not a link] (x)"},
		{"héllo_wörld", `héllo_wörld`},
	}
	for _, tt := range tests {
		if got := render(tt.in); got != tt.want {
			t.Errorf("render(%q) =\n%q\nwant\n%q", tt.in, got, tt.want)
		}
	}
}

func TestTableFitsContentLimit(t *testing.T) {
	row := "| cfg_key | value_x | notes |\n"
	table := strings.Repeat(row, maxLen/len(row))
	if n := len([]rune(render(table))); n > contentLimit {
		t.Fatalf("a %d-rune markdown table rendered to %d runes, over Discord's %d", len(table), n, contentLimit)
	}
}

func TestContentTruncatesOverflow(t *testing.T) {
	a := &adapter{log: slog.New(slog.DiscardHandler)}
	got := a.content(strings.Repeat("_", maxLen))
	if n := len([]rune(got)); n != contentLimit || !strings.HasSuffix(got, "…") {
		t.Fatalf("escaped overflow rendered to %d runes, want exactly %d ending in an ellipsis", n, contentLimit)
	}
	if got := a.content(strings.Repeat("a", maxLen)); got != strings.Repeat("a", maxLen) {
		t.Fatal("MaxLen of plain text was altered")
	}
}

func TestStripMention(t *testing.T) {
	self := snowflake.ID(42)
	for _, tt := range []struct {
		in, want string
		found    bool
	}{
		{"<@42> hi", "hi", true},
		{"hey <@!42>, there", "hey , there", true},
		{"<@43> hi", "<@43> hi", false},
		{"  plain  ", "plain", false},
	} {
		got, found := stripMention(tt.in, self)
		if got != tt.want || found != tt.found {
			t.Errorf("stripMention(%q) = %q, %v; want %q, %v", tt.in, got, found, tt.want, tt.found)
		}
	}
}

func TestGatewayErrClassification(t *testing.T) {
	for _, tt := range []struct {
		err  error
		auth string
	}{
		{fmt.Errorf("failed to open gateway connection: %w", &websocket.CloseError{Code: 4004, Text: "Authentication failed."}), tokenHint},
		{&websocket.CloseError{Code: 4014, Text: "Disallowed intent(s)."}, intentHint},
		{&websocket.CloseError{Code: 4000, Text: "Unknown error"}, ""},
		{errors.New("websocket: close 4004: text alone is not a close frame"), ""},
		{errors.New("dial tcp: connection refused"), ""},
	} {
		err := gatewayErr(tt.err)
		ae, ok := errors.AsType[*authError](err)
		if (tt.auth != "") != ok || (ok && ae.hint != tt.auth) {
			t.Errorf("gatewayErr(%q) = %v, want auth hint %q", tt.err, err, tt.auth)
		}
	}
	if err := gatewayErr(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("gatewayErr(Canceled) = %v", err)
	}
}

func TestInboundQueueDropsWhenFull(t *testing.T) {
	a := &adapter{log: slog.New(slog.DiscardHandler), clock: core.SystemClock{}}
	jobs := make(chan job, 2)
	h := a.handler(context.Background(), jobs)
	for i := range 5 {
		h(nil, gateway.EventTypeMessageCreate, i+1, gateway.EventMessageCreate{Message: discord.Message{ID: snowflake.ID(i + 1)}})
	}
	if len(jobs) != 2 || a.dropped.Load() != 3 {
		t.Fatalf("queued %d, dropped %d; want 2 queued and 3 dropped without blocking", len(jobs), a.dropped.Load())
	}
	if a.resume.Seq != 5 {
		t.Fatalf("tracked seq %d, want 5", a.resume.Seq)
	}
}

func TestSystemDialerFailsClosed(t *testing.T) {
	proxy, _ := url.Parse("http://proxy.internal:3128")
	proxied := &http.Transport{DialContext: (&net.Dialer{}).DialContext, Proxy: http.ProxyURL(proxy)}
	for _, c := range []*http.Client{nil, {}, {Transport: roundTripper(nil)}, {Transport: &http.Transport{}}, {Transport: proxied}} {
		deps := core.AdapterDeps{HTTP: func(core.SystemDest) *http.Client { return c }}
		for _, secure := range []bool{false, true} {
			if _, err := systemDialer(deps, core.SystemDest{Hosts: []string{"gateway.discord.gg:443"}}, secure); !errors.Is(err, core.ErrForbidden) {
				t.Fatalf("systemDialer(secure=%v) with client %+v = %v, want ErrForbidden", secure, c, err)
			}
		}
	}
	direct := &http.Transport{DialContext: (&net.Dialer{}).DialContext, Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }}
	deps := core.AdapterDeps{HTTP: func(core.SystemDest) *http.Client { return &http.Client{Transport: direct} }}
	if _, err := systemDialer(deps, core.SystemDest{Hosts: []string{"gateway.discord.gg:443"}}, true); err != nil {
		t.Fatalf("systemDialer with a direct proxy func = %v", err)
	}
}

func TestPrivateAddressesOnlyForConfiguredHosts(t *testing.T) {
	cfg := config.Default().Channels.Discord
	cfg.APIBase = "http://LOCALHOST:8080/api/v10"
	cfg.GatewayURL = "ws://127.0.0.1:9090/gateway"
	a, err := newAdapter(cfg, core.AdapterDeps{
		Secret: func(context.Context, string) (core.Secret, error) { return core.Secret{}, nil },
		HTTP:   func(core.SystemDest) *http.Client { return newPlainClient() },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, hp := range []string{"localhost:8080", "127.0.0.1:9090"} {
		if !a.dest(hp).AllowPrivate {
			t.Fatalf("dest(%s): an admin-set endpoint must admit private addresses", hp)
		}
	}
	// Hosts learned at run time (resume URLs, GET /gateway/bot, CDN links)
	// come from the server, not the admin.
	for _, hp := range []string{"gateway-us-east1-b.discord.gg:443", "cdn.discordapp.com:443", "10.0.0.5:9090", "127.0.0.1:8080"} {
		if a.dest(hp).AllowPrivate {
			t.Fatalf("dest(%s) admits private addresses", hp)
		}
	}
}

func TestInteractionTokensStayOutOfLogsAndErrors(t *testing.T) {
	const token = "aW50ZXJhY3Rpb246c2VjcmV0"
	var buf bytes.Buffer
	log := slog.New(quietHandler{slog.NewTextHandler(&buf, nil)}).With("url", "/webhooks/1/"+token)
	log.Warn("rate limit exceeded", "endpoint", "/interactions/123/"+token+"/callback",
		"err", errors.New(`Post "https://discord.com/api/v10/webhooks/2/`+token+`": EOF`),
		slog.Group("g", "path", "/webhooks/3/"+token+"?wait=true"))
	if out := buf.String(); strings.Contains(out, token) || !strings.Contains(out, "/interactions/123/<token>/callback") {
		t.Fatalf("log output leaks the interaction token: %s", out)
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	cfg := config.Default().Channels.Discord
	cfg.APIBase = srv.URL + "/api/v10"
	srv.Close()
	a, err := newAdapter(cfg, core.AdapterDeps{
		Secret: func(context.Context, string) (core.Secret, error) { return core.NewSecret([]byte("tok")), nil },
		HTTP:   func(core.SystemDest) *http.Client { return newPlainClient() },
	})
	if err != nil {
		t.Fatal(err)
	}
	err = a.Ack(context.Background(), core.ActionEvent{AckRef: "1100000000000000002:" + token}, "Approved")
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("Ack against a dead API = %v, want an error without the interaction token", err)
	}
	if err := a.client().DeferUpdate(context.Background(), 1, token); err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("DeferUpdate against a dead API = %v, want an error without the interaction token", err)
	}
}

func TestNilEgressClientFailsClosed(t *testing.T) {
	a, err := newAdapter(config.Default().Channels.Discord, core.AdapterDeps{
		Secret: func(context.Context, string) (core.Secret, error) { return core.NewSecret([]byte("tok")), nil },
		HTTP:   func(core.SystemDest) *http.Client { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := a.Send(ctx, core.OutMessage{Chat: core.ChatRef{Channel: name, ChatID: "1"}, Text: "x"}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("Send without an egress client = %v, want ErrForbidden", err)
	}
	if _, err := a.Fetch(ctx, core.Attachment{FetchRef: "https://cdn.discordapp.com/a"}, 10); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("Fetch without an egress client = %v, want ErrForbidden", err)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestQuietHandlerDropsDebug(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(quietHandler{slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})}).With("k", "v")
	log.Debug("sending gateway message", "data", `{"token":"x"}`)
	log.Warn("reconnecting")
	if out := buf.String(); strings.Contains(out, "token") || !strings.Contains(out, "reconnecting") || !strings.Contains(out, "k=v") {
		t.Fatalf("log output %q", out)
	}
}

func TestCappedBody(t *testing.T) {
	read := func(body string, limit int64) (string, error) {
		b, err := io.ReadAll(&cappedBody{ReadCloser: io.NopCloser(strings.NewReader(body)), left: limit})
		return string(b), err
	}
	if got, err := read("12345", 5); got != "12345" || err != nil {
		t.Fatalf("exactly at the limit: %q, %v", got, err)
	}
	if _, err := read("123456", 5); !errors.Is(err, errTooLarge) {
		t.Fatalf("over the limit: %v, want errTooLarge", err)
	}
}

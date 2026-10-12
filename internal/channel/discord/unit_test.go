package discord

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"

	"github.com/bothyhq/bothy/internal/core"
)

func TestRender(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"**bold** and *italic* and `co_de`", "**bold** and *italic* and `co_de`"},
		{"snake_case ~strike~ a|b <@123> <t:1>", `snake\_case \~strike\~ a\|b \<@123> \<t:1>`},
		{"# Title_1\n## Sub", "**Title\\_1**\n**Sub**"},
		{"> not a quote\n-# not subtext\n- list item", "\\> not a quote\n\\-# not subtext\n- list item"},
		{"```go\nx := a_b || c\n```\nafter_", "```go\nx := a_b || c\n```\nafter\\_"},
		{"see [the_docs](https://example.com/a_b)", `see [the\_docs](https://example.com/a_b)`},
		{"[evil](hxxps://evil[.]com)", "evil (hxxps://evil[.]com)"},
		{`escaped \_ stays \*`, `escaped \_ stays \*`},
		{"``a ` b`` and `unclosed", "``a ` b`` and `unclosed"},
		{"[not a link] (x)", "[not a link] (x)"},
		{"héllo_wörld", `héllo\_wörld`},
	}
	for _, tt := range tests {
		if got := render(tt.in); got != tt.want {
			t.Errorf("render(%q) =\n%q\nwant\n%q", tt.in, got, tt.want)
		}
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
		msg  string
		auth string
	}{
		{"failed to open gateway connection: websocket: close 4004: Authentication failed.", tokenHint},
		{"websocket: close 4014: Disallowed intent(s).", intentHint},
		{"websocket: close 4000: Unknown error", ""},
		{"dial tcp: connection refused", ""},
	} {
		err := gatewayErr(errors.New(tt.msg))
		ae, ok := errors.AsType[*authError](err)
		if (tt.auth != "") != ok || (ok && ae.hint != tt.auth) {
			t.Errorf("gatewayErr(%q) = %v, want auth hint %q", tt.msg, err, tt.auth)
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
	for _, c := range []*http.Client{nil, {}, {Transport: roundTripper(nil)}, {Transport: &http.Transport{}}} {
		deps := core.AdapterDeps{HTTP: func(core.SystemDest) *http.Client { return c }}
		if _, _, err := systemDialer(deps, core.SystemDest{Hosts: []string{"gateway.discord.gg:443"}}); !errors.Is(err, core.ErrForbidden) {
			t.Fatalf("systemDialer with client %+v = %v, want ErrForbidden", c, err)
		}
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

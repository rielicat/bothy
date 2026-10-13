package slack

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func TestRender(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"**bold** and *italic* and `x < y`", "*bold* and _italic_ and `x &lt; y`"},
		{"<!channel> <@U123> a & b", "&lt;!channel&gt; &lt;@U123&gt; a &amp; b"},
		{"[docs](https://example.com/a?b=1&c=|>)", "<https://example.com/a?b=1&amp;c=%7C%3E|docs>"},
		{"[evil](hxxps://evil[.]com)", "evil (hxxps://evil[.]com)"},
		{"# Title **x**\n- one\n* two\n  - nested", "*Title x*\n• one\n• two\n  • nested"},
		{"```go\nif a < b && c {}\n```\ndone", "```\nif a &lt; b &amp;&amp; c {}\n```\ndone"},
		{"```inline``` code", "`inline` code"},
		{"2 * 3 * 4 and snake_case", "2 * 3 * 4 and snake_case"},
		{`not \*italic\*`, `not *italic*`},
		{"**unclosed and *also", "**unclosed and *also"},
		{"[a|b](https://x.example)", "<https://x.example|a¦b>"},
	}
	for _, tt := range tests {
		if got := render(tt.in); got != tt.want {
			t.Errorf("render(%q) =\n%q\nwant\n%q", tt.in, got, tt.want)
		}
	}
}

func TestPlainText(t *testing.T) {
	tests := []struct{ in, want string }{
		{"hi <@U123|alice> in <#C1|general>", "hi @alice in #general"},
		{"<@U999> and <!here> and <!subteam^S1|@devs>", "@U999 and @here and @devs"},
		{"<http://example.com|example.com> <https://x.example/a|docs> <mailto:a@b.co|a@b.co>", "http://example.com docs (https://x.example/a) mailto:a@b.co"},
		{"a &lt;b&gt; &amp;lt;", "a <b> &lt;"},
	}
	for _, tt := range tests {
		if got := plainText(tt.in); got != tt.want {
			t.Errorf("plainText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStripMentionAndTimestamps(t *testing.T) {
	got, found := stripMention("<@UBOT|bothy> hi <@UOTHER>", "UBOT")
	if got != "hi <@UOTHER>" || !found {
		t.Fatalf("stripMention = %q, %v", got, found)
	}
	if _, found := stripMention("hello", "UBOT"); found {
		t.Fatal("found a mention in plain text")
	}
	if ts := tsTime("1760000000.000123"); !ts.Equal(time.Unix(1760000000, 123000)) {
		t.Fatalf("tsTime = %v", ts)
	}
	if !tsTime("garbage").IsZero() {
		t.Fatal("tsTime accepted garbage")
	}
}

func TestSectionsSplitAtLines(t *testing.T) {
	text := strings.Repeat("x", 2000) + "\n" + strings.Repeat("y", 2000)
	blocks := sections(text)
	if len(blocks) != 2 {
		t.Fatalf("%d sections, want 2", len(blocks))
	}
	if s := blocks[0].(*slack.SectionBlock).Text.Text; s != strings.Repeat("x", 2000) {
		t.Fatalf("first section has %d runes, want the first line", len(s))
	}
	if n := len(sections(strings.Repeat("z", 7000))); n != 3 {
		t.Fatalf("an unbroken 7000-rune text gave %d sections, want 3", n)
	}
}

func TestSocketErrClassification(t *testing.T) {
	if !isAuthErr(socketErr(errors.New("invalid_auth"))) || !isAuthErr(socketErr(slack.StatusCodeError{Code: http.StatusNotFound})) {
		t.Fatal("token rejections must be auth errors")
	}
	if isAuthErr(socketErr(errors.New("dial tcp: connection refused"))) {
		t.Fatal("a network error is not an auth error")
	}
	if err := socketErr(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("socketErr(Canceled) = %v", err)
	}
}

// fakeSocket records acknowledgements; it never connects.
type fakeSocket struct {
	mu   sync.Mutex
	acks []string
}

func (s *fakeSocket) Run(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

func (s *fakeSocket) Events() <-chan socketmode.Event { return nil }

func (s *fakeSocket) Ack(_ context.Context, id string) error {
	s.mu.Lock()
	s.acks = append(s.acks, id)
	s.mu.Unlock()
	return nil
}

func TestFullQueueLeavesRequestUnacked(t *testing.T) {
	a := &adapter{log: slog.New(slog.DiscardHandler), clock: core.SystemClock{}}
	a.setSelf("UBOT", "BBOT", "bothy")
	sock := &fakeSocket{}
	jobs := make(chan job, 1)
	event := func(id string) socketmode.Event {
		return socketmode.Event{Type: socketmode.EventTypeEventsAPI, Request: &socketmode.Request{EnvelopeID: id},
			Data: slackevents.EventsAPIEvent{Type: slackevents.CallbackEvent, InnerEvent: slackevents.EventsAPIInnerEvent{
				Data: &slackevents.MessageEvent{Type: "message", Channel: "D1", ChannelType: "im", User: "U1", Text: "hi " + id, TimeStamp: "1.000001"}}}}
	}
	a.onEvent(context.Background(), sock, jobs, event("e1"))
	a.onEvent(context.Background(), sock, jobs, event("e2"))
	if len(sock.acks) != 1 || sock.acks[0] != "e1" || a.dropped.Load() != 1 {
		t.Fatalf("acks %v, dropped %d; want only the queued request acknowledged", sock.acks, a.dropped.Load())
	}
	a.onEvent(context.Background(), sock, jobs, socketmode.Event{Type: socketmode.EventTypeEventsAPI, Request: &socketmode.Request{EnvelopeID: "e3"},
		Data: slackevents.EventsAPIEvent{Type: slackevents.CallbackEvent, InnerEvent: slackevents.EventsAPIInnerEvent{
			Data: &slackevents.MessageEvent{Type: "message", SubType: "message_changed", Channel: "D1", User: "U1"}}}})
	if len(sock.acks) != 2 || sock.acks[1] != "e3" {
		t.Fatalf("acks %v: an event the hub does not need must still be acknowledged", sock.acks)
	}
}

func TestSystemDialerFailsClosed(t *testing.T) {
	proxy, _ := url.Parse("http://proxy.internal:3128")
	proxied := &http.Transport{DialContext: (&net.Dialer{}).DialContext, Proxy: http.ProxyURL(proxy)}
	for _, c := range []*http.Client{nil, {}, {Transport: &http.Transport{}}, {Transport: proxied}} {
		deps := core.AdapterDeps{HTTP: func(core.SystemDest) *http.Client { return c }}
		for _, secure := range []bool{false, true} {
			if _, err := systemDialer(deps, core.SystemDest{Hosts: []string{"wss-primary.slack.com:443"}}, secure); !errors.Is(err, core.ErrForbidden) {
				t.Fatalf("systemDialer(secure=%v) with client %+v = %v, want ErrForbidden", secure, c, err)
			}
		}
	}
	// A proxy function that leaves this destination direct is fine.
	direct := &http.Transport{DialContext: (&net.Dialer{}).DialContext, Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }}
	deps := core.AdapterDeps{HTTP: func(core.SystemDest) *http.Client { return &http.Client{Transport: direct} }}
	if _, err := systemDialer(deps, core.SystemDest{Hosts: []string{"wss-primary.slack.com:443"}}, true); err != nil {
		t.Fatalf("systemDialer with a direct proxy func = %v", err)
	}
}

func TestPrivateAddressesOnlyForConfiguredAPIHost(t *testing.T) {
	cfg := config.Default().Channels.Slack
	cfg.APIBase = "http://LOCALHOST:8080/api"
	a, err := newAdapter(cfg, core.AdapterDeps{
		Secret: func(context.Context, string) (core.Secret, error) { return core.Secret{}, nil },
		HTTP:   func(core.SystemDest) *http.Client { return newPlainClient() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !a.dest("localhost:8080").AllowPrivate {
		t.Fatal("the admin-set api_base host must admit private addresses")
	}
	// Hosts handed out at run time (the Socket Mode URL, file and response
	// URLs) come from the server, not the admin.
	for _, hp := range []string{"wss-primary.slack.com:443", "files.slack.com:443", "hooks.slack.com:443", "localhost:8081", "10.0.0.1:8080"} {
		if a.dest(hp).AllowPrivate {
			t.Fatalf("dest(%s) admits private addresses", hp)
		}
	}
}

func TestActionOnThreadRootStaysInChannel(t *testing.T) {
	a := &adapter{log: slog.New(slog.DiscardHandler), clock: core.SystemClock{}}
	for _, tt := range []struct {
		name       string
		container  slack.Container
		msgThread  string
		wantThread string
	}{
		{"root via message", slack.Container{ChannelID: "C1", MessageTs: "1.000001"}, "1.000001", ""},
		{"root via container", slack.Container{ChannelID: "C1", MessageTs: "1.000001", ThreadTs: "1.000001"}, "1.000001", ""},
		{"reply in thread", slack.Container{ChannelID: "C1", MessageTs: "1.000002", ThreadTs: "1.000001"}, "1.000001", "1.000001"},
	} {
		cb := slack.InteractionCallback{Type: slack.InteractionTypeBlockActions, Container: tt.container, User: slack.User{ID: "U1"},
			ResponseURL: "https://hooks.slack.com/actions/x"}
		cb.Message.ThreadTimestamp = tt.msgThread
		cb.ActionCallback.BlockActions = []*slack.BlockAction{{Value: "s:run_1"}}
		var got core.ActionEvent
		a.actionJob(cb)(context.Background(), actionSink(func(ev core.ActionEvent) { got = ev }))
		if got.Chat.ThreadID != tt.wantThread || got.Msg.Chat != got.Chat || got.Msg.ID != tt.container.MessageTs {
			t.Errorf("%s: ActionEvent = %+v, want thread %q", tt.name, got, tt.wantThread)
		}
	}
}

type actionSink func(core.ActionEvent)

func (actionSink) OnMessage(context.Context, core.Inbound) {}

func (f actionSink) OnAction(_ context.Context, ev core.ActionEvent) { f(ev) }

func TestNilEgressClientFailsClosed(t *testing.T) {
	a, err := newAdapter(config.Default().Channels.Slack, core.AdapterDeps{
		Secret: func(context.Context, string) (core.Secret, error) { return core.NewSecret([]byte("xoxb-x")), nil },
		HTTP:   func(core.SystemDest) *http.Client { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := a.Send(ctx, core.OutMessage{Chat: core.ChatRef{Channel: name, ChatID: "C1"}, Text: "x"}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("Send without an egress client = %v, want ErrForbidden", err)
	}
	if _, err := a.Fetch(ctx, core.Attachment{FetchRef: "https://files.slack.com/a"}, 10); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("Fetch without an egress client = %v, want ErrForbidden", err)
	}
}

func TestCappedBody(t *testing.T) {
	read := func(body string, limit int64) (string, error) {
		b, err := io.ReadAll(&cappedBody{ReadCloser: io.NopCloser(strings.NewReader(body)), left: limit})
		return string(b), err
	}
	if got, err := read("abc", 3); got != "abc" || err != nil {
		t.Fatalf("exactly at the limit: %q, %v", got, err)
	}
	if _, err := read("abcd", 3); !errors.Is(err, errTooLarge) {
		t.Fatalf("over the limit: %v, want errTooLarge", err)
	}
}

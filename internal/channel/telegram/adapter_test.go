package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/channel/telegram/telegramtest"
	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/conformance"
	"github.com/bothyhq/bothy/internal/testkit/memstore"
)

const tokenRef = "secret:telegram_bot_token"

type env struct {
	srv   *telegramtest.Server
	a     *Adapter
	meta  core.MetaRepo
	mu    sync.Mutex
	dests []core.SystemDest
}

func newEnv(t *testing.T, mod ...func(*config.Telegram)) *env {
	t.Helper()
	e := &env{srv: telegramtest.New(telegramtest.Config{}), meta: memstore.New().Meta()}
	t.Cleanup(e.srv.Close)
	e.a = e.adapter(t, e.meta, mod...)
	return e
}

func (e *env) adapter(t *testing.T, meta core.MetaRepo, mod ...func(*config.Telegram)) *Adapter {
	t.Helper()
	cfg := config.Telegram{Enabled: true, Token: tokenRef, APIBase: e.srv.URL, Groups: config.Groups{MentionOnly: true}}
	for _, m := range mod {
		m(&cfg)
	}
	ch, err := New(cfg, core.AdapterDeps{
		Secret: func(_ context.Context, ref string) (core.Secret, error) {
			if ref != tokenRef {
				return core.Secret{}, core.ErrNotFound
			}
			return core.NewSecret([]byte(e.srv.Token)), nil
		},
		HTTP: func(d core.SystemDest) *http.Client {
			e.mu.Lock()
			e.dests = append(e.dests, d)
			e.mu.Unlock()
			return e.srv.Client()
		},
		Meta: meta, Log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ch.(*Adapter)
}

// sink records deliveries without blocking the adapter.
type sink struct {
	msgs chan core.Inbound
	acts chan core.ActionEvent
}

func newSink() *sink {
	return &sink{msgs: make(chan core.Inbound, 64), acts: make(chan core.ActionEvent, 64)}
}

func (s *sink) OnMessage(_ context.Context, in core.Inbound)    { s.msgs <- in }
func (s *sink) OnAction(_ context.Context, ev core.ActionEvent) { s.acts <- ev }

func (s *sink) next(t *testing.T) core.Inbound {
	t.Helper()
	select {
	case in := <-s.msgs:
		return in
	case <-time.After(5 * time.Second):
		t.Fatal("no inbound message delivered")
	}
	return core.Inbound{}
}

func (s *sink) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case in := <-s.msgs:
		t.Fatalf("unexpected delivery %+v", in)
	case <-time.After(d):
	}
}

// run starts the adapter and waits until it reports connected.
func (e *env) run(t *testing.T, a *Adapter, s core.InboundSink) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, s) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	waitFor(t, func() bool { return a.Status().State == "connected" })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConformance(t *testing.T) {
	e := newEnv(t)
	conformance.Channel(t, e.a, e.srv)
	if got := e.srv.Commands(); len(got) == 0 {
		t.Error("setMyCommands was not called on connect")
	}
}

func TestSystemDestIsExactHost(t *testing.T) {
	e := newEnv(t)
	if _, err := e.a.SelfTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(e.srv.URL, "http://")
	e.mu.Lock()
	d := e.dests[len(e.dests)-1]
	e.mu.Unlock()
	if d.Name != "channel:telegram" || len(d.Hosts) != 1 || d.Hosts[0] != host || !d.AllowPrivate {
		t.Fatalf("dest = %+v, want channel:telegram [%s] with private allowed for a custom api_base", d, host)
	}
	_ = e.a.Reconfigure(&config.Config{Channels: config.Channels{Telegram: config.Telegram{Enabled: true, Token: tokenRef}}})
	if _, err := e.a.newClient(context.Background(), e.a.cfg); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	d = e.dests[len(e.dests)-1]
	e.mu.Unlock()
	if d.Hosts[0] != "api.telegram.org:443" || d.AllowPrivate {
		t.Fatalf("default dest = %+v, want api.telegram.org:443 without private addresses", d)
	}
}

func TestSendUsesHTMLAndDisablesPreviews(t *testing.T) {
	e := newEnv(t)
	chat := core.ChatRef{Channel: Name, ChatID: "42", Kind: core.ChatDM}
	ctx := context.Background()
	ref, err := e.a.Send(ctx, core.OutMessage{Chat: chat, Text: "**see** https://example.com and `code`"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.a.Edit(ctx, ref, core.OutMessage{Chat: chat, Text: "edited [link](https://example.org)"}); err != nil {
		t.Fatal(err)
	}
	msgs := e.srv.Messages(42)
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v", msgs)
	}
	m := msgs[0]
	if m.ParseMode != "HTML" || !m.PreviewsDisabled || m.Raw != `edited <a href="https://example.org">link</a>` || m.Edits != 1 {
		t.Fatalf("message = %+v", m)
	}
	if !e.srv.PreviewsDisabled() {
		t.Fatal("a send or edit left link previews on")
	}
}

func TestHTMLInjectionEscaped(t *testing.T) {
	e := newEnv(t)
	chat := core.ChatRef{Channel: Name, ChatID: "42", Kind: core.ChatDM}
	evil := `<b>hi</b><script>alert(1)</script> & <a href="https://evil.example">x</a> "q" ` + "`</code><i>`"
	if _, err := e.a.Send(context.Background(), core.OutMessage{Chat: chat, Text: evil}); err != nil {
		t.Fatal(err)
	}
	m := e.srv.Messages(42)[0]
	want := `<b>hi</b><script>alert(1)</script> & <a href="https://evil.example">x</a> "q" </code><i>`
	if m.Text != want {
		t.Fatalf("visible text = %q, want the input shown literally %q", m.Text, want)
	}
	if strings.Contains(m.Raw, "<script>") || strings.Contains(m.Raw, `<a href="https://evil`) {
		t.Fatalf("raw HTML carries user markup: %q", m.Raw)
	}
}

func TestRateLimitHonoursRetryAfter(t *testing.T) {
	e := newEnv(t)
	e.srv.RateLimit("sendMessage", 1)
	start := time.Now()
	_, err := e.a.Send(context.Background(), core.OutMessage{Chat: core.ChatRef{Channel: Name, ChatID: "42"}, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < time.Second || e.srv.Calls("sendMessage") != 2 {
		t.Fatalf("sent after %v with %d calls; want a retry after retry_after=1s", time.Since(start), e.srv.Calls("sendMessage"))
	}
}

func TestInboundNormalization(t *testing.T) {
	e := newEnv(t)
	s := newSink()
	e.run(t, e.a, s)

	e.srv.Inject(telegramtest.Inbound{ChatID: -100, SenderID: 5, Username: "alice", FirstName: "Alice", LastName: "A", Text: "what's up", Mention: true})
	in := s.next(t)
	if in.Chat.Kind != core.ChatGroup || in.Chat.ChatID != "-100" || !in.Mentioned || in.Text != "what's up" ||
		in.SenderID != "5" || in.SenderName != "Alice A" || in.SenderUsername != "alice" || in.MsgID != "-100:1" {
		t.Fatalf("mentioned group message = %+v", in)
	}

	e.srv.Inject(telegramtest.Inbound{ChatID: -100, SenderID: 5, Text: "just chatting"})
	if in := s.next(t); in.Mentioned {
		t.Fatalf("unmentioned group message marked mentioned: %+v", in)
	}

	e.srv.Inject(telegramtest.Inbound{ChatID: -100, SenderID: 5, Text: "/stop@other_bot"})
	e.srv.Inject(telegramtest.Inbound{ChatID: -100, SenderID: 5, Text: "/stop@" + e.srv.Bot + " now"})
	if in := s.next(t); in.Text != "/stop now" || !in.Mentioned {
		t.Fatalf("command for this bot = %+v (the one for another bot must be dropped)", in)
	}

	_, botMsg := e.srv.Inject(telegramtest.Inbound{ChatID: -100, SenderID: 5, Text: "x"})
	s.next(t)
	e.srv.Inject(telegramtest.Inbound{ChatID: -100, SenderID: 5, Text: "replying", ReplyTo: botMsg})
	if in := s.next(t); !in.Mentioned {
		t.Fatalf("reply to the bot not marked mentioned: %+v", in)
	}

	e.srv.Inject(telegramtest.Inbound{ChatID: -100, SenderID: 5, Text: "topic", ThreadID: 7, Mention: true})
	if in := s.next(t); in.Chat.ThreadID != "7" {
		t.Fatalf("topic message thread = %q", in.Chat.ThreadID)
	}

	// A content-less message (a sticker, say) is dropped; the bot-added
	// update names who added the bot.
	e.srv.Inject(telegramtest.Inbound{ChatID: -200, ChatType: "group", SenderID: 5})
	e.srv.InjectBotAdded(-200, "Family", 9)
	in = s.next(t)
	if in.Chat.ChatID != "-200" || in.SenderID != "9" || in.AddedBy != "9" || in.ChatTitle != "Family" || in.Text != "" ||
		in.Mentioned || len(in.Attachments) != 0 || !strings.Contains(in.MsgID, ":joined:") {
		t.Fatalf("bot-added announcement = %+v", in)
	}
}

func TestCallbackAckAndFetch(t *testing.T) {
	e := newEnv(t)
	s := newSink()
	e.run(t, e.a, s)
	ctx := context.Background()

	doc := &telegramtest.File{ID: "doc1", Name: "report.pdf", MIME: "application/pdf", Data: []byte("%PDF-1.7 tiny")}
	e.srv.Inject(telegramtest.Inbound{ChatID: 5, SenderID: 5, Text: "see attached", Document: doc})
	in := s.next(t)
	if len(in.Attachments) != 1 || in.Text != "see attached" {
		t.Fatalf("document message = %+v", in)
	}
	att := in.Attachments[0]
	if att.Name != "report.pdf" || att.MIME != "application/pdf" || att.Size != int64(len(doc.Data)) {
		t.Fatalf("attachment = %+v", att)
	}
	rc, err := e.a.Fetch(ctx, att, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(data) != string(doc.Data) {
		t.Fatalf("fetched %q, %v", data, err)
	}
	if _, err := e.a.Fetch(ctx, att, 4); err == nil {
		t.Fatal("Fetch ignored the size limit")
	}

	ref, err := e.a.Send(ctx, core.OutMessage{Chat: core.ChatRef{Channel: Name, ChatID: "5", Kind: core.ChatDM}, Text: "card",
		Buttons: [][]core.Button{{{Label: "Deny", Data: "a:x:d:mac"}, {Label: "Web", URL: "https://bothy.example.com/approvals/x"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if b := e.srv.Messages(5)[0].Buttons; len(b) != 1 || len(b[0]) != 2 {
		t.Fatalf("keyboard = %+v", b)
	}
	e.srv.InjectButton("5", ref.ID, "5", "a:x:d:mac")
	var ev core.ActionEvent
	select {
	case ev = <-s.acts:
	case <-time.After(5 * time.Second):
		t.Fatal("press not delivered")
	}
	if ev.Msg.ID != ref.ID || ev.Data != "a:x:d:mac" || ev.AckRef == "" || ev.SenderID != "5" {
		t.Fatalf("action = %+v", ev)
	}
	if err := e.a.Ack(ctx, ev, "Denied"); err != nil {
		t.Fatal(err)
	}
	if a := e.srv.Answers(); len(a) != 1 || a[0].Text != "Denied" {
		t.Fatalf("answers = %+v", a)
	}
}

func TestDownloadRefusesDotSegments(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++ }))
	defer srv.Close()
	cl := &client{base: srv.URL, token: telegramtest.DefaultToken, http: srv.Client()}
	for _, p := range []string{"../bot1:x/sendMessage", "docs/./a", "docs//a", ""} {
		if rc, err := cl.download(context.Background(), p, 1<<20); err == nil {
			rc.Close()
			t.Fatalf("download(%q) succeeded", p)
		}
	}
	if hits != 0 {
		t.Fatalf("%d requests made for unusable file paths", hits)
	}
}

func TestOffsetPersistsAcrossRestarts(t *testing.T) {
	e := newEnv(t)
	s := newSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.a.Run(ctx, s) }()
	waitFor(t, func() bool { return e.a.Status().State == "connected" })
	upd, _ := e.srv.Inject(telegramtest.Inbound{ChatID: 5, SenderID: 5, Text: "first"})
	first := s.next(t)
	waitFor(t, func() bool { v, err := e.meta.Get(ctx, offsetKey); return err == nil && v == "2" })
	cancel()
	<-done

	b := e.adapter(t, e.meta)
	e.run(t, b, s)
	s.none(t, 300*time.Millisecond)
	if err := e.srv.Redeliver(upd); err != nil {
		t.Fatal(err)
	}
	if again := s.next(t); again.MsgID != first.MsgID {
		t.Fatalf("redelivered update has MsgID %q, want %q so the hub's dedup drops it", again.MsgID, first.MsgID)
	}
}

func TestAuthFailureNeverLeaksToken(t *testing.T) {
	e := newEnv(t)
	bad := e.adapter(t, e.meta)
	bad.deps.Secret = func(context.Context, string) (core.Secret, error) {
		return core.NewSecret([]byte("7000001:WRONG-token")), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bad.Run(ctx, newSink()) }()
	waitFor(t, func() bool { return bad.Status().State == "auth_failed" })
	st := bad.Status()
	cancel()
	<-done
	if !strings.Contains(st.Detail, "401") || strings.Contains(st.Detail, "WRONG-token") {
		t.Fatalf("auth_failed detail = %q", st.Detail)
	}
	if _, err := bad.SelfTest(context.Background()); err == nil || strings.Contains(err.Error(), "WRONG-token") {
		t.Fatalf("SelfTest error = %v", err)
	}

	e.srv.Close()
	_, err := e.a.Send(context.Background(), core.OutMessage{Chat: core.ChatRef{Channel: Name, ChatID: "1"}, Text: "x"})
	if err == nil || strings.Contains(err.Error(), e.srv.Token) {
		t.Fatalf("transport error = %v; it must not contain the token", err)
	}
}

func TestMissingSecretIsAuthFailure(t *testing.T) {
	e := newEnv(t, func(c *config.Telegram) { c.Token = "secret:other" })
	_, err := e.a.SelfTest(context.Background())
	var ae *authError
	if !errors.As(err, &ae) || !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("SelfTest error = %v, want an auth error wrapping ErrNotFound", err)
	}
}

func TestDisabledAdapterIdles(t *testing.T) {
	e := newEnv(t, func(c *config.Telegram) { c.Enabled = false })
	if st := e.a.Status(); st.State != "disabled" || st.Enabled {
		t.Fatalf("status = %+v", st)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.a.Run(ctx, newSink()) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := e.srv.Calls("getMe") + e.srv.Calls("getUpdates"); n != 0 {
		t.Fatalf("a disabled adapter made %d API calls", n)
	}
}

func TestDeepLinkAndOpenDM(t *testing.T) {
	e := newEnv(t)
	if got := e.a.DeepLink("7KQ2-MX4D"); got != "" {
		t.Fatalf("DeepLink before the username is known = %q", got)
	}
	id, err := e.a.SelfTest(context.Background())
	if err != nil || id != "@"+e.srv.Bot {
		t.Fatalf("SelfTest = %q, %v", id, err)
	}
	if got := e.a.DeepLink("7KQ2-MX4D"); got != "https://t.me/"+e.srv.Bot+"?start=7KQ2-MX4D" {
		t.Fatalf("DeepLink = %q", got)
	}
	if got := e.a.DeepLink("a b&c=d"); got != "https://t.me/"+e.srv.Bot+"?start=abcd" {
		t.Fatalf("DeepLink does not sanitize: %q", got)
	}
	chat, err := e.a.OpenDM(context.Background(), "12345")
	if err != nil || chat != (core.ChatRef{Channel: Name, ChatID: "12345", Kind: core.ChatDM}) {
		t.Fatalf("OpenDM = %+v, %v", chat, err)
	}
	if _, err := e.a.OpenDM(context.Background(), "abc"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("OpenDM(abc) = %v", err)
	}
}

func TestEditAndDeleteAreIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	chat := core.ChatRef{Channel: Name, ChatID: "42"}
	ref, err := e.a.Send(ctx, core.OutMessage{Chat: chat, Text: "same"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.a.Edit(ctx, ref, core.OutMessage{Chat: chat, Text: "same"}); err != nil {
		t.Fatalf("no-op edit = %v", err)
	}
	if err := e.a.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err := e.a.Delete(ctx, ref); err != nil {
		t.Fatalf("second delete = %v", err)
	}
	if err := e.a.Edit(ctx, ref, core.OutMessage{Chat: chat, Text: "gone"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("edit of a deleted message = %v, want ErrNotFound", err)
	}
	if err := e.a.Typing(ctx, core.ChatRef{Channel: Name, ChatID: "42", ThreadID: "3"}); err != nil {
		t.Fatal(err)
	}
	if acts := e.srv.ChatActions(); len(acts) != 1 || acts[0].Action != "typing" || acts[0].ThreadID != 3 {
		t.Fatalf("chat actions = %+v", acts)
	}
}

func TestKeyboard(t *testing.T) {
	kb, err := keyboard([][]core.Button{
		{{Label: "Deny", Data: "a:x:d:m"}, {Label: "Local", URL: "http://localhost:8080/approvals/x"}},
		{{Label: "Loop", URL: "http://127.0.0.1/a"}, {Label: "JS", URL: "javascript:alert(1)"}},
		{{Label: "Web", URL: "https://bothy.example.com/approvals/x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(kb.InlineKeyboard) != 2 || len(kb.InlineKeyboard[0]) != 1 || kb.InlineKeyboard[1][0].URL != "https://bothy.example.com/approvals/x" {
		t.Fatalf("keyboard = %+v; want loopback and non-http links dropped", kb)
	}
	if _, err := keyboard([][]core.Button{{{Label: "x", Data: strings.Repeat("d", 65)}}}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("65-byte callback data = %v", err)
	}
	if kb, err := keyboard(nil); kb != nil || err != nil {
		t.Fatalf("empty keyboard = %+v, %v", kb, err)
	}
}

func TestReconfigureDropsCachedClient(t *testing.T) {
	e := newEnv(t)
	if _, err := e.a.client(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := &config.Config{Channels: config.Channels{Telegram: e.a.cfg}}
	_ = e.a.Reconfigure(c)
	if e.a.cl == nil {
		t.Fatal("an unchanged section dropped the client")
	}
	c.Channels.Telegram.Token = "secret:rotated"
	_ = e.a.Reconfigure(c)
	if e.a.cl != nil || e.a.cfg.Token != "secret:rotated" {
		t.Fatal("a new token ref kept the old client")
	}
}

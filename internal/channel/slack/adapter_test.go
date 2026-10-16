package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/channel/slack/slacktest"
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/conformance"
)

func TestConformance(t *testing.T) {
	f := newFixture(t)
	conformance.Channel(t, f.a, f.srv)
}

func TestEnvelopesAckedWhileHubIsBusy(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	dm, user := f.srv.ConformanceIDs()
	f.sink.gate.Lock() // the hub is stuck on agent work
	for i := range 3 {
		f.srv.InjectText(dm, user, "busy "+string(rune('a'+i)), false)
	}
	waitFor(t, 3*time.Second, func() bool { return len(f.srv.Acks()) == 3 }, "three acks while the sink is blocked")
	for _, ack := range f.srv.Acks() {
		if ack.After >= 3*time.Second {
			t.Fatalf("envelope acked after %v, want < 3s", ack.After)
		}
	}
	f.sink.gate.Unlock()
	for range 3 {
		f.sink.message(t)
	}
}

func TestReconnectOnDisconnect(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	f.srv.Disconnect()
	waitFor(t, 10*time.Second, func() bool { return f.srv.Connections() == 2 && f.srv.Connected() }, "a second connection")
	opens := 0
	for _, c := range f.srv.Calls() {
		if c.Method == "apps.connections.open" {
			opens++
		}
	}
	if opens != 2 {
		t.Fatalf("apps.connections.open called %d times, want 2", opens)
	}
	dm, user := f.srv.ConformanceIDs()
	f.srv.InjectText(dm, user, "after reconnect", false)
	if in := f.sink.message(t); in.Text != "after reconnect" {
		t.Fatalf("got %+v", in)
	}
}

func TestReconnectAfterConnectionLoss(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	f.srv.Drop()
	dm, user := f.srv.ConformanceIDs()
	f.srv.InjectText(dm, user, "queued while down", false) // Slack holds unacknowledged events
	if in := f.sink.message(t); in.Text != "queued while down" {
		t.Fatalf("got %+v", in)
	}
	if n := f.srv.Connections(); n != 2 {
		t.Fatalf("%d connections, want 2", n)
	}
}

func TestButtonPressBecomesActionEvent(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	ctx := context.Background()
	const channel, user, thread = "C0000100", "U0000100", "1750000000.000001"
	chat := core.ChatRef{Channel: name, ChatID: channel, ThreadID: thread, Kind: core.ChatGroup}
	ref, err := f.a.Send(ctx, core.OutMessage{Chat: chat, Text: "Run `rm -rf build`?", Buttons: [][]core.Button{
		{{Label: "Approve", Data: "a:apr_1:o:mac", Style: core.BtnPrimary}, {Label: "Deny", Data: "a:apr_1:d:mac", Style: core.BtnDanger}},
		{{Label: "Open in web", URL: "https://bothy.example/approvals/apr_1"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if m := f.srv.Messages(channel)[0]; m.ThreadTS != thread {
		t.Fatalf("posted with thread_ts %q, want %q", m.ThreadTS, thread)
	}
	acks := len(f.srv.Acks())
	f.srv.InjectButton(channel, ref.ID, user, "a:apr_1:d:mac")
	ev := f.sink.action(t)
	want := core.ActionEvent{Chat: chat, Msg: ref, SenderID: user, Data: "a:apr_1:d:mac", AckRef: ev.AckRef}
	if ev != want || !strings.Contains(ev.AckRef, "/response/") {
		t.Fatalf("ActionEvent = %+v, want %+v with the response_url as AckRef", ev, want)
	}
	waitFor(t, 3*time.Second, func() bool { return len(f.srv.Acks()) > acks }, "the interactive envelope ack")
	if err := f.a.Ack(ctx, ev, "Denied"); err != nil {
		t.Fatal(err)
	}
	if r := f.srv.Responses(); len(r) != 1 || r[0].Text != "Denied" || r[0].ResponseType != "ephemeral" || r[0].ReplaceOriginal {
		t.Fatalf("response_url posts = %+v, want one ephemeral toast", r)
	}
	if err := f.a.Ack(ctx, core.ActionEvent{AckRef: "https://evil.example/hook"}, "x"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Ack to a foreign host = %v, want ErrInvalid", err)
	}
}

func TestButtonPressOnThreadRoot(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	ctx := context.Background()
	const channel, user = "C0000150", "U0000150"
	chat := core.ChatRef{Channel: name, ChatID: channel, Kind: core.ChatGroup}
	ref, err := f.a.Send(ctx, core.OutMessage{Chat: chat, Text: "Running…", Buttons: [][]core.Button{{{Label: "Stop", Data: "s:run_9", Style: core.BtnDanger}}}})
	if err != nil {
		t.Fatal(err)
	}
	// Someone replies in the card's thread, which makes Slack report the card
	// as the thread's root (thread_ts == ts).
	f.srv.Inject(slacktest.Incoming{ChatID: channel, SenderID: user, Text: "what is it doing?", ThreadTS: ref.ID})
	f.sink.message(t)
	f.srv.InjectButton(channel, ref.ID, user, "s:run_9")
	ev := f.sink.action(t)
	if ev.Chat != chat || ev.Msg != ref {
		t.Fatalf("ActionEvent chat %+v msg %+v, want %+v and the MsgRef Send returned (%+v)", ev.Chat, ev.Msg, chat, ref)
	}
}

func TestReplyInBotThreadIsMentioned(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	const channel, user = "C0000160", "U0000160"
	chat := core.ChatRef{Channel: name, ChatID: channel, Kind: core.ChatGroup}
	ref, err := f.a.Send(context.Background(), core.OutMessage{Chat: chat, Text: "Here is the summary."})
	if err != nil {
		t.Fatal(err)
	}
	f.srv.Inject(slacktest.Incoming{ChatID: channel, SenderID: user, Text: "and the details?", ThreadTS: ref.ID})
	if in := f.sink.message(t); !in.Mentioned || in.Chat.ThreadID != ref.ID {
		t.Fatalf("reply in the bot's thread = %+v, want Mentioned in thread %s", in, ref.ID)
	}
	f.srv.Inject(slacktest.Incoming{ChatID: channel, SenderID: user, Text: "side chat", ThreadTS: "1760000000.999999"})
	if in := f.sink.message(t); in.Mentioned {
		t.Fatalf("reply in someone else's thread = %+v, want not Mentioned", in)
	}
}

func TestAckErrorHidesResponseURL(t *testing.T) {
	f := newFixture(t)
	ackRef := strings.TrimSuffix(f.srv.URL(), "/api/") + "/response/secret-hook-path"
	f.srv.Close()
	err := f.a.Ack(context.Background(), core.ActionEvent{AckRef: ackRef}, "Approved")
	if err == nil || strings.Contains(err.Error(), "secret-hook-path") {
		t.Fatalf("Ack against a dead response_url = %v, want an error without the URL", err)
	}
}

func TestUnfurlsDisabledAndBlocks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const channel = "C0000200"
	chat := core.ChatRef{Channel: name, ChatID: channel, Kind: core.ChatGroup}
	ref, err := f.a.Send(ctx, core.OutMessage{Chat: chat, Text: "see https://example.com", Buttons: [][]core.Button{{{Label: "Stop", Data: "s:run_1", Style: core.BtnDanger}}}})
	if err != nil {
		t.Fatal(err)
	}
	var blocks []map[string]any
	if err := json.Unmarshal(f.srv.Messages(channel)[0].Blocks, &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0]["type"] != "section" || blocks[1]["type"] != "actions" {
		t.Fatalf("blocks = %v, want a section and an actions block", blocks)
	}
	el := blocks[1]["elements"].([]any)[0].(map[string]any)
	if el["value"] != "s:run_1" || el["style"] != "danger" || el["action_id"] == "" {
		t.Fatalf("button = %v", el)
	}
	if err := f.a.Edit(ctx, ref, core.OutMessage{Chat: chat, Text: "Stopped."}); err != nil {
		t.Fatal(err)
	}
	m := f.srv.Messages(channel)[0]
	if string(m.Blocks) != "[]" || m.Text != "Stopped." || m.Edits != 1 {
		t.Fatalf("after edit: %+v, want text only with blocks cleared", m)
	}
	if !f.srv.PreviewsDisabled() {
		t.Fatal("a post or update did not set unfurl_links=false and unfurl_media=false")
	}
	for _, bad := range [][][]core.Button{{{{Label: "x"}}}, {{{Data: "d"}}}} {
		if _, err := f.a.Send(ctx, core.OutMessage{Chat: chat, Text: "x", Buttons: bad}); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("Send with buttons %+v = %v, want ErrInvalid", bad, err)
		}
	}
	long := strings.Repeat("line of text that is long enough\n", 200)
	if _, err := f.a.Send(ctx, core.OutMessage{Chat: chat, Text: long, Buttons: [][]core.Button{{{Label: "Stop", Data: "s:run_2"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(f.srv.Messages(channel)[1].Blocks, &blocks); err != nil || len(blocks) != 4 {
		t.Fatalf("long text split into %d blocks (%v), want 3 sections + actions", len(blocks), err)
	}
}

func TestMentionDetection(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	const channel, user = "C0000300", "U0000300"
	f.srv.SetUser(user, "alice", "Alice A.")

	f.srv.InjectText(channel, user, "chatting", false)
	in := f.sink.message(t)
	if in.Mentioned || in.Chat != (core.ChatRef{Channel: name, ChatID: channel, Kind: core.ChatGroup}) || in.SenderName != "Alice A." || in.SenderUsername != "alice" {
		t.Fatalf("plain channel message = %+v", in)
	}

	ts := f.srv.Inject(slacktest.Incoming{ChatID: channel, SenderID: user, Text: "help me", Mention: true, ThreadTS: "1760000000.000009"})
	first, second := f.sink.message(t), f.sink.message(t)
	for _, in := range []core.Inbound{first, second} {
		if !in.Mentioned || in.Text != "help me" || in.MsgID != channel+":"+ts || in.Chat.ThreadID != "1760000000.000009" {
			t.Fatalf("mention = %+v, want Mentioned, stripped text, MsgID %s:%s and the thread", in, channel, ts)
		}
	}

	dm, _ := f.srv.ConformanceIDs()
	f.srv.InjectText(dm, user, "hi", false)
	if in = f.sink.message(t); !in.Mentioned || in.Chat.Kind != core.ChatDM {
		t.Fatalf("DM = %+v, want Mentioned in a DM", in)
	}

	f.srv.Inject(slacktest.Incoming{ChatID: channel, SenderID: slacktest.DefaultBotUser, Text: "own echo"})
	f.srv.InjectText(channel, user, "next", false)
	if in = f.sink.message(t); in.Text != "next" {
		t.Fatalf("got %+v, want the adapter's own message dropped", in)
	}
	calls := 0
	for _, c := range f.srv.Calls() {
		if c.Method == "users.info" {
			calls++
		}
	}
	if calls != 1 {
		t.Fatalf("users.info called %d times, want 1 (cached)", calls)
	}
}

func TestSlashCommand(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	dm, user := f.srv.ConformanceIDs()
	var in core.Inbound
	for _, tt := range []struct{ text, want string }{
		{"stop", "/stop"},
		{"Model openai/gpt-5.1", "/model openai/gpt-5.1"},
		{"", "/help"},
		{"what is <https://x.example|this>?", "what is this (https://x.example)?"},
	} {
		f.srv.InjectSlash(dm, user, tt.text)
		in = f.sink.message(t)
		if in.Text != tt.want || !in.Mentioned || in.SenderID != user || in.Chat.Kind != core.ChatDM || !strings.HasPrefix(in.MsgID, dm+":cmd:") ||
			!in.Private || in.ChatTitle != "" {
			t.Fatalf("/bothy %q = %+v, want text %q", tt.text, in, tt.want)
		}
	}
	if in.Chat.ChatID != dm {
		t.Fatalf("a command in the bot's own DM moved to %q, want %q", in.Chat.ChatID, dm)
	}
	// Typed in a DM between two people, which the bot cannot post in, the
	// command is answered in the sender's own DM with the bot.
	f.srv.InjectSlash("D0000999", user, "status")
	if in := f.sink.message(t); in.Chat.ChatID != dm || in.Chat.Kind != core.ChatDM || !in.Private {
		t.Fatalf("command in another DM = %+v, want it moved to %q", in, dm)
	}
}

// Only its sender sees a slash command, so it is Private wherever it was
// typed. A public channel is named by the payload's channel_name; for a
// private one Slack sends a placeholder, so the adapter asks
// conversations.info, which fails for one the app is not in.
func TestSlashCommandInChannel(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	const user = "U0000510"
	f.srv.SetChannel("C0000510", "ops")
	f.srv.SetChannel("G0000510", "secret-plans")

	f.srv.InjectSlash("C0000510", user, "pair ABCD-EFGH")
	in := f.sink.message(t)
	if !in.Private || in.Chat.Kind != core.ChatGroup || in.ChatTitle != "#ops" || in.Text != "/pair ABCD-EFGH" {
		t.Fatalf("slash command in a public channel = %+v", in)
	}
	if n := f.callsTo("conversations.info"); n != 0 {
		t.Fatalf("conversations.info called %d times for a channel the payload named", n)
	}

	f.srv.InjectSlash("G0000510", user, "help")
	if in = f.sink.message(t); !in.Private || in.Chat.Kind != core.ChatGroup || in.ChatTitle != "#secret-plans" {
		t.Fatalf("slash command in a private channel = %+v", in)
	}
	f.srv.InjectSlash("G0000511", user, "help")
	if in = f.sink.message(t); !in.Private || in.ChatTitle != "" {
		t.Fatalf("slash command in a private channel the app cannot see = %+v", in)
	}
}

// Group messages carry the channel's "#name", looked up once per channel;
// DMs carry none, and nothing but a slash command is Private.
func TestChannelTitles(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	const channel, user = "C0000520", "U0000520"
	f.srv.SetChannel(channel, "eng")
	for _, text := range []string{"one", "two"} {
		f.srv.InjectText(channel, user, text, false)
		if in := f.sink.message(t); in.ChatTitle != "#eng" || in.Private {
			t.Fatalf("channel message = %+v, want titled #eng and not private", in)
		}
	}
	dm, _ := f.srv.ConformanceIDs()
	f.srv.InjectText(dm, user, "hi", false)
	if in := f.sink.message(t); in.ChatTitle != "" || in.Private {
		t.Fatalf("DM = %+v, want no title", in)
	}
	if n := f.callsTo("conversations.info"); n != 1 {
		t.Fatalf("conversations.info called %d times, want 1 (cached, none for the DM)", n)
	}
}

// An app installed before the manifest asked for channels:read and
// groups:read gets missing_scope: messages still arrive, untitled, and the
// warning to reinstall is logged once, not once per channel.
func TestChannelTitleMissingScope(t *testing.T) {
	logs := &logRecorder{}
	f := newFixture(t, func(_ *config.Slack, d *core.AdapterDeps) { d.Log = slog.New(logs) })
	f.start(t, stateConnected)
	f.srv.SetChannel("C0000530", "eng")
	f.srv.SetChannelInfoError("missing_scope")
	for _, ch := range []string{"C0000530", "C0000531", "C0000530"} {
		f.srv.InjectText(ch, "U0000530", "hello", false)
		if in := f.sink.message(t); in.ChatTitle != "" || in.Text != "hello" {
			t.Fatalf("message without the scope = %+v", in)
		}
	}
	if n := logs.count("cannot read channel names"); n != 1 {
		t.Fatalf("missing scope warned %d times, want 1", n)
	}
	if n := f.callsTo("conversations.info"); n != 2 {
		t.Fatalf("conversations.info called %d times, want 2 (the miss is cached)", n)
	}
}

func TestFetchFile(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	dm, user := f.srv.ConformanceIDs()
	data := bytes.Repeat([]byte("y"), 2048)
	f.srv.Inject(slacktest.Incoming{ChatID: dm, SenderID: user, Text: "see file", Files: []slacktest.File{{Name: "a.csv", MIME: "text/csv", Data: data}}})
	in := f.sink.message(t)
	if len(in.Attachments) != 1 || in.Attachments[0].Name != "a.csv" || in.Attachments[0].Size != 2048 {
		t.Fatalf("attachments = %+v", in.Attachments)
	}
	ctx := context.Background()
	rc, err := f.a.Fetch(ctx, in.Attachments[0], 4096)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Fetch = %d bytes, %v", len(got), err)
	}
	if _, err := f.a.Fetch(ctx, in.Attachments[0], 100); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Fetch over the limit = %v, want ErrInvalid", err)
	}
	for _, ref := range []string{"https://evil.example/files/x", "http://files.slack.com/x", "https://files.slack.com.evil.example/x"} {
		if _, err := f.a.Fetch(ctx, core.Attachment{Name: "x", FetchRef: ref}, 100); !errors.Is(err, core.ErrForbidden) {
			t.Fatalf("Fetch(%s) = %v, want ErrForbidden: the bot token must not leave Slack", ref, err)
		}
	}
}

// servedManifest is the manifest GET /api/channels/slack/manifest serves
// (SPEC §8.6), as httpapi's golden test pins it.
const servedManifest = "../../httpapi/testdata/slack_manifest.golden.yaml"

// manifestScopes reads the bot scopes of the served manifest.
func manifestScopes(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(servedManifest)
	if err != nil {
		t.Fatal(err)
	}
	_, list, ok := strings.Cut(string(b), "bot: [")
	list, _, ok2 := strings.Cut(list, "]")
	if !ok || !ok2 {
		t.Fatalf("no bot scopes in %s", servedManifest)
	}
	var out []string
	for s := range strings.SplitSeq(list, ",") {
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

// methodScopes lists what each Web API call the adapter makes needs.
var methodScopes = map[string][]string{
	"auth.test":             nil,
	"apps.connections.open": nil, // app-level token with connections:write
	"chat.postMessage":      {"chat:write"},
	"chat.update":           {"chat:write"},
	"chat.delete":           {"chat:write"},
	"conversations.open":    {"im:write"},
	"users.info":            {"users:read"},
	"conversations.info":    {"channels:read", "groups:read"}, // public, private channels
}

// eventScopes lists what each event the adapter consumes needs.
var eventScopes = map[string]string{
	"app_mention": "app_mentions:read", "message.im": "im:history", "message.channels": "channels:history",
	"message.groups": "groups:history", "slash": "commands", "url_private": "files:read",
}

func TestManifestScopesCoverEveryCall(t *testing.T) {
	f := newFixture(t)
	conformance.Channel(t, f.a, f.srv) // runs the adapter itself
	f.start(t, stateConnected)
	ctx := context.Background()
	user := "U0000400"
	chat, err := f.a.OpenDM(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := f.a.Send(ctx, core.OutMessage{Chat: chat, Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.SelfTest(ctx); err != nil {
		t.Fatal(err)
	}
	f.srv.Inject(slacktest.Incoming{ChatID: "G0000400", SenderID: user, Text: "private channel", Mention: true,
		Files: []slacktest.File{{Name: "f.txt", MIME: "text/plain", Data: []byte("x")}}})
	in := f.sink.message(t)
	f.sink.message(t)
	rc, err := f.a.Fetch(ctx, in.Attachments[0], 10)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	f.srv.InjectSlash(chat.ChatID, user, "help")
	f.sink.message(t)

	granted := map[string]bool{}
	for _, s := range manifestScopes(t) {
		granted[s] = true
	}
	seen := map[string]bool{}
	for _, c := range f.srv.Calls() {
		need, known := methodScopes[c.Method]
		if !known {
			t.Errorf("the adapter called %s, which this test does not map to scopes", c.Method)
		}
		for _, s := range need {
			if !granted[s] {
				t.Errorf("%s needs %s, missing from the §8.6 manifest", c.Method, s)
			}
		}
		wantToken := "bot"
		if c.Method == "apps.connections.open" {
			wantToken = "app"
		}
		if c.Token != wantToken {
			t.Errorf("%s called with the %s token, want the %s token", c.Method, c.Token, wantToken)
		}
		seen[c.Method] = true
	}
	for m := range methodScopes {
		if !seen[m] {
			t.Errorf("%s was never exercised", m)
		}
	}
	for ev, s := range eventScopes {
		if !granted[s] {
			t.Errorf("%s needs %s, missing from the §8.6 manifest", ev, s)
		}
	}
}

func TestAuthFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		secret map[string]string
		hint   string
	}{
		{"bad bot token", map[string]string{"secret:slack_bot_token": "xoxb-wrong", "secret:slack_app_token": slacktest.DefaultAppToken}, "bot_token"},
		{"not a bot token", map[string]string{"secret:slack_bot_token": "xoxp-user", "secret:slack_app_token": slacktest.DefaultAppToken}, "xoxb-"},
		{"bad app token", map[string]string{"secret:slack_bot_token": slacktest.DefaultBotToken, "secret:slack_app_token": "xapp-wrong"}, "app_token"},
		{"missing app token", map[string]string{"secret:slack_bot_token": slacktest.DefaultBotToken}, "app_token"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, func(_ *config.Slack, d *core.AdapterDeps) {
				d.Secret = func(_ context.Context, ref string) (core.Secret, error) {
					v, ok := tt.secret[ref]
					if !ok {
						return core.Secret{}, core.ErrNotFound
					}
					return core.NewSecret([]byte(v)), nil
				}
			})
			f.start(t, stateAuthFailed)
			if st := f.a.Status(); !strings.Contains(st.Detail, tt.hint) {
				t.Fatalf("status = %+v, want a hint mentioning %q", st, tt.hint)
			}
			if _, err := f.a.SelfTest(context.Background()); !isAuthErr(err) || core.CodeOf(err) != core.FailKeyRejected {
				t.Fatalf("SelfTest = %v, want an auth error", err)
			}
		})
	}
}

func TestDisabledAdapterStaysIdle(t *testing.T) {
	f := newFixture(t, func(c *config.Slack, _ *core.AdapterDeps) { c.Enabled = false })
	if st := f.a.Status(); st.State != stateDisabled || st.Enabled {
		t.Fatalf("status = %+v, want disabled", st)
	}
	f.start(t, stateDisabled)()
	if len(f.srv.Calls()) != 0 || len(f.egress.recorded()) != 0 {
		t.Fatalf("a disabled adapter made calls: %+v", f.srv.Calls())
	}
}

// The hub builds each adapter once at boot, so turning Slack on in Settings
// reaches a running adapter through Reconfigure; it must then connect and
// take commands, and go quiet again when turned off.
func TestEnabledWhileRunning(t *testing.T) {
	f := newFixture(t, func(c *config.Slack, _ *core.AdapterDeps) { c.Enabled = false })
	f.start(t, stateDisabled)()
	cfg := config.Default()
	cfg.Channels.Slack = f.a.conf().cfg
	cfg.Channels.Slack.Enabled = true
	if err := f.a.Reconfigure(cfg); err != nil {
		t.Fatal(err)
	}
	if st := f.a.Status(); !st.Enabled || st.State != stateConnecting {
		t.Fatalf("status after enabling = %+v, want enabled and connecting", st)
	}
	stop := f.start(t, stateConnected)
	dm, user := f.srv.ConformanceIDs()
	f.srv.InjectSlash(dm, user, "pair ZQJR-9Q0M")
	if in := f.sink.message(t); in.Text != "/pair ZQJR-9Q0M" {
		t.Fatalf("slash command = %q, want /pair ZQJR-9Q0M", in.Text)
	}
	stop()
	cfg.Channels.Slack.Enabled = false
	if err := f.a.Reconfigure(cfg); err != nil {
		t.Fatal(err)
	}
	if st := f.a.Status(); st.Enabled || st.State != stateDisabled {
		t.Fatalf("status after disabling = %+v, want disabled", st)
	}
}

func TestReconfigureRefetchesTokens(t *testing.T) {
	var resolved atomic.Int32
	f := newFixture(t, func(_ *config.Slack, d *core.AdapterDeps) {
		inner := d.Secret
		d.Secret = func(ctx context.Context, ref string) (core.Secret, error) {
			resolved.Add(1)
			return inner(ctx, ref)
		}
	})
	ctx := context.Background()
	if _, err := f.a.SelfTest(ctx); err != nil {
		t.Fatal(err)
	}
	before := resolved.Load()
	cfg := config.Default()
	cfg.Channels.Slack = f.a.conf().cfg
	if err := f.a.Reconfigure(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.SelfTest(ctx); err != nil {
		t.Fatal(err)
	}
	if resolved.Load() == before {
		t.Fatal("tokens were not resolved again after Reconfigure")
	}
}

func TestReconfigureRejectsBadAPIBase(t *testing.T) {
	f := newFixture(t)
	cfg := config.Default()
	cfg.Channels.Slack = f.a.conf().cfg
	cfg.Channels.Slack.APIBase = "ftp://x"
	if err := f.a.Reconfigure(cfg); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Reconfigure = %v, want ErrInvalid", err)
	}
	if f.a.conf().apiBase.Scheme != "http" {
		t.Fatal("a refused config replaced the running one")
	}
}

func TestEgressDestinations(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	host := strings.TrimSuffix(strings.TrimPrefix(f.srv.URL(), "http://"), "/api/")
	dests := f.egress.recorded()
	if len(dests) < 2 {
		t.Fatalf("egress destinations %+v: want the Web API client and the WebSocket dial", dests)
	}
	for _, d := range dests {
		if d.Name != "channel:slack" || len(d.Hosts) != 1 || d.Hosts[0] != host || !d.AllowPrivate {
			t.Fatalf("egress destination %+v, want channel:slack for exactly %s with AllowPrivate (overridden api_base)", d, host)
		}
	}
}

func TestDefaultEndpointsDoNotAllowPrivate(t *testing.T) {
	var got []core.SystemDest
	a, err := newAdapter(config.Default().Channels.Slack, core.AdapterDeps{
		Secret: func(context.Context, string) (core.Secret, error) { return core.NewSecret([]byte("xoxb-x")), nil },
		HTTP: func(d core.SystemDest) *http.Client {
			got = append(got, d)
			return newPlainClient()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.client(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Hosts[0] != "slack.com:443" || got[0].AllowPrivate {
		t.Fatalf("destinations = %+v, want slack.com:443 without AllowPrivate", got)
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	deps := core.AdapterDeps{Secret: func(context.Context, string) (core.Secret, error) { return core.Secret{}, nil }, HTTP: func(core.SystemDest) *http.Client { return newPlainClient() }}
	cfg := config.Default().Channels.Slack
	cfg.APIBase = "ftp://slack.com/api/"
	if _, err := New(cfg, deps); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("New(ftp api_base) = %v, want ErrInvalid", err)
	}
	if _, err := New(config.Default().Channels.Slack, core.AdapterDeps{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("New without deps = %v, want ErrInvalid", err)
	}
}

func TestOutboundEdgeCases(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.a.Send(ctx, core.OutMessage{Chat: core.ChatRef{Channel: "discord", ChatID: "1"}, Text: "x"}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Send to another channel's chat = %v, want ErrInvalid", err)
	}
	chat := core.ChatRef{Channel: name, ChatID: "C0000500", Kind: core.ChatGroup}
	if _, err := f.a.Send(ctx, core.OutMessage{Chat: chat}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("empty Send = %v, want ErrInvalid", err)
	}
	ref, err := f.a.Send(ctx, core.OutMessage{Chat: chat, Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Delete(ctx, ref); err != nil {
		t.Fatalf("second delete = %v, want nil (already gone)", err)
	}
	if err := f.a.Typing(ctx, chat); err != nil || f.a.Caps().Typing {
		t.Fatalf("Typing = %v with Caps.Typing %v, want a no-op", err, f.a.Caps().Typing)
	}
	if f.a.DeepLink("CODE") != "" {
		t.Fatal("DeepLink should be unsupported")
	}
	if err := f.a.Ack(ctx, core.ActionEvent{AckRef: f.srv.URL()}, ""); err != nil {
		t.Fatalf("Ack without toast = %v", err)
	}
}

func TestIdleFootprint(t *testing.T) {
	const n = 4 // several adapters, so leftover garbage from earlier tests does not swamp the signal
	fixtures := make([]*fixture, n)
	for i := range fixtures {
		fixtures[i] = newFixture(t)
	}
	before := settledMem()
	for _, f := range fixtures {
		f.start(t, stateConnected)
	}
	time.Sleep(200 * time.Millisecond)
	// The fakes' side of each connection is counted too, so this overstates
	// the adapter's own share.
	per := (int64(settledMem()) - int64(before)) / n
	if per > 3<<20 {
		t.Fatalf("each idle adapter grew the heap and stacks by %d bytes, budget %d", per, 3<<20)
	}
	t.Logf("idle footprint per adapter: %d KiB", per/1024)
}

func settledMem() uint64 {
	var ms runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc + ms.StackInuse
}

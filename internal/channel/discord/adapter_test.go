package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/channel/discord/discordtest"
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/conformance"
)

func TestConformance(t *testing.T) {
	f := newFixture(t)
	conformance.Channel(t, f.a, f.srv)
}

func TestInteractionAckedWithinDeadline(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	dm, user := f.srv.ConformanceIDs()
	chat := core.ChatRef{Channel: name, ChatID: dm, Kind: core.ChatDM}
	ref, err := f.a.Send(context.Background(), core.OutMessage{Chat: chat, Text: "approve?",
		Buttons: [][]core.Button{{{Label: "Approve", Data: "a:apr_x:o:mac", Style: core.BtnPrimary}}}})
	if err != nil {
		t.Fatal(err)
	}
	f.srv.InjectButton(dm, ref.ID, user, "a:apr_x:o:mac")
	ev := f.sink.action(t)
	acks := f.srv.Acks()
	if len(acks) != 1 || acks[0].Type != 6 {
		t.Fatalf("interaction callbacks = %+v, want one deferred update (type 6) before the sink saw the press", acks)
	}
	if acks[0].After >= 3*time.Second {
		t.Fatalf("press acknowledged after %v, want < 3s", acks[0].After)
	}
	want := core.ActionEvent{Chat: chat, Msg: ref, SenderID: user, Data: "a:apr_x:o:mac", AckRef: ev.AckRef}
	if ev != want || !strings.HasPrefix(ev.AckRef, discordtest.DefaultAppID+":") {
		t.Fatalf("ActionEvent = %+v, want %+v", ev, want)
	}
	if err := f.a.Ack(context.Background(), ev, "Approved"); err != nil {
		t.Fatal(err)
	}
	fu := f.srv.Followups()
	if len(fu) != 1 || fu[0].Content != "Approved" || fu[0].Flags&discordtest.Ephemeral == 0 || fu[0].Flags&discordtest.SuppressEmbeds == 0 {
		t.Fatalf("followups = %+v, want one ephemeral toast with embeds suppressed", fu)
	}
	if err := f.a.Ack(context.Background(), ev, ""); err != nil {
		t.Fatalf("Ack without toast: %v", err)
	}
	if err := f.a.Ack(context.Background(), core.ActionEvent{AckRef: "garbage"}, "x"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Ack with a malformed reference = %v, want ErrInvalid", err)
	}
}

func TestMentionDetection(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	const channel, sender = "1300000000000000001", "1300000000000000002"
	group := core.ChatRef{Channel: name, ChatID: channel, Kind: core.ChatGroup}

	f.srv.InjectText(channel, sender, "just chatting", false)
	in := f.sink.message(t)
	if in.Mentioned || in.Chat != group || in.Text != "just chatting" || in.SenderID != sender {
		t.Fatalf("plain group message = %+v, want unmentioned in %+v", in, group)
	}

	f.srv.InjectText(channel, sender, "what's up?", true)
	in = f.sink.message(t)
	if !in.Mentioned || in.Text != "what's up?" {
		t.Fatalf("mention = %+v, want Mentioned with the mention stripped", in)
	}

	if _, err := f.a.Send(context.Background(), core.OutMessage{Chat: group, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	f.srv.Inject(discordtest.Incoming{ChatID: channel, SenderID: sender, Text: "thanks", ReplyToBot: true})
	if in = f.sink.message(t); !in.Mentioned || in.Text != "thanks" {
		t.Fatalf("reply to the bot = %+v, want Mentioned", in)
	}

	dm := f.srv.DMChannel(sender)
	f.srv.InjectText(dm, sender, "private", false)
	in = f.sink.message(t)
	if !in.Mentioned || in.Chat != (core.ChatRef{Channel: name, ChatID: dm, Kind: core.ChatDM}) {
		t.Fatalf("DM = %+v, want Mentioned in a DM chat", in)
	}

	f.srv.Inject(discordtest.Incoming{ChatID: channel, SenderID: discordtest.DefaultBotID, Text: "my own echo", Mention: true})
	f.srv.Inject(discordtest.Incoming{ChatID: channel, SenderID: "1300000000000000003", Text: "beep", Bot: true})
	if in = f.sink.message(t); in.Text != "beep" || !in.SenderIsBot {
		t.Fatalf("got %+v, want only the other bot's message (the adapter's own is dropped)", in)
	}
}

func TestEmbedsSuppressedAndMentionsDisabled(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const channel = "1300000000000000010"
	chat := core.ChatRef{Channel: name, ChatID: channel, Kind: core.ChatGroup}
	ref, err := f.a.Send(ctx, core.OutMessage{Chat: chat, Text: "see https://example.com/x @everyone"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.Edit(ctx, ref, core.OutMessage{Chat: chat, Text: "now https://example.org"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.Send(ctx, core.OutMessage{Chat: chat, Text: "quiet", Silent: true, ReplyTo: ref.ID}); err != nil {
		t.Fatal(err)
	}
	if !f.srv.PreviewsDisabled() {
		t.Fatal("a create or edit did not set SUPPRESS_EMBEDS")
	}
	msgs := f.srv.Messages(channel)
	if len(msgs) != 2 || msgs[0].Edits != 1 || msgs[0].Content != "now https://example.org" {
		t.Fatalf("messages = %+v", msgs)
	}
	for _, m := range msgs {
		var am struct {
			Parse []string `json:"parse"`
			Roles []string `json:"roles"`
			Users []string `json:"users"`
		}
		if err := json.Unmarshal(m.AllowedMentions, &am); err != nil || am.Parse == nil || len(am.Parse)+len(am.Roles)+len(am.Users) != 0 {
			t.Fatalf("allowed_mentions = %s, want every list present and empty", m.AllowedMentions)
		}
	}
	const suppressNotifications = 1 << 12
	if msgs[1].Flags&suppressNotifications == 0 || msgs[1].ReplyTo != ref.ID {
		t.Fatalf("silent reply = %+v, want SUPPRESS_NOTIFICATIONS and a reference to %s", msgs[1], ref.ID)
	}
}

func TestButtonsRender(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const channel = "1300000000000000020"
	chat := core.ChatRef{Channel: name, ChatID: channel, Kind: core.ChatGroup}
	ref, err := f.a.Send(ctx, core.OutMessage{Chat: chat, Text: "card", Buttons: [][]core.Button{
		{{Label: "Allow", Data: "a:1:o:m", Style: core.BtnPrimary}, {Label: "Deny", Data: "a:1:d:m", Style: core.BtnDanger}},
		{{Label: "Open in web", URL: "https://bothy.example/approvals/1"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Type       int `json:"type"`
		Components []struct {
			Type     int    `json:"type"`
			Style    int    `json:"style"`
			Label    string `json:"label"`
			CustomID string `json:"custom_id"`
			URL      string `json:"url"`
		} `json:"components"`
	}
	if err := json.Unmarshal(f.srv.Messages(channel)[0].Components, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Type != 1 || len(rows[0].Components) != 2 ||
		rows[0].Components[0].Style != 1 || rows[0].Components[0].CustomID != "a:1:o:m" ||
		rows[0].Components[1].Style != 4 || rows[1].Components[0].Style != 5 || rows[1].Components[0].URL == "" || rows[1].Components[0].CustomID != "" {
		t.Fatalf("components = %+v", rows)
	}
	if err := f.a.Edit(ctx, ref, core.OutMessage{Chat: chat, Text: "Approved"}); err != nil {
		t.Fatal(err)
	}
	if got := string(f.srv.Messages(channel)[0].Components); got != "[]" {
		t.Fatalf("components after an edit without buttons = %s, want []", got)
	}
	six := make([]core.Button, 6)
	for i := range six {
		six[i] = core.Button{Label: "b", Data: "d"}
	}
	for _, bad := range [][][]core.Button{{six}, {{{Label: "x"}}}, {{{Data: "d"}}}} {
		if _, err := f.a.Send(ctx, core.OutMessage{Chat: chat, Text: "x", Buttons: bad}); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("Send with buttons %+v = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestRateLimitIsPerRoute(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const limited, other = "1300000000000000030", "1300000000000000031"
	f.srv.RateLimitNext("POST", "/api/v10/channels/"+limited+"/messages", 1)
	var wg sync.WaitGroup
	var limitedTook time.Duration
	var limitedErr error
	start := time.Now()
	wg.Go(func() {
		_, limitedErr = f.a.Send(ctx, core.OutMessage{Chat: core.ChatRef{Channel: name, ChatID: limited, Kind: core.ChatGroup}, Text: "first"})
		limitedTook = time.Since(start)
	})
	waitFor(t, 2*time.Second, func() bool {
		return slices.Contains(f.srv.Requests(), "POST /api/v10/channels/"+limited+"/messages")
	}, "the rate-limited request")
	if _, err := f.a.Send(ctx, core.OutMessage{Chat: core.ChatRef{Channel: name, ChatID: other, Kind: core.ChatGroup}, Text: "second"}); err != nil {
		t.Fatal(err)
	}
	otherTook := time.Since(start)
	wg.Wait()
	if limitedErr != nil {
		t.Fatalf("rate-limited send failed: %v", limitedErr)
	}
	if limitedTook < 900*time.Millisecond || otherTook >= limitedTook {
		t.Fatalf("limited route took %v, other route %v; want the 429 honoured on its route only", limitedTook, otherTook)
	}
	if got := f.srv.SentTexts(limited); len(got) != 1 || got[0] != "first" {
		t.Fatalf("limited channel shows %q", got)
	}
}

func TestThreadsReportedUnderParent(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	const parent, thread, sender = "1300000000000000040", "1300000000000000041", "1300000000000000042"
	f.srv.AddThread(thread, parent)
	f.srv.InjectText(thread, sender, "in a thread", true)
	in := f.sink.message(t)
	want := core.ChatRef{Channel: name, ChatID: parent, ThreadID: thread, Kind: core.ChatGroup}
	if in.Chat != want {
		t.Fatalf("thread chat = %+v, want %+v", in.Chat, want)
	}
	if _, err := f.a.Send(context.Background(), core.OutMessage{Chat: in.Chat, Text: "reply"}); err != nil {
		t.Fatal(err)
	}
	if got := f.srv.SentTexts(thread); len(got) != 1 {
		t.Fatalf("thread shows %q, want the reply", got)
	}
	f.srv.InjectText(thread, sender, "again", true)
	f.sink.message(t)
	lookups := 0
	for _, r := range f.srv.Requests() {
		if r == "GET /api/v10/channels/"+thread {
			lookups++
		}
	}
	if lookups != 1 {
		t.Fatalf("channel looked up %d times, want once (cached)", lookups)
	}
}

func TestFetchAttachment(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	dm := f.srv.DMChannel("1300000000000000050")
	data := bytes.Repeat([]byte("x"), 1000)
	f.srv.Inject(discordtest.Incoming{ChatID: dm, SenderID: "1300000000000000050", Text: "file",
		Attachments: []discordtest.File{{Name: "notes.txt", MIME: "text/plain", Data: data}}})
	in := f.sink.message(t)
	if len(in.Attachments) != 1 || in.Attachments[0].Name != "notes.txt" || in.Attachments[0].MIME != "text/plain" || in.Attachments[0].Size != 1000 {
		t.Fatalf("attachments = %+v", in.Attachments)
	}
	ctx := context.Background()
	rc, err := f.a.Fetch(ctx, in.Attachments[0], 1000)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Fetch = %d bytes, %v", len(got), err)
	}
	if _, err := f.a.Fetch(ctx, in.Attachments[0], 999); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Fetch over the limit = %v, want ErrInvalid", err)
	}
	lying := in.Attachments[0]
	lying.Size = 10
	rc, err = f.a.Fetch(ctx, lying, 500)
	if err == nil {
		_, err = io.ReadAll(rc)
		_ = rc.Close()
	}
	if err == nil {
		t.Fatal("Fetch of a body over the limit succeeded")
	}
	for _, ref := range []string{"https://evil.example/x.txt", "http://cdn.discordapp.com/x", "file:///etc/passwd"} {
		if _, err := f.a.Fetch(ctx, core.Attachment{Name: "x", FetchRef: ref}, 100); !errors.Is(err, core.ErrForbidden) {
			t.Fatalf("Fetch(%s) = %v, want ErrForbidden", ref, err)
		}
	}
}

func TestBadTokenReportsAuthFailed(t *testing.T) {
	f := newFixture(t, func(_ *config.Discord, d *core.AdapterDeps) {
		d.Secret = func(context.Context, string) (core.Secret, error) { return core.NewSecret([]byte("wrong")), nil }
	})
	f.start(t, stateAuthFailed)
	if st := f.a.Status(); !strings.Contains(st.Detail, "bot token") {
		t.Fatalf("status = %+v, want a token hint", st)
	}
	if _, err := f.a.SelfTest(context.Background()); !isAuthErr(err) {
		t.Fatalf("SelfTest = %v, want an auth error", err)
	}
}

func TestMissingSecretReportsAuthFailed(t *testing.T) {
	f := newFixture(t, func(c *config.Discord, _ *core.AdapterDeps) { c.Token = "secret:other" })
	f.start(t, stateAuthFailed)
	if len(f.srv.Requests()) != 0 {
		t.Fatalf("requests without a token: %q", f.srv.Requests())
	}
}

func TestDisabledAdapterStaysIdle(t *testing.T) {
	f := newFixture(t, func(c *config.Discord, _ *core.AdapterDeps) { c.Enabled = false })
	if st := f.a.Status(); st.State != stateDisabled || st.Enabled {
		t.Fatalf("status = %+v, want disabled", st)
	}
	stop := f.start(t, stateDisabled)
	stop()
	if len(f.srv.Requests()) != 0 || len(f.egress.recorded()) != 0 {
		t.Fatalf("a disabled adapter made requests: %q", f.srv.Requests())
	}
}

func TestSelfTestOpenDMDeleteTyping(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id, err := f.a.SelfTest(ctx)
	if err != nil || id != "@bothy" {
		t.Fatalf("SelfTest = %q, %v", id, err)
	}
	if st := f.a.Status(); st.Identity != "@bothy" {
		t.Fatalf("status identity = %q", st.Identity)
	}
	chat, err := f.a.OpenDM(ctx, "1300000000000000060")
	if err != nil || chat.Kind != core.ChatDM || chat.ChatID != f.srv.DMChannel("1300000000000000060") {
		t.Fatalf("OpenDM = %+v, %v", chat, err)
	}
	if err := f.a.Typing(ctx, chat); err != nil {
		t.Fatal(err)
	}
	ref, err := f.a.Send(ctx, core.OutMessage{Chat: chat, Text: "bye"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Delete(ctx, ref); err != nil {
		t.Fatalf("second delete = %v, want nil (already gone)", err)
	}
	if _, err := f.a.Send(ctx, core.OutMessage{Chat: core.ChatRef{Channel: "slack", ChatID: "1"}, Text: "x"}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Send to another channel's chat = %v, want ErrInvalid", err)
	}
	if _, err := f.a.OpenDM(ctx, "not-a-snowflake"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("OpenDM(bad id) = %v, want ErrInvalid", err)
	}
	if f.a.DeepLink("CODE") != "" {
		t.Fatal("DeepLink should be unsupported")
	}
}

func TestEgressDestinations(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	host := strings.TrimPrefix(f.srv.URL(), "http://")
	host = host[:strings.IndexByte(host, '/')]
	dests := f.egress.recorded()
	if len(dests) == 0 {
		t.Fatal("no egress clients requested")
	}
	for _, d := range dests {
		if d.Name != "channel:discord" || len(d.Hosts) != 1 || d.Hosts[0] != host || !d.AllowPrivate {
			t.Fatalf("egress destination %+v, want channel:discord for exactly %s with AllowPrivate (overridden api_base)", d, host)
		}
	}
	if realGateway && len(dests) < 2 {
		t.Fatalf("egress destinations %+v: the gateway dial did not go through egress", dests)
	}
}

func TestDefaultEndpointsDoNotAllowPrivate(t *testing.T) {
	var got []core.SystemDest
	a, err := newAdapter(config.Default().Channels.Discord, core.AdapterDeps{
		Secret: func(context.Context, string) (core.Secret, error) { return core.Secret{}, core.ErrNotFound },
		HTTP: func(d core.SystemDest) *http.Client {
			got = append(got, d)
			return newPlainClient()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a.client()
	if len(got) != 1 || got[0].Hosts[0] != "discord.com:443" || got[0].AllowPrivate {
		t.Fatalf("destinations = %+v, want discord.com:443 without AllowPrivate", got)
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	deps := core.AdapterDeps{Secret: func(context.Context, string) (core.Secret, error) { return core.Secret{}, nil }, HTTP: func(core.SystemDest) *http.Client { return newPlainClient() }}
	for _, mut := range []func(*config.Discord){
		func(c *config.Discord) { c.APIBase = "ftp://discord.com" },
		func(c *config.Discord) { c.GatewayURL = "https://gateway.discord.gg" },
	} {
		cfg := config.Default().Channels.Discord
		mut(&cfg)
		if _, err := New(cfg, deps); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("New(%+v) = %v, want ErrInvalid", cfg, err)
		}
	}
	if _, err := New(config.Default().Channels.Discord, core.AdapterDeps{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("New without deps = %v, want ErrInvalid", err)
	}
}

func TestInviteURLMatchesSpec(t *testing.T) {
	const want = "https://discord.com/oauth2/authorize?client_id=123456789012345678&scope=bot%20applications.commands&permissions=274877975552"
	if got := InviteURL("123456789012345678"); got != want {
		t.Fatalf("InviteURL = %s, want %s", got, want)
	}
}

package channel

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/auth"
	"github.com/rielicat/bothy/internal/channel/telegram"
	"github.com/rielicat/bothy/internal/channel/telegram/telegramtest"
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/events"
	"github.com/rielicat/bothy/internal/testkit/fakes"
	"github.com/rielicat/bothy/internal/testkit/memstore"
)

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestTelegramEndToEnd drives the hub through the real Telegram adapter and
// the fake Bot API: dedup of a redelivered update, a streamed reply with
// hostile HTML and links, an approval card pressed in Telegram, and someone
// new waiting to be let in.
func TestTelegramEndToEnd(t *testing.T) {
	srv := telegramtest.New(telegramtest.Config{})
	defer srv.Close()
	ctx := context.Background()
	store := memstore.New()
	owner := &core.User{Username: "olivia", DisplayName: "Olivia", Role: core.RoleOwner}
	member := &core.User{Username: "mark", DisplayName: "Mark", Role: core.RoleMember}
	for _, u := range []*core.User{owner, member} {
		if err := store.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	bus := events.New(events.Options{})
	c := config.Default()
	authn, err := auth.New(auth.Deps{Store: store, Config: newCfgView(c), Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Identities().Touch(ctx, &core.ChannelIdentity{Channel: "telegram", ExternalID: "1001", DisplayName: "Olivia",
		Member: core.MemberUnknown, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	teammate, yes := core.IdentityTeammate, true
	admin := core.Actor{User: owner.ID, Role: core.RoleOwner, Via: "cookie"}
	if _, err := authn.SetTeammate(ctx, admin, "telegram", "1001", auth.TeammatePatch{Status: &teammate, CanApprove: &yes}); err != nil {
		t.Fatal(err)
	}
	olivia, err := store.Identities().Get(ctx, "telegram", "1001")
	if err != nil {
		t.Fatal(err)
	}
	olivia.Greet = ""
	if err := store.Identities().Update(ctx, olivia); err != nil {
		t.Fatal(err)
	}
	runner := &fakes.Runner{Store: store}
	approvals := &fakes.Approvals{Manual: true}

	c.Channels.Telegram.Enabled = true
	c.Channels.Telegram.APIBase = srv.URL
	adapter, err := telegram.New(c.Channels.Telegram, core.AdapterDeps{
		Secret: func(context.Context, string) (core.Secret, error) { return core.NewSecret([]byte(srv.Token)), nil },
		HTTP:   func(core.SystemDest) *http.Client { return srv.Client() },
		Meta:   store.Meta(), Log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	hub, err := New(Deps{Config: newCfgView(c), Auth: authn, Runner: runner, Approvals: approvals, Budget: &fakes.Budget{},
		Store: store, Bus: bus, Audit: &fakes.Auditor{}, Redactor: fakes.NewRedactor(), WorkDir: t.TempDir(),
		Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	hub.Add(adapter)
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- hub.Run(rctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	eventually(t, "the adapter to connect", func() bool { return adapter.Status().State == "connected" })

	upd, _ := srv.Inject(telegramtest.Inbound{ChatID: 1001, SenderID: 1001, FirstName: "Olivia", Text: "hello <b>bot</b>"})
	eventually(t, "the run submission", func() bool { return len(runner.Submitted()) == 1 })
	if err := srv.Redeliver(upd); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(runner.Submitted()); n != 1 {
		t.Fatalf("redelivered update submitted again (%d submits)", n)
	}
	req := runner.Submitted()[0]
	if req.Input[0].Text != "hello <b>bot</b>" || req.Provenance != core.ProvUser || req.Principal != olivia.Principal {
		t.Fatalf("run request = %+v", req)
	}

	run := core.RunID(core.NewID(core.PrefixRun))
	emit := func(typ core.EventType, d core.EventData) {
		req.Sink.OnEvent(core.Event{Type: typ, Run: run, Session: req.Session, Data: d})
	}
	emit(core.EvRunStarted, core.RunStartedData{Trigger: core.TrigMessage})
	emit(core.EvRunDelta, core.DeltaData{Text: "Here is <script>alert(1)</script> & https://example.com/x\n\n"})
	emit(core.EvRunDelta, core.DeltaData{Text: "**done**"})
	emit(core.EvRunFinished, core.RunFinishedData{Status: core.RunDone, Steps: 1})
	want := "Here is <script>alert(1)</script> & https://example.com/x\n\ndone"
	eventually(t, "the final reply", func() bool {
		msgs := srv.Messages(1001)
		return len(msgs) == 1 && msgs[0].Text == want && len(msgs[0].Buttons) == 0
	})
	m := srv.Messages(1001)[0]
	if strings.Contains(m.Raw, "<script>") || !strings.Contains(m.Raw, "&lt;script&gt;") || !strings.Contains(m.Raw, "<b>done</b>") {
		t.Fatalf("raw HTML = %q", m.Raw)
	}
	if !srv.PreviewsDisabled() {
		t.Fatal("a streamed send or edit left link previews on")
	}

	dm := core.ChatRef{Channel: "telegram", ChatID: "1001", Kind: core.ChatDM}
	ap, err := approvals.Request(ctx, core.ApprovalRequest{Principal: olivia.Principal, Chat: &dm, Sender: "telegram:1001",
		Action:   &core.Action{Tool: "shell", Kind: core.ActExec, Argv: []string{"bash", "-lc", "echo <b>&amp;"}},
		Spec:     core.ToolSpec{Manifest: core.Manifest{Title: "Run shell command"}},
		Decision: core.Decision{Approver: core.ApproverReq{Team: true}, Choices: []core.Choice{core.AllowOnce, core.Deny}}})
	if err != nil {
		t.Fatal(err)
	}
	bus.Publish(core.Event{Type: core.EvApprovalRequested, Principal: ap.Principal, Data: core.ApprovalData{Approval: ap}})
	var card telegramtest.Message
	eventually(t, "the approval card", func() bool {
		msgs := srv.Messages(1001)
		if len(msgs) == 2 {
			card = msgs[1]
		}
		return len(msgs) == 2
	})
	if !strings.Contains(card.Text, "echo <b>&amp;") || len(card.Buttons) != 1 || len(card.Buttons[0]) != 2 {
		t.Fatalf("card = %+v", card)
	}
	srv.Press(1001, card.ID, 1001, card.Buttons[0][0].CallbackData)
	eventually(t, "the press acknowledgement", func() bool {
		return slices.ContainsFunc(srv.Answers(), func(a telegramtest.CallbackAnswer) bool { return a.Text == "Approved." })
	})

	srv.Inject(telegramtest.Inbound{ChatID: 3003, SenderID: 3003, Text: "/start ABCD-EFGH"})
	eventually(t, "the waiting line", func() bool {
		return slices.ContainsFunc(srv.SentTexts("3003"), func(s string) bool { return s == waitingLine })
	})
	if n := len(runner.Submitted()); n != 1 {
		t.Fatalf("someone new reached the runner (%d submits)", n)
	}
	_ = member
}

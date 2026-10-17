package channeltest_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/channeltest"
)

type sink struct {
	mu   sync.Mutex
	msgs []core.Inbound
	acts []core.ActionEvent
}

func (s *sink) OnMessage(_ context.Context, in core.Inbound) {
	s.mu.Lock()
	s.msgs = append(s.msgs, in)
	s.mu.Unlock()
}

func (s *sink) OnAction(_ context.Context, ev core.ActionEvent) {
	s.mu.Lock()
	s.acts = append(s.acts, ev)
	s.mu.Unlock()
}

func TestInjectAndPress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := channeltest.New("telegram")
		if c.Caps() != channeltest.TelegramCaps || c.Name() != "telegram" {
			t.Fatalf("caps = %+v", c.Caps())
		}
		c.Inject(core.Inbound{SenderID: "7", Text: "queued"})
		if st := c.Status(); st.State != "connecting" {
			t.Fatalf("status before Run = %+v", st)
		}

		var s sink
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error)
		go func() { done <- c.Run(ctx, &s) }()
		synctest.Wait()
		if len(s.msgs) != 1 || s.msgs[0].Text != "queued" {
			t.Fatalf("queued injection not delivered: %+v", s.msgs)
		}
		if st := c.Status(); st.State != "connected" || st.Identity != "@telegram_bot" {
			t.Fatalf("status while running = %+v", st)
		}

		c.Inject(core.Inbound{Chat: core.ChatRef{ChatID: "-100", Kind: core.ChatGroup}, SenderID: "8", Text: "live"})
		if len(s.msgs) != 2 {
			t.Fatal("Inject during Run must deliver synchronously")
		}
		in := s.msgs[1]
		if in.Chat.Channel != "telegram" || in.Chat.Kind != core.ChatGroup || in.MsgID == "" || in.MsgID == s.msgs[0].MsgID || in.At.IsZero() {
			t.Fatalf("defaults = %+v", in)
		}
		if s.msgs[0].Chat.Kind != core.ChatDM {
			t.Fatalf("default kind = %q", s.msgs[0].Chat.Kind)
		}

		msg := core.MsgRef{Chat: core.ChatRef{Channel: "telegram", ChatID: "7"}, ID: "3"}
		c.Press(msg, "7", "s:run_1")
		c.Press(msg, "7", "s:run_1")
		if len(s.acts) != 2 || s.acts[0].Msg != msg || s.acts[0].Data != "s:run_1" || s.acts[0].SenderID != "7" ||
			s.acts[0].AckRef == "" || s.acts[0].AckRef == s.acts[1].AckRef {
			t.Fatalf("actions = %+v", s.acts)
		}

		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v", err)
		}
		if c.Status().State != "connecting" {
			t.Fatal("status after Run")
		}
	})
}

func TestOutbound(t *testing.T) {
	ctx := context.Background()
	c := channeltest.New("discord")
	dm := core.ChatRef{Channel: "discord", ChatID: "1"}
	other := core.ChatRef{Channel: "discord", ChatID: "2"}
	r1, _ := c.Send(ctx, core.OutMessage{Chat: dm, Text: "one"})
	r2, _ := c.Send(ctx, core.OutMessage{Chat: other, Text: "elsewhere"})
	r3, _ := c.Send(ctx, core.OutMessage{Chat: dm, Text: "three"})
	if r1.ID != "1" || r2.ID != "2" || r3.ID != "3" || r1.Chat != dm {
		t.Fatalf("refs = %+v %+v %+v", r1, r2, r3)
	}
	_ = c.Edit(ctx, r1, core.OutMessage{Chat: dm, Text: "one v2"})
	_ = c.Edit(ctx, r1, core.OutMessage{Chat: dm, Text: "one v3"})
	_ = c.Delete(ctx, r3)
	if got := c.Visible("1"); !slices.Equal(got, []string{"one v3"}) {
		t.Fatalf("Visible = %q", got)
	}
	if len(c.SentMessages()) != 3 || len(c.EditedMessages()) != 2 || c.EditRefs[1] != r1 || c.Deleted[0] != r3 {
		t.Fatal("records")
	}

	_ = c.Typing(ctx, dm)
	_ = c.Ack(ctx, core.ActionEvent{}, "Approved")
	if len(c.TypingIn) != 1 || !slices.Equal(c.AckToasts(), []string{"Approved"}) {
		t.Fatal("typing/ack not recorded")
	}

	boom := errors.New("platform down")
	c.Err = boom
	if _, err := c.Send(ctx, core.OutMessage{Chat: dm}); !errors.Is(err, boom) {
		t.Fatalf("Send = %v", err)
	}
	if !errors.Is(c.Edit(ctx, r1, core.OutMessage{}), boom) || !errors.Is(c.Delete(ctx, r1), boom) {
		t.Fatal("Err must fail Edit and Delete")
	}
}

func TestMisc(t *testing.T) {
	ctx := context.Background()
	c := channeltest.New("slack")
	c.Files = map[string][]byte{"F1": []byte("hello")}
	rc, err := c.Fetch(ctx, core.Attachment{FetchRef: "F1"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(rc); string(b) != "hello" {
		t.Fatalf("Fetch = %q", b)
	}
	if _, err := c.Fetch(ctx, core.Attachment{FetchRef: "F1"}, 4); err == nil {
		t.Fatal("Fetch ignored the size limit")
	}
	if _, err := c.Fetch(ctx, core.Attachment{FetchRef: "F2"}, 4); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Fetch missing = %v", err)
	}
	if dm, _ := c.OpenDM(ctx, "U1"); dm != (core.ChatRef{Channel: "slack", ChatID: "U1", Kind: core.ChatDM}) {
		t.Fatalf("OpenDM = %+v", dm)
	}
	if _, err := c.InspectChat(ctx, core.ChatRef{ChatID: "C1"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("InspectChat of an unknown chat = %v", err)
	}
	c.SetChat("C1", core.ChatInfo{Title: "#eng", Members: 12, External: true})
	if info, err := c.InspectChat(ctx, core.ChatRef{ChatID: "C1"}); err != nil || info.Title != "#eng" || info.Members != 12 || !info.External {
		t.Fatalf("InspectChat = %+v, %v", info, err)
	}
	c.IdentityV = "@bothy_bot"
	if id, err := c.SelfTest(ctx); id != "@bothy_bot" || err != nil {
		t.Fatalf("SelfTest = %q, %v", id, err)
	}

	in := core.Inbound{Chat: core.ChatRef{Channel: "slack", ChatID: "C1", Kind: core.ChatGroup}, SenderID: "U1", Private: true, ReplyRef: "hook-1"}
	if err := c.ReplyPrivate(ctx, in, "Only you"); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("expired")
	c.SetPrivateErr(boom)
	if err := c.ReplyPrivate(ctx, in, "lost"); !errors.Is(err, boom) {
		t.Fatalf("ReplyPrivate with an error set = %v", err)
	}
	if got := c.PrivateReplies(); len(got) != 1 || got[0] != (channeltest.PrivateReply{Chat: in.Chat, Sender: "U1", Ref: "hook-1", Text: "Only you"}) {
		t.Fatalf("private replies = %+v", got)
	}
	c.SetPrivateErrAfter(2, boom)
	for i, want := range []error{nil, nil, boom, boom} {
		if err := c.ReplyPrivate(ctx, in, "again"); !errors.Is(err, want) {
			t.Fatalf("ReplyPrivate %d after SetPrivateErrAfter(2) = %v, want %v", i+1, err, want)
		}
	}
	if n := len(c.PrivateReplies()); n != 3 {
		t.Fatalf("%d private replies recorded, want 3", n)
	}
}

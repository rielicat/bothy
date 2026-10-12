package conformance

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/channeltest"
)

// memPlatform presents a channeltest.Channel as its own platform: injections
// go through Inject/Press and SentTexts is what the fake's chats show.
type memPlatform struct {
	c         *channeltest.Channel
	leaky     bool
	noMention bool
	ids       [2]string
}

func (p *memPlatform) chat(id string) core.ChatRef {
	return core.ChatRef{Channel: p.c.Name(), ChatID: id, Kind: core.ChatDM}
}

func (p *memPlatform) InjectText(chatID, senderID, text string, mention bool) {
	p.c.Inject(core.Inbound{Chat: p.chat(chatID), SenderID: senderID, Text: " " + text, Mentioned: mention && !p.noMention})
}

func (p *memPlatform) InjectButton(chatID, msgID, senderID, data string) {
	p.c.Press(core.MsgRef{Chat: p.chat(chatID), ID: msgID}, senderID, data)
}

func (p *memPlatform) SentTexts(chatID string) []string { return p.c.Visible(chatID) }
func (p *memPlatform) PreviewsDisabled() bool           { return !p.leaky }

type idPlatform struct{ *memPlatform }

func (p idPlatform) ConformanceIDs() (string, string) { return p.ids[0], p.ids[1] }

func TestChannelPassesForChanneltest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := channeltest.New("telegram")
		Channel(t, c, &memPlatform{c: c})
		if len(c.AckToasts()) != 1 || len(c.TypingIn) != 1 {
			t.Fatalf("suite did not exercise Ack/Typing: %v %v", c.AckToasts(), c.TypingIn)
		}
	})
}

func TestChannelUsesPlatformIDs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := channeltest.New("slack")
		Channel(t, c, idPlatform{&memPlatform{c: c, ids: [2]string{"D123", "U456"}}})
		for _, m := range c.SentMessages() {
			if m.Chat.ChatID != "D123" {
				t.Fatalf("sent to %q", m.Chat.ChatID)
			}
		}
	})
}

type noEdit struct{ *channeltest.Channel }

func (noEdit) Edit(context.Context, core.MsgRef, core.OutMessage) error { return nil }

type noDelete struct{ *channeltest.Channel }

func (noDelete) Delete(context.Context, core.MsgRef) error { return nil }

type truncating struct{ *channeltest.Channel }

func (c truncating) Send(ctx context.Context, m core.OutMessage) (core.MsgRef, error) {
	if len(m.Text) > 100 {
		m.Text = m.Text[:100]
	}
	return c.Channel.Send(ctx, m)
}

type slowAck struct{ *channeltest.Channel }

func (c slowAck) Ack(ctx context.Context, ev core.ActionEvent, toast string) error {
	time.Sleep(4 * time.Second)
	return c.Channel.Ack(ctx, ev, toast)
}

type deaf struct{ *channeltest.Channel }

func (deaf) Run(ctx context.Context, _ core.InboundSink) error { <-ctx.Done(); return nil }

func (c deaf) Status() core.ChannelStatus {
	st := c.Channel.Status()
	st.State = "connected"
	return st
}

type neverConnected struct{ *channeltest.Channel }

func (c neverConnected) Status() core.ChannelStatus {
	st := c.Channel.Status()
	st.State = "backoff"
	return st
}

type stubborn struct {
	*channeltest.Channel
	release chan struct{}
}

func (c stubborn) Run(ctx context.Context, sink core.InboundSink) error {
	go func() { _ = c.Channel.Run(ctx, sink) }()
	<-c.release
	return nil
}

type failingRun struct{ *channeltest.Channel }

func (c failingRun) Run(ctx context.Context, sink core.InboundSink) error {
	_ = c.Channel.Run(ctx, sink)
	return context.DeadlineExceeded
}

func TestChannelCatchesBrokenAdapters(t *testing.T) {
	tests := []struct {
		name string
		wrap func(c *channeltest.Channel) core.Channel
		p    func(p *memPlatform)
		want string
	}{
		{"no edit", func(c *channeltest.Channel) core.Channel { return noEdit{c} }, nil, "Edit did not replace"},
		{"no delete", func(c *channeltest.Channel) core.Channel { return noDelete{c} }, nil, "still on the platform"},
		{"truncates", func(c *channeltest.Channel) core.Channel { return truncating{c} }, nil, "did not arrive intact"},
		{"slow ack", func(c *channeltest.Channel) core.Channel { return slowAck{c} }, nil, "acknowledged after"},
		{"previews", nil, func(p *memPlatform) { p.leaky = true }, "did not disable link previews"},
		{"mention lost", nil, func(p *memPlatform) { p.noMention = true }, "Mentioned is false"},
		{"deaf", func(c *channeltest.Channel) core.Channel { return deaf{c} }, nil, "not delivered to the sink"},
		{"never connects", func(c *channeltest.Channel) core.Channel { return neverConnected{c} }, nil, "not connected"},
		{"no max len", func(c *channeltest.Channel) core.Channel { c.CapsV.MaxLen = 0; return c }, nil, "MaxLen = 0"},
		{"no edit interval", func(c *channeltest.Channel) core.Channel { c.CapsV.EditMinInterval = 0; return c }, nil, "EditMinInterval"},
		{"run error", func(c *channeltest.Channel) core.Channel { return failingRun{c} }, nil, "Run returned context deadline exceeded"},
		{"send fails", func(c *channeltest.Channel) core.Channel { c.Err = context.Canceled; return c }, nil, "Send:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := channeltest.New("telegram")
				var ch core.Channel = c
				if tt.wrap != nil {
					ch = tt.wrap(c)
				}
				p := &memPlatform{c: c}
				if tt.p != nil {
					tt.p(p)
				}
				wantFailure(t, record(t, func(r reporter) { runChannel(r, ch, p) }), tt.want)
			})
		})
	}
}

func TestChannelCatchesRunIgnoringCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := channeltest.New("telegram")
		s := stubborn{Channel: c, release: make(chan struct{})}
		fails := record(t, func(r reporter) { runChannel(r, s, &memPlatform{c: c}) })
		close(s.release)
		wantFailure(t, fails, "did not return within")
	})
}

func TestChannelGoodAdapterHasNoFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := channeltest.New("discord")
		c.CapsV = core.ChannelCaps{MaxLen: 2000}
		if fails := record(t, func(r reporter) { runChannel(r, c, &memPlatform{c: c}) }); len(fails) != 0 {
			t.Fatalf("unexpected failures: %q", fails)
		}
	})
}

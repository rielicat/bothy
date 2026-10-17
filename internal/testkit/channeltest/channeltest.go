// Package channeltest provides a fake core.Channel that records sends/edits and
// lets tests inject inbound messages and button presses.
package channeltest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

var (
	_ core.Channel        = (*Channel)(nil)
	_ core.PrivateReplier = (*Channel)(nil)
)

// TelegramCaps are the caps New starts with.
var TelegramCaps = core.ChannelCaps{Edit: true, Buttons: true, Typing: true, MaxLen: 4096, EditMinInterval: time.Second}

// Channel is safe for concurrent use. Send assigns message ids "1", "2", … in
// order, so Sent[i] has id strconv.Itoa(i+1); Edit appends to Edited and the
// edited ref to EditRefs. Ack records the toast. Err, when set, fails Send,
// Edit and Delete. Fetch serves Files by Attachment.FetchRef. ReplyPrivate
// records what only a Private message's sender sees.
type Channel struct {
	core.Channel
	NameV  string
	CapsV  core.ChannelCaps
	Sent   []core.OutMessage
	Edited []core.OutMessage
	Acks   []string

	mu        sync.Mutex
	EditRefs  []core.MsgRef
	Deleted   []core.MsgRef
	TypingIn  []core.ChatRef
	Files     map[string][]byte
	Err       error
	IdentityV string // SelfTest identity; default "@<name>_bot"
	// Chats answers InspectChat by chat id; a missing chat is ErrNotFound.
	Chats map[string]core.ChatInfo

	sink     core.InboundSink
	runCtx   context.Context
	running  bool
	pending  []func(context.Context, core.InboundSink)
	inSeq    int
	ackSeq   int
	privates []PrivateReply
	privErr  error
	privOK   int // ReplyPrivate calls that still succeed before privErr
}

// PrivateReply is one ReplyPrivate call: Text was shown to Sender alone in
// Chat, through Ref.
type PrivateReply struct {
	Chat   core.ChatRef
	Sender string
	Ref    string
	Text   string
}

// New returns a fake with Telegram-like caps unless overridden.
func New(name string) *Channel { return &Channel{NameV: name, CapsV: TelegramCaps} }

// Inject delivers an inbound message to the sink passed to Run.
//
// Delivery is synchronous while Run is active; before Run starts the message
// is queued and delivered when it does. Empty Chat.Channel, Chat.Kind, MsgID
// and At default to the channel name, a DM, "in-<n>" and now.
func (c *Channel) Inject(in core.Inbound) {
	c.mu.Lock()
	c.inSeq++
	if in.Chat.Channel == "" {
		in.Chat.Channel = c.NameV
	}
	if in.Chat.Kind == "" {
		in.Chat.Kind = core.ChatDM
	}
	if in.MsgID == "" {
		in.MsgID = "in-" + strconv.Itoa(c.inSeq)
	}
	if in.At.IsZero() {
		in.At = time.Now()
	}
	c.mu.Unlock()
	c.deliver(func(ctx context.Context, s core.InboundSink) { s.OnMessage(ctx, in) })
}

// Press delivers a button press for a previously sent message.
//
// The ActionEvent's AckRef is "ack-<n>"; delivery follows Inject's rules.
func (c *Channel) Press(msg core.MsgRef, senderID, data string) {
	c.mu.Lock()
	c.ackSeq++
	ev := core.ActionEvent{Chat: msg.Chat, Msg: msg, SenderID: senderID, Data: data, AckRef: "ack-" + strconv.Itoa(c.ackSeq)}
	c.mu.Unlock()
	c.deliver(func(ctx context.Context, s core.InboundSink) { s.OnAction(ctx, ev) })
}

func (c *Channel) deliver(fn func(context.Context, core.InboundSink)) {
	c.mu.Lock()
	if !c.running {
		c.pending = append(c.pending, fn)
		c.mu.Unlock()
		return
	}
	ctx, sink := c.runCtx, c.sink
	c.mu.Unlock()
	fn(ctx, sink)
}

func (c *Channel) Name() string { return c.NameV }

func (c *Channel) Caps() core.ChannelCaps { return c.CapsV }

// Run delivers queued injections, then blocks until ctx is done and returns nil.
func (c *Channel) Run(ctx context.Context, sink core.InboundSink) error {
	c.mu.Lock()
	c.sink, c.runCtx, c.running = sink, ctx, true
	pending := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, fn := range pending {
		fn(ctx, sink)
	}
	<-ctx.Done()
	c.mu.Lock()
	c.sink, c.runCtx, c.running = nil, nil, false
	c.mu.Unlock()
	return nil
}

func (c *Channel) Send(_ context.Context, m core.OutMessage) (core.MsgRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return core.MsgRef{}, c.Err
	}
	c.Sent = append(c.Sent, m)
	return core.MsgRef{Chat: m.Chat, ID: strconv.Itoa(len(c.Sent))}, nil
}

func (c *Channel) Edit(_ context.Context, ref core.MsgRef, m core.OutMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	c.Edited = append(c.Edited, m)
	c.EditRefs = append(c.EditRefs, ref)
	return nil
}

func (c *Channel) Delete(_ context.Context, ref core.MsgRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	c.Deleted = append(c.Deleted, ref)
	return nil
}

func (c *Channel) Typing(_ context.Context, chat core.ChatRef) error {
	c.mu.Lock()
	c.TypingIn = append(c.TypingIn, chat)
	c.mu.Unlock()
	return nil
}

func (c *Channel) Ack(_ context.Context, _ core.ActionEvent, toast string) error {
	c.mu.Lock()
	c.Acks = append(c.Acks, toast)
	c.mu.Unlock()
	return nil
}

// ReplyPrivate implements core.PrivateReplier: it records the reply, or
// fails as SetPrivateErr or SetPrivateErrAfter says.
func (c *Channel) ReplyPrivate(_ context.Context, in core.Inbound, markdown string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.privErr != nil {
		if c.privOK == 0 {
			return c.privErr
		}
		c.privOK--
	}
	c.privates = append(c.privates, PrivateReply{Chat: in.Chat, Sender: in.SenderID, Ref: in.ReplyRef, Text: markdown})
	return nil
}

// SetPrivateErr makes ReplyPrivate fail (nil restores it).
func (c *Channel) SetPrivateErr(err error) { c.SetPrivateErrAfter(0, err) }

// SetPrivateErrAfter lets the next n ReplyPrivate calls succeed, then fails
// every later one with err, as a reply URL good for n posts does.
func (c *Channel) SetPrivateErrAfter(n int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.privErr, c.privOK = err, n
}

// PrivateReplies returns the ReplyPrivate calls so far.
func (c *Channel) PrivateReplies() []PrivateReply {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.privates)
}

func (c *Channel) OpenDM(_ context.Context, externalUserID string) (core.ChatRef, error) {
	return core.ChatRef{Channel: c.NameV, ChatID: externalUserID, Kind: core.ChatDM}, nil
}

func (c *Channel) Fetch(_ context.Context, a core.Attachment, maxBytes int64) (io.ReadCloser, error) {
	c.mu.Lock()
	b, ok := c.Files[a.FetchRef]
	c.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("attachment %q: %w", a.FetchRef, core.ErrNotFound)
	}
	if int64(len(b)) > maxBytes {
		return nil, fmt.Errorf("attachment %q is %d bytes, over the %d byte limit", a.FetchRef, len(b), maxBytes)
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(b))), nil
}

func (c *Channel) SelfTest(context.Context) (string, error) { return c.identity(), nil }

func (c *Channel) identity() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.IdentityV != "" {
		return c.IdentityV
	}
	return "@" + c.NameV + "_bot"
}

// InspectChat implements core.ChatInspector from Chats.
func (c *Channel) InspectChat(_ context.Context, chat core.ChatRef) (core.ChatInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	info, ok := c.Chats[chat.ChatID]
	if !ok {
		return core.ChatInfo{}, fmt.Errorf("chat %q: %w", chat.ChatID, core.ErrNotFound)
	}
	return info, nil
}

// SetChat sets what InspectChat reports for chatID.
func (c *Channel) SetChat(chatID string, info core.ChatInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Chats == nil {
		c.Chats = map[string]core.ChatInfo{}
	}
	c.Chats[chatID] = info
}

// Status reports "connected" while Run is active, else "connecting".
func (c *Channel) Status() core.ChannelStatus {
	id := c.identity()
	c.mu.Lock()
	defer c.mu.Unlock()
	st := core.ChannelStatus{Name: c.NameV, Enabled: true, State: "connecting", Identity: id}
	if c.running {
		st.State = "connected"
	}
	return st
}

// SentMessages returns a copy of Sent.
func (c *Channel) SentMessages() []core.OutMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.Sent)
}

// EditedMessages returns a copy of Edited.
func (c *Channel) EditedMessages() []core.OutMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.Edited)
}

// AckToasts returns a copy of Acks.
func (c *Channel) AckToasts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.Acks)
}

// Visible returns what chatID shows now: the text of every sent message in
// send order, with its latest edit applied and deleted messages left out.
func (c *Channel) Visible(chatID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for i, m := range c.Sent {
		if m.Chat.ChatID != chatID {
			continue
		}
		id := strconv.Itoa(i + 1)
		if slices.ContainsFunc(c.Deleted, func(r core.MsgRef) bool { return r.ID == id }) {
			continue
		}
		text := m.Text
		for j, r := range c.EditRefs {
			if r.ID == id {
				text = c.Edited[j].Text
			}
		}
		out = append(out, text)
	}
	return out
}

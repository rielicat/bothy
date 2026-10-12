package conformance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	defaultPlatformID = "424242"
	ackDeadline       = 3 * time.Second
	connectTimeout    = 10 * time.Second
	deliveryTimeout   = 10 * time.Second
	platformTimeout   = 2 * time.Second
	stopTimeout       = 5 * time.Second
	pollInterval      = 10 * time.Millisecond
)

// recordingSink never blocks the adapter: deliveries beyond the buffer are
// dropped, which the suite then reports as missing.
type recordingSink struct {
	msgs chan core.Inbound
	acts chan core.ActionEvent
}

func (s *recordingSink) OnMessage(_ context.Context, in core.Inbound) {
	select {
	case s.msgs <- in:
	default:
	}
}

func (s *recordingSink) OnAction(_ context.Context, ev core.ActionEvent) {
	select {
	case s.acts <- ev:
	default:
	}
}

type channelSuite struct {
	ch       core.Channel
	p        PlatformFake
	sink     *recordingSink
	ctx      context.Context
	chat     core.ChatRef
	chatID   string
	senderID string
}

func runChannel(r reporter, ch core.Channel, p PlatformFake) {
	s := &channelSuite{ch: ch, p: p, chatID: defaultPlatformID, senderID: defaultPlatformID,
		sink: &recordingSink{msgs: make(chan core.Inbound, 64), acts: make(chan core.ActionEvent, 64)}}
	if ids, ok := p.(PlatformIDs); ok {
		s.chatID, s.senderID = ids.ConformanceIDs()
	}
	name, caps := ch.Name(), ch.Caps()
	s.chat = core.ChatRef{Channel: name, ChatID: s.chatID, Kind: core.ChatDM}
	if name == "" {
		r.Errorf("Name() is empty")
	}
	if caps.MaxLen <= 0 {
		r.Errorf("%s: Caps().MaxLen = %d, want > 0", name, caps.MaxLen)
	}
	if caps.Edit && caps.EditMinInterval <= 0 {
		r.Errorf("%s: Caps().EditMinInterval = %v with Edit set, want > 0", name, caps.EditMinInterval)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	done := make(chan error, 1)
	go func() { done <- ch.Run(ctx, s.sink) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				r.Errorf("%s: Run returned %v after its context ended", name, err)
			}
		case <-time.After(stopTimeout):
			r.Errorf("%s: Run did not return within %v of its context ending", name, stopTimeout)
		}
	}()
	if err := s.awaitConnected(done); err != nil {
		r.Fatalf("%s: %v", name, err)
	}

	check(r, "inbound", s.inbound)
	check(r, "send_edit_delete", s.sendEditDelete)
	check(r, "typing", s.typing)
	check(r, "max_len", s.maxLen)
	check(r, "ack", s.ack)
	check(r, "previews_disabled", s.previews)
}

func (s *channelSuite) awaitConnected(done chan error) error {
	deadline := time.Now().Add(connectTimeout)
	for {
		st := s.ch.Status()
		if st.State == "connected" {
			return nil
		}
		select {
		case err := <-done:
			done <- err
			return errors.New("Run returned before connecting: " + errString(err))
		default:
		}
		if time.Now().After(deadline) {
			return errors.New("Status().State is " + st.State + ", not connected, " + connectTimeout.String() + " after Run started")
		}
		time.Sleep(pollInterval)
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func token(prefix string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// eventually polls cond until it holds or timeout passes.
func eventually(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
}

func anyContains(texts []string, sub string) bool {
	for _, t := range texts {
		if strings.Contains(t, sub) {
			return true
		}
	}
	return false
}

func (s *channelSuite) awaitMessage(tok string) (core.Inbound, bool) {
	timeout := time.After(deliveryTimeout)
	for {
		select {
		case in := <-s.sink.msgs:
			if strings.Contains(in.Text, tok) {
				return in, true
			}
		case <-timeout:
			return core.Inbound{}, false
		}
	}
}

func (s *channelSuite) inbound(r reporter) {
	name := s.ch.Name()
	first := token("hello")
	s.p.InjectText(s.chatID, s.senderID, first, true)
	in, ok := s.awaitMessage(first)
	if !ok {
		r.Fatalf("%s: injected message %q not delivered to the sink within %v", name, first, deliveryTimeout)
	}
	if in.Chat.Channel != name || in.Chat.ChatID != s.chatID {
		r.Errorf("%s: Inbound.Chat = %+v, want channel %q chat %q", name, in.Chat, name, s.chatID)
	}
	if in.SenderID != s.senderID {
		r.Errorf("%s: Inbound.SenderID = %q, want %q", name, in.SenderID, s.senderID)
	}
	if got := strings.TrimSpace(in.Text); got != first {
		r.Errorf("%s: Inbound.Text = %q, want %q", name, got, first)
	}
	if !in.Mentioned {
		r.Errorf("%s: Inbound.Mentioned is false for a mentioned DM", name)
	}
	if in.MsgID == "" {
		r.Errorf("%s: Inbound.MsgID is empty", name)
	}
	second := token("again")
	s.p.InjectText(s.chatID, s.senderID, second, true)
	in2, ok := s.awaitMessage(second)
	if !ok {
		r.Fatalf("%s: second injected message not delivered within %v", name, deliveryTimeout)
	}
	if in2.MsgID == in.MsgID {
		r.Errorf("%s: two messages share MsgID %q", name, in.MsgID)
	}
}

func (s *channelSuite) sendEditDelete(r reporter) {
	name := s.ch.Name()
	sent := token("sent")
	ref, err := s.ch.Send(s.ctx, core.OutMessage{Chat: s.chat, Text: "conformance " + sent})
	if err != nil {
		r.Fatalf("%s: Send: %v", name, err)
	}
	if ref.ID == "" {
		r.Errorf("%s: Send returned an empty MsgRef.ID", name)
	}
	if ref.Chat.Channel != name || ref.Chat.ChatID != s.chatID {
		r.Errorf("%s: Send returned MsgRef.Chat %+v, want channel %q chat %q", name, ref.Chat, name, s.chatID)
	}
	if !eventually(platformTimeout, func() bool { return anyContains(s.p.SentTexts(s.chatID), sent) }) {
		r.Fatalf("%s: sent text %q not on the platform; SentTexts = %q", name, sent, s.p.SentTexts(s.chatID))
	}

	edited := token("edited")
	if err := s.ch.Edit(s.ctx, ref, core.OutMessage{Chat: s.chat, Text: "conformance " + edited}); err != nil {
		r.Fatalf("%s: Edit: %v", name, err)
	}
	replaced := func() bool {
		texts := s.p.SentTexts(s.chatID)
		return anyContains(texts, edited) && !anyContains(texts, sent)
	}
	if !eventually(platformTimeout, replaced) {
		r.Fatalf("%s: Edit did not replace %q with %q on the platform; SentTexts = %q", name, sent, edited, s.p.SentTexts(s.chatID))
	}

	if err := s.ch.Delete(s.ctx, ref); err != nil {
		r.Fatalf("%s: Delete: %v", name, err)
	}
	if !eventually(platformTimeout, func() bool { return !anyContains(s.p.SentTexts(s.chatID), edited) }) {
		r.Errorf("%s: deleted message %q still on the platform; SentTexts = %q", name, edited, s.p.SentTexts(s.chatID))
	}
}

func (s *channelSuite) typing(r reporter) {
	if !s.ch.Caps().Typing {
		r.Logf("%s: no typing indicator; skipped", s.ch.Name())
		return
	}
	if err := s.ch.Typing(s.ctx, s.chat); err != nil {
		r.Errorf("%s: Typing: %v", s.ch.Name(), err)
	}
}

func (s *channelSuite) maxLen(r reporter) {
	name, n := s.ch.Name(), s.ch.Caps().MaxLen
	if n <= 0 {
		return
	}
	text := strings.Repeat("a", n)
	if _, err := s.ch.Send(s.ctx, core.OutMessage{Chat: s.chat, Text: text}); err != nil {
		r.Fatalf("%s: Send of exactly MaxLen (%d) characters: %v", name, n, err)
	}
	if !eventually(platformTimeout, func() bool { return anyContains(s.p.SentTexts(s.chatID), text) }) {
		longest := 0
		for _, t := range s.p.SentTexts(s.chatID) {
			longest = max(longest, utf8.RuneCountInString(t))
		}
		r.Errorf("%s: a %d-character message did not arrive intact as one message (longest on the platform: %d)", name, n, longest)
	}
}

func (s *channelSuite) ack(r reporter) {
	name := s.ch.Name()
	if !s.ch.Caps().Buttons {
		r.Logf("%s: no buttons; skipped", name)
		return
	}
	data := "s:" + core.NewID(core.PrefixRun)
	ref, err := s.ch.Send(s.ctx, core.OutMessage{Chat: s.chat, Text: "conformance " + token("button"),
		Buttons: [][]core.Button{{{Label: "Stop", Data: data, Style: core.BtnDanger}}}})
	if err != nil {
		r.Fatalf("%s: Send with a button: %v", name, err)
	}
	pressed := time.Now()
	s.p.InjectButton(s.chatID, ref.ID, s.senderID, data)
	var ev core.ActionEvent
	timeout := time.After(ackDeadline)
wait:
	for {
		select {
		case ev = <-s.sink.acts:
			if ev.Data == data {
				break wait
			}
		case <-timeout:
			r.Fatalf("%s: button press not delivered to the sink within %v", name, ackDeadline)
		}
	}
	if ev.SenderID != s.senderID {
		r.Errorf("%s: ActionEvent.SenderID = %q, want %q", name, ev.SenderID, s.senderID)
	}
	if ev.Msg.ID != ref.ID || ev.Chat.ChatID != s.chatID {
		r.Errorf("%s: ActionEvent for message %q in chat %q, want %q in %q", name, ev.Msg.ID, ev.Chat.ChatID, ref.ID, s.chatID)
	}
	if ev.AckRef == "" {
		r.Errorf("%s: ActionEvent.AckRef is empty", name)
	}
	ctx, cancel := context.WithDeadline(s.ctx, pressed.Add(ackDeadline))
	defer cancel()
	if err := s.ch.Ack(ctx, ev, "Stopped"); err != nil {
		r.Errorf("%s: Ack: %v", name, err)
	}
	if took := time.Since(pressed); took > ackDeadline {
		r.Errorf("%s: press acknowledged after %v, want within %v", name, took, ackDeadline)
	}
}

func (s *channelSuite) previews(r reporter) {
	name := s.ch.Name()
	if _, err := s.ch.Send(s.ctx, core.OutMessage{Chat: s.chat, Text: "see https://example.com/" + token("link")}); err != nil {
		r.Fatalf("%s: Send with a link: %v", name, err)
	}
	if !s.p.PreviewsDisabled() {
		r.Errorf("%s: an outgoing message did not disable link previews/unfurls/embeds", name)
	}
}

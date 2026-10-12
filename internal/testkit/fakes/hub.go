package fakes

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/bothyhq/bothy/internal/core"
)

var _ core.ChannelHub = (*Hub)(nil)

// Hub records Messenger sends and StreamTo calls; StreamTo returns a *Sink.
// PreferredChats maps users to their preferred DM.
//
// Streams and StreamModes are keyed by ChatRef.Key(); repeated StreamTo calls
// for one chat return the same Sink so its events accumulate. Send returns
// message ids "1", "2", … in order. OnMessage and OnAction record their
// arguments; Test, DeepLink and Status delegate to channels passed to Add.
type Hub struct {
	core.ChannelHub
	Sent           []string // "<chat key>|<markdown>"
	Streams        map[string]*Sink
	PreferredChats map[core.UserID]core.ChatRef

	mu          sync.Mutex
	StreamModes map[string]string
	Inbound     []core.Inbound
	Actions     []core.ActionEvent
	Channels    []core.Channel
}

func (h *Hub) Send(_ context.Context, chat core.ChatRef, markdown string) (core.MsgRef, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Sent = append(h.Sent, chat.Key()+"|"+markdown)
	return core.MsgRef{Chat: chat, ID: strconv.Itoa(len(h.Sent))}, nil
}

func (h *Hub) StreamTo(chat core.ChatRef, mode string) core.RunSink {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := chat.Key()
	if h.Streams == nil {
		h.Streams = map[string]*Sink{}
	}
	if h.StreamModes == nil {
		h.StreamModes = map[string]string{}
	}
	h.StreamModes[key] = mode
	s, ok := h.Streams[key]
	if !ok {
		s = &Sink{}
		h.Streams[key] = s
	}
	return s
}

// Stream returns the sink StreamTo created for chat, if any.
func (h *Hub) Stream(chat core.ChatRef) (*Sink, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.Streams[chat.Key()]
	return s, ok
}

func (h *Hub) PreferredChat(_ context.Context, u core.UserID) (core.ChatRef, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.PreferredChats[u]
	return c, ok
}

func (h *Hub) OnMessage(_ context.Context, in core.Inbound) {
	h.mu.Lock()
	h.Inbound = append(h.Inbound, in)
	h.mu.Unlock()
}

func (h *Hub) OnAction(_ context.Context, ev core.ActionEvent) {
	h.mu.Lock()
	h.Actions = append(h.Actions, ev)
	h.mu.Unlock()
}

func (h *Hub) Add(ch core.Channel) {
	h.mu.Lock()
	h.Channels = append(h.Channels, ch)
	h.mu.Unlock()
}

func (h *Hub) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (h *Hub) channel(name string) core.Channel {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.Channels {
		if ch.Name() == name {
			return ch
		}
	}
	return nil
}

func (h *Hub) Status() []core.ChannelStatus {
	h.mu.Lock()
	chs := append([]core.Channel(nil), h.Channels...)
	h.mu.Unlock()
	out := make([]core.ChannelStatus, len(chs))
	for i, ch := range chs {
		out[i] = ch.Status()
	}
	return out
}

func (h *Hub) Test(ctx context.Context, name string) (string, error) {
	ch := h.channel(name)
	if ch == nil {
		return "", fmt.Errorf("channel %q: %w", name, core.ErrNotFound)
	}
	return ch.SelfTest(ctx)
}

func (h *Hub) DeepLink(channel, code string) string {
	if ch := h.channel(channel); ch != nil {
		return ch.DeepLink(code)
	}
	return ""
}

// Messages returns a copy of Sent.
func (h *Hub) Messages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.Sent...)
}

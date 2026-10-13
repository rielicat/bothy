package discord

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	// ackTimeout leaves room inside Discord's 3 s interaction deadline.
	ackTimeout    = 2500 * time.Millisecond
	lookupTimeout = 5 * time.Second
	parentsMax    = 1024
)

func (a *adapter) onMessage(ctx context.Context, sink core.InboundSink, m discord.Message) {
	self := a.self()
	if m.Author.ID == self || m.Type.System() {
		return
	}
	chat := a.chatFor(ctx, m.ChannelID, m.GuildID)
	text, mentioned := stripMention(m.Content, self)
	in := core.Inbound{
		Chat:           chat,
		MsgID:          m.ID.String(),
		SenderID:       m.Author.ID.String(),
		SenderName:     senderName(m),
		SenderUsername: m.Author.Username,
		SenderIsBot:    m.Author.Bot || m.WebhookID != nil,
		Text:           text,
		At:             m.CreatedAt,
	}
	in.Mentioned = chat.Kind == core.ChatDM || mentioned || mentions(m.Mentions, self) ||
		(m.ReferencedMessage != nil && m.ReferencedMessage.Author.ID == self)
	for _, at := range m.Attachments {
		mime := ""
		if at.ContentType != nil {
			mime = *at.ContentType
		}
		in.Attachments = append(in.Attachments, core.Attachment{Name: at.Filename, MIME: mime, Size: int64(at.Size), FetchRef: at.URL})
	}
	if in.Text == "" && len(in.Attachments) == 0 {
		return
	}
	if in.At.IsZero() {
		in.At = m.ID.Time()
	}
	sink.OnMessage(ctx, in)
}

// onPress acknowledges a button press with a deferred update before the
// sink sees it, so the 3 s deadline holds however long the hub takes and a
// later Ack's followup always comes after the interaction response.
func (a *adapter) onPress(ctx context.Context, push func(job), ci discord.ComponentInteraction) {
	data, ok := ci.Data.(discord.ButtonInteractionData)
	if !ok {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, ackTimeout)
	err := a.client().DeferUpdate(dctx, ci.ID(), ci.Token())
	cancel()
	if err != nil {
		a.log.Warn("discord: cannot acknowledge button press", "err", err)
	}
	chat := a.chatFor(ctx, ci.Message.ChannelID, ci.GuildID())
	ev := core.ActionEvent{
		Chat:     chat,
		Msg:      core.MsgRef{Chat: chat, ID: ci.Message.ID.String()},
		SenderID: ci.User().ID.String(),
		Data:     data.CustomID(),
		AckRef:   ci.ApplicationID().String() + ":" + ci.Token(),
	}
	push(func(ctx context.Context, sink core.InboundSink) { sink.OnAction(ctx, ev) })
}

// chatFor maps a channel to its ChatRef. A thread is reported as
// ThreadID under its parent channel, as Slack threads are, so a group space
// covers the threads of its channel.
func (a *adapter) chatFor(ctx context.Context, channel snowflake.ID, guild *snowflake.ID) core.ChatRef {
	if guild == nil {
		return core.ChatRef{Channel: name, ChatID: channel.String(), Kind: core.ChatDM}
	}
	chat := core.ChatRef{Channel: name, ChatID: channel.String(), Kind: core.ChatGroup}
	parent, ok := a.parents.get(channel)
	if !ok {
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		p, err := a.client().ThreadParent(lctx, channel)
		cancel()
		if err != nil {
			a.log.Warn("discord: cannot look up channel", "channel", channel.String(), "err", err)
			return chat
		}
		a.parents.put(channel, p)
		parent = p
	}
	if parent != 0 {
		chat.ChatID, chat.ThreadID = parent.String(), channel.String()
	}
	return chat
}

// stripMention removes mentions of the bot ("<@id>" and the legacy "<@!id>")
// and reports whether there were any.
func stripMention(content string, self snowflake.ID) (string, bool) {
	if self == 0 {
		return strings.TrimSpace(content), false
	}
	id := self.String()
	out, found := content, false
	for _, tag := range []string{"<@" + id + ">", "<@!" + id + ">"} {
		if strings.Contains(out, tag) {
			found = true
			out = strings.ReplaceAll(out, tag, "")
		}
	}
	return strings.TrimSpace(out), found
}

func mentions(users []discord.User, self snowflake.ID) bool {
	for _, u := range users {
		if self != 0 && u.ID == self {
			return true
		}
	}
	return false
}

func senderName(m discord.Message) string {
	if m.Member != nil && m.Member.Nick != nil && *m.Member.Nick != "" {
		return *m.Member.Nick
	}
	return m.Author.EffectiveName()
}

// parentCache remembers which channels are threads (parent != 0). Gateway
// caches are off, so this tiny map is the only channel state kept.
type parentCache struct {
	mu sync.Mutex
	m  map[snowflake.ID]snowflake.ID
}

func (c *parentCache) get(id snowflake.ID) (snowflake.ID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.m[id]
	return p, ok
}

func (c *parentCache) put(id, parent snowflake.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= parentsMax {
		c.m = make(map[snowflake.ID]snowflake.ID)
	}
	c.m[id] = parent
}

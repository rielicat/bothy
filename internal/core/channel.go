package core

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type ChatKind string

const (
	ChatDM    ChatKind = "dm"
	ChatGroup ChatKind = "group" // groups, Discord guild channels, Slack channels
	ChatWeb   ChatKind = "web"
	ChatAPI   ChatKind = "api"
)

type ChatRef struct {
	Channel  string   `json:"channel"` // "telegram" | "discord" | "slack" | "web" | "api"
	ChatID   string   `json:"chat_id"`
	ThreadID string   `json:"thread_id,omitempty"` // Slack thread ts, Telegram topic, Discord thread
	Kind     ChatKind `json:"kind"`
}

// Key is the session lookup key for channel sessions.
func (c ChatRef) Key() string { return c.Channel + ":" + c.ChatID + ":" + c.ThreadID }

type Attachment struct {
	Name     string `json:"name"`
	MIME     string `json:"mime"`
	Size     int64  `json:"size"`
	FetchRef string `json:"fetch_ref"` // adapter-private (Telegram file_id, Slack url_private, Discord CDN url)
}

type Inbound struct {
	Chat ChatRef
	// MsgID must be unique per adapter (it keys inbound_dedup): Telegram
	// "<chat_id>:<message_id>", Slack "<channel>:<ts>", Discord the message
	// snowflake. Edits of an existing message are not redelivered as new.
	MsgID          string
	SenderID       string // platform user id
	SenderName     string
	SenderUsername string
	SenderIsBot    bool
	Text           string // mention of the bot stripped
	Mentioned      bool   // DM, @mention, reply-to-bot, or slash command
	Attachments    []Attachment
	At             time.Time
	// ChatTitle is the group's title as the platform reports it (names a
	// newly sighted space, and renames one still under its fallback name);
	// "" when unknown.
	ChatTitle string
	// AddedBy is the platform user id that added the bot to this group or
	// owns it, when the platform reports it (Telegram my_chat_member.from,
	// Slack inviter, Discord guild owner); "" otherwise. A group Inbound with
	// AddedBy and no text or attachments only announces the group.
	AddedBy string
	// Forwarded marks content the sender relayed from someone else
	// (Telegram forward_origin or via_bot). The hub treats it as untrusted
	// even from a linked user and never as a command.
	Forwarded bool
	// Private marks a message only its sender saw (a Slack slash command),
	// so a code in it was not shown to the chat. In a group it never creates
	// a space, since slash commands work in channels the bot is not in, and
	// the hub answers anyone it would not answer in that group in their DM.
	Private bool
}

type MsgRef struct {
	Chat ChatRef `json:"chat"`
	ID   string  `json:"id"`
}

type ButtonStyle uint8

const (
	BtnDefault ButtonStyle = iota
	BtnPrimary
	BtnDanger
)

type Button struct {
	Label string      `json:"label"`
	Data  string      `json:"data,omitempty"` // callback payload <= 64 bytes; produced only by Approvals.ButtonData or the router ("s:<run>" stop)
	URL   string      `json:"url,omitempty"`  // link button (no callback)
	Style ButtonStyle `json:"style"`
}

// OutMessage text is the Bothy markdown subset: paragraphs, **bold**, *italic*,
// `code`, fenced code, lists, [text](url), headings rendered as bold. No images,
// no raw HTML. The router has already split it to fit Caps.MaxLen and
// neutralized links for exposed sessions. Agent output never carries Buttons.
type OutMessage struct {
	Chat    ChatRef
	Text    string
	Buttons [][]Button
	ReplyTo string
	Silent  bool
}

type ActionEvent struct {
	Chat     ChatRef
	Msg      MsgRef
	SenderID string
	Data     string
	AckRef   string // adapter-private: callback_query id, interaction token, Slack response_url
}

type ChannelCaps struct {
	Edit            bool          `json:"edit"`
	Buttons         bool          `json:"buttons"`
	Threads         bool          `json:"threads"`
	Typing          bool          `json:"typing"`
	MaxLen          int           `json:"max_len"`           // visible characters per message after rendering
	EditMinInterval time.Duration `json:"edit_min_interval"` // Telegram 1s, Discord 1s, Slack 1.2s
}

type InboundSink interface {
	OnMessage(ctx context.Context, in Inbound) // must not block on agent work
	OnAction(ctx context.Context, ev ActionEvent)
}

type ChannelStatus struct {
	Name      string    `json:"name"`
	Enabled   bool      `json:"enabled"`
	State     string    `json:"state"` // connecting | connected | backoff | auth_failed | disabled
	Detail    string    `json:"detail,omitempty"`
	Identity  string    `json:"identity,omitempty"` // "@my_bothy_bot"
	Since     time.Time `json:"since"`
	DeepLinks bool      `json:"deep_links"`
}

// Channel adapters never do auth, policy, splitting or neutralization: the
// router does. Adapters escape for their dialect and always disable link
// previews/unfurls (Telegram link_preview_options.is_disabled, Slack
// unfurl_links/unfurl_media=false, Discord flags SUPPRESS_EMBEDS).
type Channel interface {
	Name() string
	Caps() ChannelCaps
	Run(ctx context.Context, sink InboundSink) error // blocks; reconnects internally with backoff
	Send(ctx context.Context, m OutMessage) (MsgRef, error)
	Edit(ctx context.Context, ref MsgRef, m OutMessage) error
	Delete(ctx context.Context, ref MsgRef) error
	Typing(ctx context.Context, chat ChatRef) error
	Ack(ctx context.Context, ev ActionEvent, toast string) error // within 3s
	OpenDM(ctx context.Context, externalUserID string) (ChatRef, error)
	Fetch(ctx context.Context, a Attachment, max int64) (io.ReadCloser, error)
	SelfTest(ctx context.Context) (identity string, err error) // getMe / users/@me / auth.test
	DeepLink(code string) string                               // "" when unsupported; Telegram t.me/<bot>?start=<code>
	Status() ChannelStatus
}

// Cards on channels without Caps.Buttons (P1 email, Signal) are always sent
// truncated as text with the Card.WebURL link only; they can never approve.

// Messenger sends a system-originated message (message tool, schedule delivery,
// hello DM). Bound to explicit chats; the message tool only passes env.Chat.
type Messenger interface {
	Send(ctx context.Context, chat ChatRef, markdown string) (MsgRef, error)
}

// ChannelHub is the channel router. It owns adapters, inbound gatekeeping,
// commands, run streaming to chats and approval card delivery.
type ChannelHub interface {
	InboundSink
	Messenger
	Add(ch Channel)
	Run(ctx context.Context) error
	Status() []ChannelStatus
	Test(ctx context.Context, name string) (string, error)
	DeepLink(channel, code string) string
	// PreferredChat returns the user's DM on their preferred linked channel.
	PreferredChat(ctx context.Context, u UserID) (ChatRef, bool)
	// StreamTo returns a sink that delivers a run to a chat: mode "stream"
	// (throttled edits) or "final" (one message at run end; schedules).
	StreamTo(chat ChatRef, mode string) RunSink
}

// AdapterDeps is what channel adapters may use. Adapters import only core,
// config and their protocol library.
type AdapterDeps struct {
	Secret   func(ctx context.Context, ref string) (Secret, error) // resolves "secret:<name>" (global scope) and "aws-sm:<secret>"
	HTTP     func(d SystemDest) *http.Client                       // Egress.SystemClient
	Meta     MetaRepo                                              // long-poll offsets, resume cursors (keys "channel.<name>.*")
	StateDir string                                                // <data>/channels/<name>/ (0700): P1 adapters with their own state (Matrix crypto store, WhatsApp device store)
	Clock    Clock
	Log      *slog.Logger
}

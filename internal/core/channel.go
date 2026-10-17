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
	// AddedBy is the platform user id that added the bot to this group,
	// when the platform reports it (Telegram my_chat_member.from); ""
	// otherwise. A group Inbound with AddedBy and no text or attachments only
	// announces the group.
	AddedBy string
	// Team is a group chat's team when the platform says: the Discord guild
	// id; "" otherwise.
	Team string
	// Forwarded marks content the sender relayed from someone else
	// (Telegram forward_origin or via_bot). The hub treats it as untrusted
	// even from the teammate whose DM it is, and never as a command.
	Forwarded bool
	// Private marks a message only its sender saw (a Slack slash command).
	// In a group it never creates a space, since slash commands work in
	// channels the bot is not in; the hub answers in their DM anyone it
	// would not answer in that group.
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
	// Mentions are the platform user ids whose "<app>://user?id=…" link in
	// Text shows as that person's mention (Slack <@U…>, Discord's pill).
	// Only the hub's approval lines set it: the same link in model text
	// stays a plain link, so nothing Bothy reads can make it ping someone.
	Mentions []string
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
	// CommandPrefix is what a person types before a chat command's name:
	// "/bothy " on Slack, whose one slash command takes the rest as its
	// text; "" means "/" (Telegram, and Discord's plain-text commands).
	CommandPrefix string `json:"command_prefix,omitempty"`
}

type InboundSink interface {
	OnMessage(ctx context.Context, in Inbound) // must not block on agent work
	OnAction(ctx context.Context, ev ActionEvent)
}

type ChannelStatus struct {
	Name     string    `json:"name"`
	Enabled  bool      `json:"enabled"`
	State    string    `json:"state"` // connecting | connected | backoff | auth_failed | disabled
	Detail   string    `json:"detail,omitempty"`
	Identity string    `json:"identity,omitempty"` // "@my_bothy_bot"
	Since    time.Time `json:"since"`
	Team     string    `json:"team,omitempty"` // Slack: the workspace name (auth.test); Discord: "" (several servers)
	Org      bool      `json:"org,omitempty"`  // Slack: installed org-wide on Enterprise Grid
	// MemberCheck (Discord): "ok", or "no_access" when every server refused
	// the last member lookup, so only people an admin lets in get in.
	MemberCheck string `json:"member_check,omitempty"`
	// IdleServers (Discord, connected): servers Bothy is in where no channel
	// is on, whose members therefore can't use it.
	IdleServers int `json:"idle_servers,omitempty"`
}

// MemberStatus is what a chat app says about whether a person belongs to
// the team Bothy serves there.
type MemberStatus string

const (
	MemberUnchecked MemberStatus = ""         // never asked
	MemberUnknown   MemberStatus = "unknown"  // the platform can't tell (Telegram; Discord with no known server or no access)
	MemberYes       MemberStatus = "member"   // in the workspace or an enabled server
	MemberOutsider  MemberStatus = "outsider" // guest, other company, bot, deactivated, not in an enabled server
)

type Membership struct {
	Status MemberStatus
	Team   string // the workspace or server's name for copy; "" when unknown or several
}

// MemberChecker is implemented by adapters whose platform can say whether a
// person belongs to the team Bothy serves (Slack, Discord).
type MemberChecker interface {
	// CheckMember asks about externalID. teams are the Discord guild ids of
	// the group chats Bothy is turned on in (Slack checks its own workspace).
	// An error means the platform did not answer; it is never a verdict.
	CheckMember(ctx context.Context, externalID string, teams []string) (Membership, error)
	// MemberScope names what membership means right now (Slack: team and
	// enterprise ids); when it changes, every verdict on this chat app is stale.
	MemberScope() string
}

// ChatInfo describes a group chat for the admins' turn-on choice.
type ChatInfo struct {
	TeamID, TeamName, Title string
	Members                 int  // 0 = unknown
	External                bool // people from other companies are in it (Slack Connect)
}

// ChatInspector is implemented by adapters that can describe a group chat.
type ChatInspector interface {
	InspectChat(ctx context.Context, chat ChatRef) (ChatInfo, error)
}

type TeamInfo struct{ ID, Name string }

// TeamLister: Discord lists the servers it is in (GUILD_CREATE), so the hub
// can warn about servers where no channel is on.
type TeamLister interface{ Teams() []TeamInfo }

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
	// PrincipalChat is where Bothy's own messages to a principal go: an
	// admitted teammate's DM (opened when unknown), an enabled channel's chat;
	// false for web accounts, which hear on the web only.
	PrincipalChat(ctx context.Context, p PrincipalID) (ChatRef, bool)
	// TeamActive is nil when a team-owned principal may run a schedule now;
	// otherwise why not (blocked, no longer in the team, channel off, or a
	// membership verdict older than MemberStale that couldn't be renewed).
	TeamActive(ctx context.Context, p PrincipalID) error
	// Greet sends a teammate their pending greeting (hello or moved) once.
	Greet(ctx context.Context, channel, externalID string) error
	// TellApprover tells a teammate an admin made them an approver.
	TellApprover(ctx context.Context, channel, externalID string) error
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

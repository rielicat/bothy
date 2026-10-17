package slack

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/rielicat/bothy/internal/core"
)

const (
	lookupTimeout  = 5 * time.Second
	namesMax       = 512
	nameTTL        = time.Hour
	nameMissTTL    = time.Minute
	scopeWarnEvery = time.Hour
)

// commands are the hub's chat commands (SPEC §5.1); "/bothy stop" arrives
// as "/stop", anything else after /bothy as a plain mentioned message.
var commands = map[string]bool{
	"stop": true, "stopall": true, "resume": true, "new": true, "status": true, "pin": true, "pins": true,
	"unpin": true, "compact": true, "model": true, "usage": true, "help": true, "pair": true, "start": true,
	"refine": true,
}

// eventJob turns an Events API callback into a job, or nil for events the
// hub does not need. app_mention and the message.channels copy of the same
// message share a MsgID, so the hub's dedup keeps one.
func (a *adapter) eventJob(e slackevents.EventsAPIEvent) job {
	if e.Type != slackevents.CallbackEvent {
		return nil
	}
	var in core.Inbound
	var ok bool
	switch ev := e.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		in, ok = a.fromMention(ev)
	case *slackevents.MessageEvent:
		in, ok = a.fromMessage(ev)
	}
	if !ok {
		return nil
	}
	return func(ctx context.Context, sink core.InboundSink) {
		in.SenderName, in.SenderUsername = a.userNames(ctx, in.SenderID)
		if in.Chat.Kind == core.ChatGroup {
			in.ChatTitle = a.channelTitle(ctx, in.Chat.ChatID)
		}
		sink.OnMessage(ctx, in)
	}
}

func (a *adapter) fromMention(e *slackevents.AppMentionEvent) (core.Inbound, bool) {
	self, _ := a.self()
	// Other apps and workflows post with a bot_id; Bothy answers people only.
	if e.User == "" || e.User == self || e.BotID != "" {
		return core.Inbound{}, false
	}
	text, _ := stripMention(e.Text, self)
	in := core.Inbound{
		Chat:        chatRef(e.Channel, "", e.ThreadTimeStamp),
		MsgID:       e.Channel + ":" + e.TimeStamp,
		SenderID:    e.User,
		Text:        plainText(text),
		Mentioned:   true,
		Attachments: attachments(e.Files),
		At:          tsTime(e.TimeStamp),
	}
	return in, in.Text != "" || len(in.Attachments) > 0
}

func (a *adapter) fromMessage(e *slackevents.MessageEvent) (core.Inbound, bool) {
	switch e.SubType {
	case "", "file_share", "thread_broadcast":
	default: // edits, deletions, joins and other bots' bot_message posts
		return core.Inbound{}, false
	}
	self, _ := a.self()
	if e.User == "" || e.User == self || e.BotID != "" || e.Message != nil && (e.Message.BotID != "" || e.Message.BotProfile != nil) {
		return core.Inbound{}, false
	}
	text, mentioned := stripMention(e.Text, self)
	in := core.Inbound{
		Chat:     chatRef(e.Channel, e.ChannelType, e.ThreadTimeStamp),
		MsgID:    e.Channel + ":" + e.TimeStamp,
		SenderID: e.User,
		Text:     plainText(text),
		At:       tsTime(e.TimeStamp),
	}
	if e.Message != nil {
		in.Attachments = attachments(e.Message.Files)
	}
	// A reply in a thread the bot started is Slack's reply-to-bot.
	replyToBot := self != "" && in.Chat.ThreadID != "" && e.Message != nil && e.Message.ParentUserId == self
	in.Mentioned = in.Chat.Kind == core.ChatDM || mentioned || replyToBot
	return in, in.Text != "" || len(in.Attachments) > 0
}

// slashJob maps a /bothy command. Slack shows it to no one but its sender,
// so it is Private, and it works in channels the app is not in; typed in a
// channel, its response_url is how the hub answers the sender alone there.
func (a *adapter) slashJob(c slack.SlashCommand) job {
	in := core.Inbound{
		Chat: chatRef(c.ChannelID, "", ""),
		// Slash commands have no ts; the trigger id is unique per invocation.
		MsgID:          c.ChannelID + ":cmd:" + c.TriggerID,
		SenderID:       c.UserID,
		SenderUsername: c.UserName,
		Text:           slashText(c.Text),
		Mentioned:      true,
		At:             a.clock.Now(),
		Private:        true,
	}
	lookup := false
	if in.Chat.Kind == core.ChatGroup {
		in.ReplyRef = c.ResponseURL
		in.ChatTitle = hashName(c.ChannelName)
		lookup = in.ChatTitle == "" && !strings.HasPrefix(c.ChannelName, "mpdm-")
	}
	return func(ctx context.Context, sink core.InboundSink) {
		in.SenderName, _ = a.userNames(ctx, in.SenderID)
		if lookup {
			in.ChatTitle = a.channelTitle(ctx, in.Chat.ChatID)
		}
		if in.Chat.Kind == core.ChatDM {
			// A slash command also works in a DM between two people, which
			// the bot cannot post in: answer in the sender's own DM with the
			// bot. conversations.open returns that DM, so a command typed
			// there keeps its chat.
			dm, err := a.OpenDM(ctx, in.SenderID)
			if err != nil {
				a.log.Warn("slack: opening the sender's DM failed", "err", err)
				return
			}
			in.Chat = dm
		}
		sink.OnMessage(ctx, in)
	}
}

// hashName is a channel as Slack shows it, "#name"; "" for the placeholders
// a slash command sends instead ("directmessage", and "privategroup" for a
// private channel) and for group DMs, whose "mpdm-…" names are no title.
func hashName(name string) string {
	switch {
	case name == "", name == "directmessage", name == "privategroup", strings.HasPrefix(name, "mpdm-"):
		return ""
	}
	return "#" + name
}

func slashText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "/help"
	}
	first, rest, _ := strings.Cut(text, " ")
	if cmd := strings.ToLower(first); commands[cmd] {
		return strings.TrimSpace("/" + cmd + " " + rest)
	}
	return plainText(text)
}

// actionJob maps a block_actions press. Link buttons report presses too,
// with no value; they need nothing from the hub.
func (a *adapter) actionJob(cb slack.InteractionCallback) job {
	if cb.Type != slack.InteractionTypeBlockActions {
		return nil
	}
	channel := either(cb.Container.ChannelID, cb.Channel.ID)
	ts := either(cb.Container.MessageTs, cb.Message.Timestamp)
	thread := either(cb.Container.ThreadTs, cb.Message.ThreadTimestamp)
	if thread == ts {
		// A message that has thread replies reports its own ts as thread_ts,
		// but it was sent to the channel and Send's MsgRef says so.
		thread = ""
	}
	chat := chatRef(channel, "", thread)
	var evs []core.ActionEvent
	for _, act := range cb.ActionCallback.BlockActions {
		if act.Value == "" {
			continue
		}
		evs = append(evs, core.ActionEvent{Chat: chat, Msg: core.MsgRef{Chat: chat, ID: ts}, SenderID: cb.User.ID, Data: act.Value, AckRef: cb.ResponseURL})
	}
	if len(evs) == 0 || channel == "" || ts == "" {
		return nil
	}
	return func(ctx context.Context, sink core.InboundSink) {
		for _, ev := range evs {
			sink.OnAction(ctx, ev)
		}
	}
}

func either(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// chatRef maps a conversation: "im" (or a D… id) is a DM, every other kind
// (public and private channels, group DMs) a group.
func chatRef(channel, channelType, threadTS string) core.ChatRef {
	kind := core.ChatGroup
	if channelType == slackevents.ChannelTypeIM || (channelType == "" && strings.HasPrefix(channel, "D")) {
		kind = core.ChatDM
	}
	return core.ChatRef{Channel: name, ChatID: channel, ThreadID: threadTS, Kind: kind}
}

func attachments(files []slack.File) []core.Attachment {
	var out []core.Attachment
	for _, f := range files {
		ref := either(f.URLPrivateDownload, f.URLPrivate)
		if ref == "" {
			continue
		}
		out = append(out, core.Attachment{Name: f.Name, MIME: f.Mimetype, Size: int64(f.Size), FetchRef: ref})
	}
	return out
}

func tsTime(ts string) time.Time {
	sec, frac, _ := strings.Cut(ts, ".")
	s, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return time.Time{}
	}
	us, _ := strconv.ParseInt((frac + "000000")[:6], 10, 64)
	return time.Unix(s, us*1000)
}

// stripMention removes mentions of the bot ("<@U…>" or "<@U…|name>") and
// reports whether there were any.
func stripMention(text, self string) (string, bool) {
	if self == "" {
		return strings.TrimSpace(text), false
	}
	found := false
	out := mentionRe.ReplaceAllStringFunc(text, func(m string) string {
		if mentionRe.FindStringSubmatch(m)[1] != self {
			return m
		}
		found = true
		return ""
	})
	return strings.TrimSpace(out), found
}

var (
	mentionRe = regexp.MustCompile(`<@([A-Z0-9]+)(?:\|[^>]*)?>`)
	linkRe    = regexp.MustCompile(`<([^<>|]*)(?:\|([^<>]*))?>`)
)

// plainText turns Slack's message markup into the text a person reads:
// mentions and channel links become @name and #name, links show their
// label and target, and the &amp; &lt; &gt; escapes are undone.
func plainText(s string) string {
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := linkRe.FindStringSubmatch(m)
		target, label := sub[1], sub[2]
		switch {
		case strings.HasPrefix(target, "@"), strings.HasPrefix(target, "#"):
			if label != "" {
				return target[:1] + strings.TrimPrefix(label, target[:1])
			}
			return target
		case strings.HasPrefix(target, "!"):
			if label != "" {
				return label
			}
			return "@" + strings.TrimPrefix(target, "!")
		case label == "" || label == target || strings.HasSuffix(target, "/"+label) || strings.HasSuffix(target, ":"+label):
			// Slack auto-links "example.com" as <http://example.com|example.com>.
			return target
		default:
			return label + " (" + target + ")"
		}
	})
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(s)
}

// userNames returns a user's handle and display name via users.info,
// cached so that busy channels cost one call per user per hour.
func (a *adapter) userNames(ctx context.Context, id string) (display, handle string) {
	if id == "" {
		return "", ""
	}
	if e, ok := a.names.get(id, a.clock.Now()); ok {
		return e.display, e.handle
	}
	if _, err := a.userInfo(ctx, id); err != nil {
		a.log.Warn("slack: users.info failed", "user", id, "err", err)
		a.names.put(id, nameEntry{}, a.clock.Now().Add(nameMissTTL))
		return "", ""
	}
	e, _ := a.names.get(id, a.clock.Now())
	return e.display, e.handle
}

// channelTitle returns a channel's "#name" via conversations.info, cached
// like userNames, or "" when Slack will not say. An app installed before
// the manifest asked for channels:read and groups:read gets missing_scope
// until it is reinstalled; its spaces keep their fallback names meanwhile.
func (a *adapter) channelTitle(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	if e, ok := a.channels.get(id, a.clock.Now()); ok {
		return e.display
	}
	var e nameEntry
	api, err := a.client(ctx)
	if err == nil {
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		var ch *slack.Channel
		ch, err = api.GetConversationInfoContext(lctx, &slack.GetConversationInfoInput{ChannelID: id})
		cancel()
		if err == nil && !ch.IsIM && !ch.IsMpIM {
			e.display = hashName(ch.Name)
		}
	}
	ttl := nameTTL
	if err != nil {
		ttl = nameMissTTL
		switch slackCode(err) {
		case "missing_scope":
			a.warnScope(err)
		case "channel_not_found":
			// A slash command from a private channel the app is not in.
		default:
			a.log.Warn("slack: conversations.info failed", "channel", id, "err", err)
		}
	}
	a.channels.put(id, e, a.clock.Now().Add(ttl))
	return e.display
}

// warnScope logs at most once per scopeWarnEvery: every new channel of an
// app without the scopes would otherwise repeat it.
func (a *adapter) warnScope(err error) {
	now := a.clock.Now()
	a.mu.Lock()
	warn := a.scopeWarned.IsZero() || now.Sub(a.scopeWarned) >= scopeWarnEvery
	if warn {
		a.scopeWarned = now
	}
	a.mu.Unlock()
	if warn {
		a.log.Warn("slack: the app cannot read channel names; reinstall it to your workspace to grant channels:read and groups:read",
			"err", err)
	}
}

type nameEntry struct {
	display, handle string
	expires         time.Time
}

type nameCache struct {
	mu sync.Mutex
	m  map[string]nameEntry
}

func (c *nameCache) get(id string, now time.Time) (nameEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	return e, ok && now.Before(e.expires)
}

func (c *nameCache) put(id string, e nameEntry, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= namesMax {
		c.m = make(map[string]nameEntry)
	}
	e.expires = expires
	c.m[id] = e
}

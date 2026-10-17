package channel

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/rielicat/bothy/internal/core"
)

// maxChatQueue bounds the per-chat work waiting behind a slow submission; the
// runner's own mailbox (agent.queue_depth, 8) is the real limit.
const maxChatQueue = 32

// OnMessage queues a message for the adapter's dispatcher and never blocks:
// when the 256-slot buffer is full the message is dropped and counted.
func (h *Hub) OnMessage(_ context.Context, in core.Inbound) {
	a := h.adapter(in.Chat.Channel)
	if a == nil {
		return
	}
	select {
	case a.queue <- in:
	default:
		if n := a.dropped.Add(1); n == 1 || n%100 == 0 {
			h.log.Warn("channel inbound buffer full; message dropped", "channel", a.name, "dropped_total", n)
		}
	}
}

// OnAction handles a button press on its own goroutine so the acknowledgement
// (due within 3 s) never waits behind message processing.
func (h *Hub) OnAction(_ context.Context, ev core.ActionEvent) {
	a := h.adapter(ev.Chat.Channel)
	if a == nil {
		return
	}
	select {
	case h.actionSem <- struct{}{}:
	default:
		h.log.Warn("channel: too many button presses in flight; press dropped", "channel", a.name)
		return
	}
	if !h.goAsync(func(ctx context.Context) {
		defer func() { <-h.actionSem }()
		h.handleAction(ctx, a, ev)
	}) {
		<-h.actionSem
	}
}

func (h *Hub) dispatch(a *adapter) {
	for {
		select {
		case <-h.ctx.Done():
			return
		case in := <-a.queue:
			h.contain("inbound message", func() { h.handleMessage(h.ctx, a, in) })
		}
	}
}

// target is where an accepted message goes: the principal that owns the
// session and the provenance its text carries.
type target struct {
	principal *core.Principal
	space     *core.Space
	prov      core.Provenance
}

// handleMessage runs the SPEC §5.1 pipeline up to the command fast path.
// Anything that can be slow (membership lookups, attachments, Submit,
// platform sends) leaves the dispatcher, so /stop is never stuck behind a
// busy chat.
func (h *Hub) handleMessage(ctx context.Context, a *adapter, in core.Inbound) {
	if in.MsgID == "" || in.SenderID == "" || in.SenderIsBot {
		return
	}
	in.Chat.Channel = a.name
	now := h.clock.Now()
	seen, err := h.d.Store.Dedup().Seen(ctx, a.name, in.MsgID, now)
	if err != nil {
		h.log.Error("channel dedup failed; message dropped", "channel", a.name, "err", err)
		return
	}
	if seen {
		return
	}
	ident, err := h.touchIdentity(ctx, a, in, now)
	if err != nil {
		h.log.Error("channel identity update failed; message dropped", "channel", a.name, "err", err)
		return
	}
	res, err := h.d.Auth.ResolveIdentity(ctx, a.name, in.SenderID)
	if err != nil {
		h.log.Error("channel identity lookup failed; message dropped", "channel", a.name, "err", err)
		return
	}
	if res.Status == core.IdentityBlocked {
		return
	}
	if in.Chat.Kind == core.ChatDM {
		// The limiter runs before any lookup: one account must not turn
		// into a stream of platform calls.
		if ok, retry := h.allowSender(a, in); !ok {
			if res.Admitted() {
				h.slowDown(in.Chat, a.name+":"+in.SenderID, retry)
			}
			return
		}
		h.dm(ctx, a, in, ident, res)
		return
	}
	h.group(ctx, a, in, ident, res)
}

func (h *Hub) allowSender(a *adapter, in core.Inbound) (bool, time.Duration) {
	return h.d.Budget.AllowSender(a.name + ":" + in.SenderID)
}

// touchIdentity records the sender and, for a DM, remembers its chat. A
// sender first seen on a chat app that can't say who is in the team starts
// with the verdict "unknown": only an admin can let them in.
func (h *Hub) touchIdentity(ctx context.Context, a *adapter, in core.Inbound, now time.Time) (core.ChannelIdentity, error) {
	t := &core.ChannelIdentity{Channel: a.name, ExternalID: in.SenderID, Username: in.SenderUsername, DisplayName: in.SenderName,
		LastSeen: now, FirstSeen: now}
	if _, ok := checkerOf(a); !ok {
		t.Member = core.MemberUnknown
	}
	row, err := h.d.Store.Identities().Touch(ctx, t)
	if err != nil {
		return core.ChannelIdentity{}, err
	}
	if in.Chat.Kind != core.ChatDM {
		return *row, nil
	}
	return h.rememberDM(ctx, *row, in.Chat.ChatID)
}

// rememberDM records chatID as the identity's DM chat, which PrincipalChat
// and card routing use to reach the person later. It writes that column
// alone: row may be older than an admin's block or an approver change.
func (h *Hub) rememberDM(ctx context.Context, row core.ChannelIdentity, chatID string) (core.ChannelIdentity, error) {
	if row.DMChat == chatID {
		return row, nil
	}
	if err := h.d.Store.Identities().SetDMChat(ctx, row.Channel, row.ExternalID, chatID); err != nil {
		return core.ChannelIdentity{}, err
	}
	row.DMChat = chatID
	return row, nil
}

// offIn is what a teammate hears in their DM about a channel Bothy is off
// in (or isn't in), naming it: in their DM, "this channel" would be the DM.
func offIn(title string) string {
	name := cmp.Or(strings.TrimSpace(title), "that channel")
	return plainLine("Bothy is off in " + name + ". You can message Bothy here, or ask an admin to turn " + name + " on.")
}

const (
	// offEvery bounds offIn per sender and channel: a slash command can be
	// repeated in a channel Bothy ignores as fast as anyone types it.
	offEvery = time.Hour

	waitingLine  = "You'll be able to use Bothy once an admin lets you in."
	cantCheck    = "Bothy can't check your access right now. Try again in a few minutes."
	inspectEvery = 24 * time.Hour
)

// dm answers a direct message: teammates the chat app vouches for (or an
// admin let in) get Bothy; people outside the team get one line a day;
// everyone else waits for an admin.
func (h *Hub) dm(ctx context.Context, a *adapter, in core.Inbound, ident core.ChannelIdentity, res core.IdentityResolution) {
	h.check(ctx, a, ident, res, h.teamsOf(ctx, a), func(ctx context.Context, res core.IdentityResolution) {
		switch {
		case res.Admitted():
			h.teammateDM(ctx, a, in, ident, res)
		case res.Status == core.IdentityTeammate && !res.SpaceOn:
			// Blocking turns the space off with the status; a teammate whose
			// space is off anyway gets nothing.
		case res.Member == core.MemberOutsider:
			h.outsiderLine(ctx, a, in, ident)
		default:
			h.waiting(ctx, a, in, ident, res)
		}
	}, func(context.Context) { h.lookupFailed(a, in) })
}

func (h *Hub) teammateDM(ctx context.Context, a *adapter, in core.Inbound, ident core.ChannelIdentity, res core.IdentityResolution) {
	sp, err := h.d.Store.Spaces().Get(ctx, core.SystemViewer(), res.Principal)
	if err != nil {
		h.log.Error("channel space lookup failed", "channel", a.name, "err", err)
		return
	}
	p, err := h.d.Store.Principals().Get(ctx, core.SystemViewer(), res.Principal)
	if err != nil {
		h.log.Error("channel principal lookup failed", "channel", a.name, "principal", res.Principal, "err", err)
		return
	}
	if ident.Greet != "" {
		chat := in.Chat
		h.enqueueChat(chat.Key(), func(ctx context.Context) { _ = h.greetIn(ctx, chat, a.name, in.SenderID) })
	}
	tg := target{principal: p, space: sp, prov: core.ProvUser}
	// Forwarded text was written by someone else, whoever relayed it.
	if in.Forwarded {
		tg.prov = core.ProvUntrusted
	}
	h.route(ctx, a, in, res, tg)
}

// route runs a command or hands the message to the runner.
func (h *Hub) route(ctx context.Context, a *adapter, in core.Inbound, res core.IdentityResolution, tg target) {
	cmd, args, isCmd := parseCommand(in.Text)
	isCmd = isCmd && !in.Forwarded
	if isCmd && commands[cmd].urgent {
		h.runCommand(ctx, a, in, res, tg, cmd, args)
		return
	}
	h.enqueueChat(in.Chat.Key(), func(ctx context.Context) {
		if isCmd {
			h.runCommand(ctx, a, in, res, tg, cmd, args)
			return
		}
		if word, ok := unknownCommand(in.Text); ok && !in.Forwarded && len(in.Attachments) == 0 {
			h.reply(ctx, in.Chat, "There's no "+escapeMD(word)+" command. "+commandIn(a.ch.Caps(), "help")+" lists the commands.")
			return
		}
		h.submit(ctx, a, in, tg)
	})
}

// outsiderLine tells someone outside the team, at most once a day, who
// Bothy works with. Nobody is notified and no model runs; it is sent
// whatever channels.unknown_senders says, since they can't be let in.
func (h *Hub) outsiderLine(ctx context.Context, a *adapter, in core.Inbound, ident core.ChannelIdentity) {
	ok, err := h.d.Auth.NoticeSender(ctx, ident)
	if err != nil {
		h.log.Error("channel sender notice failed", "channel", a.name, "err", err)
		return
	}
	if !ok {
		return
	}
	text := "Bothy works only with members of its team."
	if a.name == "slack" {
		text = "Bothy works with members of " + escapeMD(cmp.Or(a.ch.Status().Team, "this workspace")) + "."
	}
	h.replyAsync(in.Chat, text)
}

// waiting handles someone only an admin can let in: listed in the admins'
// inbox, told once a day, and announced to approvers.
func (h *Hub) waiting(ctx context.Context, a *adapter, in core.Inbound, ident core.ChannelIdentity, res core.IdentityResolution) {
	if res.Member == core.MemberUnchecked {
		if _, err := h.d.Auth.Admit(ctx, ident, core.Membership{Status: core.MemberUnknown}); err != nil {
			h.log.Warn("channel: recording an unknown verdict failed", "channel", a.name, "err", err)
		}
	}
	h.keepFirstText(ctx, a.name, ident, in)
	h.inboxChanged("sender:" + a.name + ":" + in.SenderID)
	if h.cfg().Channels.UnknownSenders == "ignore" {
		return
	}
	ok, err := h.d.Auth.NoticeSender(ctx, ident)
	if err != nil {
		h.log.Error("channel sender notice failed", "channel", a.name, "err", err)
		return
	}
	if !ok {
		return
	}
	h.replyAsync(in.Chat, waitingLine)
	h.notifyWaiting(ctx, a.name, ident)
}

// lookupFailed answers a DM whose sender Bothy couldn't check, at most once
// an hour per sender.
func (h *Hub) lookupFailed(a *adapter, in core.Inbound) {
	if h.noteOnce("lookup:"+a.name+":"+in.SenderID, lookupFailedEvery) {
		h.replyAsync(in.Chat, cantCheck)
	}
}

// group handles a message in a group chat. A chat app that knows who is in
// the team (Slack, Discord) answers teammates only; in a Telegram group
// anyone who isn't blocked may talk, as untrusted.
func (h *Hub) group(ctx context.Context, a *adapter, in core.Inbound, ident core.ChannelIdentity, res core.IdentityResolution) {
	sp, err := h.groupSpace(ctx, a, in)
	if errors.Is(err, core.ErrNotFound) {
		// A private message may come from a channel the bot is not in, and
		// never records one.
		if in.Private {
			h.privateOff(ctx, a, in, ident, res, spaceTitle(in.ChatTitle))
			return
		}
		// Anyone can add the bot to groups; the limiter bounds how fast one
		// account can create spaces and notices. A skipped sighting is
		// retried with the group's next message.
		if ok, _ := h.allowSender(a, in); ok {
			h.firstSighting(ctx, a, in)
		}
		return
	}
	if err != nil {
		h.log.Error("channel space lookup failed", "channel", a.name, "err", err)
		return
	}
	if in.Private && !sp.Enabled {
		h.privateOff(ctx, a, in, ident, res, sp.Title)
		return
	}
	// Chatter Bothy must stay silent in costs nothing, not even the limiter.
	if !sp.Enabled || (sp.MentionOnly && !in.Mentioned) {
		return
	}
	if ok, _ := h.allowSender(a, in); !ok {
		return
	}
	h.refreshChat(a, sp)
	if _, ok := checkerOf(a); !ok {
		h.channelMessage(ctx, a, in, res, sp)
		return
	}
	teams := h.teamsOf(ctx, a)
	if sp.Team != "" && !slices.Contains(teams, sp.Team) {
		teams = append([]string{sp.Team}, teams...)
	}
	h.check(ctx, a, ident, res, teams, func(ctx context.Context, res core.IdentityResolution) {
		// Guests, other companies, bots and Slackbot: silence in channels.
		if res.Admitted() {
			h.channelMessage(ctx, a, in, res, sp)
		}
	}, func(context.Context) {})
}

func (h *Hub) channelMessage(ctx context.Context, a *adapter, in core.Inbound, res core.IdentityResolution, sp *core.Space) {
	p, err := h.d.Store.Principals().Get(ctx, core.SystemViewer(), sp.Principal)
	if err != nil {
		h.log.Error("channel principal lookup failed", "channel", a.name, "principal", sp.Principal, "err", err)
		return
	}
	h.route(ctx, a, in, res, target{principal: p, space: sp, prov: core.ProvUntrusted})
}

// refreshChat asks the platform again, at most once a day, how many people
// are in a group and whether it is shared with other companies.
func (h *Hub) refreshChat(a *adapter, sp *core.Space) {
	insp, ok := a.ch.(core.ChatInspector)
	if !ok || !h.noteOnce("inspect:"+sp.Chat.Key(), inspectEvery) {
		return
	}
	h.enqueueChat(a.name+":inspect:"+sp.Chat.ChatID, func(ctx context.Context) {
		release, ok := h.acquireLookup(a.name)
		if !ok {
			return
		}
		lctx, cancel := context.WithTimeout(ctx, recheckWait)
		info, err := insp.InspectChat(lctx, sp.Chat)
		cancel()
		release()
		if err != nil {
			h.log.Info("channel: describing a group failed", "channel", a.name, "err", err)
			return
		}
		h.applyChatInfo(ctx, a, sp.Principal, info, true)
	})
}

// privateOff answers a message only its sender saw (a Slack slash command)
// in a channel where Bothy is off or that it isn't in, in their DM: a
// teammate is told the channel (title) is off, anyone else gets what a DM
// would.
func (h *Hub) privateOff(ctx context.Context, a *adapter, in core.Inbound, ident core.ChannelIdentity, res core.IdentityResolution, title string) {
	key := a.name + ":" + in.SenderID
	if ok, _ := h.allowSender(a, in); !ok {
		return
	}
	h.check(ctx, a, ident, res, h.teamsOf(ctx, a), func(ctx context.Context, res core.IdentityResolution) {
		switch {
		case res.Admitted():
			if !h.noteOnce(key+":"+in.Chat.ChatID, offEvery) {
				return
			}
			h.inSenderDM(a, in, ident, func(ctx context.Context, in core.Inbound, _ core.ChannelIdentity) {
				h.reply(ctx, in.Chat, offIn(title))
			})
		case res.Status == core.IdentityTeammate && !res.SpaceOn:
		case res.Member == core.MemberOutsider:
			h.inSenderDM(a, in, ident, func(ctx context.Context, in core.Inbound, ident core.ChannelIdentity) {
				h.outsiderLine(ctx, a, in, ident)
			})
		default:
			h.inSenderDM(a, in, ident, func(ctx context.Context, in core.Inbound, ident core.ChannelIdentity) {
				h.waiting(ctx, a, in, ident, res)
			})
		}
	}, func(context.Context) {
		h.inSenderDM(a, in, ident, func(_ context.Context, in core.Inbound, _ core.ChannelIdentity) { h.lookupFailed(a, in) })
	})
}

// inSenderDM opens the sender's DM, remembers it as the identity's DM chat
// and runs fn with the message moved there. Opening is a platform call, so
// it runs off the dispatcher, in order per sender.
func (h *Hub) inSenderDM(a *adapter, in core.Inbound, ident core.ChannelIdentity, fn func(context.Context, core.Inbound, core.ChannelIdentity)) {
	h.enqueueChat(a.name+":dm-of:"+in.SenderID, func(ctx context.Context) {
		dm, err := a.ch.OpenDM(ctx, in.SenderID)
		if err != nil {
			h.log.Warn("channel: opening a DM failed", "channel", a.name, "err", err)
			return
		}
		dm.Channel, dm.Kind = a.name, core.ChatDM
		if ident, err = h.rememberDM(ctx, ident, dm.ChatID); err != nil {
			h.log.Error("channel identity update failed; message dropped", "channel", a.name, "err", err)
			return
		}
		in.Chat = dm
		fn(ctx, in, ident)
	})
}

// noteOnce reports whether key was not noted in the last every, noting it.
func (h *Hub) noteOnce(key string, every time.Duration) bool {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if until, ok := h.noted[key]; ok && now.Before(until) {
		return false
	}
	h.noted[key] = now.Add(every)
	return true
}

const maxFirstText = 120

// keepFirstText stores the start of a pending sender's first message for
// the admins' inbox item, once. It is cleaned to one line and redacted; the
// inbox shows it verbatim as the sender's words, and it never reaches a run.
func (h *Hub) keepFirstText(ctx context.Context, channel string, ident core.ChannelIdentity, in core.Inbound) {
	if ident.Status != core.IdentityPending || ident.FirstText != "" {
		return
	}
	// Telegram sends "/start" when someone opens the bot; their own words
	// come after it.
	if _, _, cmd := parseCommand(in.Text); cmd {
		return
	}
	text := truncateRunes(spaceTitle(h.redact(in.Text)), maxFirstText)
	if text == "" {
		return
	}
	// Read and write in one transaction: an admin's decision made in between
	// must not be overwritten with the pending row read before it.
	err := h.d.Store.Tx(ctx, func(tx core.Store) error {
		cur, err := tx.Identities().Get(ctx, channel, in.SenderID)
		if err != nil || cur.Status != core.IdentityPending || cur.FirstText != "" {
			return err
		}
		cur.FirstText = text
		return tx.Identities().Update(ctx, cur)
	})
	if err != nil {
		h.log.Warn("channel: keeping an unknown sender's first words failed", "channel", channel, "err", err)
	}
}

// inboxChanged tells admins' inboxes that an item appeared or changed.
func (h *Hub) inboxChanged(item string) {
	h.d.Bus.Publish(core.Event{Type: core.EvInboxChanged, Data: core.InboxChangedData{Item: item}})
}

const (
	// askDMs bounds the approver DMs about people waiting (and new
	// channels) per askWindow: each throwaway account gets one notice a
	// day, but nothing else stops a hundred accounts from becoming a
	// hundred DMs. The banner still counts every one.
	askDMs    = 3
	askWindow = time.Hour
)

// askLimiter counts unknown senders in fixed windows of askWindow.
type askLimiter struct {
	start time.Time
	asked int // senders noticed in this window
	dms   int // approver DMs sent in this window
	held  int // senders noticed without a DM since the last one
}

// note records one more unknown sender. It reports how many asked in this
// window, whether approvers get a DM about this one and how many
// held-back senders that DM should mention. Without anyone to DM (canDM
// false) no DM is spent.
func (l *askLimiter) note(now time.Time, canDM bool) (asked int, dm bool, held int) {
	if now.Sub(l.start) >= askWindow {
		l.start, l.asked, l.dms = now, 0, 0
	}
	l.asked++
	if !canDM {
		l.held++
		return l.asked, false, 0
	}
	if l.dms >= askDMs {
		l.held++
		return l.asked, false, 0
	}
	l.dms++
	held, l.held = l.held, 0
	return l.asked, true, held
}

// notifyWaiting tells admins (web banner) and chat approvers (their DMs)
// that someone is waiting to be let in.
func (h *Hub) notifyWaiting(ctx context.Context, channel string, ident core.ChannelIdentity) {
	who, app := senderLabel(ident), channelTitle(channel)
	approvers := h.chatApprovers(ctx)
	h.mu.Lock()
	asked, dm, held := h.asks.note(h.clock.Now(), len(approvers) > 0)
	h.mu.Unlock()
	banner := who + " on " + app + " is waiting to use Bothy. Let them in or block them in Approvals."
	if asked > 1 {
		banner = fmt.Sprintf("%d people asked to use Bothy in the past hour, most recently %s on %s. Let them in or block them in Approvals.",
			asked, who, app)
	}
	h.d.Bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{
		Level: "warn", Code: "unknown_sender", Action: "/approvals", Text: banner,
	}})
	if !dm {
		return
	}
	text := inert(revealInvisible(who)) + " messaged Bothy on " + app + " and is waiting to be let in. An admin can let them in or block them in Bothy."
	switch {
	case held == 1:
		text += " 1 other person has also messaged Bothy since the last notice."
	case held > 1:
		text += fmt.Sprintf(" %d other people have also messaged Bothy since the last notice.", held)
	}
	h.tellApprovers(approvers, text, h.webButton("Open in Bothy", "/approvals"))
}

// tellApprovers DMs each chat approver.
func (h *Hub) tellApprovers(approvers []approverDM, text string, buttons [][]core.Button) {
	for _, ap := range approvers {
		chat := ap.chat
		h.enqueueChat(chat.Key(), func(ctx context.Context) { h.sendWithButtons(ctx, chat, text, buttons) })
	}
}

func senderLabel(id core.ChannelIdentity) string {
	name := strings.TrimSpace(id.DisplayName)
	if name == "" {
		name = id.ExternalID
	}
	if id.Username != "" {
		name += " (@" + id.Username + ")"
	}
	return name
}

func channelTitle(name string) string {
	switch name {
	case "telegram":
		return "Telegram"
	case "discord":
		return "Discord"
	case "slack":
		return "Slack"
	}
	if name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// slowDown answers a rate-limited sender at most once per limiter window.
func (h *Hub) slowDown(chat core.ChatRef, key string, retry time.Duration) {
	window := max(retry, time.Minute)
	now := time.Now()
	h.mu.Lock()
	if until, ok := h.slowed[key]; ok && now.Before(until) {
		h.mu.Unlock()
		return
	}
	h.slowed[key] = now.Add(window)
	h.mu.Unlock()
	secs := int(max(retry, time.Second).Round(time.Second) / time.Second)
	h.replyAsync(chat, fmt.Sprintf("Bothy will read new messages again in about %d s.", secs))
}

// firstSighting records an unknown group as a disabled channel that
// belongs to the team, and tells admins and chat approvers. Nobody owns it:
// whoever added the bot or spoke first must not become its approver.
func (h *Hub) firstSighting(ctx context.Context, a *adapter, in core.Inbound) {
	title := cmp.Or(spaceTitle(in.ChatTitle), fallbackTitle(a.name, in.Chat.ChatID))
	now := h.clock.Now()
	sp := &core.Space{
		Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: a.name, ChatID: in.Chat.ChatID, Kind: core.ChatGroup},
		Team: in.Team, AddedBy: in.AddedBy, Title: title, MentionOnly: mentionOnlyDefault(h.cfg(), a.name),
		CreatedAt: now, UpdatedAt: now,
	}
	p := &core.Principal{
		ID: core.PrincipalID(core.NewID(core.PrefixSpace)), Kind: core.PrincipalSpace, DisplayName: title,
		Workspace: core.WorkspaceID(core.NewID(core.PrefixWorkspace)), ToolProfile: "restricted", CreatedAt: now,
	}
	if err := h.d.Store.Spaces().Create(ctx, sp, p); err != nil {
		if !errors.Is(err, core.ErrConflict) {
			h.log.Error("channel: recording new group failed", "channel", a.name, "err", err)
		}
		return
	}
	item := "group:" + string(sp.Principal)
	h.inboxChanged(item)
	if insp, ok := a.ch.(core.ChatInspector); ok {
		h.noteOnce("inspect:"+sp.Chat.Key(), inspectEvery)
		h.enqueueChat(a.name+":inspect:"+sp.Chat.ChatID, func(ctx context.Context) {
			lctx, cancel := context.WithTimeout(ctx, recheckWait)
			info, err := insp.InspectChat(lctx, sp.Chat)
			cancel()
			if err != nil {
				h.log.Info("channel: describing a new group failed", "channel", a.name, "err", err)
				return
			}
			h.applyChatInfo(ctx, a, sp.Principal, info, true)
			h.inboxChanged(item)
		})
	}
	app := channelTitle(a.name)
	h.d.Bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{
		Level: "info", Code: "new_channel", Action: "/approvals",
		Text: "Bothy was added to “" + title + "” in " + app + ". Turn it on in Approvals.",
	}})
	approvers := h.chatApprovers(ctx)
	h.mu.Lock()
	_, dm, _ := h.channelAsks.note(now, len(approvers) > 0)
	h.mu.Unlock()
	if !dm {
		return
	}
	text := "Bothy was added to “" + inert(title) + "” in " + app + ". It stays silent there until an admin turns it on in Bothy."
	h.tellApprovers(approvers, text, h.webButton("Open in Bothy", "/approvals"))
}

// fallbackTitle names a group whose platform gave no title.
func fallbackTitle(channel, chatID string) string {
	return channelTitle(channel) + " group " + chatID
}

// groupSpace returns the group's space. One still under its fallback title
// takes the platform's title once a message carries one (a Slack app gains
// channels:read only when reinstalled); a title anyone gave it stays. A
// Discord channel recorded before Bothy knew its server learns it here.
// Neither touches reviewed_at.
func (h *Hub) groupSpace(ctx context.Context, a *adapter, in core.Inbound) (*core.Space, error) {
	sp, err := h.d.Store.Spaces().ByChat(ctx, a.name, in.Chat.ChatID)
	if err != nil {
		return nil, err
	}
	fallback, title := fallbackTitle(a.name, in.Chat.ChatID), spaceTitle(in.ChatTitle)
	rename := sp.Title == fallback && title != "" && title != fallback
	team := sp.Team == "" && in.Team != ""
	if !rename && !team {
		return sp, nil
	}
	// Re-read inside the transaction: an admin's rename made in between wins.
	err = h.d.Store.Tx(ctx, func(tx core.Store) error {
		cur, err := tx.Spaces().ByChat(ctx, a.name, in.Chat.ChatID)
		if err != nil {
			return err
		}
		if team && cur.Team == "" {
			cur.Team = in.Team
		}
		renamed := rename && cur.Title == fallback
		if renamed {
			cur.Title = title
		}
		cur.UpdatedAt = h.clock.Now()
		if err := tx.Spaces().Update(ctx, cur); err != nil {
			return err
		}
		if renamed {
			p, err := tx.Principals().Get(ctx, core.SystemViewer(), cur.Principal)
			if err != nil {
				return err
			}
			if p.DisplayName == fallback {
				p.DisplayName = title
				if err := tx.Principals().Update(ctx, p); err != nil {
					return err
				}
			}
		}
		sp = cur
		return nil
	})
	if err != nil {
		h.log.Warn("channel: naming a group from its platform title failed", "channel", a.name, "err", err)
	}
	if team {
		h.InvalidateTeams()
	}
	return sp, nil
}

const maxTitleRunes = 128

// spaceTitle cleans a platform group title, which any group admin controls,
// before it names a space and its principal: one line, no control or
// invisible characters, bounded length.
func spaceTitle(s string) string {
	var b strings.Builder
	gap := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			gap = b.Len() > 0
			continue
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), invisible(r):
			continue
		}
		if gap {
			b.WriteByte(' ')
			gap = false
		}
		b.WriteRune(r)
	}
	return truncateRunes(b.String(), maxTitleRunes)
}

func (h *Hub) userName(ctx context.Context, id core.UserID) string {
	u, err := h.d.Store.Users().Get(ctx, id)
	if err != nil {
		return string(id)
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// submit hands an accepted message to the runner (SPEC §5.1 steps 8-10).
func (h *Hub) submit(ctx context.Context, a *adapter, in core.Inbound, tg target) {
	if strings.TrimSpace(in.Text) == "" && len(in.Attachments) == 0 {
		return
	}
	sess, err := h.resolveSession(ctx, tg.principal.ID, in.Chat)
	if err != nil {
		h.log.Error("channel session lookup failed", "channel", a.name, "err", err)
		h.reply(ctx, in.Chat, "Sorry, something went wrong. Try again in a moment.")
		return
	}
	var parts []core.Part
	if strings.TrimSpace(in.Text) != "" {
		parts = append(parts, core.Part{Type: core.PartText, Text: in.Text})
	}
	files, notes := h.fetchAttachments(ctx, a, in, tg.principal)
	if len(notes) > 0 {
		parts = append(parts, core.Part{Type: core.PartText, Text: "[" + strings.Join(notes, "; ") + "]"})
	}
	parts = append(parts, files...)
	chat := in.Chat
	req := core.RunRequest{
		Session: sess.ID, Principal: tg.principal.ID, User: tg.principal.User, Trigger: core.TrigMessage,
		Input: parts, Provenance: tg.prov, Sender: a.name + ":" + in.SenderID, SenderName: in.SenderName, Chat: &chat,
		Sink: h.StreamTo(chat, "stream"),
	}
	res, err := h.d.Runner.Submit(ctx, req)
	if err != nil {
		h.reply(ctx, chat, h.submitError(a.ch.Caps(), err))
		return
	}
	switch {
	case res.Steered:
		key, waiting := h.activeRunKey(sess.ID, res.Run)
		if waiting {
			h.replyOnce(ctx, chat, key+":wait", "Bothy will read this after you answer the approval above.")
		} else {
			h.replyOnce(ctx, chat, key+":steer", "Got it, adding that to the current task.")
		}
	case res.Queued:
		key, _ := h.activeRunKey(sess.ID, res.Run)
		h.replyOnce(ctx, chat, key+":queue", "Bothy will read this when it finishes the current task.")
	}
}

// resolveSession applies agent.session_idle_reset before resolving the chat's
// open session.
func (h *Hub) resolveSession(ctx context.Context, p core.PrincipalID, chat core.ChatRef) (*core.Session, error) {
	sess, err := h.d.Runner.ResolveSession(ctx, p, chat)
	if err != nil {
		return nil, err
	}
	reset := h.cfg().Agent.SessionIdleReset.D()
	if reset <= 0 || sess.LastMessageAt.IsZero() || h.clock.Now().Sub(sess.LastMessageAt) < reset {
		return sess, nil
	}
	if err := h.d.Store.Sessions().Archive(ctx, core.SystemViewer(), sess.ID); err != nil {
		return nil, err
	}
	return h.d.Runner.ResolveSession(ctx, p, chat)
}

func (h *Hub) activeRunKey(s core.SessionID, run core.RunID) (string, bool) {
	waiting := false
	for _, ri := range h.d.Runner.Active() {
		if ri.Session == s {
			if run == "" {
				run = ri.Run
			}
			waiting = ri.Status == core.RunWaitingApproval
		}
	}
	if run == "" {
		return string(s), waiting
	}
	return string(run), waiting
}

func (h *Hub) submitError(caps core.ChannelCaps, err error) string {
	var be *core.BudgetError
	switch {
	case errors.Is(err, core.ErrBusy):
		return "Busy. Send " + commandIn(caps, "stop") + " to interrupt."
	case errors.Is(err, core.ErrPaused):
		return "Bothy is paused by an admin."
	case errors.As(err, &be) && be.Kind == core.BudgetDailyCost:
		return "Today's budget is used up; it resets at midnight (" + escapeMD(h.cfg().Timezone) + ")."
	case errors.As(err, &be):
		return h.budgetText(be.Kind, be.Limit)
	}
	h.log.Error("channel submit failed", "err", err)
	return "Sorry, something went wrong. Try again in a moment."
}

// budgetText says which limit stopped a task in the runner's words
// (docs/design/ui.md §7.6); limit is 0 when it isn't known.
func (h *Hub) budgetText(kind core.BudgetKind, limit int64) string {
	switch {
	case kind == core.BudgetSteps && limit > 0:
		return fmt.Sprintf("Bothy paused after %d steps to check in.", limit)
	case kind == core.BudgetTokens:
		return "Bothy stopped because this task reached its length limit."
	case kind == core.BudgetTime:
		return "Bothy stopped because this task ran out of time."
	case kind == core.BudgetRunCost && limit > 0:
		return "Bothy stopped because this task reached its " + moneyText(core.Micros(limit)) + " limit."
	case kind == core.BudgetRunCost:
		return "Bothy stopped because this task reached its spending limit."
	case kind == core.BudgetDailyCost:
		amount := ""
		if limit > 0 {
			amount = moneyText(core.Micros(limit)) + " "
		}
		return "Bothy has reached today's " + amount + "spending limit. It can start again at midnight (" + escapeMD(h.cfg().Timezone) + ")."
	}
	return "Bothy stopped because this task reached a limit."
}

// ---- per-chat serial work ----

type chatQueue struct {
	jobs    []func(context.Context)
	running bool
}

// enqueueChat runs job after earlier jobs for the same chat, on a goroutine
// that exits once the chat's queue is empty.
func (h *Hub) enqueueChat(key string, job func(context.Context)) {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return
	}
	q := h.chats[key]
	if q == nil {
		q = &chatQueue{}
		h.chats[key] = q
	}
	if len(q.jobs) >= maxChatQueue {
		h.mu.Unlock()
		h.log.Warn("channel chat queue full; work dropped", "chat", key)
		return
	}
	q.jobs = append(q.jobs, job)
	if q.running {
		h.mu.Unlock()
		return
	}
	q.running = true
	h.wg.Add(1)
	h.mu.Unlock()
	go func() {
		defer h.wg.Done()
		h.drainChat(key, q)
	}()
}

func (h *Hub) drainChat(key string, q *chatQueue) {
	for {
		h.mu.Lock()
		if len(q.jobs) == 0 || h.ctx.Err() != nil {
			q.running, q.jobs = false, nil
			if h.chats[key] == q {
				delete(h.chats, key)
			}
			h.mu.Unlock()
			return
		}
		job := q.jobs[0]
		q.jobs = q.jobs[1:]
		h.mu.Unlock()
		h.contain("chat work", func() { job(h.ctx) })
	}
}

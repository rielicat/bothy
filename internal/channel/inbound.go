package channel

import (
	"cmp"
	"context"
	"errors"
	"fmt"
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
// Anything that can be slow (attachments, Submit, platform sends) leaves the
// dispatcher, so /stop is never stuck behind a busy chat.
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
	ident, err := h.touchIdentity(ctx, a.name, in, now)
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
	if in.Chat.Kind == core.ChatGroup && !in.Private && h.burnExposedCode(ctx, a, in, ident) {
		return
	}
	known := res.Status == core.IdentityLinked || res.Status == core.IdentityGuest
	if !known && in.Chat.Kind == core.ChatDM {
		// Unknown senders are limited too, silently: otherwise every wrong
		// pairing code would make the bot answer, without bound.
		if ok, _ := h.d.Budget.AllowSender(a.name + ":" + in.SenderID); !ok {
			return
		}
		h.unknownDM(ctx, a, in, ident)
		return
	}
	if in.Chat.Kind == core.ChatGroup && in.Private && h.privateInGroup(ctx, a, in, ident, known) {
		return
	}
	tg, ok := h.gate(ctx, a, in, res)
	if !ok {
		return
	}
	// The limiter runs after gating so the bot never answers in a group it
	// must stay silent in, and unaddressed group chatter costs nothing.
	if ok, retry := h.d.Budget.AllowSender(a.name + ":" + in.SenderID); !ok {
		h.slowDown(in.Chat, a.name+":"+in.SenderID, retry)
		return
	}
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
			h.reply(ctx, in.Chat, "There's no "+escapeMD(word)+" command. /help lists the commands.")
			return
		}
		h.submit(ctx, a, in, tg)
	})
}

// touchIdentity records the sender and, for a DM, remembers its chat.
func (h *Hub) touchIdentity(ctx context.Context, channel string, in core.Inbound, now time.Time) (core.ChannelIdentity, error) {
	row, err := h.d.Store.Identities().Touch(ctx, &core.ChannelIdentity{Channel: channel, ExternalID: in.SenderID,
		Username: in.SenderUsername, DisplayName: in.SenderName, LastSeen: now})
	if err != nil {
		return core.ChannelIdentity{}, err
	}
	if in.Chat.Kind != core.ChatDM {
		return *row, nil
	}
	return h.rememberDM(ctx, *row, in.Chat.ChatID)
}

// rememberDM records chatID as the identity's DM chat, which PreferredChat
// and card routing use to reach the user later.
func (h *Hub) rememberDM(ctx context.Context, row core.ChannelIdentity, chatID string) (core.ChannelIdentity, error) {
	if row.DMChat == chatID {
		return row, nil
	}
	ids := h.d.Store.Identities()
	cur, err := ids.Get(ctx, row.Channel, row.ExternalID)
	if err != nil {
		return core.ChannelIdentity{}, err
	}
	cur.DMChat = chatID
	if err := ids.Update(ctx, cur); err != nil {
		return core.ChannelIdentity{}, err
	}
	return *cur, nil
}

const (
	codeShared = "That code was shared in a group, so it no longer works. Create a new one in Bothy and send it to me in a direct message."
	offHere    = "Bothy is off in this channel. Write to me here, or ask an admin to turn the channel on in Approvals."
	// offEvery bounds offHere per sender and channel: a slash command can be
	// repeated in a channel Bothy ignores as fast as anyone types it.
	offEvery = time.Hour
)

// burnExposedCode cancels a live link code posted where the whole group can
// read it, whoever posted it and whatever Bothy does in that group: anyone
// there could redeem it first. It reports whether it did; anything else (no
// code, a dead one, the attempt limit) goes on as before.
func (h *Hub) burnExposedCode(ctx context.Context, a *adapter, in core.Inbound, ident core.ChannelIdentity) bool {
	cmd, code, ok := parseCommand(in.Text)
	if !ok || in.Forwarded || (cmd != "pair" && cmd != "start") || code == "" {
		return false
	}
	if err := h.d.Auth.BurnPairing(ctx, ident, code); err != nil {
		if !errors.Is(err, core.ErrNotFound) {
			h.log.Info("channel: checking a code posted in a group failed", "channel", a.name, "err", err)
		}
		return false
	}
	// A security notice, so it is sent even in a group Bothy ignores.
	if ok, _ := h.d.Budget.AllowSender(a.name + ":" + in.SenderID); ok {
		h.replyAsync(in.Chat, codeShared)
	}
	return true
}

// privateInGroup handles a message only its sender saw (a Slack slash
// command) in a group. Such a message never creates a space: slash commands
// work in channels the bot is not in. In a group without an enabled space,
// an unknown sender is handled exactly as in a DM and a known one is told
// the group is off, both in their DM. In an enabled space only an unknown
// sender's pairing code goes to their DM; everything else reports false and
// takes the normal path, where anyone may talk as untrusted (step 7).
func (h *Hub) privateInGroup(ctx context.Context, a *adapter, in core.Inbound, ident core.ChannelIdentity, known bool) bool {
	key := a.name + ":" + in.SenderID
	sp, err := h.groupSpace(ctx, a, in)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		h.log.Error("channel space lookup failed", "channel", a.name, "err", err)
		return true
	}
	enabled := err == nil && sp.Enabled
	if !known {
		cmd, code, isCmd := parseCommand(in.Text)
		if enabled && (!isCmd || (cmd != "pair" && cmd != "start") || code == "") {
			return false
		}
		if ok, _ := h.d.Budget.AllowSender(key); ok {
			h.inSenderDM(a, in, ident, func(ctx context.Context, in core.Inbound, ident core.ChannelIdentity) {
				h.unknownDM(ctx, a, in, ident)
			})
		}
		return true
	}
	if enabled {
		return false
	}
	if ok, _ := h.d.Budget.AllowSender(key); !ok || !h.noteOnce(key+":"+in.Chat.ChatID, offEvery) {
		return true
	}
	h.inSenderDM(a, in, ident, func(ctx context.Context, in core.Inbound, _ core.ChannelIdentity) {
		h.reply(ctx, in.Chat, offHere)
	})
	return true
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

// unknownDM handles a DM from a sender with no link: pairing codes are
// redeemed, everyone else gets at most one notice a day. Nothing here ever
// reaches the runner, so no model call is made for an unknown sender.
func (h *Hub) unknownDM(ctx context.Context, a *adapter, in core.Inbound, ident core.ChannelIdentity) {
	if cmd, code, ok := parseCommand(in.Text); ok && !in.Forwarded && (cmd == "pair" || cmd == "start") && code != "" {
		linked, err := h.d.Auth.RedeemPairing(ctx, ident, code)
		if err != nil {
			h.log.Info("channel pairing failed", "channel", a.name, "err", err)
			h.replyAsync(in.Chat, "That code didn't work. It may have expired, so create a new one in Bothy under Settings → Account.")
			return
		}
		name := h.userName(ctx, linked.User)
		h.enqueueChat(in.Chat.Key(), func(ctx context.Context) {
			h.reply(ctx, in.Chat, "Linked to "+escapeMD(name)+".")
			h.reply(ctx, in.Chat, helloText)
		})
		return
	}
	if h.cfg().Channels.UnknownSenders == "ignore" {
		return
	}
	notice, err := h.d.Auth.SenderNotice(ctx, ident)
	if err != nil {
		h.log.Error("channel sender notice failed", "channel", a.name, "err", err)
		return
	}
	h.keepFirstText(ctx, a.name, ident, in)
	if !notice.Reply {
		return
	}
	h.replyAsync(in.Chat, "Bothy only works with people on this team. Ask an admin to approve code "+escapeMD(notice.Code.Code)+".")
	h.notifyUnknownSender(ctx, a.name, ident)
	h.inboxChanged("sender:"+a.name+":"+in.SenderID, "")
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

// inboxChanged tells admins' inboxes, and owner's when set, that an item
// appeared.
func (h *Hub) inboxChanged(item string, owner core.UserID) {
	ps := []core.PrincipalID{""}
	if owner != "" {
		ps = append(ps, core.UserPrincipal(owner))
	}
	for _, p := range ps {
		h.d.Bus.Publish(core.Event{Type: core.EvInboxChanged, Principal: p, Data: core.InboxChangedData{Item: item}})
	}
}

const helloText = "Write to Bothy here any time; /help lists the commands."

const (
	// askDMs bounds the admin DMs about unknown senders per askWindow: each
	// throwaway account gets one notice a day, but nothing else stops a
	// hundred accounts from becoming a hundred DMs. The banner still counts
	// every one.
	askDMs    = 3
	askWindow = time.Hour
)

// askLimiter counts unknown senders in fixed windows of askWindow.
type askLimiter struct {
	start time.Time
	asked int // senders noticed in this window
	dms   int // admin DMs sent in this window
	held  int // senders noticed without a DM since the last one
}

// note records one more unknown sender. It reports how many asked in this
// window, whether admins get a DM about this one and how many held-back
// senders that DM should mention.
func (l *askLimiter) note(now time.Time) (asked int, dm bool, held int) {
	if now.Sub(l.start) >= askWindow {
		l.start, l.asked, l.dms = now, 0, 0
	}
	l.asked++
	if l.dms >= askDMs {
		l.held++
		return l.asked, false, 0
	}
	l.dms++
	held, l.held = l.held, 0
	return l.asked, true, held
}

// notifyUnknownSender tells admins (web banner and their preferred DM) that
// someone asked for access. The approval code is deliberately not included:
// the admin has to get it from the person, out of band.
func (h *Hub) notifyUnknownSender(ctx context.Context, channel string, ident core.ChannelIdentity) {
	who := senderLabel(ident)
	h.mu.Lock()
	asked, dm, held := h.asks.note(h.clock.Now())
	h.mu.Unlock()
	banner := who + " on " + channelTitle(channel) + " asked for access. Let them chat, link them or block them in Approvals."
	if asked > 1 {
		banner = fmt.Sprintf("%d people asked for access in the past hour, most recently %s on %s. Let them chat, link them or block them in Approvals.",
			asked, who, channelTitle(channel))
	}
	h.d.Bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{
		Level: "warn", Code: "unknown_sender", Action: "/approvals", Text: banner,
	}})
	if !dm {
		return
	}
	users, err := h.d.Store.Users().List(ctx)
	if err != nil {
		h.log.Error("channel: listing admins failed", "err", err)
		return
	}
	text := "Someone new messaged Bothy on " + channelTitle(channel) + ": " + inert(revealInvisible(who)) +
		". Let them chat, link them or block them in Approvals."
	switch {
	case held == 1:
		text += " 1 other person has also messaged Bothy since the last notice."
	case held > 1:
		text += fmt.Sprintf(" %d other people have also messaged Bothy since the last notice.", held)
	}
	buttons := h.webButton("Open Approvals", "/approvals")
	for _, u := range users {
		if !u.Role.IsAdmin() || u.Disabled {
			continue
		}
		if chat, ok := h.PreferredChat(ctx, u.ID); ok {
			h.enqueueChat(chat.Key(), func(ctx context.Context) { h.sendWithButtons(ctx, chat, text, buttons) })
		}
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

// gate maps the message to a principal (SPEC §5.1 step 7) or rejects it.
func (h *Hub) gate(ctx context.Context, a *adapter, in core.Inbound, res core.IdentityResolution) (target, bool) {
	var tg target
	var pid core.PrincipalID
	switch in.Chat.Kind {
	case core.ChatDM:
		switch res.Status {
		case core.IdentityLinked:
			pid, tg.prov = res.Principal, core.ProvUser
			if pid == "" {
				pid = core.UserPrincipal(res.User)
			}
		case core.IdentityGuest:
			sp, err := h.d.Store.Spaces().Get(ctx, core.SystemViewer(), res.Principal)
			if err != nil || !sp.Enabled {
				return tg, false
			}
			pid, tg.space, tg.prov = res.Principal, sp, core.ProvUntrusted
		default:
			return tg, false
		}
	case core.ChatGroup:
		sp, err := h.groupSpace(ctx, a, in)
		if errors.Is(err, core.ErrNotFound) {
			// A private message may come from a channel the bot is not in.
			if in.Private {
				return tg, false
			}
			// Anyone can add the bot to groups; the limiter bounds how fast
			// one account can create spaces and owner notices. A skipped
			// sighting is retried with the group's next message.
			if ok, _ := h.d.Budget.AllowSender(a.name + ":" + in.SenderID); ok {
				h.firstSighting(ctx, a, in, res)
			}
			return tg, false
		}
		if err != nil {
			h.log.Error("channel space lookup failed", "channel", a.name, "err", err)
			return tg, false
		}
		if !sp.Enabled || (sp.MentionOnly && !in.Mentioned) {
			return tg, false
		}
		pid, tg.space, tg.prov = sp.Principal, sp, core.ProvUntrusted
		if res.Status == core.IdentityLinked && res.User == sp.Owner {
			tg.prov = core.ProvUser
		}
	default:
		return tg, false
	}
	// Forwarded text was written by someone else, whoever relayed it.
	if in.Forwarded {
		tg.prov = core.ProvUntrusted
	}
	p, err := h.d.Store.Principals().Get(ctx, core.SystemViewer(), pid)
	if err != nil {
		h.log.Error("channel principal lookup failed", "channel", a.name, "principal", pid, "err", err)
		return tg, false
	}
	tg.principal = p
	return tg, true
}

// firstSighting records an unknown group as a disabled space and asks its
// owner whether to enable it. The owner is the linked user the platform
// reports as having added the bot (Inbound.AddedBy), otherwise the instance
// owner: whoever merely speaks first must not become the space's approver.
func (h *Hub) firstSighting(ctx context.Context, a *adapter, in core.Inbound, res core.IdentityResolution) {
	var owner core.UserID
	switch in.AddedBy {
	case "":
	case in.SenderID:
		if res.Status == core.IdentityLinked {
			owner = res.User
		}
	default:
		if r, err := h.d.Auth.ResolveIdentity(ctx, a.name, in.AddedBy); err == nil && r.Status == core.IdentityLinked {
			owner = r.User
		}
	}
	if owner == "" {
		u, ok := h.instanceOwner(ctx)
		if !ok {
			return
		}
		owner = u.ID
	}
	title := cmp.Or(spaceTitle(in.ChatTitle), fallbackTitle(a.name, in.Chat.ChatID))
	now := h.clock.Now()
	sp := &core.Space{
		Kind: "group", Chat: core.ChatRef{Channel: a.name, ChatID: in.Chat.ChatID, Kind: core.ChatGroup},
		Title: title, Owner: owner, MentionOnly: mentionOnlyDefault(h.cfg(), a.name), CreatedAt: now, UpdatedAt: now,
	}
	p := &core.Principal{
		ID: core.PrincipalID(core.NewID(core.PrefixSpace)), Kind: core.PrincipalSpace, User: owner, DisplayName: title,
		Workspace: core.WorkspaceID(core.NewID(core.PrefixWorkspace)), ToolProfile: "restricted", CreatedAt: now,
	}
	if err := h.d.Store.Spaces().Create(ctx, sp, p); err != nil {
		if !errors.Is(err, core.ErrConflict) {
			h.log.Error("channel: recording new group failed", "channel", a.name, "err", err)
		}
		return
	}
	h.inboxChanged("group:"+string(sp.Principal), owner)
	chat, ok := h.PreferredChat(ctx, owner)
	if !ok {
		return
	}
	text := "Bothy was added to “" + inert(title) + "” in " + channelTitle(a.name) +
		". It stays silent there until you turn it on in Approvals."
	buttons := h.webButton("Open Approvals", "/approvals")
	h.enqueueChat(chat.Key(), func(ctx context.Context) { h.sendWithButtons(ctx, chat, text, buttons) })
}

// fallbackTitle names a group whose platform gave no title.
func fallbackTitle(channel, chatID string) string {
	return channelTitle(channel) + " group " + chatID
}

// groupSpace returns the group's space. One still under its fallback title
// takes the platform's title once a message carries one (a Slack app gains
// channels:read only when reinstalled); a title anyone gave it stays.
func (h *Hub) groupSpace(ctx context.Context, a *adapter, in core.Inbound) (*core.Space, error) {
	sp, err := h.d.Store.Spaces().ByChat(ctx, a.name, in.Chat.ChatID)
	if err != nil {
		return nil, err
	}
	fallback, title := fallbackTitle(a.name, in.Chat.ChatID), spaceTitle(in.ChatTitle)
	if sp.Title != fallback || title == "" || title == fallback {
		return sp, nil
	}
	// Re-read inside the transaction: an admin's rename made in between wins.
	err = h.d.Store.Tx(ctx, func(tx core.Store) error {
		cur, err := tx.Spaces().ByChat(ctx, a.name, in.Chat.ChatID)
		if err != nil || cur.Title != fallback {
			return err
		}
		cur.Title, cur.UpdatedAt = title, h.clock.Now()
		if err := tx.Spaces().Update(ctx, cur); err != nil {
			return err
		}
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
		sp = cur
		return nil
	})
	if err != nil {
		h.log.Warn("channel: naming a group from its platform title failed", "channel", a.name, "err", err)
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

func (h *Hub) instanceOwner(ctx context.Context) (*core.User, bool) {
	users, err := h.d.Store.Users().List(ctx)
	if err != nil {
		h.log.Error("channel: listing users failed", "err", err)
		return nil, false
	}
	for _, u := range users {
		if u.Role == core.RoleOwner && !u.Disabled {
			return u, true
		}
	}
	return nil, false
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
		Input: parts, Provenance: tg.prov, Sender: a.name + ":" + in.SenderID, Chat: &chat,
		Sink: h.StreamTo(chat, "stream"),
	}
	res, err := h.d.Runner.Submit(ctx, req)
	if err != nil {
		h.reply(ctx, chat, h.submitError(err))
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

func (h *Hub) submitError(err error) string {
	var be *core.BudgetError
	switch {
	case errors.Is(err, core.ErrBusy):
		return "Busy. Send /stop to interrupt."
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

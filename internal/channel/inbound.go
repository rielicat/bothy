package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/bothyhq/bothy/internal/core"
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
	if isCmd && commands[cmd].urgent {
		h.runCommand(ctx, a, in, res, tg, cmd, args)
		return
	}
	h.enqueueChat(in.Chat.Key(), func(ctx context.Context) {
		if isCmd {
			h.runCommand(ctx, a, in, res, tg, cmd, args)
			return
		}
		h.submit(ctx, a, in, tg)
	})
}

// touchIdentity records the sender and remembers its DM chat, which
// PreferredChat and card routing use to reach the user later.
func (h *Hub) touchIdentity(ctx context.Context, channel string, in core.Inbound, now time.Time) (core.ChannelIdentity, error) {
	ids := h.d.Store.Identities()
	row, err := ids.Touch(ctx, &core.ChannelIdentity{Channel: channel, ExternalID: in.SenderID,
		Username: in.SenderUsername, DisplayName: in.SenderName, LastSeen: now})
	if err != nil {
		return core.ChannelIdentity{}, err
	}
	if in.Chat.Kind != core.ChatDM || row.DMChat == in.Chat.ChatID {
		return *row, nil
	}
	cur, err := ids.Get(ctx, channel, in.SenderID)
	if err != nil {
		return core.ChannelIdentity{}, err
	}
	cur.DMChat = in.Chat.ChatID
	if err := ids.Update(ctx, cur); err != nil {
		return core.ChannelIdentity{}, err
	}
	return *cur, nil
}

// unknownDM handles a DM from a sender with no link: pairing codes are
// redeemed, everyone else gets at most one notice a day. Nothing here ever
// reaches the runner, so no model call is made for an unknown sender.
func (h *Hub) unknownDM(ctx context.Context, a *adapter, in core.Inbound, ident core.ChannelIdentity) {
	if cmd, code, ok := parseCommand(in.Text); ok && (cmd == "pair" || cmd == "start") && code != "" {
		linked, err := h.d.Auth.RedeemPairing(ctx, ident, code)
		if err != nil {
			h.log.Info("channel pairing failed", "channel", a.name, "err", err)
			h.replyAsync(in.Chat, "That code did not work. Codes expire after a while; create a new one in the web UI under Settings → Channels.")
			return
		}
		name := h.userName(ctx, linked.User)
		h.enqueueChat(in.Chat.Key(), func(ctx context.Context) {
			h.reply(ctx, in.Chat, "Linked to "+escapeMD(name)+".")
			h.reply(ctx, in.Chat, helloText(name))
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
	if !notice.Reply {
		return
	}
	h.replyAsync(in.Chat, "I'm a private assistant. Ask my owner to approve code "+escapeMD(notice.Code.Code)+".")
	h.notifyUnknownSender(ctx, a.name, ident)
}

func helloText(name string) string {
	return "Hi " + escapeMD(name) + "! I'm your Bothy assistant. Write to me here any time; /help lists the commands."
}

// notifyUnknownSender tells admins (web banner and their preferred DM) that
// someone asked for access. The approval code is deliberately not included:
// the admin has to get it from the person, out of band.
func (h *Hub) notifyUnknownSender(ctx context.Context, channel string, ident core.ChannelIdentity) {
	who := senderLabel(ident)
	h.d.Bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{
		Level: "warn", Code: "unknown_sender", Action: "/settings/people",
		Text: who + " on " + channelTitle(channel) + " asked for access. Approve the code they were given in Settings → People.",
	}})
	users, err := h.d.Store.Users().List(ctx)
	if err != nil {
		h.log.Error("channel: listing admins failed", "err", err)
		return
	}
	text := "Someone new wrote to me on " + channelTitle(channel) + ": " + escapeMD(who) +
		". If you know them, approve the code they received in Settings → People."
	buttons := h.webButton("Open Settings → People", "/settings/people")
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
	h.replyAsync(chat, fmt.Sprintf("Slow down a little; I'll read new messages again in about %d s.", secs))
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
		sp, err := h.d.Store.Spaces().ByChat(ctx, a.name, in.Chat.ChatID)
		if errors.Is(err, core.ErrNotFound) {
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
	p, err := h.d.Store.Principals().Get(ctx, core.SystemViewer(), pid)
	if err != nil {
		h.log.Error("channel principal lookup failed", "channel", a.name, "principal", pid, "err", err)
		return tg, false
	}
	tg.principal = p
	return tg, true
}

// firstSighting records an unknown group as a disabled space and asks its
// owner whether to enable it. The owner is the linked user who added the bot
// when the adapter reports it (a presence signal), otherwise the instance
// owner: whoever merely speaks first must not become the space's approver.
func (h *Hub) firstSighting(ctx context.Context, a *adapter, in core.Inbound, res core.IdentityResolution) {
	var owner core.UserID
	if isPresence(in) && res.Status == core.IdentityLinked {
		owner = res.User
	}
	if owner == "" {
		u, ok := h.instanceOwner(ctx)
		if !ok {
			return
		}
		owner = u.ID
	}
	title := ""
	if t, ok := a.ch.(ChatTitler); ok {
		title = spaceTitle(t.ChatTitle(in.Chat.ChatID))
	}
	if title == "" {
		title = channelTitle(a.name) + " group " + in.Chat.ChatID
	}
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
	chat, ok := h.PreferredChat(ctx, owner)
	if !ok {
		return
	}
	text := "I was added to “" + escapeMD(title) + "” on " + channelTitle(a.name) +
		". I stay silent there until you enable it in Settings → Spaces."
	buttons := h.webButton("Open Settings → Spaces", "/settings/spaces")
	h.enqueueChat(chat.Key(), func(ctx context.Context) { h.sendWithButtons(ctx, chat, text, buttons) })
}

// isPresence reports the adapters' "bot added to this group by SenderID"
// signal: a group Inbound with no text and no attachments.
func isPresence(in core.Inbound) bool {
	return in.Chat.Kind == core.ChatGroup && strings.TrimSpace(in.Text) == "" && len(in.Attachments) == 0
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
			h.replyOnce(ctx, chat, key+":wait", "Waiting for your approval above; I'll read this after.")
		} else {
			h.replyOnce(ctx, chat, key+":steer", "Got it, adding that to the current task.")
		}
	case res.Queued:
		key, _ := h.activeRunKey(sess.ID, res.Run)
		h.replyOnce(ctx, chat, key+":queue", "Queued; I'll get to it when the current task is done.")
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
		return "Budget exceeded (" + escapeMD(string(be.Kind)) + ")."
	}
	h.log.Error("channel submit failed", "err", err)
	return "Sorry, something went wrong. Try again in a moment."
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

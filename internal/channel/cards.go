package channel

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// maxChatDiff is how much of a diff a chat card shows (SPEC §5.9); a longer
// diff makes the card truncated, so it can only be denied from chat.
const maxChatDiff = 3 << 10

func (h *Hub) onBusEvent(e core.Event) {
	switch d := e.Data.(type) {
	case core.ApprovalData:
		ap := d.Approval
		if ap == nil {
			return
		}
		switch e.Type {
		case core.EvApprovalRequested:
			// AdminOnly and multi-approver approvals are published more than
			// once (one copy per audience principal).
			if h.markDelivered(ap.ID) {
				h.enqueueChat(cardQueue(ap.ID), func(ctx context.Context) { h.deliverCard(ctx, ap) })
			}
		case core.EvApprovalVoted:
			if h.markResolved(ap.ID, core.ApprovalStatus(fmt.Sprintf("votes:%d", len(ap.Votes)))) {
				h.enqueueChat(cardQueue(ap.ID), func(ctx context.Context) { h.refreshCards(ctx, ap) })
			}
		}
	case core.ApprovalResolvedData:
		if d.Status == core.ApprovalPending || d.Status == core.ApprovalParked {
			return
		}
		if h.markResolved(d.Approval, d.Status) {
			h.enqueueChat(cardQueue(d.Approval), func(ctx context.Context) { h.updateCards(ctx, d) })
		}
	case core.BannerData:
		h.onBanner(e, d)
	}
}

// cardQueue serializes delivery and outcome edits of one approval, so a
// quick resolution never overtakes the card it edits.
func cardQueue(id core.ApprovalID) string { return "approval:" + string(id) }

func (h *Hub) markDelivered(id core.ApprovalID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.delivered[id]; ok {
		return false
	}
	h.delivered[id] = time.Now()
	return true
}

func (h *Hub) markResolved(id core.ApprovalID, st core.ApprovalStatus) bool {
	key := string(id) + ":" + string(st)
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.resolved[key]; ok {
		return false
	}
	h.resolved[key] = time.Now()
	return true
}

// resyncCards delivers pending approvals that have no card message yet: ones
// created before the hub ran, or missed when the bus subscription overflowed.
func (h *Hub) resyncCards() {
	pending, err := h.d.Approvals.Pending(h.ctx, core.SystemViewer())
	if err != nil {
		h.log.Error("channel: listing pending approvals failed", "err", err)
		return
	}
	for _, ap := range pending {
		if ap.Status != core.ApprovalPending {
			continue
		}
		refs, err := h.d.Approvals.MessageRefs(h.ctx, ap.ID)
		if err != nil || len(refs) > 0 || !h.markDelivered(ap.ID) {
			continue
		}
		h.enqueueChat(cardQueue(ap.ID), func(ctx context.Context) { h.deliverCard(ctx, ap) })
	}
}

// approverDM is a chat approver's DM; full is false when their membership
// verdict is too old for a card that approves (they get a link instead).
type approverDM struct {
	key  string // "<channel>:<external id>"
	chat core.ChatRef
	full bool
}

// chatApprovers lists the teammates an admin marked "Can approve" whom
// Bothy can reach now: their chat app is on, they are still in the team,
// and their DM is known or can be opened.
func (h *Hub) chatApprovers(ctx context.Context) []approverDM {
	ids, err := h.d.Store.Identities().List(ctx, core.IdentityTeammate)
	if err != nil {
		h.log.Error("channel: listing approvers failed", "err", err)
		return nil
	}
	synced := map[string]bool{}
	var out []approverDM
	for _, id := range ids {
		if !id.CanApprove {
			continue
		}
		a := h.live(id.Channel)
		if a == nil {
			continue
		}
		mc, checks := checkerOf(a)
		if checks && !synced[a.name] {
			synced[a.name] = true
			h.syncScope(ctx, a, mc)
		}
		res, err := h.d.Auth.ResolveIdentity(ctx, id.Channel, id.ExternalID)
		if err != nil || !res.SpaceOn || !res.CanApprove || res.Member == core.MemberOutsider {
			continue
		}
		chat, err := h.dmOf(ctx, a, *id)
		if err != nil {
			h.log.Warn("channel: opening an approver's DM failed", "channel", a.name, "err", err)
			continue
		}
		full := res.Admitted() && (!checks || h.fresh(res, core.ApproverFresh))
		out = append(out, approverDM{key: id.Channel + ":" + id.ExternalID, chat: chat, full: full})
	}
	return out
}

// deliverCard routes an approval to chat (SPEC §5.9). Web accounts' own
// requests stay on the web. A team request (a teammate's or a channel's)
// goes to chat approvers as a card, or as a link to Bothy when only an
// admin can decide it or the approver's membership needs renewing; the
// chat that asked gets a waiting line its requester can cancel from.
func (h *Hub) deliverCard(ctx context.Context, ap *core.Approval) {
	pr, err := h.d.Store.Principals().Get(ctx, core.SystemViewer(), ap.Principal)
	if err != nil || !pr.TeamOwned() {
		return
	}
	var origin *core.ChatRef
	if ap.Chat != nil && h.live(ap.Chat.Channel) != nil {
		origin = ap.Chat
	}
	switch {
	case ap.Approver.Multi():
		if origin != nil {
			who := "someone else"
			if n := ap.Approver.Needed(); n > 1 {
				who = fmt.Sprintf("%d other people", n)
			}
			h.sendNotice(ctx, ap, *origin, "Waiting for "+who+" to approve "+cardIng(ap)+".")
		}
	case ap.Approver.AdminOnly:
		for _, dm := range h.chatApprovers(ctx) {
			h.sendLinkCard(ctx, ap, dm.chat)
		}
		if origin != nil {
			h.sendNotice(ctx, ap, *origin, needsAdmin)
		}
	case ap.Approver.Team:
		full, ownDM := 0, false
		for _, dm := range h.chatApprovers(ctx) {
			if !dm.full {
				h.sendLinkCard(ctx, ap, dm.chat)
				continue
			}
			full++
			h.sendCard(ctx, ap, dm.chat)
			if origin != nil && origin.Kind == core.ChatDM && dm.chat.Key() == origin.Key() {
				ownDM = true
			}
		}
		switch {
		case origin == nil, ownDM:
		case full > 0:
			h.sendNotice(ctx, ap, *origin, "Waiting for approval: "+strings.TrimSuffix(cardQuestion(ap), "?")+".")
		default:
			h.sendNotice(ctx, ap, *origin, needsAdmin)
		}
	}
}

const needsAdmin = "This needs an admin's approval in Bothy."

func (h *Hub) sendCard(ctx context.Context, ap *core.Approval, chat core.ChatRef) {
	a := h.live(chat.Channel)
	if a == nil {
		return
	}
	msg, truncated := h.renderCard(ap, a.ch.Caps())
	h.sendRecorded(ctx, a, ap, chat, msg, core.CardMessage{Kind: core.CardMsgCard, Truncated: truncated})
}

// sendLinkCard sends a card that only points to Bothy on the web: no
// question, no context, nothing to press but the link.
func (h *Hub) sendLinkCard(ctx context.Context, ap *core.Approval, chat core.ChatRef) {
	a := h.live(chat.Channel)
	if a == nil {
		return
	}
	msg := renderLinkCard(ap)
	h.sendRecorded(ctx, a, ap, chat, msg, core.CardMessage{Kind: core.CardMsgLink, Truncated: true})
}

func renderLinkCard(ap *core.Approval) core.OutMessage {
	if !isHTTPURL(ap.Card.WebURL) {
		return core.OutMessage{Text: plainLine("A request from chat apps needs an admin. Admins can approve it in Bothy under Approvals.")}
	}
	return core.OutMessage{Text: plainLine("A request from chat apps needs an admin. Admins can approve it in Bothy."),
		Buttons: [][]core.Button{{{Label: "Open in Bothy", URL: ap.Card.WebURL}}}}
}

// sendNotice posts the waiting line in the chat that asked. Its one button
// lets the person who asked cancel the request.
func (h *Hub) sendNotice(ctx context.Context, ap *core.Approval, chat core.ChatRef, line string) {
	a := h.live(chat.Channel)
	if a == nil {
		return
	}
	msg := core.OutMessage{Text: h.redact(plainLine(line))}
	if a.ch.Caps().Buttons {
		msg.Buttons = [][]core.Button{{{Label: "Cancel request", Data: h.d.Approvals.ButtonData(ap.ID, core.Deny)}}}
	}
	h.sendRecorded(ctx, a, ap, chat, msg, core.CardMessage{Kind: core.CardMsgNotice, Truncated: true})
}

func (h *Hub) sendRecorded(ctx context.Context, a *adapter, ap *core.Approval, chat core.ChatRef, msg core.OutMessage, rec core.CardMessage) {
	msg.Chat = chat
	ref, err := a.ch.Send(ctx, msg)
	if err != nil {
		h.log.Warn("channel: sending approval message failed", "approval", ap.ID, "chat", chat.Key(), "kind", rec.Kind, "err", err)
		return
	}
	rec.Ref = ref
	if err := h.d.Approvals.AddMessage(ctx, ap.ID, rec); err != nil {
		h.log.Error("channel: recording approval message failed", "approval", ap.ID, "err", err)
	}
}

func cardTitle(ap *core.Approval) string {
	if ap.Card.Title != "" {
		return ap.Card.Title
	}
	if ap.Action != nil {
		return ap.Action.Tool
	}
	return "Approval"
}

// cardQuestion is the card's plain question; cards stored before v0.2 have
// only a title.
func cardQuestion(ap *core.Approval) string {
	if ap.Card.Question != "" {
		return ap.Card.Question
	}
	return cardTitle(ap) + "?"
}

// cardIng names the action in a sentence: "deleting notes/a.md".
func cardIng(ap *core.Approval) string {
	if ap.Card.Ing != "" {
		return ap.Card.Ing
	}
	return "“" + cardTitle(ap) + "”"
}

// choiceLabel is a button's words: allow_once carries the card's verb. On
// a teammate's request the grant buttons name whose chat they cover.
func choiceLabel(c core.Card, ch core.Choice, person string) string {
	switch ch {
	case core.AllowOnce:
		if c.PrimaryLabel != "" && c.PrimaryLabel != allowForChat {
			return c.PrimaryLabel
		}
		return "Allow"
	case core.AllowSession:
		if person != "" {
			return "Allow in " + person + "'s chat"
		}
		return allowForChat
	case core.AllowAlways:
		if person != "" {
			return "Always allow for " + person
		}
		return "Always allow"
	case core.Deny:
		return "Deny"
	}
	return ""
}

// asker is who asked on a team request: the person's name for button
// labels ("" for a channel's request), the card's "asked" line and the
// platform id it mentions.
type asker struct {
	person, asked, mention string
	channel                bool
}

func teamAsker(ap *core.Approval) asker {
	r := ap.Requester
	if !ap.Approver.Team || r == nil || r.Identity == "" {
		return asker{}
	}
	app, _, _ := strings.Cut(r.Identity, ":")
	where := " asked on " + escapeMD(channelTitle(app)) + "."
	if r.Chat != "" {
		where = " asked in " + inert(r.Chat) + "."
	}
	link, mention := mentionLink(r.Identity, r.Name, r.Handle)
	out := asker{asked: link + where, mention: mention}
	if r.Chat != "" {
		out.channel = true
		return out
	}
	out.person = truncateRunes(oneLine(r.Name), 24)
	return out
}

var (
	telegramIDRe = regexp.MustCompile(`^[0-9]{1,20}$`)
	slackIDRe    = regexp.MustCompile(`^[UW][A-Z0-9]{2,20}$`)
	discordIDRe  = regexp.MustCompile(`^[0-9]{15,21}$`)
)

// mentionLink names a chat person by the platform's own mention, so the
// card shows their real profile whatever display name they chose. The
// adapters render the link as a mention only for the id returned, which
// the message must carry in OutMessage.Mentions.
func mentionLink(identity, name, handle string) (link, id string) {
	app, ext, _ := strings.Cut(identity, ":")
	label := strings.Map(func(r rune) rune {
		if strings.ContainsRune("[]()\\`*_<>|~", r) || r == '\n' {
			return -1
		}
		return r
	}, revealInvisible(strings.TrimSpace(name)))
	if label == "" {
		label = "Someone"
	}
	switch {
	case app == "telegram" && telegramIDRe.MatchString(ext):
		s := "[" + label + "](tg://user?id=" + ext + ")"
		if handle != "" {
			s += " (@" + escapeMD(handle) + ")"
		}
		return s, ext
	case app == "slack" && slackIDRe.MatchString(ext):
		return "[" + label + "](slack://user?id=" + ext + ")", ext
	case app == "discord" && discordIDRe.MatchString(ext):
		return "[" + label + "](discord://user?id=" + ext + ")", ext
	}
	return inert(name), ""
}

const allowForChat = "Allow for this chat"

// plainLine shows a sentence of card copy verbatim. Words a chat client
// would turn into a link (a file name like staging.md passes for a domain)
// go in code spans: one curious tap must not fetch anything.
func plainLine(s string) string {
	s = revealInvisible(s)
	if !core.LooksLikeLink(s) {
		return escapeMD(s)
	}
	words := strings.Split(s, " ")
	for i, w := range words {
		lead := strings.TrimLeft(w, "“(")
		word := strings.TrimRight(lead, "?.,:;!”)")
		head, tail := w[:len(w)-len(lead)], lead[len(word):]
		if word != "" && core.LooksLikeLink(word) {
			words[i] = escapeMD(head) + codeSpan(word) + escapeMD(tail)
		} else {
			words[i] = escapeMD(w)
		}
	}
	return strings.Join(words, " ")
}

// heading is a card's first line: the question in bold. Telegram cannot
// nest code in bold, so its renderer breaks the bold around a code span.
func heading(s string) string {
	return "**" + plainLine(s) + "**"
}

// chatCode is the code block a chat card shows. A command, a script or a
// file's content is the action itself and always shows. Elsewhere Code is
// the action's JSON (hashes, base commits): a built-in tool's fields already
// say what it does, so in a chat message the JSON stays behind the web UI's
// Details. MCP, admin-class and unknown tools keep it, since their fields
// show only some of the arguments, and so do cards from before v0.2.
func chatCode(ap *core.Approval) string {
	c, a := ap.Card, ap.Action
	if c.Code == "" || a == nil || c.Question == "" || c.Risk == core.SEAdmin {
		return c.Code
	}
	switch a.Kind {
	case core.ActExec, core.ActMCP, core.ActGeneric:
		return c.Code
	}
	code, err := core.CanonicalJSON([]byte(c.Code))
	payload, perr := core.CanonicalJSON(a.Payload)
	if err != nil || perr != nil || !bytes.Equal(code, payload) {
		return c.Code
	}
	return ""
}

// renderCard formats the policy-rendered Card for chat in the words of the
// web card (question, consequence, warnings, fields, the verb on the
// button). Every value is shown verbatim (escaped, in code where it is code)
// with invisible characters made visible. A card that does not fit, has a
// diff too long to show, or goes to a channel without buttons is truncated:
// Deny plus a link to the web UI.
func (h *Hub) renderCard(ap *core.Approval, caps core.ChannelCaps) (core.OutMessage, bool) {
	c := ap.Card
	header := heading(cardQuestion(ap))
	lines := []string{header}
	who := teamAsker(ap)
	if who.asked != "" {
		lines = append(lines, who.asked)
	}
	if c.Consequence != "" {
		lines = append(lines, plainLine(c.Consequence))
	}
	if ap.Approver.Multi() && ap.Requester != nil {
		lines = append(lines, plainLine(ap.Requester.Name+" asked."))
	}
	for _, w := range c.Warnings {
		lines = append(lines, "⚠ "+plainLine(w))
	}
	for _, f := range c.Fields {
		v := revealInvisible(f.Value)
		label := "**" + escapeMD(revealInvisible(f.Label)) + ":**"
		switch {
		case strings.Contains(v, "\n"):
			lines = append(lines, label+"\n"+codeBlock("", v))
		case f.Mono:
			lines = append(lines, label+" "+codeSpan(v))
		default:
			lines = append(lines, label+" "+inert(v))
		}
	}
	if ap.Approver.Multi() {
		lines = append(lines, h.approvalLines(ap)...)
	}
	if code := chatCode(ap); code != "" {
		lines = append(lines, codeBlock("", revealInvisible(code)))
	}
	diff := c.Diff
	if c.Kind == core.CardPullRequest {
		// The pull request's diff opens with its own repository, branch,
		// title and body; the card's fields already say those.
		if i := strings.Index(diff, "diff --git "); i > 0 {
			diff = diff[i:]
		}
	}
	diffCut := len(diff) > maxChatDiff
	if diff != "" && !diffCut {
		lines = append(lines, codeBlock("diff", revealInvisible(diff)))
	}
	if c.Context != "" {
		lines = append(lines, contextLine(c.Context))
	}
	if !c.Expires.IsZero() {
		lines = append(lines, "Expires "+h.expiresText(c.Expires)+".")
	}
	text := h.redact(strings.Join(lines, "\n"))
	web := ""
	if isHTTPURL(c.WebURL) {
		web = c.WebURL
	}
	var choiceRow []core.Button
	primary := core.AllowOnce
	if !slices.Contains(c.Choices, core.AllowOnce) {
		primary = core.AllowSession
	}
	for _, ch := range c.Choices {
		if who.channel && (ch == core.AllowSession || ch == core.AllowAlways) {
			continue
		}
		label := choiceLabel(c, ch, who.person)
		if label == "" {
			continue
		}
		// Deny is never red: the danger fill marks what can't be undone.
		b := core.Button{Label: label, Data: h.d.Approvals.ButtonData(ap.ID, ch)}
		if ch == primary {
			b.Style = core.BtnPrimary
			if c.Tone == core.ToneDestructive {
				b.Style = core.BtnDanger
			}
		}
		choiceRow = append(choiceRow, b)
	}
	if caps.Buttons && !diffCut && len(choiceRow) > 0 && ulen(text) <= caps.MaxLen {
		buttons := [][]core.Button{choiceRow}
		if web != "" {
			buttons = append(buttons, []core.Button{{Label: "Open in web", URL: web}})
		}
		msg := core.OutMessage{Text: text, Buttons: buttons}
		if who.mention != "" {
			msg.Mentions = []string{who.mention}
		}
		return msg, false
	}

	short := []string{header}
	if c.Context != "" {
		short = append(short, contextLine(c.Context))
	}
	switch {
	case !caps.Buttons && web != "":
		short = append(short, "Review it in the web UI: "+web)
	case !caps.Buttons:
		short = append(short, "Review it in Bothy's web UI → Approvals.")
	case web != "":
		short = append(short, "This request is too long to show here. Open it in the web UI to approve.")
	default:
		short = append(short, "This request is too long to show here. Open Bothy's web UI → Approvals to approve.")
	}
	msg := core.OutMessage{Text: h.redact(strings.Join(short, "\n"))}
	if caps.Buttons {
		msg.Buttons = [][]core.Button{{{Label: choiceLabel(c, core.Deny, ""), Data: h.d.Approvals.ButtonData(ap.ID, core.Deny)}}}
		if web != "" {
			msg.Buttons = append(msg.Buttons, []core.Button{{Label: "Open in web to approve", URL: web}})
		}
	}
	return msg, true
}

// inert shows a card value verbatim. A value a chat client would turn into
// a link goes in a code span instead: the URL an action is about to fetch may
// carry the data it would leak, and one curious tap on the card must not
// send it before the approver has decided.
func inert(v string) string {
	if core.LooksLikeLink(v) {
		return codeSpan(v)
	}
	return escapeMD(v)
}

// contextLine is the card's italic context line; a link-like word in it,
// such as a file name in the chat's title, is a code span.
func contextLine(ctx string) string {
	return "_" + plainLine(ctx) + "_"
}

// expiresText is the card's expiry. A short wait is a countdown in the
// web's words ("in less than a minute", "in 14 min"); a longer one is a
// clock time, because a card is drawn once and a countdown would go stale.
func (h *Hub) expiresText(t time.Time) string {
	d := t.Sub(h.clock.Now())
	if d < time.Minute {
		return "in less than a minute"
	}
	if d < time.Hour {
		return fmt.Sprintf("in %d min", d.Round(time.Minute)/time.Minute)
	}
	loc, err := time.LoadLocation(h.cfg().Timezone)
	if err != nil {
		loc = time.UTC
	}
	return "at " + t.In(loc).Format("15:04 MST on Jan 2")
}

// updateCards edits every chat message of a resolved approval to its
// outcome and removes the buttons. A card's reader is the approver whose
// DM it is; the waiting line in the chat that asked shows the approver's
// reason only in a DM, never to a group.
func (h *Hub) updateCards(ctx context.Context, d core.ApprovalResolvedData) {
	refs, err := h.d.Approvals.MessageRefs(ctx, d.Approval)
	if err != nil {
		return
	}
	ap, err := h.d.Approvals.Get(ctx, core.SystemViewer(), d.Approval)
	if err != nil {
		if len(refs) == 0 {
			return
		}
		ap = &core.Approval{ID: d.Approval, Card: core.Card{Title: "Approval"}}
	}
	readers := h.cardReaders(ctx)
	inOrigin := false
	for _, r := range refs {
		a := h.live(r.Ref.Chat.Channel)
		if a == nil {
			continue
		}
		var text string
		switch r.Kind {
		case core.CardMsgNotice:
			inOrigin = true
			text = h.lineText(ctx, d, ap)
		case core.CardMsgLink:
			// A link-only card held back what was asked; its reader may not
			// be someone who can decide it, or even still in the team.
			text = linkOutcome(d)
		default:
			// Cards only go to DMs, and a stored card ref keeps no chat kind.
			dm := r.Ref.Chat
			dm.Kind = core.ChatDM
			here := ap.Chat != nil && dm.Channel == ap.Chat.Channel && dm.ChatID == ap.Chat.ChatID
			inOrigin = inOrigin || here
			reader := readers[dm.Channel+"\x00"+dm.ChatID]
			text = h.redact(plainLine(h.outcomeText(ctx, d, ap, func(by core.UserID, ident string) bool {
				return ident != "" && ident == reader
			}, true, here)))
		}
		if err := a.ch.Edit(ctx, r.Ref, core.OutMessage{Chat: r.Ref.Chat, Text: text}); err != nil {
			h.log.Warn("channel: updating approval card failed", "approval", d.Approval, "err", err)
		}
	}
	// A waiting line that failed to send is posted now for the outcomes
	// the chat that asked must hear.
	switch d.Status {
	case core.ApprovalAllowed, core.ApprovalDenied, core.ApprovalExpired:
	default:
		return
	}
	if inOrigin || ap.Chat == nil || h.live(ap.Chat.Channel) == nil {
		return
	}
	pr, err := h.d.Store.Principals().Get(ctx, core.SystemViewer(), ap.Principal)
	if err != nil || !pr.TeamOwned() {
		return
	}
	h.reply(ctx, *ap.Chat, h.lineText(ctx, d, ap))
}

// cardReaders maps each teammate's DM ("<channel>\x00<chat>") to their
// identity key, so a card can say "You" to its reader.
func (h *Hub) cardReaders(ctx context.Context) map[string]string {
	out := map[string]string{}
	ids, err := h.d.Store.Identities().List(ctx, core.IdentityTeammate)
	if err != nil {
		return out
	}
	for _, id := range ids {
		if id.DMChat != "" {
			out[id.Channel+"\x00"+id.DMChat] = id.Channel + ":" + id.ExternalID
		}
	}
	return out
}

// lineText is the outcome a waiting line becomes. In the requester's own
// DM they read it as "you"; a group never sees the approver's reason.
func (h *Hub) lineText(ctx context.Context, d core.ApprovalResolvedData, ap *core.Approval) string {
	if d.Status == core.ApprovalCancelled && d.Via == "stop" {
		return plainLine("✗ Stopped.")
	}
	dm := ap.Chat != nil && ap.Chat.Kind == core.ChatDM
	requester := ap.Approver.RequesterIdentity
	return h.redact(plainLine(h.outcomeText(ctx, d, ap, func(_ core.UserID, ident string) bool {
		return dm && ident != "" && ident == requester
	}, dm, true)))
}

// linkOutcome is what a link-only card becomes: that it was decided, never
// what was asked or why.
func linkOutcome(d core.ApprovalResolvedData) string {
	switch d.Status {
	case core.ApprovalAllowed, core.ApprovalDenied:
		return plainLine("Decided in Bothy.")
	case core.ApprovalExpired:
		return plainLine("Nobody answered in time.")
	}
	return plainLine("No longer waiting.")
}

// identityName names a chat person in an outcome: display name, else
// handle.
func (h *Hub) identityName(ctx context.Context, key string) string {
	app, ext, _ := strings.Cut(key, ":")
	id, err := h.d.Store.Identities().Get(ctx, app, ext)
	if err != nil {
		return "Someone"
	}
	if n := strings.TrimSpace(id.DisplayName); n != "" {
		return n
	}
	if id.Username != "" {
		return "@" + id.Username
	}
	return "Someone"
}

// outcomeText is the decided line a card becomes in place of its question
// (docs/design/ui.md §8.4), e.g. "✓ You approved deleting notes/a.md." or
// "✗ Sam Patel denied deleting notes/a.md on the web."; reader tells
// whether the decider reads it, here whether it is read in the chat the
// request came from. It is plain text; the caller formats it.
func (h *Hub) outcomeText(ctx context.Context, d core.ApprovalResolvedData, ap *core.Approval, reader func(core.UserID, string) bool, comment, here bool) string {
	ing := cardIng(ap)
	who, where := "Someone", viaText(d.Via)
	switch {
	case reader(d.By, d.ByIdentity):
		who, where = "You", ""
	case d.By != "":
		who = h.userName(ctx, d.By)
	case d.ByIdentity != "":
		who = h.identityName(ctx, d.ByIdentity)
	}
	switch d.Status {
	case core.ApprovalAllowed:
		if ap.Approver.Multi() {
			if names := h.allowedBy(ctx, ap, reader); names != "" {
				return "✓ " + names + " approved " + ing + "."
			}
		}
		switch d.Choice {
		case core.AllowSession:
			chat := "this chat"
			if !here {
				chat = h.teamChat(ctx, ap, true)
			}
			return "✓ " + who + " approved " + ing + where + ", and similar requests in " + chat + "."
		case core.AllowAlways:
			if !here {
				return "✓ " + who + " approved " + ing + where + ", and similar requests " + h.teamChat(ctx, ap, false) + " from now on."
			}
			return "✓ " + who + " approved " + ing + where + ", and similar requests from now on."
		}
		return "✓ " + who + " approved " + ing + where + "."
	case core.ApprovalDenied:
		s := "✗ " + who + " denied " + ing + where + "."
		// The requester's deny is how they withdraw their own request.
		withdrew := d.ByIdentity != "" && d.ByIdentity == ap.Approver.RequesterIdentity ||
			ap.Approver.Multi() && d.By != "" && d.By == ap.Approver.Requester
		if withdrew {
			s = "✗ Cancelled by " + who + "."
			if who == "You" {
				s = "✗ You cancelled the request."
			}
		}
		if c := oneLine(d.Comment); c != "" && comment && !withdrew {
			s += " “" + c + "”"
		}
		return s
	case core.ApprovalExpired:
		return "✗ Nobody answered in time, so Bothy didn't go ahead."
	case core.ApprovalCancelled:
		return "Cancelled when the chat was stopped."
	}
	return string(d.Status)
}

// teamChat names, for a reader elsewhere, what a team request's grant
// covers: in a teammate's chat ("Maya Chen's chat", or "for Maya Chen"
// from now on) or in a channel ("#eng", "in #eng").
func (h *Hub) teamChat(ctx context.Context, ap *core.Approval, session bool) string {
	name, person := "", false
	if pr, err := h.d.Store.Principals().Get(ctx, core.SystemViewer(), ap.Principal); err == nil {
		name, person = strings.TrimSpace(pr.DisplayName), pr.Kind == core.PrincipalPerson
		if sp, err := h.d.Store.Spaces().Get(ctx, core.SystemViewer(), ap.Principal); err == nil && !person {
			name = sp.Title
		}
	}
	switch {
	case name == "" && session:
		return "the chat it came from"
	case name == "":
		return "there"
	case person && session:
		return name + "'s chat"
	case person:
		return "for " + name
	case session:
		return name
	}
	return "in " + name
}

func viaText(via string) string {
	switch via {
	case "web":
		return " on the web"
	case "telegram", "discord", "slack":
		return " in " + channelTitle(via)
	}
	return ""
}

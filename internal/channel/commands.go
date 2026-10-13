package channel

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

type access uint8

const (
	accessOwner  access = iota // the session principal's user, or an admin
	accessAdmin                // owners and admins only
	accessAnyone               // anyone who passed gating
)

type commandSpec struct {
	// urgent commands run on the dispatcher instead of the chat's queue, so
	// they are never stuck behind a busy chat.
	urgent bool
	access access
	usage  string
	help   string
}

var commands = map[string]commandSpec{
	"stop":    {urgent: true, usage: "/stop", help: "stop the current task"},
	"status":  {urgent: true, usage: "/status", help: "current task, queue, today's spend and the sandbox"},
	"stopall": {urgent: true, access: accessAdmin, usage: "/stopall", help: "pause Bothy and stop every task (admins)"},
	"resume":  {urgent: true, access: accessAdmin, usage: "/resume", help: "lift the pause (admins)"},
	"help":    {urgent: true, access: accessAnyone, usage: "/help", help: "this list"},
	"new":     {usage: "/new", help: "start a fresh conversation"},
	"pin":     {usage: "/pin <text>", help: "keep an instruction in view"},
	"pins":    {usage: "/pins", help: "list pinned instructions"},
	"unpin":   {usage: "/unpin <n>", help: "remove pin number n"},
	"compact": {usage: "/compact", help: "summarize older context now"},
	"model":   {usage: "/model [provider/model]", help: "set or clear this conversation's model"},
	"usage":   {usage: "/usage", help: "today's and this conversation's cost"},
	"pair":    {access: accessAnyone, usage: "/pair CODE", help: "link this account with a code from the web UI"},
	"start":   {access: accessAnyone},
}

var helpOrder = []string{"stop", "new", "status", "usage", "pin", "pins", "unpin", "compact", "model", "pair", "stopall", "resume", "help"}

const maxPinRunes = 1000

// parseCommand recognises "/name args" (and Telegram's "/name@bot args") for
// the commands above only; any other leading slash is ordinary text, so a
// message such as "/etc/hosts is broken" still reaches the agent.
func parseCommand(text string) (name, args string, ok bool) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "/") {
		return "", "", false
	}
	word, rest := t, ""
	if i := strings.IndexFunc(t, unicode.IsSpace); i >= 0 {
		word, rest = t[:i], t[i:]
	}
	name = strings.ToLower(word[1:])
	if at := strings.IndexByte(name, '@'); at >= 0 {
		name = name[:at]
	}
	if _, known := commands[name]; !known {
		return "", "", false
	}
	return name, strings.TrimSpace(rest), true
}

func allowed(acc access, res core.IdentityResolution, p *core.Principal) bool {
	switch acc {
	case accessAnyone:
		return true
	case accessAdmin:
		return res.Status == core.IdentityLinked && res.Role.IsAdmin()
	}
	return res.Status == core.IdentityLinked && (res.User == p.User || res.Role.IsAdmin())
}

func (h *Hub) runCommand(ctx context.Context, a *adapter, in core.Inbound, res core.IdentityResolution, tg target, cmd, args string) {
	spec := commands[cmd]
	chat := in.Chat
	say := func(text string) { h.reply(ctx, chat, text) }
	if spec.urgent {
		say = func(text string) { h.replyAsync(chat, text) }
	}
	if !allowed(spec.access, res, tg.principal) {
		say("Not allowed: only this conversation's owner or an admin can use /" + cmd + ".")
		return
	}
	switch cmd {
	case "stop":
		say(h.cmdStop(ctx, tg, chat, res.User))
	case "stopall":
		say(h.cmdStopAll(ctx, a.name, in.SenderID, res.User))
	case "resume":
		say(h.cmdResume(ctx, a.name, in.SenderID, res.User))
	case "status":
		say(h.cmdStatus(ctx, tg, chat))
	case "help":
		say(helpText())
	case "new":
		say(h.cmdNew(ctx, tg, chat))
	case "pin":
		say(h.cmdPin(ctx, tg, chat, args))
	case "pins":
		say(h.cmdPins(ctx, tg, chat))
	case "unpin":
		say(h.cmdUnpin(ctx, tg, chat, args))
	case "compact":
		if text := h.cmdCompact(ctx, tg, chat); text != "" {
			say(text)
		}
	case "model":
		say(h.cmdModel(ctx, tg, chat, args))
	case "usage":
		say(h.cmdUsage(ctx, tg, chat))
	case "pair", "start":
		switch {
		case args != "" && chat.Kind != core.ChatDM:
			// Codes are single use: anyone here could now redeem it first.
			say("Pairing codes only work in a direct message with me. This one is now visible here, so create a new code in the web UI and send it to me privately.")
		case args != "" && res.Status == core.IdentityGuest:
			say("This account is a guest; an admin can link it to a user in Settings → People.")
		case args != "":
			say("This account is already linked.")
		case res.Status == core.IdentityLinked && chat.Kind == core.ChatDM:
			say(helloText(h.userName(ctx, res.User)))
		default:
			say(helpText())
		}
	}
}

const somethingWrong = "Sorry, something went wrong. Try again in a moment."

func helpText() string {
	var b strings.Builder
	b.WriteString("Commands:")
	for _, name := range helpOrder {
		spec := commands[name]
		b.WriteString("\n" + escapeMD(spec.usage) + " · " + escapeMD(spec.help))
	}
	return b.String()
}

// chatSession returns the chat's open session without creating one.
func (h *Hub) chatSession(ctx context.Context, tg target, chat core.ChatRef) (*core.Session, error) {
	return h.d.Store.Sessions().ByChannelKey(ctx, tg.principal.ID, chat.Key())
}

func (h *Hub) cmdStop(ctx context.Context, tg target, chat core.ChatRef, by core.UserID) string {
	sess, err := h.chatSession(ctx, tg, chat)
	if errors.Is(err, core.ErrNotFound) {
		return "Nothing to stop."
	}
	if err != nil {
		h.log.Error("channel /stop: session lookup failed", "err", err)
		return somethingWrong
	}
	stopped, err := h.d.Runner.Stop(ctx, core.SystemViewer(), sess.ID, by)
	if err != nil {
		h.log.Error("channel /stop failed", "session", sess.ID, "err", err)
		return somethingWrong
	}
	if !stopped {
		return "Nothing to stop."
	}
	return "Stopped."
}

func (h *Hub) cmdStopAll(ctx context.Context, channel, ext string, by core.UserID) string {
	if err := h.d.Runner.SetPaused(ctx, true, by); err != nil {
		h.log.Error("channel /stopall: pause failed", "err", err)
		return somethingWrong
	}
	n, err := h.d.Runner.StopAll(ctx, by)
	if err != nil {
		h.log.Error("channel /stopall: stop failed", "err", err)
	}
	h.audit(ctx, "admin.pause", channel, ext, by)
	return fmt.Sprintf("Paused. Stopped %d task(s). /resume lifts the pause.", n)
}

func (h *Hub) cmdResume(ctx context.Context, channel, ext string, by core.UserID) string {
	if err := h.d.Runner.SetPaused(ctx, false, by); err != nil {
		h.log.Error("channel /resume failed", "err", err)
		return somethingWrong
	}
	h.audit(ctx, "admin.resume", channel, ext, by)
	return "Resumed."
}

func (h *Hub) audit(ctx context.Context, action, channel, ext string, by core.UserID) {
	if h.d.Audit == nil {
		return
	}
	detail, _ := json.Marshal(map[string]string{"user_id": string(by), "via": channel})
	err := h.d.Audit.Record(ctx, core.AuditEntry{At: h.clock.Now(), Actor: "channel:" + channel + ":" + ext,
		Action: action, Target: "kill_switch", Outcome: "ok", Detail: detail})
	if err != nil {
		h.log.Error("channel audit failed", "action", action, "err", err)
	}
}

func (h *Hub) cmdStatus(ctx context.Context, tg target, chat core.ChatRef) string {
	var lines []string
	if h.d.Runner.Paused() {
		lines = append(lines, "Bothy is paused by an admin.")
	}
	task := "No task running."
	if sess, err := h.chatSession(ctx, tg, chat); err == nil {
		for _, ri := range h.d.Runner.Active() {
			if ri.Session != sess.ID {
				continue
			}
			task = "Task: " + strings.ReplaceAll(string(ri.Status), "_", " ")
			if !ri.Started.IsZero() {
				task += fmt.Sprintf(" for %s", h.clock.Now().Sub(ri.Started).Round(time.Second))
			}
			if ri.Queued > 0 {
				task += fmt.Sprintf(", %d queued", ri.Queued)
			}
			task += "."
		}
	}
	lines = append(lines, task)
	if spent, limit, err := h.d.Budget.Daily(ctx, tg.principal.User); err == nil {
		lines = append(lines, "Today: "+spendText(spent, limit)+".")
	}
	if h.d.Sandbox != nil {
		st := h.d.Sandbox.Status(ctx)
		if st.Available {
			lines = append(lines, "Sandbox: "+escapeMD(st.Backend)+" ("+escapeMD(st.Runtime)+").")
		} else {
			lines = append(lines, "Sandbox: off. "+escapeMD(st.Reason))
		}
	}
	return strings.Join(lines, "\n")
}

func spendText(spent, limit core.Micros) string {
	if limit <= 0 {
		return spent.String() + " (no cap)"
	}
	return spent.String() + " of " + limit.String()
}

func (h *Hub) cmdNew(ctx context.Context, tg target, chat core.ChatRef) string {
	sess, err := h.chatSession(ctx, tg, chat)
	if errors.Is(err, core.ErrNotFound) {
		return "Started a fresh conversation."
	}
	if err == nil {
		err = h.d.Store.Sessions().Archive(ctx, core.SystemViewer(), sess.ID)
	}
	if err != nil {
		h.log.Error("channel /new failed", "err", err)
		return somethingWrong
	}
	return "Started a fresh conversation."
}

func (h *Hub) cmdPin(ctx context.Context, tg target, chat core.ChatRef, text string) string {
	if text == "" {
		return "Usage: /pin <text>"
	}
	if utf8.RuneCountInString(text) > maxPinRunes {
		return fmt.Sprintf("Pins are limited to %d characters.", maxPinRunes)
	}
	sess, err := h.resolveSession(ctx, tg.principal.ID, chat)
	if err == nil {
		err = h.d.Store.Pins().Add(ctx, &core.Pin{Session: sess.ID, Text: text, Source: "user", CreatedAt: h.clock.Now()})
	}
	if err != nil {
		h.log.Error("channel /pin failed", "err", err)
		return somethingWrong
	}
	return "Pinned. /pins lists them."
}

func (h *Hub) pins(ctx context.Context, tg target, chat core.ChatRef) (*core.Session, []*core.Pin, error) {
	sess, err := h.chatSession(ctx, tg, chat)
	if err != nil {
		return nil, nil, err
	}
	list, err := h.d.Store.Pins().List(ctx, core.SystemViewer(), sess.ID)
	slices.SortFunc(list, func(x, y *core.Pin) int { return cmp.Compare(x.ID, y.ID) })
	return sess, list, err
}

func (h *Hub) cmdPins(ctx context.Context, tg target, chat core.ChatRef) string {
	_, list, err := h.pins(ctx, tg, chat)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		h.log.Error("channel /pins failed", "err", err)
		return somethingWrong
	}
	if len(list) == 0 {
		return "No pins."
	}
	var b strings.Builder
	b.WriteString("Pinned instructions:")
	for i, p := range list {
		fmt.Fprintf(&b, "\n%d. %s", i+1, escapeMD(truncateRunes(strings.ReplaceAll(p.Text, "\n", " "), 200)))
	}
	return b.String()
}

func (h *Hub) cmdUnpin(ctx context.Context, tg target, chat core.ChatRef, arg string) string {
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 {
		return "Usage: /unpin <n> (see /pins)"
	}
	sess, list, err := h.pins(ctx, tg, chat)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		h.log.Error("channel /unpin failed", "err", err)
		return somethingWrong
	}
	if n > len(list) {
		return fmt.Sprintf("There is no pin %d.", n)
	}
	if err := h.d.Store.Pins().Delete(ctx, core.SystemViewer(), sess.ID, list[n-1].ID); err != nil {
		h.log.Error("channel /unpin failed", "err", err)
		return somethingWrong
	}
	return "Unpinned."
}

// compactWait bounds how long a /compact waits for its turn behind the
// session's active run before saying it is still queued.
const compactWait = 15 * time.Minute

const compactFailed = "Compaction failed; it will run again automatically when needed."

// cmdCompact starts the compaction in the background and replies when it is
// done. Runner.Compact queues behind the session's active run, and waiting
// for it in the chat's queue would hold back every later message in this
// chat, steers included, until that run ends.
func (h *Hub) cmdCompact(ctx context.Context, tg target, chat core.ChatRef) string {
	sess, err := h.chatSession(ctx, tg, chat)
	if errors.Is(err, core.ErrNotFound) {
		return "Nothing to compact yet."
	}
	if err != nil {
		h.log.Warn("channel /compact failed", "err", err)
		return compactFailed
	}
	h.mu.Lock()
	if _, ok := h.compacting[sess.ID]; ok {
		h.mu.Unlock()
		return "Already compacting this conversation."
	}
	h.compacting[sess.ID] = struct{}{}
	h.mu.Unlock()
	done := func() {
		h.mu.Lock()
		delete(h.compacting, sess.ID)
		h.mu.Unlock()
	}
	busy := slices.ContainsFunc(h.d.Runner.Active(), func(ri core.RunInfo) bool { return ri.Session == sess.ID })
	started := h.goAsync(func(ctx context.Context) {
		defer done()
		wait, cancel := context.WithTimeout(ctx, compactWait)
		err := h.d.Runner.Compact(wait, core.SystemViewer(), sess.ID)
		cancel()
		text := "Compacted."
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, context.DeadlineExceeded):
			text = "Compaction is still waiting for the current task; it runs when that ends."
		case err != nil:
			h.log.Warn("channel /compact failed", "session", sess.ID, "err", err)
			text = compactFailed
		}
		h.reply(ctx, chat, text)
	})
	if !started {
		done()
		return ""
	}
	if busy {
		return "I'll compact this conversation when the current task is done."
	}
	return ""
}

func (h *Hub) cmdModel(ctx context.Context, tg target, chat core.ChatRef, ref string) string {
	if ref != "" {
		if _, err := core.ParseModelRef(ref); err != nil || strings.ContainsFunc(ref, unicode.IsSpace) {
			return "Usage: /model <provider/model>, for example /model anthropic/claude-sonnet-4-6. /model alone clears it."
		}
	}
	sess, err := h.resolveSession(ctx, tg.principal.ID, chat)
	if err == nil {
		sess.ModelOverride = ref
		err = h.d.Store.Sessions().Update(ctx, sess)
	}
	if err != nil {
		h.log.Error("channel /model failed", "err", err)
		return somethingWrong
	}
	if ref == "" {
		return "Model override cleared; this conversation uses the default model."
	}
	return "This conversation now uses " + codeSpan(ref) + "."
}

func (h *Hub) cmdUsage(ctx context.Context, tg target, chat core.ChatRef) string {
	spent, limit, err := h.d.Budget.Daily(ctx, tg.principal.User)
	if err != nil {
		h.log.Error("channel /usage failed", "err", err)
		return somethingWrong
	}
	var sessCost core.Micros
	if sess, err := h.chatSession(ctx, tg, chat); err == nil {
		sessCost = h.sessionCost(ctx, sess.ID)
	}
	return "Today: " + spendText(spent, limit) + ". This conversation: " + sessCost.String() + "."
}

func (h *Hub) sessionCost(ctx context.Context, s core.SessionID) core.Micros {
	var total core.Micros
	page := core.Page{Limit: 500}
	for {
		runs, err := h.d.Store.Runs().List(ctx, core.SystemViewer(), core.RunFilter{Session: s, Page: page})
		if err != nil {
			h.log.Error("channel: listing runs failed", "err", err)
			return total
		}
		for _, r := range runs {
			total += r.Cost
		}
		if len(runs) < page.Limit {
			return total
		}
		// The cursor must move back in time, or a page of zero or equal
		// timestamps would be fetched forever.
		next := runs[len(runs)-1].CreatedAt.UnixMilli()
		if next <= 0 || (page.Before > 0 && next >= page.Before) {
			return total
		}
		page.Before = next
	}
}

package channel

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
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
	"status":  {urgent: true, usage: "/status", help: "what Bothy is doing and today's spending"},
	"stopall": {urgent: true, access: accessAdmin, usage: "/stopall", help: "pause Bothy and stop every task"},
	"resume":  {urgent: true, access: accessAdmin, usage: "/resume", help: "lift the pause"},
	"help":    {urgent: true, access: accessAnyone, usage: "/help", help: "this list"},
	"new":     {usage: "/new", help: "start a fresh conversation"},
	"pin":     {usage: "/pin <text>", help: "keep an instruction in view"},
	"pins":    {usage: "/pins", help: "list pinned instructions"},
	"unpin":   {usage: "/unpin <n>", help: "remove pin number n"},
	"compact": {usage: "/compact", help: "summarise older messages now"},
	"model":   {usage: "/model [provider/model|default]", help: "show or change the AI model of this conversation"},
	"refine":  {usage: "/refine [on|off|default]", help: "check answers more carefully here (slower, costs more)"},
	"usage":   {usage: "/usage", help: "today's spending and this conversation's cost"},
	"pair":    {access: accessAnyone, usage: "/pair CODE", help: "link this account with a code from the web UI"},
	"start":   {access: accessAnyone},
}

var helpOrder = []string{"stop", "new", "status", "usage", "pin", "pins", "unpin", "compact", "model", "refine", "pair", "stopall", "resume", "help"}

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

var commandWord = regexp.MustCompile(`^/([A-Za-z0-9_]{1,32})(@[A-Za-z0-9_]+)?$`)

// unknownCommand recognises a message that is only "/word" (or Telegram's
// "/word@bot") naming no command: almost always a mistyped one, so it gets a
// short reply instead of a model run. A path such as "/etc/hosts" is not a
// word and still reaches the agent.
func unknownCommand(text string) (string, bool) {
	m := commandWord.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return "", false
	}
	if _, known := commands[strings.ToLower(m[1])]; known {
		return "", false
	}
	return "/" + m[1], true
}

func allowed(acc access, res core.IdentityResolution, p *core.Principal) bool {
	switch acc {
	case accessAnyone:
		return true
	case accessAdmin:
		return isAdmin(res)
	}
	return res.Status == core.IdentityLinked && (res.User == p.User || res.Role.IsAdmin())
}

func isAdmin(res core.IdentityResolution) bool {
	return res.Status == core.IdentityLinked && res.Role.IsAdmin()
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
		say(helpText(isAdmin(res)))
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
	case "refine":
		say(h.cmdRefine(ctx, tg, chat, args))
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
			say(helpText(isAdmin(res)))
		}
	}
}

const somethingWrong = "Sorry, something went wrong. Try again in a moment."

// helpText lists the commands; the admin-only ones only for admins.
func helpText(admin bool) string {
	var b strings.Builder
	b.WriteString("Commands:")
	for _, name := range helpOrder {
		spec := commands[name]
		if spec.access == accessAdmin && !admin {
			continue
		}
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

// cmdStatus says, in plain words, what the chat's task is doing, today's
// spending, whether refine is on here (it multiplies the cost of every
// answer) and, only when it is off, that running code is unavailable.
func (h *Hub) cmdStatus(ctx context.Context, tg target, chat core.ChatRef) string {
	var lines []string
	if h.d.Runner.Paused() {
		lines = append(lines, "Bothy is paused by an admin.")
	}
	task := "No task running."
	var sessID core.SessionID
	if sess, err := h.chatSession(ctx, tg, chat); err == nil {
		sessID = sess.ID
		for _, ri := range h.d.Runner.Active() {
			if ri.Session == sess.ID {
				task = h.taskText(ri)
			}
		}
	}
	lines = append(lines, task)
	if spent, limit, err := h.d.Budget.Daily(ctx, tg.principal.User); err == nil {
		lines = append(lines, "Today: "+spendText(spent, limit)+".")
	}
	if _, on, err := h.refineSetting(ctx, sessID); err == nil && on {
		lines = append(lines, "Refine is on for this conversation, so answers are slower and cost more. /refine off turns it off.")
	}
	if h.d.Sandbox != nil && !h.d.Sandbox.Status(ctx).Available {
		lines = append(lines, "Running code is off on this server.")
	}
	return strings.Join(lines, "\n")
}

// taskText is the /status line of the chat's task, e.g. "Working on a task
// for 2 min 10 s. 1 more message is waiting."
func (h *Hub) taskText(ri core.RunInfo) string {
	var s string
	switch ri.Status {
	case core.RunWaitingApproval, core.RunParked:
		s = "Waiting for approval"
	case core.RunQueued:
		s = "A task is about to start"
	default:
		s = "Working on a task"
	}
	if !ri.Started.IsZero() && ri.Status != core.RunQueued {
		s += " for " + durationText(h.clock.Now().Sub(ri.Started))
	}
	s += "."
	switch {
	case ri.Queued == 1:
		s += " 1 more message is waiting."
	case ri.Queued > 1:
		s += fmt.Sprintf(" %d more messages are waiting.", ri.Queued)
	}
	return s
}

// durationText follows the UI's durations (docs/design/ui.md §14.2): "3 s",
// "2 min 10 s", "1 h 5 min".
func durationText(d time.Duration) string {
	d = max(d.Round(time.Second), time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", d/time.Second)
	case d < time.Hour:
		m, sec := d/time.Minute, d%time.Minute/time.Second
		if sec == 0 {
			return fmt.Sprintf("%d min", m)
		}
		return fmt.Sprintf("%d min %d s", m, sec)
	}
	hr, m := d/time.Hour, d%time.Hour/time.Minute
	if m == 0 {
		return fmt.Sprintf("%d h", hr)
	}
	return fmt.Sprintf("%d h %d min", hr, m)
}

func spendText(spent, limit core.Micros) string {
	if limit <= 0 {
		return moneyText(spent) + ", no daily limit"
	}
	return moneyText(spent) + " of the " + moneyText(limit) + " daily limit"
}

// moneyText follows the UI's money rule (docs/design/ui.md §14.2): "<$0.01"
// for less than a cent, two decimals under $1,000, whole dollars above.
func moneyText(m core.Micros) string {
	cents := math.Round(m.USD() * 100)
	switch {
	case m > 0 && cents < 1:
		return "<$0.01"
	case cents < 100_000:
		return fmt.Sprintf("$%.2f", cents/100)
	}
	whole := strconv.FormatInt(int64(math.Round(m.USD())), 10)
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	return "$" + whole
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
	// Archived chats leave the web UI's chat list; their tasks in Activity
	// still open them.
	return "Started a fresh conversation. The previous one is archived; Activity in the web UI still opens it."
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
		err = h.d.Store.Pins().Add(ctx, &core.Pin{Session: sess.ID, Text: h.redactFor(tg.principal, text), Source: "user", CreatedAt: h.clock.Now()})
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

// cmdModel shows the conversation's model, or sets it; only an explicit
// word clears the override, so checking the model never changes it.
func (h *Hub) cmdModel(ctx context.Context, tg target, chat core.ChatRef, arg string) string {
	main := h.cfg().Models.Main
	switch strings.ToLower(arg) {
	case "":
		sess, err := h.chatSession(ctx, tg, chat)
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			h.log.Error("channel /model failed", "err", err)
			return somethingWrong
		}
		if err == nil && sess.ModelOverride != "" {
			return "This conversation uses " + codeSpan(sess.ModelOverride) + ". /model default goes back to the default model."
		}
		if len(main) > 0 {
			return "This conversation uses the default model, " + codeSpan(main[0]) + ". /model provider/model changes it."
		}
		return "This conversation uses the default model. /model provider/model changes it."
	case "default", "clear", "reset":
		sess, err := h.chatSession(ctx, tg, chat)
		if err == nil && sess.ModelOverride != "" {
			sess.ModelOverride = ""
			err = h.d.Store.Sessions().Update(ctx, sess)
		}
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			h.log.Error("channel /model failed", "err", err)
			return somethingWrong
		}
		return "This conversation uses the default model again."
	}
	if _, err := core.ParseModelRef(arg); err != nil || strings.ContainsFunc(arg, unicode.IsSpace) {
		example := "anthropic/claude-sonnet-4-6"
		if len(main) > 0 {
			example = main[0]
		}
		return "Usage: /model <provider/model>, for example " + codeSpan("/model "+example) + ". /model alone shows the model; /model default goes back to the default one."
	}
	sess, err := h.resolveSession(ctx, tg.principal.ID, chat)
	if err == nil {
		sess.ModelOverride = arg
		err = h.d.Store.Sessions().Update(ctx, sess)
	}
	if err != nil {
		h.log.Error("channel /model failed", "err", err)
		return somethingWrong
	}
	return "This conversation now uses " + codeSpan(arg) + "."
}

// cmdRefine shows or sets the conversation's refine mode, which the runner
// reads from the session's meta key at the start of each run.
func (h *Hub) cmdRefine(ctx context.Context, tg target, chat core.ChatRef, arg string) string {
	arg = strings.ToLower(strings.TrimSpace(arg))
	switch arg {
	case "", core.RefineOn, core.RefineOff, "default":
	default:
		return "Usage: /refine on, /refine off or /refine default. /refine alone shows the setting."
	}
	sess, err := h.resolveSession(ctx, tg.principal.ID, chat)
	if err != nil {
		h.log.Error("channel /refine failed", "err", err)
		return somethingWrong
	}
	key, meta := core.RefineMetaKey(sess.ID), h.d.Store.Meta()
	switch arg {
	case core.RefineOn, core.RefineOff:
		err = meta.Set(ctx, key, arg)
	case "default":
		if err = meta.Delete(ctx, key); errors.Is(err, core.ErrNotFound) {
			err = nil
		}
	}
	var on bool
	if err == nil {
		arg, on, err = h.refineSetting(ctx, sess.ID)
	}
	if err != nil {
		h.log.Error("channel /refine failed", "session", sess.ID, "err", err)
		return somethingWrong
	}
	if !on {
		if arg == core.RefineOff {
			return "Refine is off for this conversation. /refine on turns it on."
		}
		return "Refine is off for this conversation, as by default. /refine on turns it on."
	}
	c := h.cfg().Agent.Refine
	how := fmt.Sprintf("I draft each answer %s and check each draft %s before I reply, so answers are slower and cost about %d times as much.",
		times(c.Drafts), times(c.Depth), core.RefineTypicalCalls(c.Drafts, c.Depth, c.EarlyStop))
	if arg == core.RefineOn {
		return "Refine is on for this conversation: " + how + " /refine off turns it off."
	}
	return "Refine is on for this conversation, as by default: " + how + " /refine off turns it off."
}

// refineSetting returns the session's own refine setting ("" when it follows
// agent.refine.enabled) and whether refine is on for it; a chat without a
// session yet follows the default.
func (h *Hub) refineSetting(ctx context.Context, s core.SessionID) (string, bool, error) {
	var arg string
	if s != "" {
		v, err := h.d.Store.Meta().Get(ctx, core.RefineMetaKey(s))
		switch {
		case err == nil:
			arg = v
		case !errors.Is(err, core.ErrNotFound):
			return "", false, err
		}
	}
	return arg, arg == core.RefineOn || (arg != core.RefineOff && h.cfg().Agent.Refine.Enabled), nil
}

func times(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return strconv.Itoa(n) + " times"
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
	return "Today: " + spendText(spent, limit) + ". This conversation: " + moneyText(sessCost) + "."
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

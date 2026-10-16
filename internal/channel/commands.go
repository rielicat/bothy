package channel

import (
	"cmp"
	"context"
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
	accessChat     access = iota // the DM's teammate; in a channel, someone who can approve
	accessAnyone                 // anyone Bothy answers in this chat
	accessApprover               // an admitted chat approver, in any chat
	accessWeb                    // only on the web: the reply says where
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
	"stop":    {urgent: true, access: accessAnyone, usage: "/stop", help: "stop the current task"},
	"status":  {urgent: true, access: accessAnyone, usage: "/status", help: "what Bothy is doing and today's spending"},
	"stopall": {urgent: true, access: accessApprover},
	"resume":  {urgent: true, access: accessWeb},
	"help":    {urgent: true, access: accessAnyone, usage: "/help", help: "this list"},
	"new":     {usage: "/new", help: "start a fresh conversation"},
	"pin":     {usage: "/pin <text>", help: "keep an instruction in view"},
	"pins":    {access: accessAnyone, usage: "/pins", help: "list pinned instructions"},
	"unpin":   {usage: "/unpin <n>", help: "remove pin number n"},
	"compact": {usage: "/compact", help: "summarise older messages now"},
	"model":   {usage: "/model [model name|default]", help: "show or change the AI model of this conversation"},
	"refine":  {usage: "/refine [on|off|default]", help: "check answers more carefully here (slower, costs more)"},
	"usage":   {access: accessAnyone, usage: "/usage", help: "today's spending and this conversation's cost"},
	"pair":    {access: accessAnyone},
	"start":   {access: accessAnyone},
}

var helpOrder = []string{"stop", "new", "status", "usage", "pin", "pins", "unpin", "compact", "model", "refine", "help"}

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

// allowed applies a command's access rule to whoever sent it.
func (h *Hub) allowed(acc access, a *adapter, res core.IdentityResolution, tg target) bool {
	switch acc {
	case accessAnyone, accessWeb:
		return true
	case accessApprover:
		return res.CanApprove && h.usable(a, res, core.MemberStale)
	}
	return tg.space != nil && tg.space.Kind == core.SpacePerson || res.CanApprove && h.usable(a, res, core.MemberStale)
}

// chatOrigin is the audit origin of something a chat sender did.
func chatOrigin(ctx context.Context, a *adapter, in core.Inbound) context.Context {
	name := strings.TrimSpace(in.SenderName)
	if name == "" {
		name = in.SenderID
	}
	return core.WithAuditOrigin(ctx, core.AuditOrigin{Actor: core.ChatActor(a.name, in.SenderID), Name: name + " · " + channelTitle(a.name)})
}

func (h *Hub) runCommand(ctx context.Context, a *adapter, in core.Inbound, res core.IdentityResolution, tg target, cmd, args string) {
	spec := commands[cmd]
	chat := in.Chat
	say := func(text string) { h.reply(ctx, chat, text) }
	if spec.urgent {
		say = func(text string) { h.replyAsync(chat, text) }
	}
	if !h.allowed(spec.access, a, res, tg) {
		if spec.access == accessApprover {
			say("Only someone who can approve can pause Bothy.")
		} else {
			say("Only someone who can approve can use /" + cmd + " here.")
		}
		return
	}
	switch cmd {
	case "stop":
		say(h.cmdStop(chatOrigin(ctx, a, in), tg, chat))
	case "stopall":
		say(h.cmdStopAll(chatOrigin(ctx, a, in)))
	case "resume":
		say("Resume Bothy on the web, under Settings → Security.")
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
	case "refine":
		say(h.cmdRefine(ctx, tg, chat, args))
	case "usage":
		say(h.cmdUsage(ctx, tg, chat))
	case "pair":
		say("Bothy doesn't use link codes any more. Write here to ask it anything.")
	case "start":
		if chat.Kind == core.ChatDM {
			say(helloText(in.SenderName))
		} else {
			say(helpText())
		}
	}
}

const somethingWrong = "Sorry, something went wrong. Try again in a moment."

// helpText lists the commands everyone sees.
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

// cmdStop stops the chat's task; ctx carries the sender as audit origin.
func (h *Hub) cmdStop(ctx context.Context, tg target, chat core.ChatRef) string {
	sess, err := h.chatSession(ctx, tg, chat)
	if errors.Is(err, core.ErrNotFound) {
		return "Nothing to stop."
	}
	if err != nil {
		h.log.Error("channel /stop: session lookup failed", "err", err)
		return somethingWrong
	}
	stopped, err := h.d.Runner.Stop(ctx, core.SystemViewer(), sess.ID, "")
	if err != nil {
		h.log.Error("channel /stop failed", "session", sess.ID, "err", err)
		return somethingWrong
	}
	if !stopped {
		return "Nothing to stop."
	}
	return "Stopped."
}

// cmdStopAll is an approver's kill switch from chat. The runner audits it
// and names the sender in the banner; /resume points to the web.
func (h *Hub) cmdStopAll(ctx context.Context) string {
	if err := h.d.Runner.SetPaused(ctx, true, ""); err != nil {
		h.log.Error("channel /stopall: pause failed", "err", err)
		return somethingWrong
	}
	n, err := h.d.Runner.StopAll(ctx, "")
	if err != nil {
		h.log.Error("channel /stopall: stop failed", "err", err)
	}
	switch n {
	case 0:
		return "Paused. No tasks were running. /resume lifts the pause."
	case 1:
		return "Paused. Stopped 1 task. /resume lifts the pause."
	}
	return fmt.Sprintf("Paused. Stopped %d tasks. /resume lifts the pause.", n)
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
	if spent, limit, err := h.d.Budget.Daily(ctx, tg.principal.ID); err == nil {
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

const compactFailed = "Bothy couldn't summarise older messages. It will try again when the chat gets long."

// cmdCompact starts the compaction in the background and replies when it is
// done. Runner.Compact queues behind the session's active run, and waiting
// for it in the chat's queue would hold back every later message in this
// chat, steers included, until that run ends.
func (h *Hub) cmdCompact(ctx context.Context, tg target, chat core.ChatRef) string {
	sess, err := h.chatSession(ctx, tg, chat)
	if errors.Is(err, core.ErrNotFound) {
		return "Nothing to summarise yet."
	}
	if err != nil {
		h.log.Warn("channel /compact failed", "err", err)
		return compactFailed
	}
	h.mu.Lock()
	if _, ok := h.compacting[sess.ID]; ok {
		h.mu.Unlock()
		return "Already summarising older messages in this chat."
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
		text := "Older messages were summarised."
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, context.DeadlineExceeded):
			text = "Bothy will summarise older messages when the current task ends."
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
		return "Bothy will summarise older messages when the current task is done."
	}
	return ""
}

// cmdModel shows the chat's model, or sets it; only an explicit word clears
// the override, so checking the model never changes it.
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
			return "This chat uses " + h.modelText(ctx, sess.ModelOverride) + ". /model default goes back to the main model."
		}
		if len(main) > 0 {
			return "This chat uses the main model, " + h.modelText(ctx, main[0]) + ". /model <model name> changes it."
		}
		return "This chat uses the main model. /model <model name> changes it."
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
		return "This chat uses the main model again."
	}
	ref, choices := h.findModel(ctx, arg)
	if len(choices) > 0 {
		return "That name matches several models: " + strings.Join(choices, ", ") + ". Type one of those names."
	}
	if ref == "" {
		example := ""
		if len(main) > 0 {
			// Suggest the name only if typing it would choose that very model.
			name := h.modelName(ctx, main[0])
			if r, _ := h.findModel(ctx, name); name == "" || r != main[0] {
				name = main[0]
			}
			example = ", for example " + codeSpan("/model "+name)
		}
		return "Usage: /model <model name>" + example + ". /model alone shows the model; /model default goes back to the main model."
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
	return "This chat now uses " + h.modelText(ctx, ref) + "."
}

// findModel resolves what was typed after /model to a model ref: a model's
// plain or short name ("Claude Sonnet 4.6 (US)", "sonnet 4.6"), else a
// provider/model ref, stored as typed. A name several models share chooses
// the one the main or quick-tasks model already uses; otherwise choices lists
// them, by the names that tell them apart, and ref is "".
func (h *Hub) findModel(ctx context.Context, arg string) (ref string, choices []string) {
	arg = oneLine(arg)
	if arg == "" {
		return "", nil
	}
	if h.d.LLM != nil {
		ms := h.d.LLM.Models(ctx)
		models := h.cfg().Models
		for _, name := range []func(core.ModelInfo) string{
			func(m core.ModelInfo) string { return m.DisplayName },
			func(m core.ModelInfo) string { return m.ShortName },
		} {
			var hits []core.ModelInfo
			for _, m := range ms {
				if n := name(m); n != "" && strings.EqualFold(n, arg) && !slices.ContainsFunc(hits, func(x core.ModelInfo) bool { return x.Ref == m.Ref }) {
					hits = append(hits, m)
				}
			}
			switch len(hits) {
			case 0:
				continue
			case 1:
				return hits[0].Ref.String(), nil
			}
			var inUse []string
			for _, m := range hits {
				if r := m.Ref.String(); slices.Contains(models.Main, r) || slices.Contains(models.Fast, r) {
					inUse = append(inUse, r)
				}
			}
			if len(inUse) == 1 {
				return inUse[0], nil
			}
			for _, m := range hits {
				choices = append(choices, escapeMD(cmp.Or(m.DisplayName, m.ShortName))+" ("+codeSpan(m.Ref.String())+")")
			}
			return "", choices
		}
	}
	if _, err := core.ParseModelRef(arg); err != nil || strings.ContainsFunc(arg, unicode.IsSpace) {
		return "", nil
	}
	return arg, nil
}

// modelName is a model's plain name as listed (with the region that sets it
// apart, "Claude Sonnet 4.6 (US)"), "" when no provider can name the ref.
func (h *Hub) modelName(ctx context.Context, ref string) string {
	r, err := core.ParseModelRef(ref)
	if err != nil || h.d.LLM == nil {
		return ""
	}
	for _, m := range h.d.LLM.Models(ctx) {
		if m.Ref == r && m.DisplayName != "" {
			return m.DisplayName
		}
	}
	m, ok := h.d.LLM.Model(r)
	if !ok {
		return ""
	}
	return m.DisplayName
}

// modelText names a model in a reply; a ref nobody can name is shown as
// stored, in code.
func (h *Hub) modelText(ctx context.Context, ref string) string {
	if n := h.modelName(ctx, ref); n != "" {
		return escapeMD(n)
	}
	return codeSpan(ref)
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
	calls := core.RefineTypicalCalls(c.Drafts, c.Depth, c.EarlyStop)
	how := fmt.Sprintf("Bothy writes %d drafts, checks them and sends the best, so answers are slower and cost about %d times as much.", c.Drafts, calls)
	if c.Drafts <= 1 {
		how = fmt.Sprintf("Bothy checks each answer before sending it, so answers are slower and cost about %d times as much.", calls)
	}
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

func (h *Hub) cmdUsage(ctx context.Context, tg target, chat core.ChatRef) string {
	spent, limit, err := h.d.Budget.Daily(ctx, tg.principal.ID)
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
